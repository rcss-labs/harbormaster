package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
)

// syncLog is a goroutine-safe log sink.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// gatewayHarness is a gateway over a stub Harbormaster.
type gatewayHarness struct {
	gateway  *httptest.Server
	upstream *recordingServer
	log      *syncLog
	token    string // a well-formed token
	display  string // its public display id
}

type harnessOptions struct {
	mutateConfig func(*Config)
	upstream     http.HandlerFunc
}

func newHarness(t *testing.T, opts harnessOptions) *gatewayHarness {
	t.Helper()
	handler := opts.upstream
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}
	up := newRecordingServer(t, handler)
	cfg := Config{
		Listen: ":0", UpstreamURL: up.URL, PublicURL: "https://mcp.example.com",
		TrustedProxy: false, MaxResultBytes: 100000, UpstreamTimeout: 5 * time.Second,
	}
	if opts.mutateConfig != nil {
		opts.mutateConfig(&cfg)
	}
	upstream, err := NewUpstream(cfg.UpstreamURL, cfg.UpstreamTimeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncLog{}
	gateway := httptest.NewServer(NewHandler(cfg, upstream, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(gateway.Close)

	raw, parsed, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	return &gatewayHarness{gateway: gateway, upstream: up, log: logs, token: raw, display: apitoken.DisplayID(parsed)}
}

func (h *gatewayHarness) url() string { return h.gateway.URL + "/mcp" }

// rpc posts one JSON-RPC request to /mcp with the given headers.
func (h *gatewayHarness) rpc(t *testing.T, headers map[string]string, method string, params any) *http.Response {
	t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		message["params"] = params
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return h.post(t, headers, raw)
}

func (h *gatewayHarness) post(t *testing.T, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		if value == "" {
			req.Header.Del(name)
		} else {
			req.Header.Set(name, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *gatewayHarness) authed() map[string]string {
	return map[string]string{"Authorization": "Bearer " + h.token}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toolResult is a decoded tools/call answer.
type toolResult struct {
	IsError bool
	Text    string
}

func decodeToolResult(t *testing.T, resp *http.Response) toolResult {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	body := readBody(t, resp)
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error: %s", envelope.Error.Message)
	}
	if len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one text item", envelope.Result.Content)
	}
	return toolResult{IsError: envelope.Result.IsError, Text: envelope.Result.Content[0].Text}
}

func (h *gatewayHarness) callTool(t *testing.T, name string, args map[string]any) *http.Response {
	t.Helper()
	return h.rpc(t, h.authed(), "tools/call", map[string]any{"name": name, "arguments": args})
}

func respondJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestHandler_OriginNotAllowed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	headers := h.authed()
	headers["Origin"] = "https://evil.example"
	resp := h.rpc(t, headers, "tools/list", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "origin_not_allowed") {
		t.Errorf("status %d body %q, want 403 origin_not_allowed", resp.StatusCode, body)
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls, want 0 before anything else happens", n)
	}
}

func TestHandler_OriginIsCheckedBeforeTheToken(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp := h.rpc(t, map[string]string{"Origin": "https://evil.example"}, "tools/list", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 even without a token", resp.StatusCode)
	}
	readBody(t, resp)
}

func TestHandler_OriginAllowedWhenListed(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: func(c *Config) { c.AllowedOrigins = []string{"https://claude.ai"} }})
	headers := h.authed()
	headers["Origin"] = "https://claude.ai"
	resp := h.rpc(t, headers, "tools/list", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, body %s", resp.StatusCode, readBody(t, resp))
	}
	// A near miss is still refused: exact match only.
	for _, origin := range []string{"https://claude.ai.evil.example", "http://claude.ai", "https://CLAUDE.ai"} {
		headers["Origin"] = origin
		resp := h.rpc(t, headers, "tools/list", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: status %d, want 403", origin, resp.StatusCode)
		}
		readBody(t, resp)
	}
}

func TestHandler_NoOriginPasses(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp := h.rpc(t, h.authed(), "tools/list", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, body %s", resp.StatusCode, readBody(t, resp))
	}
}

