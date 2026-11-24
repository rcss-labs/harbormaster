package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// fakeKey is assembled from two pieces so secret scanners do not flag it.
const fakeKey = "AIza" + "SyFAKE-test-key-0000000000000000000"

type captured struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	urls    []string
}

func (c *captured) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, string(body))
	c.headers = append(c.headers, r.Header.Clone())
	c.urls = append(c.urls, r.URL.String())
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sseServer(t *testing.T, cap *captured, status int, header http.Header, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			cap.record(r)
		}
		for k, v := range header {
			w.Header()[k] = v
		}
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProvider(t *testing.T, srv *httptest.Server) provider.Provider {
	t.Helper()
	p, err := New(Config{APIKey: fakeKey, BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func simpleRequest() provider.Request {
	return provider.Request{
		Model:    "gemini-test",
		System:   "You are helpful.",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	}
}

func TestGenerate_TextOnly_StreamsDeltasAndUsage(t *testing.T) {
	srv := sseServer(t, nil, 200, nil, fixture(t, "text.sse"))
	var deltas []string
	resp, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, "|"); got != "Hello|, world|!" {
		t.Errorf("deltas = %q", got)
	}
	if resp.Text != "Hello, world!" {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.StopReason != "end" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
	// Thought tokens are billed as output.
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 10 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerate_ToolCall_ParsesArguments(t *testing.T) {
	srv := sseServer(t, nil, 200, nil, fixture(t, "toolcall.sse"))
	resp, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop reason = %q", resp.StopReason)
	}
	if resp.Text != "Let me look." {
		t.Errorf("text = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	first := resp.ToolCalls[0]
	if first.Name != "list_clusters" || first.Arguments["region"] != "eu-west-1" || first.Arguments["limit"] != float64(5) {
		t.Errorf("first call = %+v", first)
	}
	if first.ID == "" || first.ID == resp.ToolCalls[1].ID {
		t.Errorf("call ids must be non-empty and unique: %q %q", first.ID, resp.ToolCalls[1].ID)
	}
	if len(resp.Opaque) == 0 {
		t.Error("the thought signature must be carried in Opaque")
	}
	if resp.Usage.InputTokens != 30 || resp.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerate_SendsToolResultInVendorForm(t *testing.T) {
	cap := &captured{}
	srv := sseServer(t, cap, 200, nil, fixture(t, "toolcall.sse"))
	p := newProvider(t, srv)

	first, err := p.Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := provider.Request{
		Model:           "gemini-test",
		System:          "You are helpful.",
		MaxOutputTokens: 777,
		Tools: []provider.ToolDef{{
			Name:        "list_clusters",
			Description: "Lists clusters.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"region": map[string]any{"type": "string"}}},
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "hi"},
			{Role: provider.RoleAssistant, Text: first.Text, ToolCalls: first.ToolCalls, Opaque: first.Opaque},
			{Role: provider.RoleTool, ToolCallID: first.ToolCalls[0].ID, ToolName: "list_clusters", Text: "two clusters"},
			{Role: provider.RoleTool, ToolCallID: first.ToolCalls[1].ID, ToolName: "get_service", Text: "boom", IsError: true},
		},
	}
	if _, err := p.Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.bodies) != 2 {
		t.Fatalf("requests = %d", len(cap.bodies))
	}
	if got := cap.headers[1].Get("x-goog-api-key"); got != fakeKey {
		t.Errorf("api key header = %q", got)
	}
	if strings.Contains(cap.urls[1], "key=") || strings.Contains(cap.urls[1], fakeKey) {
		t.Errorf("the key must not be in the URL: %s", cap.urls[1])
	}
	if !strings.Contains(cap.urls[1], "models/gemini-test:streamGenerateContent") {
		t.Errorf("url = %s", cap.urls[1])
	}

	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string `json:"text"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
		SystemInstruction struct {
			Parts []struct{ Text string } `json:"parts"`
		} `json:"systemInstruction"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name                 string         `json:"name"`
				ParametersJsonSchema map[string]any `json:"parametersJsonSchema"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		GenerationConfig struct {
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal([]byte(cap.bodies[1]), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Contents) != 3 {
		t.Fatalf("contents = %s", cap.bodies[1])
	}
	if body.Contents[0].Role != "user" || body.Contents[1].Role != "model" || body.Contents[2].Role != "user" {
		t.Errorf("roles = %s %s %s", body.Contents[0].Role, body.Contents[1].Role, body.Contents[2].Role)
	}
	model := body.Contents[1].Parts
	var sawSignature bool
	var calls int
	for _, part := range model {
		if part.FunctionCall != nil {
			calls++
		}
		if part.ThoughtSignature == "c2lnLW9uZQ==" && part.FunctionCall != nil && part.FunctionCall.Name == "list_clusters" {
			sawSignature = true
		}
	}
	if calls != 2 || !sawSignature {
		t.Errorf("model turn must echo both calls and the thought signature: %s", cap.bodies[1])
	}
	results := body.Contents[2].Parts
	if len(results) != 2 || results[0].FunctionResponse == nil || results[1].FunctionResponse == nil {
		t.Fatalf("tool results must share one content: %s", cap.bodies[1])
	}
	if results[0].FunctionResponse.Name != "list_clusters" || results[0].FunctionResponse.Response["output"] != "two clusters" {
		t.Errorf("first result = %+v", results[0].FunctionResponse)
	}
	if results[1].FunctionResponse.Response["error"] != "boom" {
		t.Errorf("second result = %+v", results[1].FunctionResponse)
	}
	if len(body.SystemInstruction.Parts) != 1 || body.SystemInstruction.Parts[0].Text != "You are helpful." {
		t.Errorf("system = %+v", body.SystemInstruction)
	}
	if len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 || body.Tools[0].FunctionDeclarations[0].ParametersJsonSchema["type"] != "object" {
		t.Errorf("tools = %s", cap.bodies[1])
	}
	if body.GenerationConfig.MaxOutputTokens != 777 {
		t.Errorf("max output tokens = %d", body.GenerationConfig.MaxOutputTokens)
	}
}

func TestGenerate_IgnoresUnparseableOpaque(t *testing.T) {
	cap := &captured{}
	srv := sseServer(t, cap, 200, nil, fixture(t, "text.sse"))
	req := simpleRequest()
	req.Messages = append(req.Messages, provider.Message{Role: provider.RoleAssistant, Text: "earlier", Opaque: json.RawMessage(`{"not":"ours"}`)},
		provider.Message{Role: provider.RoleUser, Text: "again"})
	if _, err := newProvider(t, srv).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.bodies[0], `"earlier"`) {
		t.Errorf("assistant text must be sent when Opaque is not ours: %s", cap.bodies[0])
	}
}

func TestGenerate_MapsErrors(t *testing.T) {
	const secretBody = "SECRET-UPSTREAM-DETAIL"
	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		kind       string
		retryable  bool
		retryAfter time.Duration
	}{
		{"unauthorized", 401, nil, `{"error":{"code":401,"message":"` + secretBody + `","status":"UNAUTHENTICATED"}}`, "auth", false, 0},
		{"forbidden", 403, nil, `{"error":{"code":403,"message":"` + secretBody + `","status":"PERMISSION_DENIED"}}`, "auth", false, 0},
		{"rate limited", 429, http.Header{"Retry-After": {"7"}}, `{"error":{"code":429,"message":"` + secretBody + `","status":"RESOURCE_EXHAUSTED"}}`, "rate_limited", true, 7 * time.Second},
		{"server error", 500, nil, `{"error":{"code":500,"message":"` + secretBody + `","status":"INTERNAL"}}`, "unavailable", true, 0},
		{"overloaded", 503, nil, `{"error":{"code":503,"message":"` + secretBody + `","status":"UNAVAILABLE"}}`, "unavailable", true, 0},
		{"context too long", 400, nil, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576). ` + secretBody + `","status":"INVALID_ARGUMENT"}}`, "context_too_long", false, 0},
		{"bad request", 400, nil, `{"error":{"code":400,"message":"` + secretBody + `","status":"INVALID_ARGUMENT"}}`, "invalid_request", false, 0},
		{"not found", 404, nil, `plain text ` + secretBody, "invalid_request", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := sseServer(t, nil, tc.status, tc.header, []byte(tc.body))
			_, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("err = %v, want *provider.Error", err)
			}
			if perr.Kind != tc.kind || perr.Retryable != tc.retryable || perr.RetryAfter != tc.retryAfter {
				t.Errorf("got %+v", perr)
			}
		})
	}
}

func TestGenerate_ErrorMessageNeverContainsKeyOrBody(t *testing.T) {
	const marker = "UPSTREAM-BODY-MARKER"
	bodies := map[int]string{
		400: `{"error":{"code":400,"message":"` + marker + ` ` + fakeKey + `","status":"INVALID_ARGUMENT"}}`,
		401: `{"error":{"code":401,"message":"` + marker + ` ` + fakeKey + `","status":"UNAUTHENTICATED"}}`,
		429: `{"error":{"code":429,"message":"` + marker + `","status":"RESOURCE_EXHAUSTED"}}`,
		500: marker + " " + fakeKey,
	}
	for status, body := range bodies {
		srv := sseServer(t, nil, status, nil, []byte(body))
		_, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if s := err.Error(); strings.Contains(s, marker) || strings.Contains(s, fakeKey) || strings.Contains(s, srv.URL) {
			t.Errorf("status %d: error leaks: %s", status, s)
		}
	}

	// A malformed stream chunk also must not leak into the error.
	srv := sseServer(t, nil, 200, nil, []byte("data: {not json "+marker+" "+fakeKey+"\n\n"))
	_, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
	if err == nil || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), fakeKey) {
		t.Errorf("malformed chunk error = %v", err)
	}
}

