package execbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---- harness ----------------------------------------------------------------

type testFrame struct {
	text bool
	data []byte
}

// fakeAgent is the SSM-agent side of an in-memory FrameConn. It records every
// frame the client writes and lets tests push frames to the client.
type fakeAgent struct {
	mu       sync.Mutex
	texts    [][]byte
	binaries [][]byte
	toClient chan testFrame
}

type pipeConn struct {
	agent  *fakeAgent
	closed chan struct{}
	once   sync.Once
}

func newPipeConn(t *testing.T) (FrameConn, *fakeAgent) {
	t.Helper()
	agent := &fakeAgent{toClient: make(chan testFrame, 1024)}
	conn := &pipeConn{agent: agent, closed: make(chan struct{})}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, agent
}

func (p *pipeConn) ReadFrame(ctx context.Context) (bool, []byte, error) {
	select {
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case <-p.closed:
		return false, nil, io.EOF
	case f := <-p.agent.toClient:
		return f.text, f.data, nil
	}
}

func (p *pipeConn) WriteBinary(_ context.Context, b []byte) error {
	p.agent.mu.Lock()
	defer p.agent.mu.Unlock()
	p.agent.binaries = append(p.agent.binaries, append([]byte(nil), b...))
	return nil
}

func (p *pipeConn) WriteText(_ context.Context, b []byte) error {
	p.agent.mu.Lock()
	defer p.agent.mu.Unlock()
	p.agent.texts = append(p.agent.texts, append([]byte(nil), b...))
	return nil
}

func (p *pipeConn) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (a *fakeAgent) push(msg ClientMessage) {
	raw, err := msg.Serialize()
	if err != nil {
		panic(err)
	}
	a.toClient <- testFrame{data: raw}
}

func (a *fakeAgent) pushRaw(b []byte) { a.toClient <- testFrame{data: b} }

func (a *fakeAgent) parsedFrames() []ClientMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []ClientMessage
	for _, b := range a.binaries {
		m, err := Deserialize(b)
		if err != nil {
			panic(err)
		}
		out = append(out, m)
	}
	return out
}

func (a *fakeAgent) rawBinaries() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.binaries...)
}

func (a *fakeAgent) inputs() []ClientMessage {
	var out []ClientMessage
	for _, m := range a.parsedFrames() {
		if m.MessageType == InputStreamMessage {
			out = append(out, m)
		}
	}
	return out
}

func (a *fakeAgent) acks() []AcknowledgeContent {
	var out []AcknowledgeContent
	for _, m := range a.parsedFrames() {
		if m.MessageType == AcknowledgeMessage {
			c, err := m.AckContent()
			if err != nil {
				panic(err)
			}
			out = append(out, c)
		}
	}
	return out
}

func (a *fakeAgent) acksFor(seq int64) int {
	n := 0
	for _, c := range a.acks() {
		if c.SequenceNumber == seq {
			n++
		}
	}
	return n
}

func (a *fakeAgent) openText() OpenDataChannelInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	var in OpenDataChannelInput
	if len(a.texts) == 0 {
		panic("no text frame written")
	}
	if err := json.Unmarshal(a.texts[0], &in); err != nil {
		panic(err)
	}
	return in
}

const testAgentVersion = "3.3.1230.0"

func agentMessage(seq int64, pt PayloadType, payload []byte) ClientMessage {
	return ClientMessage{
		MessageType:    OutputStreamMessage,
		SchemaVersion:  1,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: seq,
		MessageID:      uuid.New(),
		PayloadType:    uint32(pt),
		Payload:        payload,
	}
}

func handshakeRequestWith(seq int64, actions ...RequestedClientAction) ClientMessage {
	p, _ := json.Marshal(HandshakeRequestPayload{AgentVersion: testAgentVersion, RequestedClientActions: actions})
	return agentMessage(seq, HandshakeRequest, p)
}

func sessionTypeAction(sessionType string) RequestedClientAction {
	params, _ := json.Marshal(SessionTypeRequest{SessionType: sessionType, Properties: map[string]any{}})
	return RequestedClientAction{ActionType: ActionSessionType, ActionParameters: params}
}

