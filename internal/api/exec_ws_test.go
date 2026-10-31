package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const wsWait = 10 * time.Second

// execRig is a running API wired to a fake SSM agent server, with the mockup estate polled and
// the dev-seed users (dana, oncall, ...) able to sign in.
type execRig struct {
	srv *httptest.Server
	fk  *fake.Fake
	st  *store.Store
	ssm *fake.SSMServer
}

func newExecRig(t *testing.T, mutate func(*Server)) *execRig {
	t.Helper()
	st := storetest.Open(t)
	log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	cfg := testConfig()
	cfg.ExecMaxSessionsPerUser = 3
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name  string
		admin bool
	}{{"pavel", true}, {"omer", false}} {
		if _, err := st.CreateUser(t.Context(), u.name, hash, u.admin, false); err != nil {
			t.Fatal(err)
		}
	}
	setup, err := auth.NewSetup(t.Context(), st, log)
	if err != nil {
		t.Fatal(err)
	}
	fk := fake.New(fake.MockupEstate(time.Now))
	ssm := fake.NewSSMServer(fake.SSMServerOptions{})
	ssm.Start()
	t.Cleanup(ssm.Close)
	fk.ExecServer = ssm

	s := Server{
		Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
		Clients: fk, Prober: fk, Cfg: cfg, Log: log, Now: time.Now,
		ExecAllowInsecureStream: true,
	}
	if mutate != nil {
		mutate(&s)
	}
	srv := httptest.NewServer(New(s).Handler())
	t.Cleanup(srv.Close)

	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)
	return &execRig{srv: srv, fk: fk, st: st, ssm: ssm}
}

// taskWithAgent finds a RUNNING task of service whose first container reports the given agent
// status (via exec-targets) and returns its id and container name.
func (r *execRig) taskWithAgent(t *testing.T, service, agent string) (id, container string) {
	t.Helper()
	admin := login(t, r.srv, "pavel", testPassword)
	for _, task := range getTasks(t, admin, "/api/tasks?filter=all&limit=500").Items {
		if task.Service == nil || task.Service.Name != service || task.Status != "RUNNING" {
			continue
		}
		targets := getExecTargets(t, admin, task.ID, http.StatusOK)
		if len(targets.Containers) > 0 && targets.Containers[0].ExecAgent == agent {
			return task.ID, targets.Containers[0].Name
		}
	}
	t.Fatalf("no RUNNING %s task with agent %s", service, agent)
	return "", ""
}

func (r *execRig) paymentsTask(t *testing.T) string {
	t.Helper()
	return taskIDWhere(t, r.srv, "payments-api", "", "RUNNING")
}

// taskShortID is the first 12 characters of the task id (last ARN segment), as used in operation text.
func taskShortID(t *testing.T, taskID string) string {
	t.Helper()
	_, _, arn, err := model.ParseTaskID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	return arn[strings.LastIndex(arn, "/")+1:][:12]
}

type wsFrame struct {
	Type      string `json:"type"`
	State     string `json:"state"`
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
}

// dialExec opens the exec socket as c; query is the part after "?".
func (r *execRig) dialExec(t *testing.T, c *apiClient, taskID, query string, header http.Header) (*wsClient, *http.Response, error) {
	t.Helper()
	u, _ := url.Parse(r.srv.URL)
	if header == nil {
		header = http.Header{}
	}
	if c != nil {
		for _, ck := range c.http.Jar.Cookies(u) {
			header.Add("Cookie", ck.Name+"="+ck.Value)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), wsWait)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+u.Host+"/api/tasks/"+taskID+"/exec?"+query, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { conn.CloseNow() })
	return &wsClient{t: t, conn: conn}, resp, nil
}

func (r *execRig) open(t *testing.T, c *apiClient, taskID, container, command string) *wsClient {
	t.Helper()
	q := url.Values{"container": {container}, "command": {command}}.Encode()
	w, _, err := r.dialExec(t, c, taskID, q, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return w
}

func startFrame(csrf string) []byte {
	return []byte(`{"type":"start","csrf":"` + csrf + `","cols":120,"rows":32}`)
}

func (w *wsClient) sendText(b []byte) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wsWait)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageText, b); err != nil {
		w.t.Fatalf("write text: %v", err)
	}
}

