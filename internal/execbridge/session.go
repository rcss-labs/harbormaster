package execbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// Target names the container to open a shell in.
type Target struct {
	AccountID, Region, Cluster, TaskARN, Container, Command string
}

// Options configures Open.
type Options struct {
	Clients awsx.Clients
	// IdleTimeout closes the session after this long without Write/Resize (output does not count). Zero disables it.
	IdleTimeout time.Duration
	// MaxDuration closes the session this long after Open returned. Zero disables it.
	MaxDuration time.Duration
	// AllowInsecureStream permits ws:// to loopback (tests with the fake agent).
	AllowInsecureStream bool
	HandshakeTimeout    time.Duration
	// DataChannel overrides the data-channel tuning; ClientID and Token are always set by Open.
	DataChannel *DataChannelConfig
	Now         func() time.Time
	Logger      *slog.Logger
}

// CloseReason says why a session ended.
type CloseReason string

const (
	CloseByClient    CloseReason = "client"
	CloseByAgent     CloseReason = "channel_closed"
	CloseIdle        CloseReason = "idle_timeout"
	CloseMaxDuration CloseReason = "max_duration"
	CloseSSMError    CloseReason = "ssm_error"
)

// OpenError says which stage of Open failed: "execute_command", "stream_url", "dial" or "handshake".
type OpenError struct {
	Stage string
	Err   error
}

func (e *OpenError) Error() string { return fmt.Sprintf("open exec session: %s: %v", e.Stage, e.Err) }
func (e *OpenError) Unwrap() error { return e.Err }

const (
	terminateTimeout = 10 * time.Second
	// drainGrace is how long a slow consumer may still receive buffered output after the agent closed.
	drainGrace = 2 * time.Second
	// outBuffer is the Session event buffer.
	outBuffer = 256
)

// ErrSessionClosed is returned by Write and Resize after the session ended.
var ErrSessionClosed = ErrChannelClosed

// Session is one ECS Exec session, from ExecuteCommand to TerminateSession.
type Session struct {
	opts   Options
	target Target
	ssmTgt awsx.Target
	log    *slog.Logger
	now    func() time.Time

	id        string
	startedAt time.Time

	conn FrameConn
	dc   *DataChannel

	// ctx is the session lifetime: it drives the data channel and every write.
	ctx    context.Context
	cancel context.CancelFunc

	out chan Event

	handshaken      atomic.Bool
	handshakeSignal chan struct{}
	handshakeOnce   sync.Once

	runDone  chan struct{} // closed when DataChannel.Run returned
	runErr   error         // valid after runDone
	pumpDone chan struct{} // closed when the event pump finished

	closeOnce    sync.Once
	drainOnce    sync.Once
	closing      chan struct{} // closed when Close begins
	drainOver    chan struct{} // closed when buffered output may be discarded
	done         chan struct{}
	closeReason  CloseReason
	closeErr     error
	closedAt     time.Time
	idleTimer    *time.Timer
	maxTimer     *time.Timer
	timerMu      sync.Mutex
	timersClosed bool

	// resizeMu serialises size bookkeeping and sending so sizes reach the agent in order.
	resizeMu           sync.Mutex
	wantCols, wantRows uint32
	haveWant           bool
	sentCols, sentRows uint32
	haveSent           bool
}