func TestGenerate_StreamBreak_ReturnsUnavailableWithUsage(t *testing.T) {
	srv := sseServer(t, nil, 200, nil, fixture(t, "broken.sse"))
	var deltas []string
	resp, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Kind != "unavailable" {
		t.Fatalf("err = %v", err)
	}
	if perr.Retryable {
		t.Error("must not be retryable once text was emitted")
	}
	if len(deltas) != 1 || deltas[0] != "partial" {
		t.Errorf("deltas = %v", deltas)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 1 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerate_StreamEndsWithoutFinish_BeforeText_IsRetryable(t *testing.T) {
	srv := sseServer(t, nil, 200, nil, []byte(""))
	_, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Kind != "unavailable" || !perr.Retryable {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerate_ContextCancel_ReturnsContextError(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := newProvider(t, srv).Generate(ctx, simpleRequest(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	var perr *provider.Error
	if errors.As(err, &perr) {
		t.Error("cancellation must not be a provider.Error")
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	_, err := newProvider(t, srv).Generate(context.Background(), simpleRequest(), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("redirect target received %d requests", hits)
	}
}

func TestClient_IgnoresEnvironmentOverrides(t *testing.T) {
	var mu sync.Mutex
	var hostile []string
	hostileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hostile = append(hostile, r.URL.String())
		mu.Unlock()
	}))
	defer hostileSrv.Close()

	t.Setenv("GOOGLE_GEMINI_BASE_URL", hostileSrv.URL)
	t.Setenv("GOOGLE_VERTEX_BASE_URL", hostileSrv.URL)
	t.Setenv("GOOGLE_API_KEY", "env-key-should-not-be-used")
	t.Setenv("GEMINI_API_KEY", "env-key-should-not-be-used")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "true")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "hostile-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "hostile-location")
	t.Setenv("GOOGLE_GENAI_API_VERSION", "hostile")
	t.Setenv("HTTP_PROXY", hostileSrv.URL)
	t.Setenv("HTTPS_PROXY", hostileSrv.URL)
	t.Setenv("ALL_PROXY", hostileSrv.URL)

	cap := &captured{}
	srv := sseServer(t, cap, 200, nil, fixture(t, "text.sse"))
	// No HTTPClient: the package builds its own, which must ignore the proxy variables.
	p, err := New(Config{APIKey: fakeKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), simpleRequest(), nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hostile) != 0 {
		t.Errorf("hostile server received %v", hostile)
	}
	if len(cap.urls) != 1 || !strings.HasPrefix(cap.urls[0], "/v1beta/models/gemini-test:streamGenerateContent") {
		t.Errorf("urls = %v", cap.urls)
	}
	if cap.headers[0].Get("x-goog-api-key") != fakeKey {
		t.Error("the configured key must be used, not the environment's")
	}
}

func TestNew_RequiresKey(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected an error for a missing key")
	}
}