func (w *wsClient) sendBinary(b string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wsWait)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageBinary, []byte(b)); err != nil {
		w.t.Fatalf("write binary: %v", err)
	}
}

// next reads one frame; text frames are decoded into f.
func (w *wsClient) next() (typ websocket.MessageType, f wsFrame, data []byte, err error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), wsWait)
	defer cancel()
	typ, data, err = w.conn.Read(ctx)
	if err == nil && typ == websocket.MessageText {
		_ = json.Unmarshal(data, &f)
	}
	return
}

// waitStatus reads frames until a status frame with state arrives, collecting binary output.
func (w *wsClient) waitStatus(state string) (wsFrame, []byte) {
	w.t.Helper()
	var out []byte
	for {
		typ, f, data, err := w.next()
		if err != nil {
			w.t.Fatalf("waiting for status %q: %v (output %q)", state, err, out)
		}
		if typ == websocket.MessageBinary {
			out = append(out, data...)
			continue
		}
		if f.Type == "status" && f.State == state {
			return f, out
		}
		if f.Type == "error" {
			w.t.Fatalf("waiting for status %q: got error frame %+v", state, f)
		}
	}
}

// waitOutput reads until the accumulated binary output contains want.
func (w *wsClient) waitOutput(want string) string {
	w.t.Helper()
	var out []byte
	for !bytes.Contains(out, []byte(want)) {
		typ, _, data, err := w.next()
		if err != nil {
			w.t.Fatalf("waiting for output %q: %v (got %q)", want, err, out)
		}
		if typ == websocket.MessageBinary {
			out = append(out, data...)
		}
	}
	return string(out)
}

// drain reads until the socket closes and returns the text frames, output bytes and close code.
func (w *wsClient) drain() (frames []wsFrame, output []byte, code websocket.StatusCode) {
	w.t.Helper()
	for {
		typ, f, data, err := w.next()
		if err != nil {
			code = websocket.CloseStatus(err)
			if code == -1 {
				w.t.Fatalf("socket ended without a close frame: %v", err)
			}
			return
		}
		if typ == websocket.MessageBinary {
			output = append(output, data...)
		} else {
			frames = append(frames, f)
		}
	}
}

func lastFrameOfType(frames []wsFrame, typ string) (wsFrame, bool) {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Type == typ {
			return frames[i], true
		}
	}
	return wsFrame{}, false
}

