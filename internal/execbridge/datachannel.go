// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// Ported to Harbormaster from github.com/aws/session-manager-plugin
// (src/datachannel/streaming.go, src/config/config.go);
// modifications Copyright 2026 Harbormaster contributors, MIT licence.
//
// Harbormaster changes: the outgoing buffer applies backpressure instead of
// evicting the oldest message, the resend scheduler updates LastSentTime on
// the stored element, and the logic is driven by a FrameConn instead of a
// concrete websocket.

package execbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Protocol constants (upstream config.go).
const (
	MessageSchemaVersion          = "1.0"
	StreamDataPayloadSize         = 1024
	OutgoingMessageBufferCapacity = 10000
	IncomingMessageBufferCapacity = 10000
	DefaultTransmissionTimeout    = 200 * time.Millisecond
	DefaultRoundTripTime          = 100 * time.Millisecond
	DefaultRoundTripTimeVariation = 0
	ClockGranularity              = 10 * time.Millisecond
	MaxTransmissionTimeout        = time.Second
	ResendSleepInterval           = 100 * time.Millisecond
	ResendMaxAttempt              = 3000
	PingTimeInterval              = 5 * time.Minute
	ShellPluginName               = "Standard_Stream"
	DefaultHandshakeTimeout       = 15 * time.Second

	rttConstant  = 1.0 / 8
	rttVConstant = 1.0 / 4
)

// ClientVersion is sent in the open message and the handshake response: the
// session-manager-plugin version this port follows, prefixed with our name.
var ClientVersion = "harbormaster/1.3.0.0"

var (
	ErrResendTimeout    = errors.New("execbridge: ssm stopped acknowledging input")
	ErrHandshakeTimeout = errors.New("execbridge: handshake not completed in time")
	errChannelDead      = errors.New("execbridge: data channel is not running")
)

// FrameConn is the websocket as seen by the data channel. Writes must be safe
// for concurrent use; ReadFrame must return promptly when ctx ends.
type FrameConn interface {
	ReadFrame(ctx context.Context) (text bool, data []byte, err error)
	WriteBinary(ctx context.Context, b []byte) error
	WriteText(ctx context.Context, b []byte) error
	Close() error
}

type EventKind uint8

const (
	// EventOutput carries terminal bytes (PayloadType Output or StdErr).
	EventOutput EventKind = iota + 1
	// EventExitCode carries the raw ExitCode payload in Data.
	EventExitCode
	EventHandshakeComplete
	// EventChannelClosed has Closed set.
	EventChannelClosed
	// EventError has Err set; the channel is dead.
	EventError
)

type Event struct {
	Kind        EventKind
	PayloadType PayloadType
	Data        []byte
	Closed      ChannelClosed
	Err         error
}

type DataChannelConfig struct {
	ClientID, Token, ClientVersion string
	ResendInterval                 time.Duration
	ResendMaxAttempts              int
	InitialRTO, MaxRTO             time.Duration
	OutgoingCapacity               int
	IncomingCapacity               int
	HandshakeTimeout               time.Duration
	Now                            func() time.Time
	Logger                         *slog.Logger
}