// Open starts an exec session and returns once the data-channel handshake is complete.
// After ExecuteCommand succeeded, every failure path still calls TerminateSession.
func Open(ctx context.Context, o Options, t Target) (*Session, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = DefaultHandshakeTimeout
	}
	tgt := awsx.Target{AccountID: t.AccountID, Region: t.Region}

	ecsAPI, err := o.Clients.ECS(ctx, tgt)
	if err != nil {
		return nil, &OpenError{Stage: "execute_command", Err: err}
	}
	out, err := ecsAPI.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String(t.Cluster),
		Task:        aws.String(t.TaskARN),
		Container:   aws.String(t.Container),
		Command:     aws.String(t.Command),
		Interactive: true,
	})
	if err != nil {
		return nil, &OpenError{Stage: "execute_command", Err: err}
	}
	if out == nil || out.Session == nil || aws.ToString(out.Session.SessionId) == "" {
		return nil, &OpenError{Stage: "execute_command", Err: errors.New("ExecuteCommand returned no session")}
	}
	sessionID := aws.ToString(out.Session.SessionId)
	streamURL := aws.ToString(out.Session.StreamUrl)
	token := aws.ToString(out.Session.TokenValue)

	fail := func(stage string, err error) (*Session, error) {
		terminateSession(o, tgt, sessionID)
		return nil, &OpenError{Stage: stage, Err: err}
	}

	if err := ValidateStreamURL(streamURL, o.AllowInsecureStream); err != nil {
		return fail("stream_url", err)
	}
	creds, err := o.Clients.Credentials(ctx, tgt)
	if err != nil {
		return fail("dial", fmt.Errorf("credentials: %w", err))
	}
	conn, err := Dial(ctx, streamURL, DialOptions{
		Region: t.Region, Credentials: creds, AllowInsecure: o.AllowInsecureStream, Logger: o.Logger,
	})
	if err != nil {
		return fail("dial", err)
	}

	cfg := DefaultConfig("", "")
	if o.DataChannel != nil {
		cfg = *o.DataChannel
	}
	cfg.ClientID = uuid.NewString()
	cfg.Token = token
	cfg.HandshakeTimeout = o.HandshakeTimeout
	if cfg.Logger == nil {
		cfg.Logger = o.Logger
	}
	if cfg.Now == nil {
		cfg.Now = o.Now
	}
	dc := NewDataChannel(conn, cfg)
	if err := dc.Open(ctx); err != nil {
		_ = conn.Close()
		return fail("handshake", err)
	}

	s := &Session{
		opts: o, target: t, ssmTgt: tgt, log: o.Logger, now: o.Now,
		id: sessionID, startedAt: o.Now(),
		conn: conn, dc: dc,
		out:             make(chan Event, outBuffer),
		handshakeSignal: make(chan struct{}),
		runDone:         make(chan struct{}),
		pumpDone:        make(chan struct{}),
		closing:         make(chan struct{}),
		drainOver:       make(chan struct{}),
		done:            make(chan struct{}),
	}
	// The session outlives the request context that opened it.
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.pump()
	go s.run()

	timeout := time.NewTimer(o.HandshakeTimeout + time.Second)
	defer timeout.Stop()
	var failure error
	select {
	case <-s.handshakeSignal:
	case <-s.runDone:
		failure = s.runErr
		if failure == nil {
			failure = errors.New("data channel closed before the handshake completed")
		}
	case <-timeout.C:
		failure = ErrHandshakeTimeout
	case <-ctx.Done():
		failure = ctx.Err()
	}
	if failure != nil {
		s.Close(CloseSSMError)
		return nil, &OpenError{Stage: "handshake", Err: failure}
	}

	s.timerMu.Lock()
	if o.IdleTimeout > 0 {
		s.idleTimer = time.AfterFunc(o.IdleTimeout, func() { s.Close(CloseIdle) })
	}
	if o.MaxDuration > 0 {
		s.maxTimer = time.AfterFunc(o.MaxDuration, func() { s.Close(CloseMaxDuration) })
	}
	s.timerMu.Unlock()
	return s, nil
}

// terminateSession ends the SSM session with a fresh context; failures are only logged.
func terminateSession(o Options, tgt awsx.Target, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), terminateTimeout)
	defer cancel()
	api, err := o.Clients.SSM(ctx, tgt)
	if err != nil {
		o.Logger.Warn("execbridge: terminate session: no SSM client", "session", sessionID, "error", err)
		return
	}
	if _, err := api.TerminateSession(ctx, &ssm.TerminateSessionInput{SessionId: aws.String(sessionID)}); err != nil {
		o.Logger.Warn("execbridge: terminate session failed", "session", sessionID, "error", err)
	}
}

func (s *Session) ID() string            { return s.id }
func (s *Session) StartedAt() time.Time  { return s.startedAt }
func (s *Session) Events() <-chan Event  { return s.out }
func (s *Session) Done() <-chan struct{} { return s.done }

// Duration is the time from Open to Close, or to now while the session is open.
func (s *Session) Duration() time.Duration {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if !s.closedAt.IsZero() {
		return s.closedAt.Sub(s.startedAt)
	}
	return s.now().Sub(s.startedAt)
}