func TestHandler_MissingOrMalformedTokenIs401WithoutUpstreamCall(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for name, auth := range map[string]string{
		"no header":         "",
		"basic":             "Basic dXNlcjpwYXNz",
		"bearer empty":      "Bearer ",
		"bearer garbage":    "Bearer not-a-token",
		"bearer wrong kind": "Bearer xyz_abcdefghijkl_" + strings.Repeat("a", 43),
		"bearer truncated":  "Bearer " + h.token[:20],
		"bare token":        h.token,
	} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{}
			if auth != "" {
				headers["Authorization"] = auth
			}
			for _, method := range []string{"initialize", "tools/list", "tools/call"} {
				resp := h.rpc(t, headers, method, map[string]any{"name": "whoami"})
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s: status %d, want 401", method, resp.StatusCode)
				}
				if got := resp.Header.Get("WWW-Authenticate"); got != `Bearer realm="harbormaster-mcp"` {
					t.Errorf("%s: WWW-Authenticate = %q", method, got)
				}
				readBody(t, resp)
			}
		})
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls, want 0: garbage must never reach Harbormaster", n)
	}
}

func TestHandler_InitializeValidatesTheToken(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"no"}}`)})
	resp := h.rpc(t, h.authed(), "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"},
	})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("status %d WWW-Authenticate %q, want 401 with the header", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	readBody(t, resp)
	seen := h.upstream.seen()
	if len(seen) != 1 || seen[0].EscapedPath != "/api/tokens/current" || seen[0].Header.Get("Authorization") != "Bearer "+h.token {
		t.Errorf("upstream saw %+v, want one GET /api/tokens/current with the caller's bearer", seen)
	}
}

func TestHandler_TokenCheckRunsForNonCallMethods(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, method := range []string{"initialize", "tools/list", "ping"} {
		before := len(h.upstream.seen())
		resp := h.rpc(t, h.authed(), method, map[string]any{
			"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"},
		})
		readBody(t, resp)
		got := h.upstream.seen()[before:]
		if len(got) != 1 || got[0].EscapedPath != "/api/tokens/current" {
			t.Errorf("%s: upstream saw %+v, want exactly the token check", method, got)
		}
	}
}

func TestHandler_TokenCheckFailuresMap(t *testing.T) {
	cases := []struct {
		status     int
		wantStatus int
	}{
		{http.StatusInternalServerError, http.StatusBadGateway},
		{http.StatusForbidden, http.StatusBadGateway},
		{http.StatusFound, http.StatusBadGateway},
		// Deviation recorded by the implementer: a 429 from the token check passes through with Retry-After.
		{http.StatusTooManyRequests, http.StatusTooManyRequests},
	}
	for _, c := range cases {
		h := newHarness(t, harnessOptions{upstream: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(c.status)
		}})
		resp := h.rpc(t, h.authed(), "tools/list", nil)
		if resp.StatusCode != c.wantStatus {
			t.Errorf("upstream %d: gateway %d, want %d", c.status, resp.StatusCode, c.wantStatus)
		}
		if c.status == http.StatusTooManyRequests && resp.Header.Get("Retry-After") != "7" {
			t.Errorf("Retry-After = %q, want 7", resp.Header.Get("Retry-After"))
		}
		readBody(t, resp)
	}
	// Harbormaster down at connect time.
	h := newHarness(t, harnessOptions{})
	h.upstream.Close()
	resp := h.rpc(t, h.authed(), "tools/list", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("upstream down: status %d, want 502", resp.StatusCode)
	}
	readBody(t, resp)
}

// Drives the real SDK client, as an MCP host would.
func TestHandler_SDKClientConnectsAndListsAll39ReadOnlyTools(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	session := connectSDK(t, h, h.token)
	defer session.Close()

	init := session.InitializeResult()
	if init.ServerInfo.Name != "harbormaster" {
		t.Errorf("server name = %q", init.ServerInfo.Name)
	}
	if init.Instructions == "" || !strings.Contains(init.Instructions, "read-only") {
		t.Errorf("instructions = %q", init.Instructions)
	}
	// Deviation recorded by the implementer: initialize advertises tools only.
	// Checked through the JSON form so the deprecated Logging field is not read.
	rawCaps, err := json.Marshal(init.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var capabilityNames map[string]json.RawMessage
	if err := json.Unmarshal(rawCaps, &capabilityNames); err != nil {
		t.Fatal(err)
	}
	if _, hasTools := capabilityNames["tools"]; !hasTools || len(capabilityNames) != 1 {
		t.Errorf("capabilities = %s, want tools only", rawCaps)
	}

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 39 {
		t.Fatalf("%d tools, want 39", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Title == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("%s: title %q description %d schema %v", tool.Name, tool.Title, len(tool.Description), tool.InputSchema != nil)
		}
		ann := tool.Annotations
		if ann == nil || !ann.ReadOnlyHint || ann.OpenWorldHint == nil || *ann.OpenWorldHint {
			t.Errorf("%s: annotations %+v, want readOnlyHint true and openWorldHint false", tool.Name, ann)
		}
		if ann != nil && ann.DestructiveHint != nil && *ann.DestructiveHint {
			t.Errorf("%s: destructive", tool.Name)
		}
	}
}

func connectSDK(t *testing.T, h *gatewayHarness, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(r)
	})}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: h.url(), HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHandler_SDKClientCallsATool(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(200, `{"services":[{"id":"s1"}],"polled_at":"2026-01-01T00:00:00Z"}`)})
	session := connectSDK(t, h, h.token)
	defer session.Close()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_services", Arguments: map[string]any{"tag": []string{"3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"id":"s1"`) {
		t.Errorf("text = %q", text)
	}
}

