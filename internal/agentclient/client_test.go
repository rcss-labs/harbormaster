package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func writeLines(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func sampleRequest() RunRequest {
	return RunRequest{
		RunID:    uuid.New(),
		Role:     "chat",
		Provider: "fake",
		Model:    "fake-1",
		Messages: []Message{{Role: "user", Text: "hello"}},
		Page:     Page{Path: "/services/x", ServiceID: "svc", ClusterID: "cl"},
	}
}

func TestRun_DeliversEventsInOrder(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w,
			`{"type":"text","delta":"Hi"}`,
			`{"type":"tool_start","tool":"get_service"}`,
			`{"type":"tool_end","tool":"get_service","ok":true}`,
			`{"type":"usage","input_tokens":10,"output_tokens":5,"cost_micro_usd":42,"provisional":true}`,
			`{"type":"done"}`,
		)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if err := c.Run(context.Background(), "tok", sampleRequest(), func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["role"] != "chat" || gotBody["model"] != "fake-1" || gotBody["max_cost_micro_usd"] == nil {
		t.Errorf("body = %v", gotBody)
	}
	want := []string{"text", "tool_start", "tool_end", "usage", "done"}
	if len(events) != len(want) {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	for i, typ := range want {
		if events[i].Type != typ {
			t.Errorf("event %d type = %q, want %q", i, events[i].Type, typ)
		}
	}
	if events[0].Delta != "Hi" || !events[2].OK || events[3].CostMicroUSD != 42 || !events[3].Provisional || events[3].InputTokens != 10 {
		t.Errorf("fields not decoded: %+v", events)
	}
}

func TestRun_ErrorEventEndsStreamWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLines(w, `{"type":"error","code":"budget","message":"over budget"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var last Event
	if err := c.Run(context.Background(), "tok", sampleRequest(), func(e Event) { last = e }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if last.Type != "error" || last.Code != "budget" {
		t.Errorf("last = %+v", last)
	}
}

func TestRun_MapsRefusals(t *testing.T) {
	status := func(code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"code":"x","message":"y"}}`))
		}))
	}
	cases := []struct {
		name string
		code int
		want error
	}{
		{"busy", 429, ErrAgentBusy},
		{"unknown model", 404, ErrUnknownModel},
		{"unauthenticated", 401, ErrAgentUnavailable},
		{"server error", 502, ErrAgentUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := status(tc.code)
			defer srv.Close()
			c, _ := New(srv.URL, "")
			err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) {})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		c, _ := New(url, "")
		err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) {})
		if !errors.Is(err, ErrAgentUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRun_TruncatedStreamIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLines(w, `{"type":"text","delta":"partial"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var n int
	err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) { n++ })
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("err = %v", err)
	}
	if n != 1 {
		t.Errorf("delivered %d events before the cut, want 1", n)
	}
}

func TestRun_CancelClosesTheConnection(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLines(w, `{"type":"text","delta":"x"}`)
		<-r.Context().Done()
		close(closed)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := c.Run(ctx, "tok", sampleRequest(), func(Event) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw the connection close")
	}
}

func TestCatalogue_CachedFor60Seconds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"providers":[{"provider":"fake","models":[{"id":"fake-1","input_per_mtok_micro_usd":1000000,"output_per_mtok_micro_usd":3000000,"context_tokens":8000}]}],"limits":{"max_run_tokens":200000,"run_timeout_seconds":300}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	now := time.Now()
	c.now = func() time.Time { return now }

	cat, err := c.Catalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.MaxRunTokens != 200000 || cat.RunTimeout != 5*time.Minute {
		t.Errorf("limits = %d %v", cat.MaxRunTokens, cat.RunTimeout)
	}
	m := cat.Providers[0].Models[0]
	if m.ID != "fake-1" || m.InputMicroUSDPerMTok != 1000000 || m.OutputMicroUSDPerMTok != 3000000 || m.ContextTokens != 8000 {
		t.Errorf("model = %+v", m)
	}
	now = now.Add(59 * time.Second)
	if _, err := c.Catalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits after 59s = %d, want 1", hits.Load())
	}
	now = now.Add(2 * time.Second)
	if _, err := c.Catalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Errorf("hits after 61s = %d, want 2", hits.Load())
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/catalogue", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	if _, err := c.Catalogue(context.Background()); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Catalogue err = %v", err)
	}
	if err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) {}); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Run err = %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("redirect target was contacted %d times", elsewhere.Load())
	}
}

func TestTest_ParsesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/test" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"ok":false,"kind":"auth","message":"bad key","input_tokens":3,"output_tokens":1,"cost_micro_usd":7}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	res, err := c.Test(context.Background(), "tok", "fake", "fake-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Kind != "auth" || res.Message != "bad key" || res.InputTokens != 3 || res.OutputTokens != 1 || res.CostMicroUSD != 7 {
		t.Errorf("res = %+v", res)
	}
}

func TestModel_MaxRunCostMicroUSD(t *testing.T) {
	m := Model{InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 5_000_000}
	if got := m.MaxRunCostMicroUSD(200_000); got != 1_000_000 {
		t.Errorf("got %d, want 1000000", got)
	}
}

// A timeout of the client's own derived context is the agent being slow, not the caller giving up.
func TestCatalogue_OwnTimeoutIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server notices a closed connection only once the body is read
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	c.catalogueTimeout = 50 * time.Millisecond
	if _, err := c.Catalogue(context.Background()); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Catalogue err = %v, want ErrAgentUnavailable", err)
	}
	c.testTimeout = 50 * time.Millisecond
	if _, err := c.Test(context.Background(), "tok", "fake", "fake-1"); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Test err = %v, want ErrAgentUnavailable", err)
	}
}

func TestCatalogue_CallerCancelIsNotUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := c.Catalogue(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// A slow catalogue call must not block other callers behind the cache mutex.
func TestCatalogue_NetworkCallDoesNotHoldTheLock(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"providers":[],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`))
	}))
	defer srv.Close()
	defer close(release)
	c, _ := New(srv.URL, "")
	go func() { _, _ = c.Catalogue(context.Background()) }()
	<-entered
	locked := make(chan struct{})
	go func() { c.mu.Lock(); defer c.mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("the cache mutex is held during the network call")
	}
}