// expectError sends a valid start and expects an error frame with code and close code.
func (r *execRig) expectRefusal(t *testing.T, c *apiClient, taskID, container, command, wantCode string, wantClose websocket.StatusCode) wsFrame {
	t.Helper()
	w := r.open(t, c, taskID, container, command)
	w.sendText(startFrame(c.csrf))
	frames, _, code := w.drain()
	e, ok := lastFrameOfType(frames, "error")
	if !ok || e.Code != wantCode || code != wantClose {
		t.Fatalf("frames %+v close %d, want error %q close %d", frames, code, wantCode, wantClose)
	}
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wsWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// operationTexts returns the operation_log rows of the given harbormaster source matching prefix.
func (r *execRig) operations(t *testing.T, like string) []struct {
	Text, Actor string
	Details     map[string]any
} {
	t.Helper()
	rows, err := r.st.Pool.Query(t.Context(),
		`select text, coalesce(actor_username, ''), coalesce(details, '{}'::jsonb) from operation_log
		 where source = 'harbormaster' and text like $1 order by id`, like)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct {
		Text, Actor string
		Details     map[string]any
	}
	for rows.Next() {
		var row struct {
			Text, Actor string
			Details     map[string]any
		}
		if err := rows.Scan(&row.Text, &row.Actor, &row.Details); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func TestExecWS_MissingCookie_401BeforeUpgrade(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	_, resp, err := r.dialExec(t, nil, id, "container=payments-api&command=/bin/sh", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v resp = %v, want 401 before upgrade", err, resp)
	}
}

func TestExecWS_BadOrigin_Rejected(t *testing.T) {
	r := newExecRig(t, func(s *Server) { s.Cfg.BaseURL = "http://hm.example.test" })
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	_, resp, err := r.dialExec(t, c, id, "container=payments-api&command=/bin/sh", http.Header{"Origin": {"https://evil.example"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v resp = %v, want 403 before upgrade", err, resp)
	}
}

func TestExecWS_FirstMessageMustBeStartWithCSRF(t *testing.T) {
	r := newExecRig(t, func(s *Server) { s.ExecStartTimeout = 100 * time.Millisecond })
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)

	cases := []struct {
		name     string
		send     func(w *wsClient)
		wantCode string
		wantWS   websocket.StatusCode
	}{
		{"wrong csrf", func(w *wsClient) { w.sendText(startFrame("not-the-token")) }, "csrf_invalid", 4401},
		{"binary first", func(w *wsClient) { w.sendBinary("hello") }, "bad_request", 4400},
		{"malformed json", func(w *wsClient) { w.sendText([]byte("{nope")) }, "bad_request", 4400},
		{"resize first", func(w *wsClient) { w.sendText([]byte(`{"type":"resize","cols":80,"rows":24}`)) }, "bad_request", 4400},
		{"silence", func(w *wsClient) {}, "bad_request", 4400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := r.open(t, c, id, "payments-api", "/bin/sh")
			tc.send(w)
			frames, _, code := w.drain()
			e, ok := lastFrameOfType(frames, "error")
			if !ok || e.Code != tc.wantCode || code != tc.wantWS {
				t.Fatalf("frames %+v close %d, want %q close %d", frames, code, tc.wantCode, tc.wantWS)
			}
		})
	}
	if n := r.fk.Calls()["ExecuteCommand"]; n != 0 {
		t.Errorf("ExecuteCommand called %d times before a valid start", n)
	}
}

func TestExecWS_InvisibleTask_4404(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	omer := login(t, r.srv, "omer", testPassword)
	r.expectRefusal(t, omer, id, "payments-api", "/bin/sh", "not_found", 4404)
	r.expectRefusal(t, omer, "bm90LWEtdGFzaw", "payments-api", "/bin/sh", "not_found", 4404)
}

func TestExecWS_ViewWithoutTerminal_Forbidden(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	dana := login(t, r.srv, "dana", testPassword)
	e := r.expectRefusal(t, dana, id, "payments-api", "/bin/sh", "forbidden", 4409)
	if !strings.HasPrefix(e.Message, "You need Terminal on ") {
		t.Errorf("message = %q", e.Message)
	}
	if n := r.fk.Calls()["ExecuteCommand"]; n != 0 {
		t.Errorf("ExecuteCommand called %d times", n)
	}
}

func TestExecWS_RejectsCommandOutsideAllowlist(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	for _, cmd := range []string{"/bin/sh;id", "", "/bin/zsh", "sh"} {
		e := r.expectRefusal(t, c, id, "payments-api", cmd, "invalid_command", 4409)
		if e.Message != "Command must be /bin/sh or /bin/bash." {
			t.Errorf("message = %q", e.Message)
		}
	}
	if n := r.fk.Calls()["ExecuteCommand"]; n != 0 {
		t.Errorf("ExecuteCommand called %d times for rejected commands", n)
	}
}

func TestExecWS_ExecDisabled_4409(t *testing.T) {
	r := newExecRig(t, nil)
	id := taskIDWhere(t, r.srv, "cert-renewer", "", "RUNNING")
	c := login(t, r.srv, "pavel", testPassword)
	container := getExecTargets(t, c, id, http.StatusOK).Containers[0].Name
	e := r.expectRefusal(t, c, id, container, "/bin/sh", "exec_disabled", 4409)
	if e.Message != "ECS Exec is off for cert-renewer." {
		t.Errorf("message = %q", e.Message)
	}
}

func TestExecWS_AgentNotRunning_4409(t *testing.T) {
	r := newExecRig(t, nil)
	id, container := r.taskWithAgent(t, "auth-service", "PENDING")
	c := login(t, r.srv, "pavel", testPassword)
	e := r.expectRefusal(t, c, id, container, "/bin/sh", "agent_not_running", 4409)
	if e.Message != "The ExecuteCommandAgent is not running in container "+container+"." {
		t.Errorf("message = %q", e.Message)
	}
	if n := r.fk.Calls()["ExecuteCommand"]; n != 0 {
		t.Errorf("ExecuteCommand called %d times", n)
	}
}

func TestExecWS_UnknownContainer_4404(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	r.expectRefusal(t, c, id, "no-such-container", "/bin/sh", "not_found", 4404)
	r.expectRefusal(t, c, id, "", "/bin/sh", "not_found", 4404)
}

func TestExecWS_SessionLimit_FourthConnectionRefused(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)

	var open []*wsClient
	for i := 0; i < 3; i++ {
		w := r.open(t, c, id, "payments-api", "/bin/sh")
		w.sendText(startFrame(c.csrf))
		w.waitStatus("connected")
		open = append(open, w)
	}
	e := r.expectRefusal(t, c, id, "payments-api", "/bin/sh", "session_limit", 4409)
	if e.Message != "You already have 3 open terminal sessions." {
		t.Errorf("message = %q", e.Message)
	}

	open[0].conn.Close(websocket.StatusNormalClosure, "")
	eventually(t, "a slot to free up", func() bool {
		w := r.open(t, c, id, "payments-api", "/bin/sh")
		w.sendText(startFrame(c.csrf))
		for {
			typ, f, _, err := w.next()
			if err != nil {
				return false
			}
			if typ == websocket.MessageText && f.Type == "status" && f.State == "connected" {
				open = append(open, w)
				return true
			}
			if typ == websocket.MessageText && f.Type == "error" {
				w.conn.CloseNow()
				return false
			}
		}
	})
}

func TestExecWS_HappyPath_StatusOutputAndOplog(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))

	_, f, _, err := w.next()
	if err != nil || f.Type != "status" || f.State != "connecting" {
		t.Fatalf("first frame = %+v err %v, want status connecting", f, err)
	}
	connected, out := w.waitStatus("connected")
	if !strings.HasPrefix(connected.SessionID, "ecs-execute-command-") {
		t.Fatalf("session_id = %q", connected.SessionID)
	}
	if !strings.Contains(string(out)+w.waitOutput("$ "), "payments-api:/app $ ") {
		t.Errorf("no prompt in output")
	}

	// The start-frame size reaches the agent without any resize frame.
	eventually(t, "start size", func() bool {
		s, _ := r.ssm.Session(connected.SessionID)
		return s.LastSize.Cols == 120 && s.LastSize.Rows == 32
	})

	w.sendBinary("hostname\r")
	w.waitOutput("ip-10-20-1-149")

	w.sendText([]byte(`{"type":"resize","cols":100,"rows":40}`))
	eventually(t, "resize", func() bool {
		s, _ := r.ssm.Session(connected.SessionID)
		return s.LastSize.Cols == 100 && s.LastSize.Rows == 40 && s.SizeCount == 2
	})

	short := taskShortID(t, id)
	ops := r.operations(t, "Opened terminal session%")
	if len(ops) != 1 || ops[0].Text != "Opened terminal session on task "+short+" (payments-api, /bin/sh)." || ops[0].Actor != "pavel" {
		t.Fatalf("operations = %+v", ops)
	}
	if ops[0].Details["session_id"] != connected.SessionID || ops[0].Details["container"] != "payments-api" ||
		ops[0].Details["command"] != "/bin/sh" || ops[0].Details["task_arn"] == "" {
		t.Errorf("details = %+v", ops[0].Details)
	}
	for _, op := range r.operations(t, "%") {
		if strings.Contains(op.Text, "hostname") || strings.Contains(toJSON(op.Details), "hostname") {
			t.Errorf("keystrokes leaked into the operations log: %+v", op)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestExecWS_ClientClose_TerminatesAndLogsClose(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))
	connected, _ := w.waitStatus("connected")

	if err := w.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Logf("close: %v", err)
	}
	eventually(t, "session terminated", func() bool {
		s, _ := r.ssm.Session(connected.SessionID)
		return s.Terminated
	})
	pattern := regexp.MustCompile(`^Closed terminal session on task ` + taskShortID(t, id) + ` \(payments-api\) after \d+s\.$`)
	eventually(t, "close operation row", func() bool { return len(r.operations(t, "Closed terminal session%")) == 1 })
	ops := r.operations(t, "Closed terminal session%")
	if !pattern.MatchString(ops[0].Text) || ops[0].Details["reason"] != "client" || ops[0].Details["session_id"] != connected.SessionID {
		t.Errorf("close row = %+v", ops[0])
	}
	if _, ok := ops[0].Details["duration_ms"]; !ok {
		t.Errorf("details lack duration_ms: %+v", ops[0].Details)
	}
}