func TestHandler_CallForwardsTheBearerAndReturnsTheBody(t *testing.T) {
	const body = `{"id":"svc-9","name":"api","polled_at":"2026-11-22T10:00:00Z"}`
	h := newHarness(t, harnessOptions{upstream: respondJSON(200, body)})
	headers := h.authed()
	headers["Cookie"] = "hm_session=client-cookie"
	headers["X-Custom"] = "client-header"
	resp := h.rpc(t, headers, "tools/call", map[string]any{"name": "get_service", "arguments": map[string]any{"service_id": "svc-9"}})
	result := decodeToolResult(t, resp)
	if result.IsError || result.Text != body {
		t.Errorf("result = %+v, want the upstream body verbatim", result)
	}
	seen := h.upstream.seen()
	if len(seen) != 1 {
		t.Fatalf("%d upstream calls, want exactly one (tools/call goes straight to its route)", len(seen))
	}
	if seen[0].EscapedPath != "/api/services/svc-9" || seen[0].Method != "GET" {
		t.Errorf("upstream saw %s %s", seen[0].Method, seen[0].EscapedPath)
	}
	if seen[0].Header.Get("Authorization") != "Bearer "+h.token {
		t.Error("the caller's bearer was not forwarded")
	}
	if seen[0].Header.Get("Cookie") != "" || seen[0].Header.Get("X-Custom") != "" {
		t.Errorf("client headers leaked upstream: %v", seen[0].Header)
	}
}

func TestHandler_ToolArgumentsReachTheRoute(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	decodeToolResult(t, h.callTool(t, "list_tasks", map[string]any{"tag": []string{"1", "2"}, "q": "web", "limit": 5}))
	got := h.upstream.seen()[0]
	if got.EscapedPath != "/api/tasks" || got.Query != "filter=problems&limit=5&q=web&tag=1&tag=2" {
		t.Errorf("upstream saw %s ?%s", got.EscapedPath, got.Query)
	}
	// A hostile id is one escaped segment of the declared route.
	decodeToolResult(t, h.callTool(t, "get_service", map[string]any{"service_id": "../users"}))
	if path := h.upstream.seen()[1].EscapedPath; path != "/api/services/..%2Fusers" {
		t.Errorf("hostile id reached %q", path)
	}
}

func TestHandler_PostToolSendsTheBody(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	decodeToolResult(t, h.callTool(t, "search_logs", map[string]any{"service_ids": []string{"a"}, "query": "boom", "range": "24h"}))
	got := h.upstream.seen()[0]
	if got.Method != "POST" || got.EscapedPath != "/api/log-search" {
		t.Fatalf("upstream saw %s %s", got.Method, got.EscapedPath)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(got.Body), &sent); err != nil || sent["query"] != "boom" || sent["range"] != "24h" {
		t.Errorf("body = %q (%v)", got.Body, err)
	}
}

