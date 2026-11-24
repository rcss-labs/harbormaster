package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// fakeKey is assembled from two pieces so secret scanners do not trip.
const fakeKey = "sk-" + "test-not-a-real-key"

type captured struct {
	body json.RawMessage
	auth string
	path string
}

// serve replays a fixture and records the request.
func serve(t *testing.T, status int, fixture string, headers map[string]string) (*httptest.Server, *captured) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.body, got.auth, got.path = body, r.Header.Get("Authorization"), r.URL.Path
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if strings.HasSuffix(fixture, ".sse") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newProvider(t *testing.T, baseURL string) provider.Provider {
	t.Helper()
	p, err := New(Config{APIKey: fakeKey, BaseURL: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func simpleRequest() provider.Request {
	return provider.Request{
		Model:    "test-model",
		System:   "be brief",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	}
}

func TestGenerate_TextOnly_StreamsDeltasAndUsage(t *testing.T) {
	srv, got := serve(t, 200, "text.sse", nil)
	var deltas []string
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Hello|, world" || resp.Text != "Hello, world" {
		t.Fatalf("deltas %v text %q", deltas, resp.Text)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 || resp.StopReason != "end" {
		t.Fatalf("usage/stop: %+v", resp)
	}
	var body map[string]any
	_ = json.Unmarshal(got.body, &body)
	if body["store"] != false || body["stream"] != true || body["instructions"] != "be brief" {
		t.Fatalf("request body: %s", got.body)
	}
	if got.auth != "Bearer "+fakeKey || got.path != "/responses" {
		t.Fatalf("auth/path: %q %q", got.auth, got.path)
	}
}

func TestGenerate_IncompleteMapsToMaxTokens(t *testing.T) {
	srv, _ := serve(t, 200, "incomplete.sse", nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err != nil || resp.StopReason != "max_tokens" || resp.Text != "Cut off" || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestGenerate_ToolCall_ParsesArguments(t *testing.T) {
	srv, got := serve(t, 200, "toolcall.sse", nil)
	req := simpleRequest()
	req.Tools = []provider.ToolDef{{Name: "list_services", Description: "List", InputSchema: map[string]any{"type": "object"}}}
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp %+v", resp)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_fake1" || call.Name != "list_services" || call.Arguments["cluster"] != "main" || call.Arguments["limit"] != float64(5) {
		t.Fatalf("call %+v", call)
	}
	if len(resp.Opaque) == 0 {
		t.Fatal("expected opaque state")
	}
	if !strings.Contains(string(got.body), `"name":"list_services"`) {
		t.Fatalf("tool definition missing: %s", got.body)
	}
}

func TestGenerate_SendsToolResultInVendorForm(t *testing.T) {
	srv, got := serve(t, 200, "text.sse", nil)
	req := simpleRequest()
	req.Messages = append(req.Messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_9", Name: "list_services", Arguments: map[string]any{"cluster": "main"}}}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "call_9", ToolName: "list_services", Text: "three services"},
	)
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Input) != 3 {
		t.Fatalf("input: %s", got.body)
	}
	call, out := body.Input[1], body.Input[2]
	if call["type"] != "function_call" || call["call_id"] != "call_9" || call["name"] != "list_services" || call["arguments"] != `{"cluster":"main"}` {
		t.Fatalf("call item: %v", call)
	}
	if _, hasID := call["id"]; hasID {
		t.Fatalf("function_call must not carry an item id without reasoning: %v", call)
	}
	if out["type"] != "function_call_output" || out["call_id"] != "call_9" || out["output"] != "three services" {
		t.Fatalf("output item: %v", out)
	}
}

func TestGenerate_OpaqueRoundTripsReasoningItems(t *testing.T) {
	opaque := `[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC"},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"t","arguments":"{}","status":"completed"}]`
	srv, got := serve(t, 200, "text.sse", nil)
	req := simpleRequest()
	req.Messages = append(req.Messages,
		provider.Message{Role: provider.RoleAssistant, Opaque: json.RawMessage(opaque), ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "t"}}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "call_1", Text: "ok"},
	)
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input   []map[string]any `json:"input"`
		Include []string         `json:"include"`
	}
	_ = json.Unmarshal(got.body, &body)
	if len(body.Input) != 4 || body.Input[1]["type"] != "reasoning" || body.Input[1]["encrypted_content"] != "ENC" ||
		body.Input[2]["id"] != "fc_1" || body.Input[3]["type"] != "function_call_output" {
		t.Fatalf("input: %s", got.body)
	}
	if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include: %v", body.Include)
	}
}

func TestGenerate_UnparseableOpaqueFallsBack(t *testing.T) {
	srv, got := serve(t, 200, "text.sse", nil)
	req := simpleRequest()
	req.Messages = append(req.Messages, provider.Message{Role: provider.RoleAssistant, Text: "earlier", Opaque: json.RawMessage(`"junk"`)})
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.body), `"earlier"`) {
		t.Fatalf("fallback text missing: %s", got.body)
	}
}