// DefaultConfig fills the upstream constants.
func DefaultConfig(clientID, token string) DataChannelConfig {
	return DataChannelConfig{
		ClientID:          clientID,
		Token:             token,
		ClientVersion:     ClientVersion,
		ResendInterval:    ResendSleepInterval,
		ResendMaxAttempts: ResendMaxAttempt,
		InitialRTO:        DefaultTransmissionTimeout,
		MaxRTO:            MaxTransmissionTimeout,
		OutgoingCapacity:  OutgoingMessageBufferCapacity,
		IncomingCapacity:  IncomingMessageBufferCapacity,
		HandshakeTimeout:  DefaultHandshakeTimeout,
		Now:               time.Now,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// outgoingMessage is an unacknowledged input message.
type outgoingMessage struct {
	seq      int64
	raw      []byte
	lastSent time.Time
	attempts int
}

// DataChannel implements the SSM agent data-channel protocol on a FrameConn.
type DataChannel struct {
	conn FrameConn
	cfg  DataChannelConfig

	events chan Event
	done   chan struct{} // closed when Run returns

	// sendMu serialises sequence assignment and writes of new input messages.
	sendMu sync.Mutex

	mu        sync.Mutex // guards the fields below
	outSeq    int64
	outBuf    []*outgoingMessage // ordered by seq, oldest first
	spaceWait chan struct{}      // closed (and replaced) whenever outBuf shrinks
	rtt       time.Duration
	rttVar    time.Duration
	rto       time.Duration

	agentVersion string
	sessionType  string
	openedAt     time.Time

	handshakeDone     chan struct{}
	handshakeDoneOnce sync.Once

	// Owned by the read loop only.
	inExpected     int64
	inBuf          map[int64]ClientMessage
	firstDelivered bool
}

func NewDataChannel(conn FrameConn, cfg DataChannelConfig) *DataChannel {
	def := DefaultConfig(cfg.ClientID, cfg.Token)
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = def.ClientVersion
	}
	if cfg.ResendInterval <= 0 {
		cfg.ResendInterval = def.ResendInterval
	}
	if cfg.ResendMaxAttempts <= 0 {
		cfg.ResendMaxAttempts = def.ResendMaxAttempts
	}
	if cfg.InitialRTO <= 0 {
		cfg.InitialRTO = def.InitialRTO
	}
	if cfg.MaxRTO <= 0 {
		cfg.MaxRTO = def.MaxRTO
	}
	if cfg.OutgoingCapacity <= 0 {
		cfg.OutgoingCapacity = def.OutgoingCapacity
	}
	if cfg.IncomingCapacity <= 0 {
		cfg.IncomingCapacity = def.IncomingCapacity
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = def.HandshakeTimeout
	}
	if cfg.Now == nil {
		cfg.Now = def.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = def.Logger
	}
	return &DataChannel{
		conn:          conn,
		cfg:           cfg,
		events:        make(chan Event, 256),
		done:          make(chan struct{}),
		spaceWait:     make(chan struct{}),
		rtt:           DefaultRoundTripTime,
		rttVar:        DefaultRoundTripTimeVariation,
		rto:           cfg.InitialRTO,
		handshakeDone: make(chan struct{}),
		inBuf:         make(map[int64]ClientMessage),
	}
}

// Events returns the channel on which output and lifecycle events arrive. It
// is closed when Run returns.
func (d *DataChannel) Events() <-chan Event { return d.events }

func (d *DataChannel) AgentVersion() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agentVersion
}

func (d *DataChannel) SessionType() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessionType
}

func (d *DataChannel) currentRTO() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rto
}

// Open sends the OpenDataChannelInput text frame.
func (d *DataChannel) Open(ctx context.Context) error {
	body, err := json.Marshal(OpenDataChannelInput{
		MessageSchemaVersion: MessageSchemaVersion,
		RequestID:            uuid.NewString(),
		TokenValue:           d.cfg.Token,
		ClientID:             d.cfg.ClientID,
		ClientVersion:        d.cfg.ClientVersion,
	})
	if err != nil {
		return fmt.Errorf("encode open data channel input: %w", err)
	}
	if err := d.conn.WriteText(ctx, body); err != nil {
		return fmt.Errorf("send open data channel input: %w", err)
	}
	d.mu.Lock()
	d.openedAt = time.Now()
	d.mu.Unlock()
	return nil
}

// Run reads frames and resends unacknowledged input until ctx ends, the
// connection fails, the agent closes the channel (nil), or a timeout fires.
// Events() is closed on return.
func (d *DataChannel) Run(ctx context.Context) error {
	defer close(d.events)
	defer close(d.done)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); results <- d.readLoop(runCtx) }()
	go func() { defer wg.Done(); results <- d.schedulerLoop(runCtx) }()

	err := <-results
	cancel()
	wg.Wait()
	return err
}

