package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func stagingWebID() string {
	return serviceID(stagingAccountID, "us-east-1", "staging-web", "web")
}

type logLineBody struct {
	T         time.Time `json:"t"`
	Container string    `json:"container"`
	TaskID    string    `json:"task_id"`
	Message   string    `json:"message"`
}

type logsBody struct {
	Group      string `json:"group"`
	Region     string `json:"region"`
	Containers []struct {
		Name         string `json:"name"`
		Driver       string `json:"driver"`
		Supported    bool   `json:"supported"`
		StreamPrefix string `json:"stream_prefix"`
		Reason       string `json:"reason"`
	} `json:"containers"`
	Lines       []logLineBody `json:"lines"`
	OlderBefore *time.Time    `json:"older_before"`
	Truncated   bool          `json:"truncated"`
}

func getLogs(t *testing.T, c *apiClient, id, query string) (int, logsBody) {
	t.Helper()
	resp := c.get("/api/services/" + id + "/logs" + query)
	var body logsBody
	if resp.StatusCode == http.StatusOK {
		decode(t, resp, &body)
	} else {
		resp.Body.Close()
	}
	return resp.StatusCode, body
}

func logsFixture(t *testing.T) (*httptest.Server, *fake.Fake, *store.Store) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	for _, u := range []string{"oncall", "omer"} {
		setPassword(t, st, u)
	}
	pollAll(t, st, fk)
	return srv, fk, st
}

func TestLogsHistory_OperatorGetsLines(t *testing.T) {
	srv, _, _ := logsFixture(t)
	status, body := getLogs(t, login(t, srv, "oncall", testPassword), paymentsID(), "")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if body.Group != "/ecs/core-services/payments-api" || body.Region != "us-east-1" {
		t.Errorf("group/region = %q %q", body.Group, body.Region)
	}
	if len(body.Containers) != 2 {
		t.Fatalf("containers = %+v, want 2", body.Containers)
	}
	main, sidecar := body.Containers[0], body.Containers[1]
	if main.Name != "payments-api" || main.Driver != "awslogs" || !main.Supported || main.StreamPrefix != "ecs/payments-api/" {
		t.Errorf("main container = %+v", main)
	}
	if sidecar.Name != "otel-collector" || sidecar.Driver != "awsfirelens" || sidecar.Supported || sidecar.Reason != "driver" {
		t.Errorf("sidecar = %+v", sidecar)
	}
	if len(body.Lines) != 500 || !body.Truncated || body.OlderBefore == nil {
		t.Errorf("lines=%d truncated=%v older_before=%v; want 500 true non-nil", len(body.Lines), body.Truncated, body.OlderBefore)
	}
	for _, l := range body.Lines {
		if l.Container == "" || l.TaskID == "" || l.Message == "" || l.T.IsZero() {
			t.Fatalf("incomplete line %+v", l)
		}
	}
}

func TestLogsHistory_ContainerFilter(t *testing.T) {
	srv, _, _ := logsFixture(t)
	c := login(t, srv, "oncall", testPassword)
	status, body := getLogs(t, c, paymentsID(), "?container=payments-api")
	if status != http.StatusOK || len(body.Lines) == 0 {
		t.Fatalf("status %d lines %d", status, len(body.Lines))
	}
	for _, l := range body.Lines {
		if l.Container != "payments-api" {
			t.Fatalf("line from %q, want only payments-api", l.Container)
		}
	}
	// The firelens sidecar does not log through CloudWatch awslogs: selecting it is an empty page.
	status, body = getLogs(t, c, paymentsID(), "?container=otel-collector")
	if status != http.StatusOK || len(body.Lines) != 0 || body.Group != "" {
		t.Errorf("sidecar: status %d group %q lines %d; want 200, no group, no lines", status, body.Group, len(body.Lines))
	}
}

