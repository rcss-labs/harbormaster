package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/execbridge"
)

const testWait = 10 * time.Second

// wsClient drives the fake server like the session-manager-plugin would.
type wsClient struct {
	t    *testing.T
	ctx  context.Context
	conn *websocket.Conn
	seq  int64
}

func dialSession(t *testing.T, streamURL string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, streamURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", streamURL, err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return &wsClient{t: t, ctx: ctx, conn: conn}
}

func (c *wsClient) open(token string) {
	c.t.Helper()
	b, _ := json.Marshal(execbridge.OpenDataChannelInput{
		MessageSchemaVersion: "1.0", RequestID: "req-1", TokenValue: token, ClientID: "client-1", ClientVersion: "test",
	})
	if err := c.conn.Write(c.ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write open: %v", err)
	}
}

func (c *wsClient) read() (execbridge.ClientMessage, error) {
	ctx, cancel := context.WithTimeout(c.ctx, testWait)
	defer cancel()
	typ, b, err := c.conn.Read(ctx)
	if err != nil {
		return execbridge.ClientMessage{}, err
	}
	if typ != websocket.MessageBinary {
		c.t.Fatalf("frame type %v, want binary", typ)
	}
	m, err := execbridge.Deserialize(b)
	if err != nil {
		c.t.Fatalf("deserialize: %v", err)
	}
	if err := m.Validate(); err != nil {
		c.t.Fatalf("invalid message: %v", err)
	}
	return m, nil
}

// next returns the next non-acknowledge message and acks it.
func (c *wsClient) next() execbridge.ClientMessage {
	c.t.Helper()
	for {
		m, err := c.read()
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		if m.MessageType == execbridge.AcknowledgeMessage {
			continue
		}
		if m.MessageType == execbridge.OutputStreamMessage {
			c.send(execbridge.NewAck(m, time.Now()))
		}
		return m
	}
}