func TestExecWS_AgentExit_StatusClosedThen1000(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))
	connected, _ := w.waitStatus("connected")
	w.sendBinary("exit\r")
	frames, _, code := w.drain()
	closed, ok := lastFrameOfType(frames, "status")
	if !ok || closed.State != "closed" || closed.Reason != "channel_closed" || closed.SessionID != connected.SessionID || code != websocket.StatusNormalClosure {
		t.Fatalf("frames %+v close %d", frames, code)
	}
	s, _ := r.ssm.Session(connected.SessionID)
	eventually(t, "terminated", func() bool { s, _ = r.ssm.Session(connected.SessionID); return s.Terminated })
	eventually(t, "close row", func() bool { return len(r.operations(t, "Closed terminal session%")) == 1 })
}

func TestExecWS_IdleTimeout_4408(t *testing.T) {
	r := newExecRig(t, func(s *Server) { s.Cfg.ExecIdleTimeout = 50 * time.Millisecond })
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))
	frames, _, code := w.drain()
	closed, ok := lastFrameOfType(frames, "status")
	if !ok || closed.State != "closed" || closed.Reason != "idle_timeout" || code != 4408 {
		t.Fatalf("frames %+v close %d", frames, code)
	}
	eventually(t, "close row", func() bool { return len(r.operations(t, "Closed terminal session%")) == 1 })
	if ops := r.operations(t, "Closed terminal session%"); ops[0].Details["reason"] != "idle_timeout" {
		t.Errorf("close row = %+v", ops[0])
	}
}