func (d *DataChannel) emit(ctx context.Context, ev Event) error {
	select {
	case d.events <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *DataChannel) fail(ctx context.Context, err error) error {
	_ = d.emit(ctx, Event{Kind: EventError, Err: err})
	return err
}

// ---- read side ---------------------------------------------------------------

func (d *DataChannel) readLoop(ctx context.Context) error {
	for {
		text, data, err := d.conn.ReadFrame(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return d.fail(ctx, fmt.Errorf("read frame: %w", err))
		}
		if text {
			d.cfg.Logger.Debug("ignoring text frame", "bytes", len(data))
			continue
		}
		msg, err := Deserialize(data)
		if err != nil {
			d.cfg.Logger.Debug("skipping undecodable frame", "err", err)
			continue
		}
		if err := msg.Validate(); err != nil {
			d.cfg.Logger.Debug("skipping invalid message", "err", err, "type", msg.MessageType)
			continue
		}
		closed, err := d.handleMessage(ctx, msg)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return d.fail(ctx, err)
		}
		if closed {
			return nil
		}
	}
}

// handleMessage processes one validated message; closed is true after channel_closed.
func (d *DataChannel) handleMessage(ctx context.Context, msg ClientMessage) (closed bool, err error) {
	switch msg.MessageType {
	case AcknowledgeMessage:
		d.handleAcknowledge(msg)
	case ChannelClosedMessage:
		cc, err := msg.ChannelClosed()
		if err != nil {
			d.cfg.Logger.Debug("undecodable channel_closed", "err", err)
		}
		if err := d.emit(ctx, Event{Kind: EventChannelClosed, Closed: cc}); err != nil {
			return false, err
		}
		return true, nil
	case OutputStreamMessage:
		return false, d.handleOutput(ctx, msg)
	case StartPublicationMessage, PausePublicationMessage:
	default:
		d.cfg.Logger.Debug("ignoring message", "type", msg.MessageType)
	}
	return false, nil
}

func (d *DataChannel) sendAck(ctx context.Context, of ClientMessage) error {
	ackMsg := NewAck(of, d.cfg.Now())
	raw, err := ackMsg.Serialize()
	if err != nil {
		return err
	}
	return d.conn.WriteBinary(ctx, raw)
}

func (d *DataChannel) handleOutput(ctx context.Context, msg ClientMessage) error {
	switch {
	case msg.SequenceNumber == d.inExpected:
		pt := PayloadType(msg.PayloadType)
		if pt == HandshakeRequest || pt == HandshakeComplete {
			if err := d.sendAck(ctx, msg); err != nil {
				return err
			}
			if err := d.deliver(ctx, msg); err != nil {
				return err
			}
		} else {
			if err := d.deliver(ctx, msg); err != nil {
				return err
			}
			if err := d.sendAck(ctx, msg); err != nil {
				return err
			}
		}
		d.inExpected++
		for {
			next, ok := d.inBuf[d.inExpected]
			if !ok {
				return nil
			}
			delete(d.inBuf, d.inExpected)
			if err := d.deliver(ctx, next); err != nil {
				return err
			}
			d.inExpected++
		}
	case msg.SequenceNumber > d.inExpected:
		if len(d.inBuf) >= d.cfg.IncomingCapacity {
			d.cfg.Logger.Debug("incoming buffer full, dropping", "seq", msg.SequenceNumber)
			return nil
		}
		if err := d.sendAck(ctx, msg); err != nil {
			return err
		}
		d.inBuf[msg.SequenceNumber] = msg
	default:
		d.cfg.Logger.Debug("dropping duplicate", "seq", msg.SequenceNumber)
	}
	return nil
}

// deliver hands an in-order message to the consumer.
func (d *DataChannel) deliver(ctx context.Context, msg ClientMessage) error {
	pt := PayloadType(msg.PayloadType)
	if !d.firstDelivered {
		d.firstDelivered = true
		// Upstream ProcessFirstMessage: output before any handshake means a
		// plain shell session.
		if pt == Output {
			d.setSessionType(ShellPluginName)
			d.markHandshakeDone()
			if err := d.emit(ctx, Event{Kind: EventHandshakeComplete}); err != nil {
				return err
			}
		}
	}
	switch pt {
	case HandshakeRequest:
		return d.processHandshakeRequest(ctx, msg)
	case HandshakeComplete:
		return d.processHandshakeComplete(ctx, msg)
	case Output, StdErr:
		return d.emit(ctx, Event{Kind: EventOutput, PayloadType: pt, Data: msg.Payload})
	case ExitCode:
		return d.emit(ctx, Event{Kind: EventExitCode, PayloadType: pt, Data: msg.Payload})
	default:
		d.cfg.Logger.Debug("dropping payload", "payloadType", msg.PayloadType)
		return nil
	}
}