func TestHandler_Upstream401BecomesHTTP401(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"expired"}}`)})
	resp := h.callTool(t, "get_overview", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d body %s, want HTTP 401 (not a tool error the model would see)", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != `Bearer realm="harbormaster-mcp"` {
		t.Errorf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
	if strings.Contains(body, "isError") || strings.Contains(body, "expired") {
		t.Errorf("401 body leaks an MCP result or upstream text: %s", body)
	}
}

func TestHandler_Upstream401MidSessionAfterAGoodCall(t *testing.T) {
	var mu sync.Mutex
	revoked := false
	h := newHarness(t, harnessOptions{upstream: func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if revoked {
			respondJSON(http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"x"}}`)(w, r)
			return
		}
		respondJSON(200, `{}`)(w, r)
	}})
	if r := decodeToolResult(t, h.callTool(t, "get_overview", nil)); r.IsError {
		t.Fatalf("first call failed: %+v", r)
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	resp := h.callTool(t, "get_overview", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status after revocation = %d, want 401 (nothing is cached)", resp.StatusCode)
	}
	readBody(t, resp)
}

func TestHandler_UpstreamErrorsBecomeToolErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		want       []string
		notWant    []string
	}{
		{name: "403", status: 403, body: `{"error":{"code":"token_not_allowed","message":"Not for tokens."}}`, want: []string{"Not for tokens.", "token_not_allowed"}},
		{name: "404", status: 404, body: `{"error":{"code":"not_found","message":"No such service."}}`, want: []string{"No such service."}},
		{name: "409", status: 409, body: `{"error":{"code":"conflict","message":"Busy."}}`, want: []string{"Busy."}},
		{name: "422", status: 422, body: `{"error":{"code":"invalid","message":"Bad range."}}`, want: []string{"Bad range."}},
		{name: "429 with retry", status: 429, retryAfter: "30", body: `{"error":{"code":"rate_limited","message":"Slow down."}}`, want: []string{"Slow down.", "Retry after 30 seconds."}},
		{name: "403 without an envelope", status: 403, body: `nope`, want: []string{"refused"}},
		{name: "500", status: 500, body: `{"error":{"code":"internal","message":"stack trace goes here"}}`, want: []string{"Harbormaster is unavailable. Try again shortly."}, notWant: []string{"stack trace"}},
		{name: "502", status: 502, body: `<html>`, want: []string{"Harbormaster is unavailable. Try again shortly."}},
		{name: "503", status: 503, body: ``, want: []string{"Harbormaster is unavailable. Try again shortly."}},
		{name: "redirect", status: 302, body: ``, want: []string{"Harbormaster is unavailable. Try again shortly."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{upstream: func(w http.ResponseWriter, r *http.Request) {
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				respondJSON(c.status, c.body)(w, r)
			}})
			result := decodeToolResult(t, h.callTool(t, "get_overview", nil))
			if !result.IsError {
				t.Fatalf("result = %+v, want isError", result)
			}
			for _, want := range c.want {
				if !strings.Contains(result.Text, want) {
					t.Errorf("text %q lacks %q", result.Text, want)
				}
			}
			for _, bad := range c.notWant {
				if strings.Contains(result.Text, bad) {
					t.Errorf("text %q must not contain %q", result.Text, bad)
				}
			}
		})
	}
}

func TestHandler_UpstreamDownIsAToolError(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.upstream.Close()
	result := decodeToolResult(t, h.callTool(t, "get_overview", nil))
	if !result.IsError || result.Text != "Harbormaster is unavailable. Try again shortly." {
		t.Errorf("result = %+v", result)
	}
}