func handshakeRequest(seq int64) ClientMessage {
	return handshakeRequestWith(seq, sessionTypeAction("Standard_Stream"))
}

func handshakeComplete(seq int64) ClientMessage {
	p, _ := json.Marshal(HandshakeCompletePayload{})
	return agentMessage(seq, HandshakeComplete, p)
}

func output(seq int64, text string) ClientMessage {
	return agentMessage(seq, Output, []byte(text))
}

func ack(of ClientMessage) ClientMessage {
	p, _ := json.Marshal(AcknowledgeContent{
		MessageType:         of.MessageType,
		MessageID:           of.MessageID.String(),
		SequenceNumber:      of.SequenceNumber,
		IsSequentialMessage: true,
	})
	return ClientMessage{
		MessageType:   AcknowledgeMessage,
		SchemaVersion: 1,
		CreatedDate:   uint64(time.Now().UnixMilli()),
		Flags:         FlagSYN | FlagFIN,
		MessageID:     uuid.New(),
		Payload:       p,
	}
}

func channelClosed(_ int64, out string) ClientMessage {
	p, _ := json.Marshal(ChannelClosed{SessionID: "sess-1", Output: out, MessageType: ChannelClosedMessage})
	return ClientMessage{
		MessageType:   ChannelClosedMessage,
		SchemaVersion: 1,
		CreatedDate:   uint64(time.Now().UnixMilli()),
		MessageID:     uuid.New(),
		Payload:       p,
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fixture runs a DataChannel against a fakeAgent.
type fixture struct {
	t      *testing.T
	dc     *DataChannel
	agent  *fakeAgent
	runErr chan error
	cancel context.CancelFunc
}

func startChannel(t *testing.T, mutate func(*DataChannelConfig)) *fixture {
	t.Helper()
	conn, agent := newPipeConn(t)
	cfg := DefaultConfig("cid", "tok")
	if mutate != nil {
		mutate(&cfg)
	}
	dc := NewDataChannel(conn, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fixture{t: t, dc: dc, agent: agent, runErr: make(chan error, 1), cancel: cancel}
	go func() { f.runErr <- dc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-f.runErr:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) next() Event {
	f.t.Helper()
	select {
	case ev, ok := <-f.dc.Events():
		if !ok {
			f.t.Fatal("events channel closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		f.t.Fatal("timed out waiting for event")
	}
	return Event{}
}

func (f *fixture) expectOutput(text string) {
	f.t.Helper()
	ev := f.next()
	if ev.Kind != EventOutput || string(ev.Data) != text {
		f.t.Fatalf("got event %+v, want output %q", ev, text)
	}
}

func (f *fixture) handshake() {
	f.t.Helper()
	f.agent.push(handshakeRequest(0))
	eventually(f.t, "handshake response", func() bool { return len(f.agent.inputs()) == 1 })
	f.agent.push(handshakeComplete(1))
	if ev := f.next(); ev.Kind != EventHandshakeComplete {
		f.t.Fatalf("got %+v, want EventHandshakeComplete", ev)
	}
}

func (f *fixture) waitRunReturn() error {
	f.t.Helper()
	select {
	case err := <-f.runErr:
		f.runErr <- err // keep for cleanup
		return err
	case <-time.After(2 * time.Second):
		f.t.Fatal("Run did not return")
	}
	return nil
}

// ---- tests ------------------------------------------------------------------

func TestDataChannel_Open_SendsOpenDataChannelTextFrame(t *testing.T) {
	conn, agent := newPipeConn(t)
	dc := NewDataChannel(conn, DefaultConfig("cid", "tok"))
	if err := dc.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	agent.mu.Lock()
	if len(agent.texts) != 1 || len(agent.binaries) != 0 {
		agent.mu.Unlock()
		t.Fatalf("want exactly one text frame, got %d text %d binary", len(agent.texts), len(agent.binaries))
	}
	if err := json.Unmarshal(agent.texts[0], &raw); err != nil {
		agent.mu.Unlock()
		t.Fatal(err)
	}
	agent.mu.Unlock()
	want := map[string]string{"MessageSchemaVersion": "1.0", "TokenValue": "tok", "ClientId": "cid", "ClientVersion": ClientVersion}
	for k, v := range want {
		if raw[k] != v {
			t.Errorf("%s = %q, want %q", k, raw[k], v)
		}
	}
	if _, err := uuid.Parse(raw["RequestId"]); err != nil {
		t.Errorf("RequestId %q is not a UUID: %v", raw["RequestId"], err)
	}
}

func TestDataChannel_Handshake_AcksThenRespondsThenCompletes(t *testing.T) {
	f := startChannel(t, nil)
	req := handshakeRequest(0)
	f.agent.push(req)
	eventually(f.t, "ack and response", func() bool { return len(f.agent.parsedFrames()) >= 2 })

	frames := f.agent.parsedFrames()
	if frames[0].MessageType != AcknowledgeMessage {
		t.Fatalf("first frame is %q, want ack", frames[0].MessageType)
	}
	c, _ := frames[0].AckContent()
	if c.SequenceNumber != 0 || c.MessageType != OutputStreamMessage || c.MessageID != req.MessageID.String() {
		t.Errorf("unexpected ack %+v", c)
	}
	resp := frames[1]
	if resp.MessageType != InputStreamMessage || resp.SequenceNumber != 0 || resp.PayloadType != uint32(HandshakeResponse) {
		t.Fatalf("unexpected second frame %+v", resp)
	}
	var payload HandshakeResponsePayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ClientVersion != ClientVersion || len(payload.ProcessedClientActions) != 1 ||
		payload.ProcessedClientActions[0].ActionType != ActionSessionType ||
		payload.ProcessedClientActions[0].ActionStatus != ActionSuccess {
		t.Errorf("unexpected handshake response %+v", payload)
	}

	f.agent.push(handshakeComplete(1))
	if ev := f.next(); ev.Kind != EventHandshakeComplete {
		t.Fatalf("got %+v, want EventHandshakeComplete", ev)
	}
	eventually(t, "ack of seq 1", func() bool { return f.agent.acksFor(1) == 1 })
	if got := f.dc.SessionType(); got != "Standard_Stream" {
		t.Errorf("SessionType = %q", got)
	}
	if got := f.dc.AgentVersion(); got != testAgentVersion {
		t.Errorf("AgentVersion = %q", got)
	}
}

func TestDataChannel_Handshake_KMSActionFailsClearly(t *testing.T) {
	f := startChannel(t, nil)
	kms := RequestedClientAction{ActionType: ActionKMSEncryption, ActionParameters: json.RawMessage(`{"KMSKeyID":"k"}`)}
	f.agent.push(handshakeRequestWith(0, kms))
	eventually(t, "handshake response", func() bool { return len(f.agent.inputs()) == 1 })

	var payload HandshakeResponsePayload
	if err := json.Unmarshal(f.agent.inputs()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ProcessedClientActions) != 1 {
		t.Fatalf("got %+v", payload)
	}
	got := payload.ProcessedClientActions[0]
	if got.ActionStatus != ActionFailed || !strings.Contains(got.Error, "KMS") {
		t.Errorf("KMS action = %+v, want ActionFailed mentioning KMS", got)
	}
}

func TestDataChannel_Output_InOrder_DeliveredAndAcked(t *testing.T) {
	f := startChannel(t, nil)
	f.handshake()
	f.agent.push(output(2, "a"))
	f.agent.push(output(3, "b"))
	f.expectOutput("a")
	f.expectOutput("b")
	eventually(t, "acks 2 and 3", func() bool { return f.agent.acksFor(2) == 1 && f.agent.acksFor(3) == 1 })
}

func TestDataChannel_Output_OutOfOrder_BufferedUntilGapFilled(t *testing.T) {
	f := startChannel(t, nil)
	f.handshake()
	f.agent.push(output(3, "b"))
	eventually(t, "ack of 3 on receipt", func() bool { return f.agent.acksFor(3) == 1 })
	f.agent.push(output(2, "a"))
	f.expectOutput("a")
	f.expectOutput("b")
	eventually(t, "ack of 2", func() bool { return f.agent.acksFor(2) == 1 })
	if n := f.agent.acksFor(3); n != 1 {
		t.Errorf("seq 3 acked %d times, want 1", n)
	}
	var order []int64
	for _, c := range f.agent.acks() {
		if c.SequenceNumber >= 2 {
			order = append(order, c.SequenceNumber)
		}
	}
	if len(order) != 2 || order[0] != 3 || order[1] != 2 {
		t.Errorf("ack order for seq>=2 = %v, want [3 2]", order)
	}
}

func TestDataChannel_Output_Duplicate_DroppedWithoutAck(t *testing.T) {
	f := startChannel(t, nil)
	f.handshake()
	f.agent.push(output(2, "a"))
	f.agent.push(output(2, "a"))
	f.agent.push(output(3, "barrier"))
	f.expectOutput("a")
	f.expectOutput("barrier")
	eventually(t, "ack of 3", func() bool { return f.agent.acksFor(3) == 1 })
	if n := f.agent.acksFor(2); n != 1 {
		t.Errorf("seq 2 acked %d times, want 1", n)
	}
}

func TestDataChannel_FirstOutputWithoutHandshake_IsShellFallback(t *testing.T) {
	f := startChannel(t, nil)
	f.agent.push(output(0, "$ "))
	if ev := f.next(); ev.Kind != EventHandshakeComplete {
		t.Fatalf("got %+v, want EventHandshakeComplete first", ev)
	}
	f.expectOutput("$ ")
	if got := f.dc.SessionType(); got != "Standard_Stream" {
		t.Errorf("SessionType = %q", got)
	}
}

func TestDataChannel_SendInput_SequenceNumbersIncrement(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) { c.ResendInterval = time.Hour })
	f.handshake()
	for _, s := range []string{"x", "y", "z"} {
		if err := f.dc.SendInput(context.Background(), Output, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	in := f.agent.inputs()
	if len(in) != 4 {
		t.Fatalf("got %d inputs, want 4 (handshake response + 3)", len(in))
	}
	for i, m := range in {
		if m.SequenceNumber != int64(i) || m.Flags != 0 || m.MessageType != InputStreamMessage {
			t.Errorf("input %d: seq %d flags %d type %q", i, m.SequenceNumber, m.Flags, m.MessageType)
		}
		if err := m.Validate(); err != nil {
			t.Errorf("input %d invalid: %v", i, err)
		}
	}
}

func TestDataChannel_SendInput_ChunksAt1024(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) { c.ResendInterval = time.Hour })
	data := bytes.Repeat([]byte("abcdefgh"), 2500/8+1)[:2500]
	if err := f.dc.SendInput(context.Background(), Output, data); err != nil {
		t.Fatal(err)
	}
	in := f.agent.inputs()
	if len(in) != 3 {
		t.Fatalf("got %d messages, want 3", len(in))
	}
	var joined []byte
	for i, want := range []int{1024, 1024, 452} {
		if len(in[i].Payload) != want || in[i].SequenceNumber != int64(i) {
			t.Errorf("message %d: len %d seq %d, want len %d seq %d", i, len(in[i].Payload), in[i].SequenceNumber, want, i)
		}
		joined = append(joined, in[i].Payload...)
	}
	if !bytes.Equal(joined, data) {
		t.Error("concatenated payloads differ from input")
	}
}

func TestDataChannel_SendInput_LoneLFBecomesCR(t *testing.T) {
	f := startChannel(t, nil)
	if err := f.dc.SendInput(context.Background(), Output, []byte{10}); err != nil {
		t.Fatal(err)
	}
	if err := f.dc.SendInput(context.Background(), Output, []byte("ab\n")); err != nil {
		t.Fatal(err)
	}
	in := f.agent.inputs()
	if !bytes.Equal(in[0].Payload, []byte{13}) {
		t.Errorf("lone LF payload = %v, want [13]", in[0].Payload)
	}
	if string(in[1].Payload) != "ab\n" {
		t.Errorf("payload = %q, want unchanged", in[1].Payload)
	}
}

func TestDataChannel_SendInput_BlocksWhenOutgoingBufferFull(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) { c.OutgoingCapacity = 2 })
	ctx := context.Background()
	for _, s := range []string{"1", "2"} {
		if err := f.dc.SendInput(ctx, Output, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := f.dc.SendInput(short, Output, []byte("3")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third SendInput err = %v, want DeadlineExceeded", err)
	}
	if n := len(f.agent.inputs()); n != 2 {
		t.Fatalf("blocked send leaked a frame: %d inputs", n)
	}

	f.agent.push(ack(f.agent.inputs()[0]))
	long, cancel2 := context.WithTimeout(ctx, 2*time.Second)
	defer cancel2()
	if err := f.dc.SendInput(long, Output, []byte("4")); err != nil {
		t.Fatalf("fourth SendInput after ack: %v", err)
	}
	in := f.agent.inputs()
	if len(in) != 3 || in[2].SequenceNumber != 2 || string(in[2].Payload) != "4" {
		t.Errorf("unexpected inputs after unblock: %+v", in)
	}
}

func TestDataChannel_Resend_UnackedOldestIsRetransmittedSameBytes(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) {
		c.InitialRTO = 5 * time.Millisecond
		c.ResendInterval = time.Millisecond
	})
	if err := f.dc.SendInput(context.Background(), Output, []byte("x")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "retransmission", func() bool { return len(f.agent.rawBinaries()) >= 2 })
	raw := f.agent.rawBinaries()
	if !bytes.Equal(raw[0], raw[1]) {
		t.Error("retransmitted frame differs from the original")
	}
}

func TestDataChannel_Resend_StopsAfterAck(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) {
		c.InitialRTO = 5 * time.Millisecond
		c.ResendInterval = time.Millisecond
	})
	if err := f.dc.SendInput(context.Background(), Output, []byte("x")); err != nil {
		t.Fatal(err)
	}
	first := f.agent.inputs()[0]
	f.agent.push(ack(first))
	eventually(t, "ack processed", func() bool { return f.dc.outstanding() == 0 })
	time.Sleep(10 * time.Millisecond) // let any resend already in flight land
	before := len(f.agent.rawBinaries())
	time.Sleep(30 * time.Millisecond)
	if after := len(f.agent.rawBinaries()); after != before {
		t.Errorf("%d frames written after ack", after-before)
	}
}