func TestCatalogueAndTest_404IsNotUnknownModel(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, _ := New(srv.URL, "")
	if _, err := c.Catalogue(context.Background()); !errors.Is(err, ErrAgentUnavailable) || errors.Is(err, ErrUnknownModel) {
		t.Errorf("Catalogue err = %v", err)
	}
	if _, err := c.Test(context.Background(), "tok", "fake", "fake-1"); !errors.Is(err, ErrAgentUnavailable) || errors.Is(err, ErrUnknownModel) {
		t.Errorf("Test err = %v", err)
	}
}

func TestRun_LineOverOneMiBIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"type":"text","delta":"`)
		fmt.Fprint(w, strings.Repeat("a", 1<<20+10))
		fmt.Fprintln(w, `"}`)
		fmt.Fprintln(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var got int
	err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) { got++ })
	if !errors.Is(err, ErrAgentUnavailable) || got != 0 {
		t.Errorf("err = %v, events = %d", err, got)
	}
}

// A failed test call says when it is certain that no model was called: the agent refused it before that, or it never
// reached the agent. Anything else may have been paid for.
func TestTest_SaysWhenNoPaidCallWasMade(t *testing.T) {
	answer := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	hangUp := func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }
	for name, tc := range map[string]struct {
		handler http.HandlerFunc
		free    bool
		is      error
	}{
		"401":       {answer(401, "{}"), true, ErrAgentUnavailable},
		"404":       {answer(404, "{}"), true, ErrAgentUnavailable},
		"429":       {answer(429, "{}"), true, ErrAgentBusy},
		"400":       {answer(400, "{}"), false, ErrAgentUnavailable},
		"302":       {answer(302, ""), false, ErrAgentUnavailable},
		"500":       {answer(500, "{}"), false, ErrAgentUnavailable},
		"502":       {answer(502, "{}"), false, ErrAgentUnavailable},
		"503":       {answer(503, "{}"), false, ErrAgentUnavailable},
		"malformed": {answer(200, "not json"), false, ErrAgentUnavailable},
		"hung up":   {hangUp, false, ErrAgentUnavailable},
	} {
		srv := httptest.NewServer(tc.handler)
		c, _ := New(srv.URL, "")
		_, err := c.Test(t.Context(), "tok", "fake", "fake-1")
		if errors.Is(err, ErrNoPaidCall) != tc.free || !errors.Is(err, tc.is) {
			t.Errorf("%s: %v, want no paid call = %v and %v", name, err, tc.free, tc.is)
		}
		srv.Close()
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	c, _ := New(gone.URL, "")
	if _, err := c.Test(t.Context(), "tok", "fake", "fake-1"); !errors.Is(err, ErrNoPaidCall) || !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("unreachable: %v", err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c, _ = New(srv.URL, "")
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-entered; cancel() }()
	if _, err := c.Test(ctx, "tok", "fake", "fake-1"); errors.Is(err, ErrNoPaidCall) || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled after it was sent: %v", err)
	}
	if _, err := c.Test(ctx, "tok", "fake", "fake-1"); !errors.Is(err, ErrNoPaidCall) || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled before it was sent: %v", err)
	}
}

