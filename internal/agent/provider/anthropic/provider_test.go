package anthropic

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

// testKey is assembled from pieces so secret scanners do not flag it.
const testKey = "sk-ant-" + "fake-test-key-0000"

type captured struct {
	body   map[string]any
	header http.Header
	path   string
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sseServer(t *testing.T, sse []byte, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &got.body)
			got.header = r.Header.Clone()
			got.path = r.URL.Path
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProvider(t *testing.T, endpoint string) provider.Provider {
	t.Helper()
	p, err := New(Config{APIKey: testKey, Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func simpleRequest() provider.Request {
	return provider.Request{
		Model:           "test-model",
		System:          "You are helpful.",
		Messages:        []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
		MaxOutputTokens: 512,
	}
}

func TestGenerate_TextOnly_StreamsDeltasAndUsage(t *testing.T) {
	var got captured
	srv := sseServer(t, fixture(t, "text_only.sse"), &got)
	p := newProvider(t, srv.URL)

	var deltas []string
	resp, err := p.Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Hello|, world" {
		t.Errorf("deltas = %q", deltas)
	}
	if resp.Text != "Hello, world" || resp.StopReason != "end" || len(resp.ToolCalls) != 0 {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage != (provider.Usage{InputTokens: 25, OutputTokens: 12}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if got.path != "/v1/messages" || got.header.Get("X-Api-Key") != testKey {
		t.Errorf("path %q key header %q", got.path, got.header.Get("X-Api-Key"))
	}
	if got.body["model"] != "test-model" || got.body["stream"] != true || got.body["max_tokens"] != float64(512) {
		t.Errorf("body = %v", got.body)
	}
	sys, _ := json.Marshal(got.body["system"])
	if !strings.Contains(string(sys), "You are helpful.") {
		t.Errorf("system = %s", sys)
	}
}

func TestGenerate_NilOnText(t *testing.T) {
	srv := sseServer(t, fixture(t, "text_only.sse"), nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err != nil || resp.Text != "Hello, world" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestGenerate_ToolCall_ParsesArguments(t *testing.T) {
	srv := sseServer(t, fixture(t, "tool_call.sse"), nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || resp.Text != "Checking." {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	first := resp.ToolCalls[0]
	if first.ID != "toolu_01" || first.Name != "get_service" || first.Arguments["name"] != "web" || first.Arguments["limit"] != float64(3) {
		t.Errorf("first = %+v", first)
	}
	second := resp.ToolCalls[1]
	if second.ID != "toolu_02" || second.Name != "get_overview" || second.Arguments == nil || len(second.Arguments) != 0 {
		t.Errorf("second = %+v", second)
	}
	if resp.Usage != (provider.Usage{InputTokens: 40, OutputTokens: 30}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerate_SendsToolResultInVendorForm(t *testing.T) {
	var got captured
	srv := sseServer(t, fixture(t, "text_only.sse"), &got)
	req := simpleRequest()
	req.Tools = []provider.ToolDef{{
		Name:        "get_service",
		Description: "Look up a service.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}}
	req.Messages = []provider.Message{
		{Role: provider.RoleUser, Text: "how is web?"},
		{Role: provider.RoleAssistant, Text: "Checking.", ToolCalls: []provider.ToolCall{
			{ID: "toolu_01", Name: "get_service", Arguments: map[string]any{"name": "web"}},
			{ID: "toolu_02", Name: "get_overview"},
		}},
		{Role: provider.RoleTool, ToolCallID: "toolu_01", ToolName: "get_service", Text: "healthy"},
		{Role: provider.RoleTool, ToolCallID: "toolu_02", ToolName: "get_overview", Text: "boom", IsError: true},
	}
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}

	tools := got.body["tools"].([]any)
	tool := tools[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if tool["name"] != "get_service" || tool["description"] != "Look up a service." || schema["type"] != "object" ||
		schema["properties"] == nil || schema["required"].([]any)[0] != "name" {
		t.Errorf("tool = %v", tool)
	}

	msgs := got.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages (user, assistant, merged tool results), got %d: %v", len(msgs), msgs)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("assistant = %v", asst)
	}
	blocks := asst["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks = %v", blocks)
	}
	use := blocks[1].(map[string]any)
	if use["type"] != "tool_use" || use["id"] != "toolu_01" || use["name"] != "get_service" || use["input"].(map[string]any)["name"] != "web" {
		t.Errorf("tool_use = %v", use)
	}
	if in, ok := blocks[2].(map[string]any)["input"].(map[string]any); !ok || len(in) != 0 {
		t.Errorf("nil arguments must be sent as an empty object, got %v", blocks[2])
	}
	results := msgs[2].(map[string]any)
	if results["role"] != "user" {
		t.Errorf("results = %v", results)
	}
	rb := results["content"].([]any)
	if len(rb) != 2 {
		t.Fatalf("result blocks = %v", rb)
	}
	r1, r2 := rb[0].(map[string]any), rb[1].(map[string]any)
	if r1["type"] != "tool_result" || r1["tool_use_id"] != "toolu_01" || r2["tool_use_id"] != "toolu_02" || r2["is_error"] != true {
		t.Errorf("tool results = %v %v", r1, r2)
	}
	if !strings.Contains(mustJSON(r1["content"]), "healthy") {
		t.Errorf("r1 content = %v", r1["content"])
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestGenerate_MapsErrors(t *testing.T) {
	const marker = "SECRETBODY"
	cases := []struct {
		name      string
		status    int
		header    map[string]string
		body      string
		kind      string
		retryable bool
		retry     time.Duration
	}{
		{"401", 401, nil, `{"type":"error","error":{"type":"authentication_error","message":"` + marker + `"}}`, "auth", false, 0},
		{"403", 403, nil, `{"type":"error","error":{"type":"permission_error","message":"` + marker + `"}}`, "auth", false, 0},
		{"429", 429, map[string]string{"Retry-After": "7"}, `{"type":"error","error":{"type":"rate_limit_error","message":"` + marker + `"}}`, "rate_limited", true, 7 * time.Second},
		{"500", 500, nil, `{"type":"error","error":{"type":"api_error","message":"` + marker + `"}}`, "unavailable", true, 0},
		{"529", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"` + marker + `"}}`, "unavailable", true, 0},
		{"context length", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum ` + marker + `"}}`, "context_too_long", false, 0},
		{"other 400", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"` + marker + `"}}`, "invalid_request", false, 0},
		{"404", 404, nil, `{"type":"error","error":{"type":"not_found_error","message":"` + marker + `"}}`, "invalid_request", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want *provider.Error, got %T %v", err, err)
			}
			if perr.Kind != tc.kind || perr.Retryable != tc.retryable || perr.RetryAfter != tc.retry {
				t.Errorf("got %+v", perr)
			}
			if hits.Load() != 1 {
				t.Errorf("SDK retried: %d requests", hits.Load())
			}
		})
	}
}

func TestGenerate_ErrorMessageNeverContainsKeyOrBody(t *testing.T) {
	const marker = "SECRETBODY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"`+marker+` `+testKey+`"}}`)
	}))
	defer srv.Close()
	_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err == nil {
		t.Fatal("want error")
	}
	if s := err.Error(); strings.Contains(s, marker) || strings.Contains(s, testKey) || strings.Contains(s, "sk-ant") {
		t.Errorf("error leaks: %q", s)
	}

	// An error event inside a stream must not leak either.
	stream := sseServer(t, fixture(t, "stream_error.sse"), nil)
	_, err = newProvider(t, stream.URL).Generate(context.Background(), simpleRequest(), nil)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Errorf("stream error = %v", err)
	}
}

func TestGenerate_StreamBreak_KeepsUsageAndRetryableOnlyBeforeText(t *testing.T) {
	cases := []struct {
		fixture   string
		retryable bool
		text      string
	}{
		{"truncated_after_text.sse", false, "Partial"},
		{"truncated_before_text.sse", true, ""},
		{"stream_error.sse", true, ""},
		{"no_final_usage.sse", false, "Hi"},
		{"stop_reason_no_usage.sse", false, "Hi"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			srv := sseServer(t, fixture(t, tc.fixture), nil)
			var emitted strings.Builder
			resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), func(d string) { emitted.WriteString(d) })
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want *provider.Error, got %T %v", err, err)
			}
			if perr.Kind != "unavailable" || perr.Retryable != tc.retryable {
				t.Errorf("got %+v", perr)
			}
			if resp.Usage.InputTokens != 25 {
				t.Errorf("usage already reported was dropped: %+v", resp.Usage)
			}
			if emitted.String() != tc.text {
				t.Errorf("emitted %q", emitted.String())
			}
		})
	}
}

func TestGenerate_ContextCancelReturnsCtxErr(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server only notices a closed connection once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := newProvider(t, srv.URL).Generate(ctx, simpleRequest(), nil)
	var perr *provider.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &perr) {
		t.Fatalf("want bare context.Canceled, got %T %v", err, err)
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var targetHits, redirectHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		redirectHits.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for name, client := range map[string]*http.Client{"default": nil, "supplied": srv.Client()} {
		t.Run(name, func(t *testing.T) {
			targetHits.Store(0)
			p, err := New(Config{APIKey: testKey, Endpoint: srv.URL, HTTPClient: client})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want *provider.Error, got %T %v", err, err)
			}
			if targetHits.Load() != 0 {
				t.Fatal("the redirect was followed")
			}
		})
	}
}

func TestClient_IgnoresEnvironmentOverrides(t *testing.T) {
	var hostile atomic.Int32
	var hostileKey atomic.Value
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostile.Add(1)
		hostileKey.Store(r.Header.Get("X-Api-Key"))
	}))
	defer evil.Close()
	for k, v := range map[string]string{
		"ANTHROPIC_BASE_URL":       evil.URL,
		"ANTHROPIC_API_KEY":        "env-" + "key",
		"ANTHROPIC_AUTH_TOKEN":     "env-" + "token",
		"ANTHROPIC_CUSTOM_HEADERS": "X-Env-Header: leaked",
		"ANTHROPIC_PROFILE":        "nope",
		"HTTPS_PROXY":              evil.URL,
		"HTTP_PROXY":               evil.URL,
		"ALL_PROXY":                evil.URL,
	} {
		t.Setenv(k, v)
	}

	var got captured
	srv := sseServer(t, fixture(t, "text_only.sse"), &got)
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil); err != nil {
		t.Fatal(err)
	}
	if hostile.Load() != 0 {
		t.Fatal("the hostile server received a request")
	}
	if got.header.Get("X-Api-Key") != testKey || got.header.Get("Authorization") != "" || got.header.Get("X-Env-Header") != "" {
		t.Errorf("headers = %v", got.header)
	}

	// With no Endpoint the request goes to the fixed vendor host, whatever the
	// environment says.
	var seen *http.Request
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(string(fixture(t, "text_only.sse")))), Request: r}, nil
	})
	fixed, err := New(Config{APIKey: testKey, HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixed.Generate(context.Background(), simpleRequest(), nil); err != nil {
		t.Fatal(err)
	}
	if seen.URL.Host != "api.anthropic.com" || seen.Header.Get("X-Api-Key") != testKey || seen.Header.Get("X-Env-Header") != "" {
		t.Errorf("request went to %s with headers %v", seen.URL.Host, seen.Header)
	}

	// The built transport must not use a proxy from the environment.
	tr, ok := newHTTPClient().Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Errorf("transport must not use proxy-from-environment")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNew_RequiresKey(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error for empty key")
	}
}

func TestGenerate_UsageCountsCachedInputAndThinkingOutput(t *testing.T) {
	sse := strings.Replace(string(fixture(t, "text_only.sse")),
		`"usage":{"input_tokens":25,"output_tokens":1}`,
		`"usage":{"input_tokens":25,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":1}`, 1)
	sse = strings.Replace(sse, `"usage":{"output_tokens":12}`, `"usage":{"output_tokens":12,"output_tokens_details":{"thinking_tokens":9}}`, 1)
	srv := sseServer(t, []byte(sse), nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Cached input is billed too; thinking tokens are already inside output_tokens.
	if resp.Usage != (provider.Usage{InputTokens: 2125, OutputTokens: 12}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerate_MalformedToolArguments_SetsArgError(t *testing.T) {
	srv := sseServer(t, fixture(t, "tool_call_truncated.sse"), nil)
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "max_tokens" || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	call := resp.ToolCalls[0]
	if call.ID != "toolu_09" || call.Arguments != nil || call.ArgError != "arguments were not a valid JSON object" {
		t.Errorf("call = %+v", call)
	}

	for _, raw := range []string{`[1]`, `null`, `"x"`, `7`} {
		sse := strings.Replace(string(fixture(t, "tool_call_truncated.sse")), `{\"name\": \"we`, strings.ReplaceAll(raw, `"`, `\"`), 1)
		s := sseServer(t, []byte(sse), nil)
		r, err := newProvider(t, s.URL).Generate(context.Background(), simpleRequest(), nil)
		if err != nil || len(r.ToolCalls) != 1 || r.ToolCalls[0].ArgError == "" || r.ToolCalls[0].Arguments != nil {
			t.Errorf("%s: resp %+v err %v", raw, r, err)
		}
	}
}

func TestGenerate_ReplaysCallWithArgErrorAsEmptyObject(t *testing.T) {
	var got captured
	srv := sseServer(t, fixture(t, "text_only.sse"), &got)
	req := simpleRequest()
	req.Messages = []provider.Message{
		{Role: provider.RoleUser, Text: "hi"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "toolu_09", Name: "get_service", ArgError: "arguments were not a valid JSON object"}}},
		{Role: provider.RoleTool, ToolCallID: "toolu_09", Text: "bad arguments", IsError: true},
	}
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	use := got.body["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if in, ok := use["input"].(map[string]any); !ok || len(in) != 0 {
		t.Errorf("tool_use = %v", use)
	}
}

// thinkingTurn runs the thinking fixture and returns the assistant turn as the
// loop would store it (text, tool calls and the Opaque, copied unchanged).
func thinkingTurn(t *testing.T) (provider.Response, provider.Message) {
	t.Helper()
	srv := sseServer(t, fixture(t, "thinking_tool_use.sse"), nil)
	var deltas []string
	resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	// Thinking lives only in Opaque, never in Text or onText.
	if resp.Text != "Checking." || strings.Join(deltas, "") != "Checking." || !strings.Contains(string(resp.Opaque), "Use the tool.") {
		t.Fatalf("text %q deltas %q opaque %s", resp.Text, deltas, resp.Opaque)
	}
	if len(resp.Opaque) == 0 || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	return resp, provider.Message{Role: provider.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, Opaque: resp.Opaque}
}

func replayBlocks(t *testing.T, assistant provider.Message) []map[string]any {
	t.Helper()
	var got captured
	srv := sseServer(t, fixture(t, "text_only.sse"), &got)
	req := simpleRequest()
	req.Messages = []provider.Message{
		{Role: provider.RoleUser, Text: "how is web?"},
		assistant,
		{Role: provider.RoleTool, ToolCallID: "toolu_10", Text: "healthy"},
	}
	if _, err := newProvider(t, srv.URL).Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, b := range got.body["messages"].([]any)[1].(map[string]any)["content"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

func TestGenerate_ThinkingBlocksRoundTripInOrder(t *testing.T) {
	_, assistant := thinkingTurn(t)
	blocks := replayBlocks(t, assistant)
	if len(blocks) != 4 {
		t.Fatalf("blocks = %v", blocks)
	}
	types := []string{}
	for _, b := range blocks {
		types = append(types, b["type"].(string))
	}
	if strings.Join(types, ",") != "thinking,text,redacted_thinking,tool_use" {
		t.Errorf("order = %v", types)
	}
	if blocks[0]["thinking"] != "Let me think. Use the tool." || blocks[0]["signature"] != "sig-abc" {
		t.Errorf("thinking = %v", blocks[0])
	}
	if blocks[1]["text"] != "Checking." || blocks[2]["data"] != "opaque-redacted" {
		t.Errorf("text/redacted = %v %v", blocks[1], blocks[2])
	}
	if blocks[3]["id"] != "toolu_10" || blocks[3]["input"].(map[string]any)["name"] != "web" {
		t.Errorf("tool_use = %v", blocks[3])
	}
}

func TestGenerate_UnusableOpaqueIsIgnored(t *testing.T) {
	_, good := thinkingTurn(t)
	call := good.ToolCalls
	for name, opaque := range map[string]string{
		"array of number":        `[1]`,
		"array of null":          `[null]`,
		"empty object":           `{}`,
		"empty array":            `[]`,
		"string":                 `"x"`,
		"not json":               `not json`,
		"other vendor reasoning": `[{"type":"reasoning","id":"rs_1","summary":[]}]`,
		"gemini signature":       `{"thoughtSignature":"abc"}`,
		"thinking no signature":  `[{"type":"thinking","thinking":"x"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_10"}]`,
		"redacted no data":       `[{"type":"redacted_thinking"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_10"}]`,
		"unknown block type":     `[{"type":"server_tool_use","id":"x"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_10"}]`,
		"tool id mismatch":       `[{"type":"thinking","thinking":"x","signature":"s"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"other"}]`,
		"text mismatch":          `[{"type":"thinking","thinking":"x","signature":"s"},{"type":"text","text":"Different"},{"type":"tool_use","id":"toolu_10"}]`,
		"missing tool call":      `[{"type":"thinking","thinking":"x","signature":"s"},{"type":"text","text":"Checking."}]`,
	} {
		t.Run(name, func(t *testing.T) {
			blocks := replayBlocks(t, provider.Message{Role: provider.RoleAssistant, Text: "Checking.", ToolCalls: call, Opaque: json.RawMessage(opaque)})
			if len(blocks) != 2 || blocks[0]["type"] != "text" || blocks[1]["type"] != "tool_use" {
				t.Errorf("want plain text+tool_use fallback, got %v", blocks)
			}
		})
	}
}

func TestGenerate_StopReasons(t *testing.T) {
	cases := []struct {
		vendor string
		stop   string // "" when an error is expected
		errMsg string
	}{
		{"end_turn", "end", ""},
		{"stop_sequence", "end", ""},
		{"tool_use", "tool_use", ""},
		{"max_tokens", "max_tokens", ""},
		{"model_context_window_exceeded", "max_tokens", ""},
		{"pause_turn", "max_tokens", ""},
		{"refusal", "", "The model declined to answer."},
		{"something_new", "", "The model stopped for an unknown reason."},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			sse := strings.Replace(string(fixture(t, "text_only.sse")), `"stop_reason":"end_turn"`, `"stop_reason":"`+tc.vendor+`"`, 1)
			srv := sseServer(t, []byte(sse), nil)
			resp, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
			if tc.errMsg == "" {
				if err != nil || resp.StopReason != tc.stop {
					t.Fatalf("resp %+v err %v", resp, err)
				}
				return
			}
			var perr *provider.Error
			if !errors.As(err, &perr) || perr.Kind != "invalid_request" || perr.Retryable || perr.Message != tc.errMsg {
				t.Fatalf("err = %#v", err)
			}
			if resp.Usage != (provider.Usage{InputTokens: 25, OutputTokens: 12}) {
				t.Errorf("usage dropped: %+v", resp.Usage)
			}
		})
	}
}

func TestGenerate_InStreamErrorsMapByType(t *testing.T) {
	cases := []struct {
		vendorType string
		kind       string
		retryable  bool
	}{
		{"rate_limit_error", "rate_limited", true},
		{"overloaded_error", "unavailable", true},
		{"api_error", "unavailable", true},
		{"invalid_request_error", "invalid_request", false},
		{"authentication_error", "auth", false},
		{"permission_error", "auth", false},
		{"brand_new_error", "unavailable", true},
	}
	for _, tc := range cases {
		for _, afterText := range []bool{false, true} {
			name := tc.vendorType
			if afterText {
				name += "/after_text"
			}
			t.Run(name, func(t *testing.T) {
				sse := string(fixture(t, "truncated_before_text.sse"))
				if afterText {
					sse = string(fixture(t, "truncated_after_text.sse"))
				}
				sse += "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + tc.vendorType + "\",\"message\":\"SECRETBODY\"}}\n\n"
				srv := sseServer(t, []byte(sse), nil)
				_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
				var perr *provider.Error
				if !errors.As(err, &perr) {
					t.Fatalf("err = %T %v", err, err)
				}
				wantRetry := tc.retryable && !afterText
				if perr.Kind != tc.kind || perr.Retryable != wantRetry || strings.Contains(perr.Error(), "SECRETBODY") {
					t.Errorf("got %+v want kind %s retryable %v", perr, tc.kind, wantRetry)
				}
			})
		}
	}
}

// The log line for a failed call needs the HTTP status and the vendor's error type, and nothing the vendor wrote.
func TestGenerate_ErrorsCarryStatusAndVendorType(t *testing.T) {
	cases := []struct {
		name, body, wantType string
		status               int
	}{
		{"rate limit", `{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`, "rate_limit_error", 429},
		{"overloaded", `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`, "overloaded_error", 529},
		{"type that is not an identifier", `{"type":"error","error":{"type":"oops, see the account page","message":"x"}}`, "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := newProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want *provider.Error, got %T %v", err, err)
			}
			if perr.Status != tc.status || perr.VendorType != tc.wantType {
				t.Errorf("status %d type %q, want %d %q", perr.Status, perr.VendorType, tc.status, tc.wantType)
			}
		})
	}
}