// paymentsTaskIDs are the ids (last ARN segment) of the stored payments-api tasks.
func paymentsTaskIDs(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, _, err := st.ListTasks(t.Context(), store.TaskQuery{
		TargetFilter: store.TargetFilter{AccountID: homelabAccountID, Region: "us-east-1"},
		Vis:          store.Visibility{Admin: true}, Cluster: "core-services", Service: "payments-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ARN[strings.LastIndex(row.ARN, "/")+1:])
	}
	if len(ids) < 2 {
		t.Fatalf("need at least two payments-api tasks to tell them apart, have %v", ids)
	}
	return ids
}

func TestLogs_TaskParamRestrictsStreams(t *testing.T) {
	srv, _, st := logsFixture(t)
	c := login(t, srv, "oncall", testPassword)
	ids := paymentsTaskIDs(t, st)

	_, all := getLogs(t, c, paymentsID(), "")
	seen := map[string]bool{}
	for _, l := range all.Lines {
		seen[l.TaskID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("unfiltered logs come from %d task(s); the filter would prove nothing", len(seen))
	}

	for _, id := range ids {
		// the drawer sends the 8 character short id; a full id works too
		for _, param := range []string{id[:8], id} {
			status, body := getLogs(t, c, paymentsID(), "?task="+param)
			if status != http.StatusOK {
				t.Fatalf("task=%s: status %d", param, status)
			}
			for _, l := range body.Lines {
				if l.TaskID != id {
					t.Fatalf("task=%s returned a line of task %s", param, l.TaskID)
				}
			}
		}
	}

	// the container filter still applies inside the task
	status, body := getLogs(t, c, paymentsID(), "?task="+ids[0][:8]+"&container=payments-api")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, l := range body.Lines {
		if l.TaskID != ids[0] || l.Container != "payments-api" {
			t.Fatalf("line %+v outside task %s / payments-api", l, ids[0])
		}
	}
}

func TestLogs_TaskParamUnknownIs404(t *testing.T) {
	srv, _, _ := logsFixture(t)
	c := login(t, srv, "oncall", testPassword)
	if status, _ := getLogs(t, c, paymentsID(), "?task=deadbeefdeadbeef"); status != http.StatusNotFound {
		t.Errorf("unknown task: status %d, want 404", status)
	}
	if status, _ := getLogs(t, c, paymentsID(), "?task=abc"); status != http.StatusUnprocessableEntity {
		t.Errorf("short task id: status %d, want 422", status)
	}
}

func TestLogsHistory_ViewOnlyIs403WithMessage(t *testing.T) {
	srv, _, _ := logsFixture(t)
	resp := login(t, srv, "omer", testPassword).get("/api/services/" + stagingWebID() + "/logs")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	code, msg := errorCode(t, resp)
	if code != "forbidden" || msg != "You need Logs on Staging" {
		t.Errorf("error = %q %q", code, msg)
	}
}

func TestLogsHistory_NoViewIs404(t *testing.T) {
	srv, _, _ := logsFixture(t)
	resp := login(t, srv, "omer", testPassword).get("/api/services/" + paymentsID() + "/logs")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLogsHistory_UnknownContainerIs422(t *testing.T) {
	srv, _, _ := logsFixture(t)
	resp := login(t, srv, "oncall", testPassword).get("/api/services/" + paymentsID() + "/logs?container=nope")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "invalid" {
		t.Errorf("code %q, want invalid", code)
	}
}

func TestLogsHistory_BeforePages(t *testing.T) {
	srv, _, _ := logsFixture(t)
	c := login(t, srv, "oncall", testPassword)
	_, first := getLogs(t, c, paymentsID(), "")
	if first.OlderBefore == nil {
		t.Fatal("first page has no older_before")
	}
	status, second := getLogs(t, c, paymentsID(), "?before="+first.OlderBefore.Format(time.RFC3339Nano))
	if status != http.StatusOK || len(second.Lines) == 0 {
		t.Fatalf("second page status %d lines %d", status, len(second.Lines))
	}
	for _, l := range second.Lines {
		if l.T.After(*first.OlderBefore) {
			t.Fatalf("line at %v is newer than before=%v", l.T, first.OlderBefore)
		}
	}
	oldestFirst := first.Lines[0].T
	if second.Lines[len(second.Lines)-1].T.After(oldestFirst) {
		t.Errorf("second page reaches %v, past the oldest line of page one %v", second.Lines[len(second.Lines)-1].T, oldestFirst)
	}
	resp := c.get("/api/services/" + paymentsID() + "/logs?before=yesterday")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad before: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

// useSplunkDriver switches every container of the staging web task definition to the splunk driver, so the
// service has no container that logs to CloudWatch. Call it before the first poll: task definitions are read once.
func useSplunkDriver(t *testing.T, fk *fake.Fake) {
	t.Helper()
	fk.Estate.Mu.Lock()
	var family string
	for _, s := range fk.Estate.Services {
		if aws.ToString(s.Service.ServiceName) == "web" && s.AccountID == stagingAccountID {
			for _, td := range fk.Estate.TaskDefinitions {
				if aws.ToString(td.Definition.TaskDefinitionArn) == aws.ToString(s.Service.TaskDefinition) {
					family = aws.ToString(td.Definition.Family)
				}
			}
		}
	}
	for i := range fk.Estate.TaskDefinitions {
		def := &fk.Estate.TaskDefinitions[i].Definition
		if family != "" && aws.ToString(def.Family) == family {
			for j := range def.ContainerDefinitions {
				def.ContainerDefinitions[j].LogConfiguration = &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverSplunk}
			}
		}
	}
	fk.Estate.Mu.Unlock()
	if family == "" {
		t.Fatal("staging web task definition not found in the fake estate")
	}
}

func TestLogsHistory_NoAwslogsContainerIs200Empty(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	useSplunkDriver(t, fk)
	pollAll(t, st, fk)

	status, body := getLogs(t, login(t, srv, "oncall", testPassword), stagingWebID(), "")
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if body.Group != "" || body.Lines == nil || len(body.Lines) != 0 || len(body.Containers) == 0 {
		t.Errorf("group=%q lines=%v containers=%d; want empty group, empty non-nil lines, listed containers", body.Group, body.Lines, len(body.Containers))
	}
	for _, c := range body.Containers {
		if c.Supported || c.Driver != "splunk" || c.Reason != "driver" {
			t.Errorf("container %+v should be unsupported by driver", c)
		}
	}
}

// ---- live tail ----

// tailFixture serves the API with a small tail idle timeout and a 2-tail limit, sharing the store and
// fake of the standard fixture. It returns the server, its limiter and the user ids.
func tailFixture(t *testing.T, idle time.Duration, tweak ...func(*Server)) (*httptest.Server, *Server, *store.Store) {
	t.Helper()
	_, fk, st := logsFixture(t)
	fk.LiveTailInterval = 20 * time.Millisecond
	cfg := testConfig()
	cfg.LogTailIdle = idle
	cfg.LogTailsPerUser = 2
	api := New(Server{Store: st, Auth: auth.NewService(st, cfg), Clients: fk, Prober: fk, Cfg: cfg, Now: time.Now})
	for _, f := range tweak {
		f(api)
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, api, st
}

func dialTail(t *testing.T, srv *httptest.Server, c *apiClient, id, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/services/" + id + "/logs/tail" + query
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func sendFrame(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readFrame returns the next JSON frame as a map, or the close error.
func readFrame(conn *websocket.Conn, wait time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// expectClose reads until the connection closes and returns the close code and reason.
func expectClose(t *testing.T, conn *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	for {
		_, err := readFrame(conn, 5*time.Second)
		if err == nil {
			continue
		}
		var closeErr websocket.CloseError
		if !asCloseError(err, &closeErr) {
			t.Fatalf("expected a close frame, got %v", err)
		}
		return closeErr.Code, closeErr.Reason
	}
}

func asCloseError(err error, target *websocket.CloseError) bool {
	return errors.As(err, target)
}

func startTail(t *testing.T, srv *httptest.Server, c *apiClient, id, query string) *websocket.Conn {
	conn := dialTail(t, srv, c, id, query)
	sendFrame(t, conn, map[string]string{"type": "start", "csrf": c.csrf})
	return conn
}

func TestLogsTail_HappyPath(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := startTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "")
	ready, err := readFrame(conn, 3*time.Second)
	if err != nil || ready["type"] != "ready" || ready["group"] != "/ecs/core-services/payments-api" {
		t.Fatalf("ready frame = %v, %v", ready, err)
	}
	if prefixes, _ := ready["prefixes"].([]any); len(prefixes) != 1 || prefixes[0] != "ecs/payments-api/" {
		t.Errorf("prefixes = %v", ready["prefixes"])
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		frame, err := readFrame(conn, time.Until(deadline))
		if err != nil {
			t.Fatalf("waiting for lines: %v", err)
		}
		if frame["type"] != "lines" {
			continue
		}
		items, _ := frame["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("empty lines frame %v", frame)
		}
		item, _ := items[0].(map[string]any)
		for _, key := range []string{"t", "container", "task_id", "message"} {
			if s, _ := item[key].(string); s == "" {
				t.Errorf("item %v missing %q", item, key)
			}
		}
		return
	}
	t.Fatal("no lines frame within 3s")
}

func TestLogsTail_WrongCSRF4401(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	c := login(t, srv, "oncall", testPassword)
	conn := dialTail(t, srv, c, paymentsID(), "")
	sendFrame(t, conn, map[string]string{"type": "start", "csrf": "wrong"})
	if code, _ := expectClose(t, conn); code != 4401 {
		t.Errorf("close code %d, want 4401", code)
	}
}

func TestLogsTail_BadStart4400(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := dialTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "")
	sendFrame(t, conn, map[string]string{"type": "hello"})
	if code, _ := expectClose(t, conn); code != 4400 {
		t.Errorf("close code %d, want 4400", code)
	}
}

func TestLogsTail_ViewOnlyForbidden4403(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := startTail(t, srv, login(t, srv, "omer", testPassword), stagingWebID(), "")
	if code, reason := expectClose(t, conn); code != 4403 || reason != "You need Logs on Staging" {
		t.Errorf("close = %d %q, want 4403 and the forbidden message", code, reason)
	}
}

func TestLogsTail_NoView4404(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := startTail(t, srv, login(t, srv, "omer", testPassword), paymentsID(), "")
	if code, _ := expectClose(t, conn); code != 4404 {
		t.Errorf("close code %d, want 4404", code)
	}
}

func TestLogsTail_UnknownContainer4422(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := startTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "?container=nope")
	if code, _ := expectClose(t, conn); code != 4422 {
		t.Errorf("close code %d, want 4422", code)
	}
}

func TestLogsTail_LongCloseReasonStillSendsCode(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	conn := startTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "?container="+strings.Repeat("x", 300))
	code, reason := expectClose(t, conn)
	if code != 4422 {
		t.Errorf("close code %d, want 4422", code)
	}
	if len(reason) > 123 {
		t.Errorf("reason is %d bytes, want <= 123", len(reason))
	}
}

func TestLogsTail_UnauthenticatedIs401BeforeUpgrade(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/services/"+paymentsID()+"/logs/tail", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial err=%v resp=%v, want a 401 response", err, resp)
	}
}

func TestLogsTail_ThirdTailIs4409(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute)
	c := login(t, srv, "oncall", testPassword)
	for range 2 {
		conn := startTail(t, srv, c, paymentsID(), "")
		if ready, err := readFrame(conn, 3*time.Second); err != nil || ready["type"] != "ready" {
			t.Fatalf("ready = %v, %v", ready, err)
		}
	}
	third := startTail(t, srv, c, paymentsID(), "")
	code, reason := expectClose(t, third)
	if code != 4409 || reason != "You already have 2 live tails open." {
		t.Errorf("close = %d %q, want 4409 and the exact limit message", code, reason)
	}
}