func TestHandler_SlowUpstreamIsAToolErrorWithinTheTimeout(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, harnessOptions{
		mutateConfig: func(c *Config) { c.UpstreamTimeout = time.Second },
		upstream: func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
	})
	defer close(release)
	started := time.Now()
	result := decodeToolResult(t, h.callTool(t, "get_overview", nil))
	if !result.IsError || !strings.Contains(result.Text, "unavailable") {
		t.Errorf("result = %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("took %s, want about the 1s timeout", elapsed)
	}
}

func TestHandler_OversizedResultIsAToolError(t *testing.T) {
	big := `{"data":"` + strings.Repeat("x", 20000) + `"}`
	h := newHarness(t, harnessOptions{
		mutateConfig: func(c *Config) { c.MaxResultBytes = 10000 },
		upstream:     respondJSON(200, big),
	})
	result := decodeToolResult(t, h.callTool(t, "get_overview", nil))
	if !result.IsError {
		t.Fatalf("result = %+v, want isError", result)
	}
	if !strings.Contains(result.Text, "too large") || !strings.Contains(result.Text, fmt.Sprint(len(big))) || !strings.Contains(result.Text, "limit") {
		t.Errorf("text = %q", result.Text)
	}
	if strings.Contains(result.Text, "xxxx") {
		t.Error("the error carries a piece of the oversized body")
	}
	// Exactly at the cap still passes.
	atCap := `{"d":"` + strings.Repeat("y", 10000-8) + `"}`
	if len(atCap) != 10000 {
		t.Fatalf("test body is %d bytes", len(atCap))
	}
	h2 := newHarness(t, harnessOptions{mutateConfig: func(c *Config) { c.MaxResultBytes = 10000 }, upstream: respondJSON(200, atCap)})
	if r := decodeToolResult(t, h2.callTool(t, "get_overview", nil)); r.IsError || r.Text != atCap {
		t.Errorf("a result exactly at the cap was rejected: isError=%v", r.IsError)
	}
}

func TestHandler_BadArgumentsAreAToolErrorWithoutUpstreamCall(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for name, c := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"missing":  {"get_service", map[string]any{}, "service_id"},
		"type":     {"get_revision", map[string]any{"service_id": "s", "revision": "x"}, "revision"},
		"unknown":  {"get_overview", map[string]any{"bogus": 1}, "bogus"},
		"enum":     {"get_service_timeline", map[string]any{"service_id": "s", "range": "1y"}, "range"},
		"dot-dot":  {"get_service", map[string]any{"service_id": ".."}, ""},
		"no logs":  {"search_logs", map[string]any{"service_ids": []string{"a"}}, "query"},
		"bad type": {"list_tasks", map[string]any{"limit": "ten"}, "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			result := decodeToolResult(t, h.callTool(t, c.tool, c.args))
			if !result.IsError || (c.want != "" && !strings.Contains(result.Text, c.want)) {
				t.Errorf("result = %+v, want an error naming %q", result, c.want)
			}
		})
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls, want 0", n)
	}
}

func TestHandler_UnknownToolIsAnError(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp := h.callTool(t, "delete_service", map[string]any{"service_id": "x"})
	body := readBody(t, resp)
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls for an unknown tool (%d: %s)", n, resp.StatusCode, body)
	}
	if strings.Contains(body, `"isError":false`) || !strings.Contains(body, "error") {
		t.Errorf("body = %s", body)
	}
}

func TestHandler_GetIs405(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, h.url(), nil)
		req.Header.Set("Authorization", "Bearer "+h.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
			t.Errorf("%s: status %d Allow %q, want 405 with Allow: POST", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
		readBody(t, resp)
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls", n)
	}
}