func (d *DataChannel) setSessionType(s string) {
	d.mu.Lock()
	d.sessionType = s
	d.mu.Unlock()
}

func (d *DataChannel) markHandshakeDone() {
	d.handshakeDoneOnce.Do(func() { close(d.handshakeDone) })
}

func isShellSession(name string) bool {
	switch name {
	case ShellPluginName, "InteractiveCommands", "NonInteractiveCommands":
		return true
	}
	return false
}

func (d *DataChannel) processHandshakeRequest(ctx context.Context, msg ClientMessage) error {
	req, err := msg.HandshakeRequest()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.agentVersion = req.AgentVersion
	d.mu.Unlock()

	resp := HandshakeResponsePayload{
		ClientVersion:          d.cfg.ClientVersion,
		ProcessedClientActions: []ProcessedClientAction{},
		Errors:                 []string{},
	}
	for _, action := range req.RequestedClientActions {
		processed := ProcessedClientAction{ActionType: action.ActionType}
		switch action.ActionType {
		case ActionSessionType:
			var st SessionTypeRequest
			switch {
			case json.Unmarshal(action.ActionParameters, &st) != nil:
				processed.ActionStatus = ActionFailed
				processed.Error = "cannot parse SessionType action parameters"
			case isShellSession(st.SessionType):
				d.setSessionType(ShellPluginName)
				processed.ActionStatus = ActionSuccess
				processed.ActionResult = st.SessionType
			case st.SessionType == "Port":
				processed.ActionStatus = ActionFailed
				processed.Error = "port sessions are not supported"
			default:
				processed.ActionStatus = ActionFailed
				processed.Error = fmt.Sprintf("Unknown session type %s", st.SessionType)
			}
		case ActionKMSEncryption:
			processed.ActionStatus = ActionFailed
			processed.Error = "KMS-encrypted sessions are not supported by Harbormaster"
		default:
			processed.ActionStatus = ActionUnsupported
			processed.Error = fmt.Sprintf("Unsupported action %s", action.ActionType)
		}
		if processed.Error != "" {
			resp.Errors = append(resp.Errors, processed.Error)
		}
		resp.ProcessedClientActions = append(resp.ProcessedClientActions, processed)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("encode handshake response: %w", err)
	}
	return d.sendInput(ctx, HandshakeResponse, body, false)
}

func (d *DataChannel) processHandshakeComplete(ctx context.Context, msg ClientMessage) error {
	complete, err := msg.HandshakeComplete()
	if err != nil {
		d.cfg.Logger.Debug("undecodable handshake complete", "err", err)
	}
	d.markHandshakeDone()
	if err := d.emit(ctx, Event{Kind: EventHandshakeComplete}); err != nil {
		return err
	}
	if complete.CustomerMessage != "" {
		return d.emit(ctx, Event{Kind: EventOutput, PayloadType: Output, Data: []byte(complete.CustomerMessage)})
	}
	return nil
}

func (d *DataChannel) handleAcknowledge(msg ClientMessage) {
	content, err := msg.AckContent()
	if err != nil {
		d.cfg.Logger.Debug("undecodable acknowledge", "err", err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, m := range d.outBuf {
		if m.seq != content.SequenceNumber {
			continue
		}
		sample := d.cfg.Now().Sub(m.lastSent)
		d.updateRTOLocked(sample)
		d.outBuf = append(d.outBuf[:i], d.outBuf[i+1:]...)
		d.signalSpaceLocked()
		return
	}
}

// updateRTOLocked applies the RFC 6298-style smoothing used upstream.
func (d *DataChannel) updateRTOLocked(sample time.Duration) {
	diff := d.rtt - sample
	if diff < 0 {
		diff = -diff
	}
	d.rttVar = time.Duration((1-rttVConstant)*float64(d.rttVar) + rttVConstant*float64(diff))
	d.rtt = time.Duration((1-rttConstant)*float64(d.rtt) + rttConstant*float64(sample))
	margin := 4 * d.rttVar
	if margin < ClockGranularity {
		margin = ClockGranularity
	}
	d.rto = min(d.rtt+margin, d.cfg.MaxRTO)
}

func (d *DataChannel) signalSpaceLocked() {
	close(d.spaceWait)
	d.spaceWait = make(chan struct{})
}

// ---- send side ---------------------------------------------------------------

// SendInput splits data into messages of at most 1024 bytes, one sequence
// number each. It blocks while the outgoing buffer is full.
func (d *DataChannel) SendInput(ctx context.Context, pt PayloadType, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), StreamDataPayloadSize)
		chunk := append([]byte(nil), data[:n]...)
		data = data[n:]
		// Upstream quirk (winpty): a lone line feed is sent as carriage return.
		if len(chunk) == 1 && chunk[0] == 0x0A {
			chunk[0] = 0x0D
		}
		if err := d.sendInput(ctx, pt, chunk, true); err != nil {
			return err
		}
	}
	return nil
}