func TestLogsTail_IdleEndsWith4408(t *testing.T) {
	srv, _, _ := tailFixture(t, 100*time.Millisecond)
	conn := startTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "")
	var sawEnd bool
	for {
		frame, err := readFrame(conn, 5*time.Second)
		if err != nil {
			var closeErr websocket.CloseError
			if !asCloseError(err, &closeErr) || closeErr.Code != 4408 {
				t.Fatalf("closed with %v, want 4408", err)
			}
			break
		}
		if frame["type"] == "end" {
			sawEnd = frame["reason"] == "idle"
		}
	}
	if !sawEnd {
		t.Error("no end frame with reason idle before the close")
	}
}

func TestLogsTail_ClientCloseReleasesSlot(t *testing.T) {
	srv, api, st := tailFixture(t, time.Minute)
	user, err := st.GetUserByUsername(t.Context(), "oncall")
	if err != nil {
		t.Fatal(err)
	}
	conn := startTail(t, srv, login(t, srv, "oncall", testPassword), paymentsID(), "")
	if ready, err := readFrame(conn, 3*time.Second); err != nil || ready["type"] != "ready" {
		t.Fatalf("ready = %v, %v", ready, err)
	}
	if got := api.TailLimiter.Count(user.ID); got != 1 {
		t.Fatalf("slots while open = %d, want 1", got)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for api.TailLimiter.Count(user.ID) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("slot still held after client close: %d", api.TailLimiter.Count(user.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dialTailWithOrigin(srv *httptest.Server, c *apiClient, id, origin string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/services/" + id + "/logs/tail"
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": []string{origin}}})
}

func TestLogsTail_BadOrigin_Rejected(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute, func(s *Server) { s.Cfg.BaseURL = "http://hm.example.test" })
	conn, resp, err := dialTailWithOrigin(srv, login(t, srv, "oncall", testPassword), paymentsID(), "https://evil.example")
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v resp = %v, want 403 before upgrade", err, resp)
	}
}

// Behind a proxy that rewrites Host, the configured base URL's origin must still be accepted, as for exec.
func TestLogsTail_BaseURLOrigin_Accepted(t *testing.T) {
	srv, _, _ := tailFixture(t, time.Minute, func(s *Server) { s.Cfg.BaseURL = "http://hm.example.test" })
	conn, _, err := dialTailWithOrigin(srv, login(t, srv, "oncall", testPassword), paymentsID(), "http://hm.example.test")
	if err != nil {
		t.Fatalf("dial with the base URL origin: %v", err)
	}
	conn.CloseNow()
}

// The task drawer asks for the newest 20 lines of one task with ?limit=20; the server must return no more than that
// (it used to ignore the parameter and answer with the full history page every 5 s).
func TestLogs_LimitParamCapsLines(t *testing.T) {
	srv, _, _ := logsFixture(t)
	c := login(t, srv, "oncall", testPassword)

	_, all := getLogs(t, c, paymentsID(), "")
	if len(all.Lines) <= 20 {
		t.Fatalf("the unlimited read has only %d lines; the cap would prove nothing", len(all.Lines))
	}
	status, body := getLogs(t, c, paymentsID(), "?limit=20")
	if status != http.StatusOK {
		t.Fatalf("limit=20: status %d", status)
	}
	if len(body.Lines) != 20 {
		t.Fatalf("limit=20 returned %d lines", len(body.Lines))
	}
	// the newest lines are kept, so the capped page ends with the newest line of the full one
	if got, want := body.Lines[19].Message, all.Lines[len(all.Lines)-1].Message; got != want {
		t.Fatalf("newest line = %q, want %q", got, want)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if status, _ := getLogs(t, c, paymentsID(), "?limit="+bad); status != http.StatusUnprocessableEntity {
			t.Fatalf("limit=%s: status %d, want 422", bad, status)
		}
	}
}
