package fake

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/execbridge"
)

// DefaultAgentVersion is what the fake agent reports in its handshake request.
const DefaultAgentVersion = "3.3.1230.0"

const (
	openFrameTimeout  = 5 * time.Second
	handshakeTimeout  = 10 * time.Second
	writeTimeout      = 10 * time.Second
	statusInvalidAuth = websocket.StatusCode(4403)
)

// SSMServerOptions are the fault-injection knobs of the fake agent.
type SSMServerOptions struct {
	// ReorderOnce swaps the first two output messages that are queued back to back.
	ReorderOnce bool
	// DuplicateOnce sends the HandshakeComplete message twice.
	DuplicateOnce bool
	// DropAcks makes the agent never acknowledge the client's input messages.
	DropAcks bool
	// AgentVersion defaults to DefaultAgentVersion.
	AgentVersion string
}

// SSMSession is a snapshot of what the fake agent observed for one session.
type SSMSession struct {
	Opened, Closed bool
	Terminated     bool
	Inputs         []byte
	LastSize       execbridge.SizeData
	OpenInput      execbridge.OpenDataChannelInput
}

type ssmSessionState struct {
	SSMSession
	taskARN, container, command string
	token                       string
	cancel                      context.CancelFunc // set while a socket is being served
}

// SSMServer is a test double of the SSM data-channel endpoint plus the agent behind it.
type SSMServer struct {
	opts SSMServerOptions

	mu       sync.Mutex
	sessions map[string]*ssmSessionState
	url      string
	srv      *http.Server
	closed   bool
	reorder  bool // ReorderOnce still pending

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSSMServer creates a server; call Start to listen.
func NewSSMServer(o SSMServerOptions) *SSMServer {
	if o.AgentVersion == "" {
		o.AgentVersion = DefaultAgentVersion
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SSMServer{opts: o, sessions: map[string]*ssmSessionState{}, reorder: o.ReorderOnce, ctx: ctx, cancel: cancel}
}

// Start listens on a loopback port and returns the base URL (http://127.0.0.1:port).
func (s *SSMServer) Start() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("fake ssm server: listen: %v", err))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/data-channel/{sessionID}", s.handle)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.url = "http://" + ln.Addr().String()
	s.srv = srv
	url := s.url
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return url
}

func (s *SSMServer) baseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// Close stops the listener, drops every socket and waits for the session goroutines.
func (s *SSMServer) Close() {
	s.mu.Lock()
	s.closed = true
	srv := s.srv
	s.mu.Unlock()
	s.cancel()
	if srv != nil {
		_ = srv.Close()
	}
	s.wg.Wait()
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Issue registers a session the way ECS ExecuteCommand does and returns its stream credentials.
func (s *SSMServer) Issue(taskARN, container, command string) (sessionID, streamURL, token string) {
	sessionID = "ecs-execute-command-" + randomHex(9)[:17]
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token = base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = &ssmSessionState{taskARN: taskARN, container: container, command: command, token: token}
	return sessionID, s.url + "/v1/data-channel/" + sessionID + "?role=publish_subscribe", token
}

// Session returns a snapshot of the session's observed state.
func (s *SSMServer) Session(id string) (SSMSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[id]
	if !ok {
		return SSMSession{}, false
	}
	snap := st.SSMSession
	snap.Inputs = append([]byte(nil), st.Inputs...)
	return snap, true
}

// Terminate marks the session terminated and drops its socket. It reports whether the session exists.
func (s *SSMServer) Terminate(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return false
	}
	st.Terminated = true
	if st.cancel != nil {
		st.cancel()
	}
	return true
}