// SendSize reports a terminal resize.
func (d *DataChannel) SendSize(ctx context.Context, cols, rows uint32) error {
	body, err := json.Marshal(SizeData{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	return d.sendInput(ctx, Size, body, true)
}

func (d *DataChannel) sendInput(ctx context.Context, pt PayloadType, payload []byte, waitForSpace bool) error {
	for {
		d.sendMu.Lock()
		d.mu.Lock()
		if waitForSpace && len(d.outBuf) >= d.cfg.OutgoingCapacity {
			wait := d.spaceWait
			d.mu.Unlock()
			d.sendMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			case <-d.done:
				return errChannelDead
			}
		}
		now := d.cfg.Now()
		msg := NewInput(d.outSeq, pt, payload, now)
		raw, err := msg.Serialize()
		if err != nil {
			d.mu.Unlock()
			d.sendMu.Unlock()
			return err
		}
		// Register before writing so an immediate ack finds the message.
		entry := &outgoingMessage{seq: d.outSeq, raw: raw, lastSent: now}
		d.outBuf = append(d.outBuf, entry)
		d.outSeq++
		d.mu.Unlock()

		err = d.conn.WriteBinary(ctx, raw)
		if err != nil {
			d.mu.Lock()
			d.removeLocked(entry)
			d.outSeq--
			d.mu.Unlock()
		}
		d.sendMu.Unlock()
		return err
	}
}

func (d *DataChannel) removeLocked(entry *outgoingMessage) {
	for i, m := range d.outBuf {
		if m == entry {
			d.outBuf = append(d.outBuf[:i], d.outBuf[i+1:]...)
			d.signalSpaceLocked()
			return
		}
	}
}

// ---- resend scheduler & handshake timer -------------------------------------

func (d *DataChannel) schedulerLoop(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.ResendInterval)
	defer ticker.Stop()

	d.mu.Lock()
	start := d.openedAt
	d.mu.Unlock()
	if start.IsZero() {
		start = time.Now()
	}
	timer := time.NewTimer(max(0, d.cfg.HandshakeTimeout-time.Since(start)))
	defer timer.Stop()
	timeout := timer.C
	handshake := d.handshakeDone

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-handshake:
			handshake, timeout = nil, nil
		case <-timeout:
			return d.fail(ctx, ErrHandshakeTimeout)
		case <-ticker.C:
			if err := d.resendOldest(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return d.fail(ctx, err)
			}
		}
	}
}

// resendOldest retransmits the oldest unacknowledged message when it is overdue.
func (d *DataChannel) resendOldest(ctx context.Context) error {
	d.mu.Lock()
	if len(d.outBuf) == 0 {
		d.mu.Unlock()
		return nil
	}
	oldest := d.outBuf[0]
	now := d.cfg.Now()
	if now.Sub(oldest.lastSent) <= d.rto {
		d.mu.Unlock()
		return nil
	}
	if oldest.attempts >= d.cfg.ResendMaxAttempts {
		d.mu.Unlock()
		return ErrResendTimeout
	}
	oldest.attempts++
	oldest.lastSent = now
	raw := oldest.raw
	d.mu.Unlock()
	return d.conn.WriteBinary(ctx, raw)
}