func TestDataChannel_Resend_MaxAttemptsEmitsErrResendTimeout(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) {
		c.InitialRTO = 2 * time.Millisecond
		c.ResendInterval = time.Millisecond
		c.ResendMaxAttempts = 3
	})
	if err := f.dc.SendInput(context.Background(), Output, []byte("x")); err != nil {
		t.Fatal(err)
	}
	ev := f.next()
	if ev.Kind != EventError || !errors.Is(ev.Err, ErrResendTimeout) {
		t.Fatalf("got %+v, want EventError(ErrResendTimeout)", ev)
	}
	if err := f.waitRunReturn(); !errors.Is(err, ErrResendTimeout) {
		t.Errorf("Run = %v, want ErrResendTimeout", err)
	}
}

func TestDataChannel_Ack_UpdatesRTOWithinBounds(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := startChannel(t, func(c *DataChannelConfig) {
		c.Now = clock.Now
		c.InitialRTO = 50 * time.Millisecond
		c.ResendInterval = time.Hour // keep the scheduler out of the way
	})
	if err := f.dc.SendInput(context.Background(), Output, []byte("x")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(300 * time.Millisecond)
	f.agent.push(ack(f.agent.inputs()[0]))
	eventually(t, "RTO update", func() bool { return f.dc.currentRTO() != 50*time.Millisecond })
	rto := f.dc.currentRTO()
	if rto <= 50*time.Millisecond || rto > time.Second {
		t.Errorf("RTO = %v, want in (50ms, 1s]", rto)
	}
}

func TestDataChannel_ChannelClosed_EmitsEventAndRunReturns(t *testing.T) {
	f := startChannel(t, nil)
	f.agent.push(channelClosed(5, ""))
	ev := f.next()
	if ev.Kind != EventChannelClosed || ev.Closed.SessionID != "sess-1" {
		t.Fatalf("got %+v", ev)
	}
	if err := f.waitRunReturn(); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
	select {
	case _, ok := <-f.dc.Events():
		if ok {
			t.Error("unexpected extra event")
		}
	case <-time.After(2 * time.Second):
		t.Error("Events not closed")
	}
}

func TestDataChannel_PublicationMessages_Ignored(t *testing.T) {
	f := startChannel(t, nil)
	f.agent.push(ClientMessage{MessageType: StartPublicationMessage, SchemaVersion: 1, MessageID: uuid.New()})
	f.agent.push(ClientMessage{MessageType: PausePublicationMessage, SchemaVersion: 1, MessageID: uuid.New()})
	f.agent.push(output(0, "barrier"))
	if ev := f.next(); ev.Kind != EventHandshakeComplete {
		t.Fatalf("got %+v, want only the fallback handshake event", ev)
	}
	f.expectOutput("barrier")
	eventually(t, "ack of 0", func() bool { return f.agent.acksFor(0) == 1 })
	if n := len(f.agent.acks()); n != 1 {
		t.Errorf("%d acks, want 1 (publication messages are not acked)", n)
	}
	select {
	case err := <-f.runErr:
		t.Fatalf("Run returned %v", err)
	default:
	}
}

func TestDataChannel_SendSize_PayloadTypeSizeJSON(t *testing.T) {
	f := startChannel(t, nil)
	if err := f.dc.SendSize(context.Background(), 120, 32); err != nil {
		t.Fatal(err)
	}
	m := f.agent.inputs()[0]
	if m.PayloadType != uint32(Size) {
		t.Errorf("PayloadType = %d, want 3", m.PayloadType)
	}
	var got map[string]uint32
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["cols"] != 120 || got["rows"] != 32 {
		t.Errorf("payload = %s", m.Payload)
	}
}

func TestDataChannel_HandshakeTimeout(t *testing.T) {
	f := startChannel(t, func(c *DataChannelConfig) { c.HandshakeTimeout = 20 * time.Millisecond })
	if err := f.waitRunReturn(); !errors.Is(err, ErrHandshakeTimeout) {
		t.Errorf("Run = %v, want ErrHandshakeTimeout", err)
	}
}

func TestDataChannel_InvalidFrame_IsLoggedAndSkipped(t *testing.T) {
	f := startChannel(t, nil)
	f.agent.pushRaw([]byte("garbage!!!"))
	f.agent.push(output(0, "ok"))
	if ev := f.next(); ev.Kind != EventHandshakeComplete {
		t.Fatalf("got %+v", ev)
	}
	f.expectOutput("ok")
	select {
	case err := <-f.runErr:
		t.Fatalf("Run returned %v", err)
	default:
	}
}

func TestDataChannel_SendInput_AfterRunReturns_ErrChannelClosed(t *testing.T) {
	f := startChannel(t, nil)
	f.agent.push(channelClosed(0, ""))
	if err := f.waitRunReturn(); err != nil {
		t.Fatal(err)
	}
	if err := f.dc.SendInput(context.Background(), Output, []byte("x")); !errors.Is(err, ErrChannelClosed) {
		t.Errorf("SendInput = %v, want ErrChannelClosed", err)
	}
	if err := f.dc.SendSize(context.Background(), 1, 1); !errors.Is(err, ErrChannelClosed) {
		t.Errorf("SendSize = %v, want ErrChannelClosed", err)
	}
	if n := len(f.agent.inputs()); n != 0 {
		t.Errorf("%d frames written after Run returned", n)
	}
}