func TestTest_UsageIsKnownOnlyWhenTheAnswerStatesIt(t *testing.T) {
	for body, known := range map[string]bool{
		`{"ok":true,"input_tokens":1,"output_tokens":1,"cost_micro_usd":0}`:  true,
		`{"ok":false,"kind":"auth","cost_micro_usd":7}`:                      true,
		`{"ok":true,"input_tokens":1,"output_tokens":1}`:                     false,
		`{"ok":true,"cost_micro_usd":null}`:                                  false,
		`{"ok":true,"input_tokens":1,"output_tokens":1,"cost_micro_usd":-1}`: false,
		`{}`: false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		c, _ := New(srv.URL, "")
		if res, err := c.Test(t.Context(), "tok", "fake", "fake-1"); err != nil || res.UsageKnown != known {
			t.Errorf("%s: known %v, %v; want %v", body, res.UsageKnown, err, known)
		}
		srv.Close()
	}
}

func TestRun_SendsTheTask(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	const serviceID = "7f3c9a2e-41d0-4b8e-9c15-0a6d2e8f1b73-checkout-service"
	req := sampleRequest()
	req.Role = "investigator"
	req.Task = &Task{
		Kind:    "investigate",
		Subject: TaskSubject{Kind: "service", ServiceIDs: []string{serviceID}, ClusterID: "cl-1", AccountID: "123456789012", Region: "eu-west-1"},
		Alert:   TaskAlert{Kind: "down", Title: "Checkout is failing", Text: "5xx above 10 percent", OpenedAt: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task, ok := gotBody["task"].(map[string]any)
	if !ok {
		t.Fatalf("body has no task object: %v", gotBody)
	}
	subject, _ := task["subject"].(map[string]any)
	alert, _ := task["alert"].(map[string]any)
	ids, _ := subject["service_ids"].([]any)
	if task["kind"] != "investigate" || subject["kind"] != "service" || len(ids) != 1 || ids[0] != serviceID ||
		subject["cluster_id"] != "cl-1" || subject["account_id"] != "123456789012" || subject["region"] != "eu-west-1" {
		t.Errorf("subject on the wire = %v", task)
	}
	if alert["kind"] != "down" || alert["title"] != "Checkout is failing" || alert["text"] != "5xx above 10 percent" ||
		alert["opened_at"] != "2026-11-24T10:00:00Z" {
		t.Errorf("alert on the wire = %v", alert)
	}
}

func TestRun_ChatRequestHasNoTaskMember(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	if err := c.Run(context.Background(), "tok", sampleRequest(), func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if task, ok := gotBody["task"]; ok {
		t.Errorf("a chat request carries a task member: %v", task)
	}
}

func TestRun_DecodesTheResultEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLines(w,
			`{"type":"result","summary":"Checkout failed after the deploy.","summary_replaced":true,"evidence":[{"tool":"get_service_timeline","service_id":"7f3c9a2e-41d0-4b8e-9c15-0a6d2e8f1b73-checkout-service","action":"Logs","note":"Pods restarted.","excerpt":"OOMKilled"}],"evidence_dropped":2}`,
			`{"type":"done"}`,
		)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var events []Event
	if err := c.Run(context.Background(), "tok", sampleRequest(), func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 2 || events[0].Type != "result" || events[1].Type != "done" {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Result != nil {
		t.Errorf("done event carries a Result: %+v", events[1].Result)
	}
	res := events[0].Result
	if res == nil {
		t.Fatal("result event carries no Result")
	}
	if res.Summary != "Checkout failed after the deploy." || !res.SummaryReplaced || res.EvidenceDropped != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(res.Evidence) != 1 {
		t.Fatalf("evidence = %+v", res.Evidence)
	}
	ev := res.Evidence[0]
	if ev.Tool != "get_service_timeline" || ev.ServiceID != "7f3c9a2e-41d0-4b8e-9c15-0a6d2e8f1b73-checkout-service" ||
		ev.Action != "Logs" || ev.Note != "Pods restarted." || ev.Excerpt != "OOMKilled" {
		t.Errorf("evidence item = %+v", ev)
	}
}

func TestRun_ResultOverTheLineCapIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"type":"result","summary":"`)
		fmt.Fprint(w, strings.Repeat("a", maxLineBytes))
		fmt.Fprintln(w, `","summary_replaced":false,"evidence":[],"evidence_dropped":0}`)
		fmt.Fprintln(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var events []Event
	err := c.Run(context.Background(), "tok", sampleRequest(), func(e Event) { events = append(events, e) })
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("err = %v, want ErrAgentUnavailable", err)
	}
	for _, e := range events {
		if e.Type == "result" {
			t.Errorf("a result over the line cap was delivered: %+v", e)
		}
	}
}

func TestCatalogue_RolesAndSupports(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		roles    []string
		supports map[string]bool
	}{
		{
			name:     "with the investigator member",
			body:     `{"providers":[],"roles":["chat","investigator","test"],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			roles:    []string{"chat", "investigator", "test"},
			supports: map[string]bool{"investigator": true, "chat": true, "test": true, "unknown": false},
		},
		{
			name:     "without the investigator member",
			body:     `{"providers":[],"roles":["chat","test"],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			roles:    []string{"chat", "test"},
			supports: map[string]bool{"investigator": false, "chat": true, "test": true},
		},
		{
			name:     "from a phase 1 agent with no roles",
			body:     `{"providers":[],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			roles:    nil,
			supports: map[string]bool{"investigator": false, "chat": true, "test": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "")
			cat, err := c.Catalogue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cat.Roles, tc.roles) {
				t.Errorf("roles = %v, want %v", cat.Roles, tc.roles)
			}
			for role, want := range tc.supports {
				if got := cat.Supports(role); got != want {
					t.Errorf("Supports(%q) = %v, want %v", role, got, want)
				}
			}
		})
	}
}

func TestRun_SendsAReviewTask(t *testing.T) {
	var gotBody struct {
		Role string          `json:"role"`
		Task json.RawMessage `json:"task"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	req := sampleRequest()
	req.Role = "reviewer"
	req.Task = &Task{
		Kind:   "review",
		Review: &TaskReview{Type: "cost", Range: "7d", Period: "2026-11-17 to 2026-11-24"},
		Scope: &TaskScope{
			Entries: []ScopeEntry{
				{Kind: "tag", TagID: "t1", Name: "payments"},
				{Kind: "account", AccountID: "123456789012", Region: "eu-west-1"},
			},
			ServicesTotal: 3,
		},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = `{"kind":"review","review":{"type":"cost","range":"7d","period":"2026-11-17 to 2026-11-24"},"scope":{"entries":[{"kind":"tag","tag_id":"t1","name":"payments"},{"kind":"account","account_id":"123456789012","region":"eu-west-1"}],"services_total":3}}`
	if gotBody.Role != "reviewer" {
		t.Errorf("role on the wire = %q", gotBody.Role)
	}
	if string(gotBody.Task) != want {
		t.Errorf("task on the wire =\n%s\nwant\n%s", gotBody.Task, want)
	}
}

func TestRun_InvestigationTaskIsUnchanged(t *testing.T) {
	var gotBody struct {
		Task json.RawMessage `json:"task"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	req := sampleRequest()
	req.Role = "investigator"
	req.Task = &Task{
		Kind:    "investigate",
		Subject: TaskSubject{Kind: "service", ServiceIDs: []string{"7f3c9a2e-41d0-4b8e-9c15-0a6d2e8f1b73-checkout-service"}, ClusterID: "cl-1", AccountID: "123456789012", Region: "eu-west-1"},
		Alert:   TaskAlert{Kind: "down", Title: "Checkout is failing", Text: "5xx above 10 percent", OpenedAt: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = `{"kind":"investigate","subject":{"kind":"service","service_ids":["7f3c9a2e-41d0-4b8e-9c15-0a6d2e8f1b73-checkout-service"],"cluster_id":"cl-1","account_id":"123456789012","region":"eu-west-1"},"alert":{"kind":"down","title":"Checkout is failing","text":"5xx above 10 percent","opened_at":"2026-11-24T10:00:00Z"}}`
	if string(gotBody.Task) != want {
		t.Errorf("investigation task on the wire =\n%s\nwant\n%s", gotBody.Task, want)
	}
}

func TestRun_InvestigationWithZeroOpenedAtKeepsItsAlert(t *testing.T) {
	var gotBody struct {
		Task json.RawMessage `json:"task"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	req := sampleRequest()
	req.Role = "investigator"
	req.Task = &Task{
		Kind:    "investigate",
		Subject: TaskSubject{Kind: "cluster", ClusterID: "cl-1"},
		Alert:   TaskAlert{Kind: "down", Title: "Checkout is failing"},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = `{"kind":"investigate","subject":{"kind":"cluster","service_ids":null,"cluster_id":"cl-1","account_id":"","region":""},"alert":{"kind":"down","title":"Checkout is failing","text":"","opened_at":"0001-01-01T00:00:00Z"}}`
	if string(gotBody.Task) != want {
		t.Errorf("investigation task on the wire =\n%s\nwant\n%s", gotBody.Task, want)
	}
}

func TestRun_WrongTypeResultLineIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeLines(w,
			`{"type":"result","summary":42,"summary_replaced":false,"evidence":[],"evidence_dropped":0}`,
			`{"type":"done"}`,
		)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	var events []Event
	err := c.Run(context.Background(), "tok", sampleRequest(), func(e Event) { events = append(events, e) })
	if !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("err = %v, want ErrAgentUnavailable", err)
	}
	for _, e := range events {
		if e.Type == "result" {
			t.Errorf("a result line of the wrong type was delivered: %+v", e)
		}
	}
}

func TestCatalogue_ReviewTypesAndSupportsReview(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantTypes []string
		supports  map[string]bool
	}{
		{
			name:      "with the reviewer role and the list",
			body:      `{"providers":[],"roles":["chat","investigator","reviewer","test"],"review_types":["cost","security","reliability","capacity"],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			wantTypes: []string{"cost", "security", "reliability", "capacity"},
			supports:  map[string]bool{"cost": true, "capacity": true, "unknown": false, "": false},
		},
		{
			name:      "with the list but without the reviewer role",
			body:      `{"providers":[],"roles":["chat","investigator","test"],"review_types":["cost"],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			wantTypes: []string{"cost"},
			supports:  map[string]bool{"cost": false},
		},
		{
			name:      "with the reviewer role and no list",
			body:      `{"providers":[],"roles":["chat","reviewer","test"],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			wantTypes: nil,
			supports:  map[string]bool{"cost": false},
		},
		{
			name:      "with the reviewer role and an empty list",
			body:      `{"providers":[],"roles":["chat","reviewer","test"],"review_types":[],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			wantTypes: nil,
			supports:  map[string]bool{"cost": false},
		},
		{
			name:      "from a phase 1 agent with no roles",
			body:      `{"providers":[],"limits":{"max_run_tokens":1,"run_timeout_seconds":1}}`,
			wantTypes: nil,
			supports:  map[string]bool{"cost": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "")
			cat, err := c.Catalogue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cat.ReviewTypes, tc.wantTypes) {
				t.Errorf("review types = %v, want %v", cat.ReviewTypes, tc.wantTypes)
			}
			for reviewType, want := range tc.supports {
				if got := cat.SupportsReview(reviewType); got != want {
					t.Errorf("SupportsReview(%q) = %v, want %v", reviewType, got, want)
				}
			}
		})
	}
}

func TestRun_SendsACustomReviewTask(t *testing.T) {
	var gotBody struct {
		Task json.RawMessage `json:"task"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	const text = "Compare spend per team.\nLeave out <b>staging</b> & sandbox accounts."
	req := sampleRequest()
	req.Role = "reviewer"
	req.Task = &Task{
		Kind:   "review",
		Review: &TaskReview{Type: "custom", Range: "7d", Period: "2026-11-17 to 2026-11-24", Instructions: text},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var task struct {
		Review struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		} `json:"review"`
	}
	if err := json.Unmarshal(gotBody.Task, &task); err != nil {
		t.Fatal(err)
	}
	if task.Review.Type != "custom" || task.Review.Instructions != text {
		t.Errorf("review on the wire = %+v, want the instructions unchanged", task.Review)
	}
}

func TestRun_BuiltInReviewTaskIsUnchanged(t *testing.T) {
	var gotBody struct {
		Task json.RawMessage `json:"task"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeLines(w, `{"type":"done"}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "")
	req := sampleRequest()
	req.Role = "reviewer"
	req.Task = &Task{
		Kind:   "review",
		Review: &TaskReview{Type: "cost", Range: "7d", Period: "2026-11-17 to 2026-11-24"},
		Scope: &TaskScope{
			Entries: []ScopeEntry{
				{Kind: "tag", TagID: "t1", Name: "payments"},
				{Kind: "account", AccountID: "123456789012", Region: "eu-west-1"},
			},
			ServicesTotal: 3,
		},
	}
	if err := c.Run(context.Background(), "tok", req, func(Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = `{"kind":"review","review":{"type":"cost","range":"7d","period":"2026-11-17 to 2026-11-24"},"scope":{"entries":[{"kind":"tag","tag_id":"t1","name":"payments"},{"kind":"account","account_id":"123456789012","region":"eu-west-1"}],"services_total":3}}`
	if string(gotBody.Task) != want {
		t.Errorf("task on the wire =\n%s\nwant\n%s", gotBody.Task, want)
	}
}