func (c *wsClient) send(m execbridge.ClientMessage) {
	c.t.Helper()
	b, err := m.Serialize()
	if err != nil {
		c.t.Fatalf("serialize: %v", err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) input(pt execbridge.PayloadType, payload []byte) {
	c.t.Helper()
	c.send(execbridge.NewInput(c.seq, pt, payload, time.Now()))
	c.seq++
}

// collectOutput reads output until it ends with the prompt suffix.
func (c *wsClient) collectOutput(until string) (string, []int64) {
	c.t.Helper()
	var buf bytes.Buffer
	var seqs []int64
	for !strings.HasSuffix(buf.String(), until) {
		m := c.next()
		if m.MessageType != execbridge.OutputStreamMessage || m.PayloadType != uint32(execbridge.Output) {
			c.t.Fatalf("unexpected message %s type %d while waiting for %q", m.MessageType, m.PayloadType, until)
		}
		buf.Write(m.Payload)
		seqs = append(seqs, m.SequenceNumber)
	}
	return buf.String(), seqs
}

// session starts a server, issues a session, dials it and returns the helpers.
func session(t *testing.T, o SSMServerOptions) (*SSMServer, string, *wsClient, string) {
	t.Helper()
	s := NewSSMServer(o)
	s.Start()
	t.Cleanup(s.Close)
	id, url, token := s.Issue("arn:aws:ecs:us-east-1:482109375512:task/core-services/abc", "payments-api", "/bin/sh")
	c := dialSession(t, url)
	return s, id, c, token
}

func (c *wsClient) handshakeWith(token string) execbridge.ClientMessage {
	c.t.Helper()
	c.open(token)
	c.next() // HandshakeRequest
	resp, _ := json.Marshal(execbridge.HandshakeResponsePayload{ClientVersion: "test"})
	c.input(execbridge.HandshakeResponse, resp)
	if done := c.next(); done.PayloadType != uint32(execbridge.HandshakeComplete) {
		c.t.Fatalf("expected HandshakeComplete, got type %d", done.PayloadType)
	}
	return c.next()
}

func waitSession(t *testing.T, s *SSMServer, id string, ok func(SSMSession) bool) SSMSession {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for {
		ss, found := s.Session(id)
		if found && ok(ss) {
			return ss
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never reached expected state; last=%+v found=%v", id, ss, found)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSSMServer_RejectsWrongToken(t *testing.T) {
	s, id, c, _ := session(t, SSMServerOptions{})
	c.open("not-the-token")
	_, err := c.read()
	if got := websocket.CloseStatus(err); got != 4403 {
		t.Fatalf("close status = %d (err %v), want 4403", got, err)
	}
	if ss, ok := s.Session(id); !ok || ss.Opened {
		t.Fatalf("session after wrong token = %+v ok=%v, want not opened", ss, ok)
	}
}

func TestSSMServer_HandshakeSequence(t *testing.T) {
	s, id, c, token := session(t, SSMServerOptions{})
	c.open(token)
	req := c.next()
	if req.SequenceNumber != 0 || req.PayloadType != uint32(execbridge.HandshakeRequest) || req.Flags != 1 {
		t.Fatalf("frame 1 seq/type/flags = %d/%d/%d, want 0/5/1", req.SequenceNumber, req.PayloadType, req.Flags)
	}
	hr, err := req.HandshakeRequest()
	if err != nil || hr.AgentVersion != "3.3.1230.0" {
		t.Fatalf("handshake request = %+v err=%v, want default agent version", hr, err)
	}
	resp, _ := json.Marshal(execbridge.HandshakeResponsePayload{ClientVersion: "test"})
	c.input(execbridge.HandshakeResponse, resp)
	done := c.next()
	if done.SequenceNumber != 1 || done.PayloadType != uint32(execbridge.HandshakeComplete) {
		t.Fatalf("frame 2 seq/type = %d/%d, want 1/7", done.SequenceNumber, done.PayloadType)
	}
	prompt := c.next()
	if prompt.SequenceNumber != 2 || prompt.PayloadType != uint32(execbridge.Output) || !strings.HasSuffix(string(prompt.Payload), "$ ") {
		t.Fatalf("frame 3 seq=%d payload=%q, want seq 2 prompt ending in \"$ \"", prompt.SequenceNumber, prompt.Payload)
	}
	ss := waitSession(t, s, id, func(ss SSMSession) bool { return ss.Opened })
	if ss.OpenInput.TokenValue != token {
		t.Fatalf("OpenInput token = %q, want issued token", ss.OpenInput.TokenValue)
	}
}

func TestSSMServer_ScriptedShell_EchoesAndRuns(t *testing.T) {
	s, id, c, token := session(t, SSMServerOptions{})
	c.handshakeWith(token)
	c.input(execbridge.Output, []byte("hostname\r"))
	out, _ := c.collectOutput("payments-api:/app $ ")
	if !strings.HasPrefix(out, "hostname") || !strings.Contains(out, "\r\nip-10-20-1-149.ec2.internal\r\n") {
		t.Fatalf("output = %q, want echo then hostname reply then prompt", out)
	}
	if ss, _ := s.Session(id); string(ss.Inputs) != "hostname\r" {
		t.Fatalf("Inputs = %q, want %q", ss.Inputs, "hostname\r")
	}
	c.input(execbridge.Output, []byte("nosuchcmd\r"))
	out, _ = c.collectOutput("$ ")
	if !strings.Contains(out, "sh: nosuchcmd: not found\r\n") {
		t.Fatalf("output = %q, want not-found message", out)
	}
}

func TestSSMServer_Exit_SendsChannelClosedThenCloses(t *testing.T) {
	s, id, c, token := session(t, SSMServerOptions{})
	c.handshakeWith(token)
	c.input(execbridge.Output, []byte("exit\r"))
	var closed execbridge.ChannelClosed
	for {
		m := c.next()
		if m.MessageType == execbridge.ChannelClosedMessage {
			var err error
			if closed, err = m.ChannelClosed(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if closed.SessionID != id || closed.MessageType != execbridge.ChannelClosedMessage {
		t.Fatalf("channel_closed = %+v, want SessionId %s", closed, id)
	}
	if _, err := c.read(); err == nil {
		t.Fatal("expected read error after channel_closed")
	}
	waitSession(t, s, id, func(ss SSMSession) bool { return ss.Closed })
}

func TestSSMServer_SizeRecorded(t *testing.T) {
	s, id, c, token := session(t, SSMServerOptions{})
	c.handshakeWith(token)
	c.input(execbridge.Size, []byte(`{"cols":100,"rows":40}`))
	ss := waitSession(t, s, id, func(ss SSMSession) bool { return ss.LastSize.Cols != 0 })
	if ss.LastSize != (execbridge.SizeData{Cols: 100, Rows: 40}) {
		t.Fatalf("LastSize = %+v, want 100x40", ss.LastSize)
	}
}

func TestSSMServer_ReorderOnce_SwapsTwoOutputs(t *testing.T) {
	_, _, c, token := session(t, SSMServerOptions{ReorderOnce: true})
	c.handshakeWith(token)
	c.input(execbridge.Output, []byte("ls\r"))
	_, seqs := c.collectOutput("$ ")
	// echo is seq 3; the reply (4) and the prompt (5) arrive swapped.
	want := []int64{3, 5, 4}
	if len(seqs) != 3 || seqs[0] != want[0] || seqs[1] != want[1] || seqs[2] != want[2] {
		// collectOutput stops at the prompt, which now arrives before the reply.
		for len(seqs) < 3 {
			m := c.next()
			seqs = append(seqs, m.SequenceNumber)
		}
	}
	if seqs[0] != 3 || seqs[1] != 5 || seqs[2] != 4 {
		t.Fatalf("arrival order = %v, want [3 5 4]", seqs)
	}
}

func TestSSMServer_DuplicateOnce_RepeatsHandshakeComplete(t *testing.T) {
	_, _, c, token := session(t, SSMServerOptions{DuplicateOnce: true})
	c.open(token)
	c.next()
	resp, _ := json.Marshal(execbridge.HandshakeResponsePayload{ClientVersion: "test"})
	c.input(execbridge.HandshakeResponse, resp)
	a, b := c.next(), c.next()
	if a.PayloadType != uint32(execbridge.HandshakeComplete) || b.PayloadType != uint32(execbridge.HandshakeComplete) || a.MessageID != b.MessageID {
		t.Fatalf("got %d/%d ids %s/%s, want the same HandshakeComplete twice", a.PayloadType, b.PayloadType, a.MessageID, b.MessageID)
	}
}

func TestSSMServer_Terminate_ClosesSocket(t *testing.T) {
	s, id, c, token := session(t, SSMServerOptions{})
	c.handshakeWith(token)
	if !s.Terminate(id) {
		t.Fatal("Terminate returned false for a known session")
	}
	if _, err := c.read(); err == nil {
		t.Fatal("expected read error after Terminate")
	}
	if ss, _ := s.Session(id); !ss.Terminated {
		t.Fatal("Terminated = false after Terminate")
	}
	if s.Terminate("ecs-execute-command-unknown") {
		t.Fatal("Terminate returned true for an unknown session")
	}
}