func TestHandler_OversizedBodyIs413(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	big := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"q":"` + strings.Repeat("a", 300<<10) + `"}}}`)
	resp := h.post(t, h.authed(), big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	readBody(t, resp)

	// The limit also holds without a Content-Length (chunked).
	req, _ := http.NewRequest(http.MethodPost, h.url(), io.NopCloser(bytes.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	chunked, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if chunked.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: status = %d, want 413", chunked.StatusCode)
	}
	readBody(t, chunked)
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls", n)
	}
}

func TestHandler_HealthzAndUnknownPaths(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.upstream.Close() // healthz must not depend on Harbormaster
	resp, err := http.Get(h.gateway.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != 200 || body != "ok" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
	for _, path := range []string{"/", "/api/overview", "/mcp/", "/healthz/x", "/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(h.gateway.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
		readBody(t, resp)
	}
}

func TestHandler_LogsNeverContainTheTokenOrTheResult(t *testing.T) {
	const marker = "RESULT-MARKER-5f2c1"
	const argMarker = "ARGUMENT-MARKER-91ab"
	h := newHarness(t, harnessOptions{upstream: respondJSON(200, `{"secret_looking":"`+marker+`"}`)})
	resp := h.rpc(t, h.authed(), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{"q": argMarker}})
	if r := decodeToolResult(t, resp); r.IsError || !strings.Contains(r.Text, marker) {
		t.Fatalf("result = %+v", r)
	}
	// Also drive failures, which log more.
	decodeToolResult(t, h.callTool(t, "get_service", map[string]any{"service_id": argMarker + "/x"}))
	h.upstream.Close()
	decodeToolResult(t, h.callTool(t, "search", map[string]any{"q": argMarker}))
	readBody(t, h.rpc(t, h.authed(), "tools/list", nil))

	logs := h.log.String()
	if !strings.Contains(logs, "tools/call") || !strings.Contains(logs, "search") || !strings.Contains(logs, h.display) {
		t.Errorf("log lacks the method, tool and display id:\n%s", logs)
	}
	secret := strings.SplitN(strings.TrimPrefix(h.token, "hmp_"), "_", 2)[1]
	for _, forbidden := range []string{h.token, secret, "Bearer", "Authorization", marker, argMarker} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("the log contains %q:\n%s", forbidden, logs)
		}
	}
}

func TestHandler_LogsRejectedTokensWithoutTheToken(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{}`)})
	readBody(t, h.callTool(t, "get_overview", nil))
	readBody(t, h.rpc(t, map[string]string{"Authorization": "Bearer garbage-token-value"}, "tools/list", nil))
	logs := h.log.String()
	secret := strings.SplitN(strings.TrimPrefix(h.token, "hmp_"), "_", 2)[1]
	for _, forbidden := range []string{h.token, secret, "garbage-token-value"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("the log contains %q:\n%s", forbidden, logs)
		}
	}
	if !strings.Contains(logs, "upstream_status=401") {
		t.Errorf("log lacks the upstream status:\n%s", logs)
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	rows := []struct {
		name    string
		r       *http.Request
		trusted bool
		want    string
	}{
		{"peer when not trusted, header ignored", req("10.0.0.5:1234", "6.6.6.6"), false, "10.0.0.5"},
		{"rightmost entry when trusted", req("10.0.0.5:1234", "6.6.6.6, 1.2.3.4"), true, "1.2.3.4"},
		{"spoofed leftmost is ignored", req("10.0.0.5:1234", "9.9.9.9, 8.8.8.8, 1.2.3.4"), true, "1.2.3.4"},
		{"trailing empty entries skipped", req("10.0.0.5:1234", "1.2.3.4, ,"), true, "1.2.3.4"},
		{"no header falls back to the peer", req("10.0.0.5:1234", ""), true, "10.0.0.5"},
		{"peer without a port", req("10.0.0.5", ""), false, "10.0.0.5"},
	}
	for _, row := range rows {
		if got := clientIP(row.r, row.trusted); got != row.want {
			t.Errorf("%s: %q, want %q", row.name, got, row.want)
		}
	}
}

func TestHandler_ForwardsTheChainUpstream(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: func(c *Config) { c.TrustedProxy = true }})
	headers := h.authed()
	headers["X-Forwarded-For"] = "203.0.113.7"
	decodeToolResult(t, h.rpc(t, headers, "tools/call", map[string]any{"name": "get_overview"}))
	got := h.upstream.seen()[0].Header.Get("X-Forwarded-For")
	if !strings.HasPrefix(got, "203.0.113.7, ") {
		t.Errorf("X-Forwarded-For upstream = %q, want the client's entry followed by the gateway's peer", got)
	}

	// Untrusted: the client's own header must not be passed on as if it were a proxy's.
	h2 := newHarness(t, harnessOptions{})
	headers = h2.authed()
	headers["X-Forwarded-For"] = "203.0.113.7"
	decodeToolResult(t, h2.rpc(t, headers, "tools/call", map[string]any{"name": "get_overview"}))
	if got := h2.upstream.seen()[0].Header.Get("X-Forwarded-For"); strings.Contains(got, "203.0.113.7") {
		t.Errorf("an untrusted client's X-Forwarded-For was forwarded: %q", got)
	}
}