// run drives the data channel and turns its end into a Close.
func (s *Session) run() {
	err := s.dc.Run(s.ctx)
	s.runErr = err
	close(s.runDone)
	switch {
	case err == nil:
		s.Close(CloseByAgent)
	case s.ctx.Err() != nil:
		// Close already cancelled us; its reason stands.
		s.Close(CloseByClient)
	default:
		s.closeWith(CloseSSMError, err)
	}
}

// pump forwards output and exit-code events and reacts to the handshake. It
// never writes to the agent itself (the read loop must not wait on us).
func (s *Session) pump() {
	defer close(s.pumpDone)
	for ev := range s.dc.Events() {
		switch ev.Kind {
		case EventHandshakeComplete:
			s.handshaken.Store(true)
			s.handshakeOnce.Do(func() { close(s.handshakeSignal) })
			go s.flushSize()
		case EventOutput, EventExitCode:
			select {
			case s.out <- ev:
			case <-s.drainOver:
			}
		}
	}
}

func (s *Session) touch() {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.idleTimer != nil && !s.timersClosed {
		s.idleTimer.Reset(s.opts.IdleTimeout)
	}
}

// Write sends keystrokes. The write uses the session's lifetime context, never
// ctx: an expiring write context would make the websocket close the connection.
func (s *Session) Write(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.closing:
		return ErrSessionClosed
	default:
	}
	s.touch()
	return s.dc.SendInput(s.ctx, Output, b)
}

// Resize records the terminal size and sends it when it changed. A size set
// before the handshake finished is sent right after it.
func (s *Session) Resize(ctx context.Context, cols, rows uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.closing:
		return ErrSessionClosed
	default:
	}
	s.touch()
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	s.wantCols, s.wantRows, s.haveWant = cols, rows, true
	if !s.handshaken.Load() {
		return nil
	}
	return s.sendWantedSizeLocked()
}

func (s *Session) flushSize() {
	s.resizeMu.Lock()
	defer s.resizeMu.Unlock()
	if err := s.sendWantedSizeLocked(); err != nil {
		s.log.Debug("execbridge: sending remembered size failed", "error", err)
	}
}

func (s *Session) sendWantedSizeLocked() error {
	if !s.haveWant || (s.haveSent && s.sentCols == s.wantCols && s.sentRows == s.wantRows) {
		return nil
	}
	if err := s.dc.SendSize(s.ctx, s.wantCols, s.wantRows); err != nil {
		return err
	}
	s.sentCols, s.sentRows, s.haveSent = s.wantCols, s.wantRows, true
	return nil
}

// Close ends the session. It is idempotent: the first call wins and every call
// returns once the session is fully closed.
func (s *Session) Close(reason CloseReason) { s.closeWith(reason, nil) }

func (s *Session) closeWith(reason CloseReason, cause error) {
	s.closeOnce.Do(func() { s.finish(reason, cause) })
	<-s.done
}

func (s *Session) finish(reason CloseReason, cause error) {
	s.timerMu.Lock()
	s.timersClosed = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.maxTimer != nil {
		s.maxTimer.Stop()
	}
	s.closedAt = s.now()
	s.timerMu.Unlock()

	s.closeReason, s.closeErr = reason, cause
	close(s.closing)
	if reason == CloseByClient {
		s.stopDraining()
	} else {
		time.AfterFunc(drainGrace, s.stopDraining)
	}

	s.cancel()
	<-s.runDone // DataChannel.Run never closes the conn; we do, afterwards
	_ = s.conn.Close()
	terminateSession(s.opts, s.ssmTgt, s.id)
	<-s.pumpDone
	s.stopDraining()

	final := Event{Kind: EventClosed, Reason: reason, Err: cause}
	for {
		select {
		case s.out <- final:
			close(s.out)
			close(s.done)
			return
		default:
			// Buffer full of stale output nobody read: drop the oldest so the close notice always lands.
			select {
			case <-s.out:
			default:
			}
		}
	}
}

func (s *Session) stopDraining() { s.drainOnce.Do(func() { close(s.drainOver) }) }