func TestGenerate_MapsErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		fixture    string
		headers    map[string]string
		kind       string
		retryable  bool
		retryAfter time.Duration
	}{
		{"401", 401, "error_401.json", nil, "auth", false, 0},
		{"429", 429, "error_429.json", map[string]string{"Retry-After": "7"}, "rate_limited", true, 7 * time.Second},
		{"500", 500, "error_500.json", nil, "unavailable", true, 0},
		{"context", 400, "error_context.json", nil, "context_too_long", false, 0},
		{"400", 400, "error_400.json", nil, "invalid_request", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, tc.status, tc.fixture, tc.headers)
			_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want provider.Error, got %v", err)
			}
			if perr.Kind != tc.kind || perr.Retryable != tc.retryable || perr.RetryAfter != tc.retryAfter {
				t.Fatalf("got %+v", perr)
			}
		})
	}
}

func TestGenerate_ErrorMessageNeverContainsKeyOrBody(t *testing.T) {
	for _, fixture := range []string{"error_401.json", "error_429.json", "error_500.json", "error_context.json", "error_400.json"} {
		status := map[string]int{"error_401.json": 401, "error_429.json": 429, "error_500.json": 500}[fixture]
		if status == 0 {
			status = 400
		}
		srv, _ := serve(t, status, fixture, nil)
		_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
		if err == nil || strings.Contains(err.Error(), "SECRET-BODY-TEXT") || strings.Contains(err.Error(), fakeKey) {
			t.Fatalf("%s: leaked or nil: %v", fixture, err)
		}
	}
	srv, _ := serve(t, 200, "failed.sse", nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Kind != "unavailable" || strings.Contains(err.Error(), "SECRET-BODY-TEXT") {
		t.Fatalf("failed event: %v", err)
	}
	if resp.Usage.InputTokens != 7 {
		t.Fatalf("usage from failed event: %+v", resp.Usage)
	}
}

func TestGenerate_BrokenStream(t *testing.T) {
	for _, tc := range []struct {
		fixture   string
		retryable bool
	}{{"broken_after_text.sse", false}, {"broken_before_text.sse", true}} {
		srv, _ := serve(t, 200, tc.fixture, nil)
		var deltas int
		_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), func(string) { deltas++ })
		var perr *provider.Error
		if !errors.As(err, &perr) || perr.Kind != "unavailable" || perr.Retryable != tc.retryable {
			t.Fatalf("%s: %v", tc.fixture, err)
		}
	}
}

func TestGenerate_ContextCancelReturnsCtxErr(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := newProvider(t, srv.URL).Generate(ctx, simpleRequest(), nil)
	var perr *provider.Error
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &perr) {
		t.Fatalf("got %v", err)
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/elsewhere", func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err == nil || hits.Load() != 0 {
		t.Fatalf("err %v, redirect target hits %d", err, hits.Load())
	}
}

// Go's proxy logic bypasses loopback, so the env test below cannot prove the
// proxy is off; check the transport directly.
func TestClient_TransportIgnoresProxyEnvironment(t *testing.T) {
	transport, ok := newHTTPClient().Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("transport must have Proxy == nil, got %+v", newHTTPClient().Transport)
	}
}

func TestClient_IgnoresEnvironmentOverrides(t *testing.T) {
	var hostile atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hostile.Add(1) }))
	defer evil.Close()
	for k, v := range map[string]string{
		"OPENAI_BASE_URL":       evil.URL,
		"OPENAI_API_KEY":        "env-key-should-not-be-used",
		"OPENAI_ORG_ID":         "org-from-env",
		"OPENAI_PROJECT_ID":     "proj-from-env",
		"OPENAI_CUSTOM_HEADERS": "X-From-Env: 1",
		"HTTPS_PROXY":           evil.URL,
		"HTTP_PROXY":            evil.URL,
		"ALL_PROXY":             evil.URL,
	} {
		t.Setenv(k, v)
	}
	var seen http.Header
	srv, _ := serve(t, 200, "text.sse", nil)
	// Re-wrap to capture headers while still replaying the fixture.
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		http.Redirect(w, r, srv.URL, http.StatusTemporaryRedirect)
	}))
	defer capture.Close()
	// Headers are checked on the first hop; the redirect is not followed, so
	// the call is expected to fail.
	_, _ = newProvider(t, capture.URL).Generate(context.Background(), simpleRequest(), nil)
	if hostile.Load() != 0 {
		t.Fatalf("hostile server received %d requests", hostile.Load())
	}
	if seen.Get("Authorization") != "Bearer "+fakeKey || seen.Get("OpenAI-Organization") != "" ||
		seen.Get("OpenAI-Project") != "" || seen.Get("X-From-Env") != "" {
		t.Fatalf("environment leaked into request headers: %v", seen)
	}
}