func TestHandler_BatchIsRejectedWithoutUpstreamCall(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	call := `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"get_overview","arguments":{}}}`
	var entries []string
	for i := 0; i < 50; i++ {
		entries = append(entries, fmt.Sprintf(call, i))
	}
	for name, body := range map[string]string{
		"batch of calls":     "[" + strings.Join(entries, ",") + "]",
		"leading whitespace": " \n\t[" + entries[0] + "]",
		"empty batch":        "[]",
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.post(t, h.authed(), []byte(body))
			text := readBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %s", resp.StatusCode, text)
			}
			if !strings.Contains(text, `"jsonrpc":"2.0"`) || !strings.Contains(text, `"error"`) {
				t.Errorf("body %q is not a JSON-RPC error", text)
			}
		})
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls, want 0", n)
	}
}

func TestHandler_AmbiguousTopLevelKeysAreRejectedWithoutUpstreamCall(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for name, body := range map[string]string{
		"case variant method":     `{"jsonrpc":"2.0","id":1,"method":"tools/list","Method":"tools/call"}`,
		"case variant first":      `{"METHOD":"tools/call","jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"only a case variant":     `{"jsonrpc":"2.0","id":1,"Method":"tools/list"}`,
		"escaped case variant":    `{"jsonrpc":"2.0","id":1,"method":"tools/list","Method":"tools/call"}`,
		"duplicate method":        `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`,
		"duplicate params":        `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{},"params":{}}`,
		"case variant params":     `{"jsonrpc":"2.0","id":1,"method":"tools/list","Params":{}}`,
		"case variant id":         `{"jsonrpc":"2.0","id":1,"method":"tools/list","ID":2}`,
		"case variant jsonrpc":    `{"jsonrpc":"2.0","id":1,"method":"tools/list","JSONRPC":"2.0"}`,
		"duplicate unrelated key": `{"jsonrpc":"2.0","id":1,"method":"tools/list","extra":1,"extra":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.post(t, h.authed(), []byte(body))
			text := readBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status %d, want 400; body %s", resp.StatusCode, text)
			}
		})
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls, want 0", n)
	}
}

func TestHandler_FakeTokenCannotBypassTheTokenCheck(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"no"}}`)})
	for _, method := range []string{"initialize", "tools/list"} {
		resp := h.rpc(t, h.authed(), method, map[string]any{})
		readBody(t, resp)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", method, resp.StatusCode)
		}
	}
	if n := len(h.upstream.seen()); n != 2 {
		t.Errorf("%d upstream calls, want one token check per request", n)
	}
}

func TestHandler_UpstreamBodyIsReadOnlyUpToTwiceTheResultCap(t *testing.T) {
	big := `{"data":"` + strings.Repeat("x", 1<<20) + `"}`
	h := newHarness(t, harnessOptions{
		mutateConfig: func(c *Config) { c.MaxResultBytes = 100000 },
		upstream:     respondJSON(200, big),
	})
	result := decodeToolResult(t, h.callTool(t, "get_overview", nil))
	want := fmt.Sprintf("(%d bytes)", upstreamReadCap(100000))
	if !result.IsError || !strings.Contains(result.Text, want) {
		t.Errorf("text = %q, want a too-large error reporting %s", result.Text, want)
	}
}

func TestUpstreamReadCap(t *testing.T) {
	if got := upstreamReadCap(140000); got != 280000 {
		t.Errorf("cap for 140000 = %d, want 280000", got)
	}
	if got := upstreamReadCap(10000); got < 2*10000 || got > 1<<20 {
		t.Errorf("cap for 10000 = %d, want a small floor of at least 20000", got)
	}
}