func (s *SSMServer) update(id string, fn func(*ssmSessionState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.sessions[id]; ok {
		fn(st)
	}
}

func (s *SSMServer) handle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionID")
	s.mu.Lock()
	st, known := s.sessions[id]
	if !known || s.closed {
		s.mu.Unlock()
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	s.wg.Add(1)
	ctx, cancel := context.WithCancel(s.ctx)
	st.cancel = cancel
	token, container := st.token, st.container
	s.mu.Unlock()
	defer s.wg.Done()
	defer cancel()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	sess := &agentSession{s: s, id: id, container: container, token: token, conn: conn}
	graceful := sess.run(ctx)
	if graceful {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	} else {
		_ = conn.CloseNow()
	}
}

// agentSession is the per-socket state; everything but write is touched by one goroutine only.
type agentSession struct {
	s         *SSMServer
	id        string
	container string
	token     string
	conn      *websocket.Conn

	writeMu sync.Mutex
	nextSeq int64
	acked   map[int64]bool
	seen    map[uuid.UUID]bool
	line    []byte

	shell         bool // handshake finished; inputs are treated as keystrokes
	exitRequested bool
}

func (a *agentSession) write(ctx context.Context, m execbridge.ClientMessage) error {
	b, err := m.Serialize()
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return a.conn.Write(ctx, websocket.MessageBinary, b)
}

func (a *agentSession) newOutput(seq int64, pt execbridge.PayloadType, flags uint64, payload []byte) execbridge.ClientMessage {
	return execbridge.ClientMessage{
		MessageType:    execbridge.OutputStreamMessage,
		SchemaVersion:  1,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: seq,
		Flags:          flags,
		MessageID:      uuid.New(),
		PayloadType:    uint32(pt),
		Payload:        payload,
	}
}

// reserve allocates sequence numbers in call order.
func (a *agentSession) reserve(pt execbridge.PayloadType, flags uint64, payload []byte) execbridge.ClientMessage {
	m := a.newOutput(a.nextSeq, pt, flags, payload)
	a.nextSeq++
	return m
}

func (a *agentSession) read(ctx context.Context) (execbridge.ClientMessage, error) {
	typ, b, err := a.conn.Read(ctx)
	if err != nil {
		return execbridge.ClientMessage{}, err
	}
	if typ != websocket.MessageBinary {
		return execbridge.ClientMessage{}, fmt.Errorf("unexpected %v frame", typ)
	}
	m, err := execbridge.Deserialize(b)
	if err != nil {
		return m, err
	}
	return m, m.Validate()
}

// run serves the socket. It reports whether the close should be a graceful 1000.
func (a *agentSession) run(ctx context.Context) (graceful bool) {
	a.acked = map[int64]bool{}
	a.seen = map[uuid.UUID]bool{}

	// 1. The first frame is the text OpenDataChannelInput carrying the token.
	openCtx, cancel := context.WithTimeout(ctx, openFrameTimeout)
	typ, b, err := a.conn.Read(openCtx)
	cancel()
	if err != nil {
		return false
	}
	var open execbridge.OpenDataChannelInput
	if typ != websocket.MessageText || json.Unmarshal(b, &open) != nil ||
		subtle.ConstantTimeCompare([]byte(open.TokenValue), []byte(a.token)) != 1 {
		_ = a.conn.Close(statusInvalidAuth, "invalid token")
		return false
	}
	a.s.update(a.id, func(st *ssmSessionState) { st.Opened = true; st.OpenInput = open })

	// 2. Handshake.
	hsCtx, hsCancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hsCancel()
	req, _ := json.Marshal(execbridge.HandshakeRequestPayload{
		AgentVersion: a.s.opts.AgentVersion,
		RequestedClientActions: []execbridge.RequestedClientAction{{
			ActionType:       execbridge.ActionSessionType,
			ActionParameters: json.RawMessage(`{"SessionType":"Standard_Stream","Properties":null}`),
		}},
	})
	if a.write(hsCtx, a.reserve(execbridge.HandshakeRequest, execbridge.FlagSYN, req)) != nil {
		return false
	}
	gotResponse := false
	for !(a.acked[0] && gotResponse) {
		m, err := a.read(hsCtx)
		if err != nil {
			return false
		}
		if m.MessageType == execbridge.InputStreamMessage && m.PayloadType == uint32(execbridge.HandshakeResponse) {
			gotResponse = true
		}
		if a.handleInbound(hsCtx, m) != nil {
			return false
		}
	}
	complete, _ := json.Marshal(execbridge.HandshakeCompletePayload{HandshakeTimeToComplete: 1000000})
	hc := a.reserve(execbridge.HandshakeComplete, 0, complete)
	if a.write(hsCtx, hc) != nil {
		return false
	}
	if a.s.opts.DuplicateOnce && a.write(hsCtx, hc) != nil {
		return false
	}
	for !a.acked[hc.SequenceNumber] {
		m, err := a.read(hsCtx)
		if err != nil || a.handleInbound(hsCtx, m) != nil {
			return false
		}
	}
	if a.write(ctx, a.reserve(execbridge.Output, 0, []byte(a.prompt()))) != nil {
		return false
	}

	// 3. Interactive loop.
	a.shell = true
	for {
		m, err := a.read(ctx)
		if err != nil {
			return false
		}
		if a.handleInbound(ctx, m) != nil {
			return false
		}
		if a.exitRequested {
			return true
		}
	}
}

func (a *agentSession) prompt() string { return a.container + ":/app $ " }

// handleInbound processes one client message: acks bookkeeping, input acks, resize and keystrokes.
func (a *agentSession) handleInbound(ctx context.Context, m execbridge.ClientMessage) error {
	switch m.MessageType {
	case execbridge.AcknowledgeMessage:
		if ack, err := m.AckContent(); err == nil {
			a.acked[ack.SequenceNumber] = true
		}
		return nil
	case execbridge.InputStreamMessage:
	default:
		return nil
	}
	if !a.s.opts.DropAcks {
		if err := a.write(ctx, execbridge.NewAck(m, time.Now())); err != nil {
			return err
		}
	}
	if a.seen[m.MessageID] { // a retransmission
		return nil
	}
	a.seen[m.MessageID] = true
	if !a.shell {
		return nil
	}
	switch execbridge.PayloadType(m.PayloadType) {
	case execbridge.Size:
		var size execbridge.SizeData
		if json.Unmarshal(m.Payload, &size) == nil {
			a.s.update(a.id, func(st *ssmSessionState) { st.LastSize = size })
		}
	case execbridge.Output:
		return a.keystrokes(ctx, m.Payload)
	}
	return nil
}

// keystrokes records and echoes typed bytes (one echo message per call, flushed
// before each line runs) and runs each completed line.
func (a *agentSession) keystrokes(ctx context.Context, data []byte) error {
	a.s.update(a.id, func(st *ssmSessionState) { st.Inputs = append(st.Inputs, data...) })
	var echo []byte
	flush := func() error {
		if len(echo) == 0 {
			return nil
		}
		err := a.send(ctx, string(echo))
		echo = echo[:0]
		return err
	}
	for _, b := range data {
		switch b {
		case '\r':
			echo = append(echo, '\r', '\n')
			if err := flush(); err != nil {
				return err
			}
			line := strings.TrimSpace(string(a.line))
			a.line = a.line[:0]
			if err := a.runLine(ctx, line); err != nil || a.exitRequested {
				return err
			}
		case 0x7f, '\b':
			if len(a.line) > 0 {
				a.line = a.line[:len(a.line)-1]
				echo = append(echo, "\b \b"...)
			}
		default:
			a.line = append(a.line, b)
			echo = append(echo, b)
		}
	}
	return flush()
}

// send emits one output message.
func (a *agentSession) send(ctx context.Context, text string) error {
	return a.write(ctx, a.reserve(execbridge.Output, 0, []byte(text)))
}

// sendPair emits two back-to-back output messages, swapping them once when ReorderOnce is armed.
func (a *agentSession) sendPair(ctx context.Context, first, second string) error {
	m1 := a.reserve(execbridge.Output, 0, []byte(first))
	m2 := a.reserve(execbridge.Output, 0, []byte(second))
	a.s.mu.Lock()
	swap := a.s.reorder
	a.s.reorder = false
	a.s.mu.Unlock()
	if swap {
		m1, m2 = m2, m1
	}
	if err := a.write(ctx, m1); err != nil {
		return err
	}
	return a.write(ctx, m2)
}

var scriptedCommands = map[string]string{
	"help":                "Commands: help ls pwd whoami hostname env ps df uptime cat exit",
	"ls":                  "bin  dev  etc  home  proc  sys  tmp  usr  var",
	"pwd":                 "/app",
	"whoami":              "app",
	"hostname":            "ip-10-20-1-149.ec2.internal",
	"env":                 "HOME=/home/app\nAWS_REGION=us-east-1",
	"ps":                  "PID   USER     COMMAND\n    1 app      node server.js\n   27 app      /bin/sh",
	"df -h":               "Filesystem      Size  Used Avail Use% Mounted on\noverlay          30G  4.1G   26G  14% /\ntmpfs            64M     0   64M   0% /dev",
	"cat /etc/os-release": "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.20.3\nPRETTY_NAME=\"Alpine Linux v3.20\"",
	"uptime":              " 12:00:00 up 3 days,  4:12,  0 users,  load average: 0.08, 0.05, 0.01",
}

func (a *agentSession) runLine(ctx context.Context, line string) error {
	switch line {
	case "":
		return a.send(ctx, a.prompt())
	case "exit":
		return a.closeChannel(ctx)
	}
	var reply string
	if out, ok := scriptedCommands[line]; ok {
		reply = strings.ReplaceAll(out, "\n", "\r\n") + "\r\n"
	} else {
		word, _, _ := strings.Cut(line, " ")
		reply = "sh: " + word + ": not found\r\n"
	}
	return a.sendPair(ctx, reply, a.prompt())
}

// closeChannel tells the client the channel is closed; the caller then closes the socket with 1000.
func (a *agentSession) closeChannel(ctx context.Context) error {
	payload, _ := json.Marshal(execbridge.ChannelClosed{
		MessageID:     uuid.NewString(),
		CreatedDate:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		SessionID:     a.id,
		MessageType:   execbridge.ChannelClosedMessage,
		SchemaVersion: 1,
	})
	msg := a.newOutput(0, 0, 0, payload)
	msg.MessageType = execbridge.ChannelClosedMessage
	if err := a.write(ctx, msg); err != nil {
		return err
	}
	a.s.update(a.id, func(st *ssmSessionState) { st.Closed = true })
	a.exitRequested = true
	return nil
}