func TestExecWS_UpstreamDrop_4502(t *testing.T) {
	r := newExecRig(t, nil)
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))
	connected, _ := w.waitStatus("connected")
	if !r.ssm.Terminate(connected.SessionID) {
		t.Fatal("fake did not know the session")
	}
	frames, _, code := w.drain()
	e, ok := lastFrameOfType(frames, "error")
	if !ok || e.Code != "aws_error" || code != 4502 {
		t.Fatalf("frames %+v close %d", frames, code)
	}
	eventually(t, "close row", func() bool { return len(r.operations(t, "Closed terminal session%")) == 1 })
}

func TestExecWS_ExecuteCommandFailure_4502(t *testing.T) {
	r := newExecRig(t, nil)
	r.fk.ExecServer = nil // the fake ECS then fails ExecuteCommand
	id := r.paymentsTask(t)
	c := login(t, r.srv, "pavel", testPassword)
	r.expectRefusal(t, c, id, "payments-api", "/bin/sh", "aws_error", 4502)
	if ops := r.operations(t, "%terminal session%"); len(ops) != 0 {
		t.Errorf("operations = %+v, want none", ops)
	}
}

func TestExecWS_StandaloneTask_Refused(t *testing.T) {
	r := newExecRig(t, nil)
	id := taskIDWhere(t, r.srv, "", "release-build", "RUNNING")
	c := login(t, r.srv, "pavel", testPassword)
	container := getExecTargets(t, c, id, http.StatusOK).Containers[0].Name
	r.expectRefusal(t, c, id, container, "/bin/sh", "exec_disabled", 4409)
	if ops := r.operations(t, "%terminal session%"); len(ops) != 0 {
		t.Errorf("operations = %+v, want none", ops)
	}
}

func TestDurationText(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"}, {7 * time.Second, "7s"}, {252 * time.Second, "4m 12s"}, {65 * time.Second, "1m 05s"},
		{3725 * time.Second, "1h 02m 05s"}, {7200 * time.Second, "2h 00m 00s"}, {7*time.Second + 900*time.Millisecond, "7s"},
	} {
		if got := durationText(tc.d); got != tc.want {
			t.Errorf("durationText(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
