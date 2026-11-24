package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
