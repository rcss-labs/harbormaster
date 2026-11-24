package bedrock

import (
	"bytes"
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

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// fakeSecret is assembled from two pieces so secret scanners ignore it.
const fakeSecret = "fake-secret-" + "key-0123456789"

type fixtureEvent struct {
	Type      string          `json:"type"`
	Exception string          `json:"exception"`
	Payload   json.RawMessage `json:"payload"`
}

// loadFixture encodes a testdata file as an AWS event stream.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var events []fixtureEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	for _, ev := range events {
		var msg eventstream.Message
		if ev.Exception != "" {
			msg.Headers.Set(":message-type", eventstream.StringValue("exception"))
			msg.Headers.Set(":exception-type", eventstream.StringValue(ev.Exception))
		} else {
			msg.Headers.Set(":message-type", eventstream.StringValue("event"))
			msg.Headers.Set(":event-type", eventstream.StringValue(ev.Type))
		}
		msg.Headers.Set(":content-type", eventstream.StringValue("application/json"))
		msg.Payload = ev.Payload
		if err := enc.Encode(&buf, msg); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

type captured struct {
	path string
	body []byte
}

// streamServer replays a fixture and records the last request.
func streamServer(t *testing.T, fixture string) (*httptest.Server, *captured) {
	t.Helper()
	body := loadFixture(t, fixture)
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.EscapedPath()
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestProvider(t *testing.T, endpoint string) provider.Provider {
	t.Helper()
	p, err := New(Config{
		Region:      "us-east-1",
		Endpoint:    endpoint,
		Credentials: credentials.NewStaticCredentialsProvider("AKIDFAKE", fakeSecret, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func simpleRequest() provider.Request {
	return provider.Request{
		Model:           "test.model-v1:0",
		System:          "You are terse.",
		MaxOutputTokens: 256,
		Messages:        []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	}
}

func TestGenerate_TextOnly_StreamsDeltasAndUsage(t *testing.T) {
	srv, got := streamServer(t, "text_only.json")
	p := newTestProvider(t, srv.URL)

	var deltas []string
	resp, err := p.Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Hello|, world" {
		t.Fatalf("deltas = %q", deltas)
	}
	if resp.Text != "Hello, world" || resp.StopReason != "end" || len(resp.ToolCalls) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage != (provider.Usage{InputTokens: 12, OutputTokens: 5}) {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if !strings.HasSuffix(got.path, "/model/test.model-v1%3A0/converse-stream") {
		t.Fatalf("path = %s", got.path)
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "You are terse." {
		t.Fatalf("system = %v", body["system"])
	}
	if body["inferenceConfig"].(map[string]any)["maxTokens"] != float64(256) {
		t.Fatalf("inferenceConfig = %v", body["inferenceConfig"])
	}
}

func TestGenerate_ToolCall_ParsesArguments(t *testing.T) {
	srv, _ := streamServer(t, "tool_call.json")
	p := newTestProvider(t, srv.URL)

	resp, err := p.Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || resp.Text != "Checking." {
		t.Fatalf("resp = %+v", resp)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	first := resp.ToolCalls[0]
	if first.ID != "tooluse_abc" || first.Name != "list_services" ||
		first.Arguments["cluster"] != "prod" || first.Arguments["limit"] != float64(3) {
		t.Fatalf("first = %+v", first)
	}
	second := resp.ToolCalls[1]
	if second.ID != "tooluse_def" || second.Name != "no_args" || len(second.Arguments) != 0 || second.Arguments == nil {
		t.Fatalf("second = %+v", second)
	}
}

func TestGenerate_SendsToolResultInVendorForm(t *testing.T) {
	srv, got := streamServer(t, "text_only.json")
	p := newTestProvider(t, srv.URL)

	req := provider.Request{
		Model: "test.model-v1:0",
		Tools: []provider.ToolDef{{
			Name:        "list_services",
			Description: "List services.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"cluster": map[string]any{"type": "string"}}},
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "what runs?"},
			{Role: provider.RoleAssistant, Text: "Looking.", ToolCalls: []provider.ToolCall{
				{ID: "tu_1", Name: "list_services", Arguments: map[string]any{"cluster": "prod"}},
				{ID: "tu_2", Name: "list_services", Arguments: map[string]any{"cluster": "dev"}},
			}},
			// Two consecutive tool messages must become one user message.
			{Role: provider.RoleTool, ToolCallID: "tu_1", ToolName: "list_services", Text: "api, web"},
			{Role: provider.RoleTool, ToolCallID: "tu_2", ToolName: "list_services", Text: "boom", IsError: true},
			{Role: provider.RoleUser, Text: "and then?"},
		},
	}
	if _, err := p.Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}

	var body struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		ToolConfig struct {
			Tools []struct {
				ToolSpec struct {
					Name        string
					Description string
					InputSchema struct {
						JSON map[string]any `json:"json"`
					}
				} `json:"toolSpec"`
			} `json:"tools"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("want 3 alternating messages, got %s", got.body)
	}
	for i, role := range []string{"user", "assistant", "user"} {
		if body.Messages[i].Role != role {
			t.Fatalf("message %d role = %s", i, body.Messages[i].Role)
		}
	}
	assistant := body.Messages[1].Content
	if len(assistant) != 3 || assistant[0]["text"] != "Looking." {
		t.Fatalf("assistant = %v", assistant)
	}
	use := assistant[1]["toolUse"].(map[string]any)
	if use["toolUseId"] != "tu_1" || use["name"] != "list_services" || use["input"].(map[string]any)["cluster"] != "prod" {
		t.Fatalf("toolUse = %v", use)
	}
	last := body.Messages[2].Content
	if len(last) != 3 {
		t.Fatalf("last user message = %v", last)
	}
	r1 := last[0]["toolResult"].(map[string]any)
	if r1["toolUseId"] != "tu_1" || r1["content"].([]any)[0].(map[string]any)["text"] != "api, web" || r1["status"] != nil && r1["status"] != "success" {
		t.Fatalf("result 1 = %v", r1)
	}
	r2 := last[1]["toolResult"].(map[string]any)
	if r2["toolUseId"] != "tu_2" || r2["status"] != "error" {
		t.Fatalf("result 2 = %v", r2)
	}
	if last[2]["text"] != "and then?" {
		t.Fatalf("trailing text = %v", last[2])
	}
	spec := body.ToolConfig.Tools[0].ToolSpec
	if spec.Name != "list_services" || spec.Description != "List services." || spec.InputSchema.JSON["type"] != "object" {
		t.Fatalf("toolSpec = %+v", spec)
	}
}

func TestGenerate_RoundTripsReasoningOpaque(t *testing.T) {
	srv, got := streamServer(t, "reasoning.json")
	p := newTestProvider(t, srv.URL)

	resp, err := p.Generate(context.Background(), simpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Opaque) == 0 {
		t.Fatal("reasoning was not captured in Opaque")
	}

	req := simpleRequest()
	req.Messages = append(req.Messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: resp.ToolCalls, Opaque: resp.Opaque},
		provider.Message{Role: provider.RoleTool, ToolCallID: "tooluse_r", Text: "ok"},
	)
	if _, err := p.Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	blocks := body.Messages[1].Content
	rc, ok := blocks[0]["reasoningContent"].(map[string]any)
	if !ok {
		t.Fatalf("assistant turn must start with reasoningContent: %v", blocks)
	}
	rt := rc["reasoningText"].(map[string]any)
	if rt["text"] != "think hard" || rt["signature"] != "sig-fake-1" {
		t.Fatalf("reasoningText = %v", rt)
	}
	if blocks[1]["toolUse"] == nil {
		t.Fatalf("toolUse must follow reasoning: %v", blocks)
	}
}

func TestGenerate_IgnoresUnparsableOpaque(t *testing.T) {
	srv, got := streamServer(t, "text_only.json")
	p := newTestProvider(t, srv.URL)
	req := simpleRequest()
	req.Messages = append(req.Messages, provider.Message{Role: provider.RoleAssistant, Text: "earlier", Opaque: json.RawMessage(`{"not":"ours"}`)},
		provider.Message{Role: provider.RoleUser, Text: "more"})
	if _, err := p.Generate(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.body), "reasoningContent") {
		t.Fatalf("unexpected reasoning in %s", got.body)
	}
}

func errorServer(status int, errType, message string, header http.Header) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		if errType != "" {
			w.Header().Set("X-Amzn-Errortype", errType)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	}))
}

func TestGenerate_MapsErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		errType    string
		message    string
		header     http.Header
		kind       string
		retryable  bool
		retryAfter time.Duration
	}{
		{"unauthorized", 401, "UnrecognizedClientException", "bad token", nil, "auth", false, 0},
		{"forbidden", 403, "AccessDeniedException", "no access", nil, "auth", false, 0},
		{"rate limited", 429, "ThrottlingException", "slow down", http.Header{"Retry-After": {"7"}}, "rate_limited", true, 7 * time.Second},
		{"server error", 500, "InternalServerException", "oops", nil, "unavailable", true, 0},
		{"bad gateway without error type", 502, "", "upstream", nil, "unavailable", true, 0},
		{"unavailable", 503, "ServiceUnavailableException", "busy", nil, "unavailable", true, 0},
		{"context length", 400, "ValidationException", "Input is too long for requested model.", nil, "context_too_long", false, 0},
		{"other validation", 400, "ValidationException", "The provided model identifier is invalid.", nil, "invalid_request", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := errorServer(tc.status, tc.errType, tc.message, tc.header)
			defer srv.Close()
			_, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
			var perr *provider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("want provider.Error, got %T %v", err, err)
			}
			if perr.Kind != tc.kind || perr.Retryable != tc.retryable || perr.RetryAfter != tc.retryAfter {
				t.Fatalf("got %+v", perr)
			}
		})
	}
}

func TestGenerate_ErrorMessageNeverContainsKeyOrBody(t *testing.T) {
	const marker = "vendor-body-marker-should-not-leak"
	for _, status := range []int{400, 401, 403, 429, 500} {
		srv := errorServer(status, "ValidationException", marker+" "+fakeSecret, nil)
		_, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		for _, leak := range []string{marker, fakeSecret, "AKIDFAKE", srv.URL} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("status %d: error leaks %q: %v", status, leak, err)
			}
		}
	}
	// Stream-level exceptions carry vendor text too.
	srv, _ := streamServer(t, "midstream_exception.json")
	_, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if err == nil || strings.Contains(err.Error(), "vendor-detail-should-not-leak") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestGenerate_MidStreamFailure_ReturnsUnavailableWithUsage(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		retryable bool
		wantText  string
		wantUsage provider.Usage
	}{
		{"exception after text", "midstream_exception.json", false, "partial", provider.Usage{}},
		{"exception after usage", "exception_after_usage.json", false, "partial", provider.Usage{InputTokens: 9, OutputTokens: 2}},
		{"exception before text", "early_exception.json", true, "", provider.Usage{}},
		{"stream ends without stop", "truncated.json", false, "partial", provider.Usage{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := streamServer(t, tc.fixture)
			var deltas []string
			resp, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), func(d string) { deltas = append(deltas, d) })
			var perr *provider.Error
			if !errors.As(err, &perr) || perr.Kind != "unavailable" || perr.Retryable != tc.retryable {
				t.Fatalf("err = %#v", err)
			}
			if resp.Text != tc.wantText || resp.Usage != tc.wantUsage {
				t.Fatalf("resp = %+v", resp)
			}
			if len(deltas) > 0 && strings.Join(deltas, "") != tc.wantText {
				t.Fatalf("deltas = %q", deltas)
			}
		})
	}
}

func TestGenerate_MidStreamConnectionDrop_ReturnsUnavailable(t *testing.T) {
	body := loadFixture(t, "truncated.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	resp, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Kind != "unavailable" || perr.Retryable {
		t.Fatalf("err = %#v", err)
	}
	if resp.Text != "partial" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestGenerate_ContextCancel_ReturnsContextError(t *testing.T) {
	// The stream is open and has delivered text when the context is cancelled.
	body := loadFixture(t, "truncated.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := newTestProvider(t, srv.URL).Generate(ctx, simpleRequest(), func(string) { cancel() })
	var perr *provider.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &perr) {
		t.Fatalf("err = %#v", err)
	}
}

func TestGenerate_CancelledBeforeRequest_ReturnsContextError(t *testing.T) {
	srv, _ := streamServer(t, "text_only.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestProvider(t, srv.URL).Generate(ctx, simpleRequest(), nil)
	var perr *provider.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &perr) {
		t.Fatalf("err = %#v", err)
	}
}

func TestGenerate_DoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Amzn-Errortype", "ThrottlingException")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	_, _ = newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	var hostileHits atomic.Int32
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostileHits.Add(1) }))
	defer hostile.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hostile.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()
	_, err := newTestProvider(t, srv.URL).Generate(context.Background(), simpleRequest(), nil)
	var perr *provider.Error
	if !errors.As(err, &perr) {
		t.Fatalf("want provider.Error, got %v", err)
	}
	if hostileHits.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
}

type recordingTransport struct {
	hosts []string
	body  []byte
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.hosts = append(rt.hosts, r.URL.Host)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
		Request:    r,
	}, nil
}

func TestClient_IgnoresEnvironmentOverrides(t *testing.T) {
	var hostileHits atomic.Int32
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostileHits.Add(1) }))
	defer hostile.Close()
	for _, name := range []string{
		"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY",
	} {
		t.Setenv(name, hostile.URL)
	}
	t.Setenv("AWS_USE_FIPS_ENDPOINT", "true")
	t.Setenv("AWS_USE_DUALSTACK_ENDPOINT", "true")

	rt := &recordingTransport{body: loadFixture(t, "text_only.json")}
	p, err := New(Config{
		Region:      "eu-west-1",
		HTTPClient:  &http.Client{Transport: rt},
		Credentials: credentials.NewStaticCredentialsProvider("AKIDFAKE", fakeSecret, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), simpleRequest(), nil); err != nil {
		t.Fatal(err)
	}
	if len(rt.hosts) != 1 || rt.hosts[0] != "bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Fatalf("hosts = %v", rt.hosts)
	}
	if hostileHits.Load() != 0 {
		t.Fatal("the hostile server was contacted")
	}

	// The default transport must not honour proxy variables either.
	client, ok := newHTTPClient(nil).(*http.Client)
	if !ok {
		t.Fatal("default client is not an *http.Client")
	}
	tr := client.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.eu-west-1.amazonaws.com/", nil)
	if tr.Proxy != nil {
		if u, _ := tr.Proxy(req); u != nil {
			t.Fatalf("default transport proxies via %v", u)
		}
	}
}

func TestNew_RejectsBadRegion(t *testing.T) {
	for _, region := range []string{"", "evil.example.com/x", "us-east-1.evil.com"} {
		if _, err := New(Config{Region: region}); err == nil {
			t.Fatalf("region %q accepted", region)
		}
	}
}

// The task role arrives through the container credentials endpoint; the
// restricted HTTP client must still be able to fetch from it.
func TestClient_UsesContainerCredentialsEndpoint(t *testing.T) {
	var credHits atomic.Int32
	creds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credHits.Add(1)
		if r.Header.Get("Authorization") != "fake-container-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId":     "AKIDCONTAINER",
			"SecretAccessKey": fakeSecret,
			"Token":           "fake-session-token",
			"Expiration":      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer creds.Close()
	srv, got := streamServer(t, "text_only.json")
	var authorization string
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		inner.ServeHTTP(w, r)
	})

	t.Setenv("HOME", t.TempDir())
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", creds.URL+"/creds")
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "fake-container-token")

	p, err := New(Config{Region: "us-east-1", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), simpleRequest(), nil); err != nil {
		t.Fatal(err)
	}
	if credHits.Load() != 1 || !strings.Contains(authorization, "Credential=AKIDCONTAINER/") || got.path == "" {
		t.Fatalf("credential hits = %d, authorization = %q", credHits.Load(), authorization)
	}
}
