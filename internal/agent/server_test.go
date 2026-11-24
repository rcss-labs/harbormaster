package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/hmtools/hmtoolstest"
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

var fakeModel = provider.Model{ID: "fake-1", InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 1_000_000, ContextTokens: 100_000}

type harness struct {
	server   *httptest.Server
	upstream *hmtoolstest.RecordingServer
	log      *syncLog
	token    string
}

type harnessOptions struct {
	providers   map[string]provider.Provider
	catalogue   provider.Catalogue
	edit        func(*Config)
	upstreamAPI http.HandlerFunc // answers everything except whoami
}

func newRunToken(t *testing.T, kind apitoken.Kind) string {
	t.Helper()
	token, _, err := apitoken.Generate(kind)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	h := &harness{log: &syncLog{}, token: newRunToken(t, apitoken.KindRun)}
	h.upstream = hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tokens/current" {
			if r.Header.Get("Authorization") != "Bearer "+h.token {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"code":"unauthenticated","message":"no"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"x"}`)
			return
		}
		if opts.upstreamAPI != nil {
			opts.upstreamAPI(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"clusters":2}`)
	})
	up, err := hmtools.NewUpstreamWithCA(h.upstream.URL, 5*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	catalogue := opts.catalogue
	if catalogue == nil {
		catalogue = provider.Catalogue{{Provider: "fake", Models: []provider.Model{fakeModel}}}
	}
	providers := opts.providers
	if providers == nil {
		providers = map[string]provider.Provider{"fake": fake.Demo()}
	}
	cfg := Config{
		Catalogue: catalogue, MaxSteps: 10, MaxRunTokens: 100_000, RunTimeout: time.Minute,
		MaxConcurrentRuns: 4, MaxResultBytes: 100_000,
	}
	if opts.edit != nil {
		opts.edit(&cfg)
	}
	h.server = httptest.NewServer(NewHandler(cfg, up, providers, slog.New(slog.NewJSONHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) runBody(mutate func(map[string]any)) []byte {
	body := map[string]any{
		"run_id": uuid.NewString(), "role": "chat", "provider": "fake", "model": "fake-1",
		"messages":           []map[string]string{{"role": "user", "text": "How many clusters?"}},
		"page":               map[string]string{"path": "/clusters", "service_id": "", "cluster_id": ""},
		"max_cost_micro_usd": 1_000_000,
	}
	if mutate != nil {
		mutate(body)
	}
	raw, _ := json.Marshal(body)
	return raw
}

func (h *harness) post(t *testing.T, path, token string, body []byte) *http.Response {
	t.Helper()
	return h.postContext(t, context.Background(), path, token, body)
}

func (h *harness) postContext(t *testing.T, ctx context.Context, path, token string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

type seenEvent struct {
	Type         string `json:"type"`
	Delta        string `json:"delta"`
	Tool         string `json:"tool"`
	OK           bool   `json:"ok"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	Provisional  bool   `json:"provisional"`
	Notice       string `json:"notice"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

func readEvents(t *testing.T, resp *http.Response) []seenEvent {
	t.Helper()
	var events []seenEvent
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var ev seenEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("line %q is not an event: %v", scanner.Text(), err)
		}
		events = append(events, ev)
	}
	return events
}

func lastEvent(t *testing.T, events []seenEvent) seenEvent {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("the stream held no events")
	}
	return events[len(events)-1]
}

func errorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("not an error envelope: %v", err)
	}
	return body.Error.Code
}

// blockingProvider enters Generate, reports it, and waits for its context to end.
type blockingProvider struct {
	entered   chan struct{}
	cancelled chan struct{}
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{entered: make(chan struct{}, 8), cancelled: make(chan struct{}, 8)}
}

func (p *blockingProvider) Name() string { return "fake" }

func (p *blockingProvider) Generate(ctx context.Context, _ provider.Request, _ func(string)) (provider.Response, error) {
	p.entered <- struct{}{}
	<-ctx.Done()
	p.cancelled <- struct{}{}
	return provider.Response{}, ctx.Err()
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestRuns_RejectsTokenWhoamiRefuses(t *testing.T) {
	demo := fake.Demo()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": demo}})
	resp := h.post(t, "/v1/runs", newRunToken(t, apitoken.KindRun), h.runBody(nil))
	if resp.StatusCode != http.StatusUnauthorized || errorCode(t, resp) != "unauthenticated" {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if n := len(demo.Requests()); n != 0 {
		t.Errorf("%d provider calls were made for a token Harbormaster refused", n)
	}
	if n := len(h.upstream.Seen()); n != 1 {
		t.Errorf("%d upstream requests, want exactly the whoami", n)
	}
}

func TestRuns_OnlyARunTokenIsEvenChecked(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for name, token := range map[string]string{
		"none": "", "garbage": "nope", "personal token": newRunToken(t, apitoken.KindPAT), "oauth access token": newRunToken(t, apitoken.KindOAuthAccess),
	} {
		for _, path := range []string{"/v1/runs", "/v1/test"} {
			resp := h.post(t, path, token, h.runBody(nil))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s on %s: status = %d", name, path, resp.StatusCode)
			}
		}
	}
	if n := len(h.upstream.Seen()); n != 0 {
		t.Errorf("%d upstream requests for tokens that are not run tokens", n)
	}
}

func TestRuns_WhoamiIsNotCachedAcrossRequests(t *testing.T) {
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": fake.New(fake.Text("a", provider.Usage{InputTokens: 1, OutputTokens: 1}), fake.Text("b", provider.Usage{InputTokens: 1, OutputTokens: 1}))}})
	for i := 0; i < 2; i++ {
		resp := h.post(t, "/v1/runs", h.token, h.runBody(nil))
		readEvents(t, resp)
	}
	whoami := 0
	for _, r := range h.upstream.Seen() {
		if r.EscapedPath == "/api/tokens/current" {
			whoami++
		}
	}
	if whoami != 2 {
		t.Errorf("%d whoami calls for 2 runs", whoami)
	}
}

func TestRuns_StreamsEventsAsNDJSON(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp := h.post(t, "/v1/runs", h.token, h.runBody(nil))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status = %d, content type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := readEvents(t, resp)
	var types []string
	var text strings.Builder
	var lastUsage seenEvent
	for _, ev := range events {
		types = append(types, ev.Type)
		text.WriteString(ev.Delta)
		if ev.Type == "usage" && !ev.Provisional {
			lastUsage = ev
		}
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "tool_start,tool_end") || (len(types) == 0 || types[len(types)-1] != "done") {
		t.Errorf("types = %s", joined)
	}
	if !strings.Contains(text.String(), "canned answer") {
		t.Errorf("text = %q", text.String())
	}
	if lastUsage.InputTokens != 200 || lastUsage.OutputTokens != 30 || lastUsage.CostMicroUSD != 230 {
		t.Errorf("final usage = %+v, want flat 200/30 tokens at 230 micro-dollars", lastUsage)
	}
	for _, ev := range events {
		if ev.Type == "tool_end" && (!ev.OK || ev.Tool != "get_overview") {
			t.Errorf("tool_end = %+v", ev)
		}
	}
	// The tool ran with the run's own token.
	var overview *hmtoolstest.RecordedRequest
	for _, r := range h.upstream.Seen() {
		if r.EscapedPath == "/api/overview" {
			overview = &r
		}
	}
	if overview == nil || overview.Header.Get("Authorization") != "Bearer "+h.token {
		t.Errorf("overview request = %+v", overview)
	}
}

func TestRuns_ProviderErrorBecomesAnErrorEvent(t *testing.T) {
	failing := fake.New(fake.Fail(&provider.Error{Kind: "auth"}))
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": failing}})
	events := readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(nil)))
	last := lastEvent(t, events)
	if last.Type != "error" || last.Code != "auth" || last.Message == "" {
		t.Errorf("last event = %+v", last)
	}
}

func TestRuns_UnknownModelIs404(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for name, mutate := range map[string]func(map[string]any){
		"unknown model":    func(b map[string]any) { b["model"] = "nope" },
		"unknown provider": func(b map[string]any) { b["provider"] = "nope" },
	} {
		resp := h.post(t, "/v1/runs", h.token, h.runBody(mutate))
		if resp.StatusCode != http.StatusNotFound || errorCode(t, resp) != "unknown_model" {
			t.Errorf("%s: status = %d", name, resp.StatusCode)
		}
	}
	// A model in the catalogue whose provider has no credential is not offered either.
	h = newHarness(t, harnessOptions{
		catalogue: provider.Catalogue{{Provider: "fake", Models: []provider.Model{fakeModel}}, {Provider: "openai", Models: []provider.Model{{ID: "o1", InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1, ContextTokens: 1000}}}},
	})
	resp := h.post(t, "/v1/runs", h.token, h.runBody(func(b map[string]any) { b["provider"], b["model"] = "openai", "o1" }))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unconfigured provider: status = %d", resp.StatusCode)
	}
}

func TestRuns_InvalidRequestIs400(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	cases := map[string]func(map[string]any){
		"tool role":   func(b map[string]any) { b["messages"] = []map[string]string{{"role": "tool", "text": "x"}} },
		"system role": func(b map[string]any) { b["messages"] = []map[string]string{{"role": "system", "text": "x"}} },
		"no messages": func(b map[string]any) { b["messages"] = []map[string]string{} },
		"last is assistant": func(b map[string]any) {
			b["messages"] = []map[string]string{{"role": "user", "text": "a"}, {"role": "assistant", "text": "b"}}
		},
		"zero max cost":     func(b map[string]any) { b["max_cost_micro_usd"] = 0 },
		"negative max cost": func(b map[string]any) { b["max_cost_micro_usd"] = -5 },
		"run id not a uuid": func(b map[string]any) { b["run_id"] = "not a uuid\nforged log line" },
		"unsupported role":  func(b map[string]any) { b["role"] = "admin" },
		"missing provider":  func(b map[string]any) { delete(b, "provider") },
	}
	for name, mutate := range cases {
		resp := h.post(t, "/v1/runs", h.token, h.runBody(mutate))
		if resp.StatusCode != http.StatusBadRequest || errorCode(t, resp) != "invalid_request" {
			t.Errorf("%s: status = %d", name, resp.StatusCode)
		}
	}
	for name, body := range map[string]string{"not json": "{", "two values": `{} {}`, "empty": ""} {
		resp := h.post(t, "/v1/runs", h.token, []byte(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, resp.StatusCode)
		}
	}
}

func TestRuns_OversizedBodyIsRefusedCleanly(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	big := h.runBody(func(b map[string]any) {
		b["messages"] = []map[string]string{{"role": "user", "text": strings.Repeat("a", 2<<20)}}
	})
	resp := h.post(t, "/v1/runs", h.token, big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || errorCode(t, resp) != "request_too_large" {
		t.Errorf("status = %d", resp.StatusCode)
	}
	// The server is still serving.
	if resp := h.post(t, "/v1/runs", h.token, h.runBody(nil)); resp.StatusCode != http.StatusOK {
		t.Errorf("after the oversized body: status = %d", resp.StatusCode)
	}
}

func TestRuns_ConcurrencyCapIs429(t *testing.T) {
	blocking := newBlockingProvider()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": blocking}, edit: func(c *Config) { c.MaxConcurrentRuns = 1 }})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+"/v1/runs", bytes.NewReader(h.runBody(nil)))
		req.Header.Set("Authorization", "Bearer "+h.token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			first <- resp
		}
	}()
	waitFor(t, blocking.entered, "the first run to reach the model")

	resp := h.post(t, "/v1/runs", h.token, h.runBody(nil))
	if resp.StatusCode != http.StatusTooManyRequests || errorCode(t, resp) != "agent_busy" {
		t.Errorf("second run: status = %d", resp.StatusCode)
	}
	if resp := h.post(t, "/v1/test", h.token, []byte(`{"provider":"fake","model":"fake-1"}`)); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("test call while busy: status = %d", resp.StatusCode)
	}

	cancel()
	waitFor(t, blocking.cancelled, "the first run to be cancelled")
	// The slot comes back once the run is over.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := h.post(t, "/v1/runs", h.token, h.runBody(nil))
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slot was never released")
		}
		time.Sleep(20 * time.Millisecond)
		_ = resp
	}
}

func TestRuns_ClientDisconnectCancelsTheRun(t *testing.T) {
	blocking := newBlockingProvider()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": blocking}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+"/v1/runs", bytes.NewReader(h.runBody(nil)))
		req.Header.Set("Authorization", "Bearer "+h.token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
	}()
	waitFor(t, blocking.entered, "the run to reach the model")
	cancel()
	waitFor(t, blocking.cancelled, "the provider call to be cancelled")
}

func TestCatalogue_ListsOnlyConfiguredProviders(t *testing.T) {
	two := provider.Catalogue{
		{Provider: "fake", Models: []provider.Model{fakeModel}},
		{Provider: "openai", Models: []provider.Model{{ID: "o1", InputMicroUSDPerMTok: 5, OutputMicroUSDPerMTok: 6, ContextTokens: 1000}}},
	}
	h := newHarness(t, harnessOptions{catalogue: two, edit: func(c *Config) { c.MaxRunTokens = 12345; c.RunTimeout = 90 * time.Second }})
	resp, err := http.Get(h.server.URL + "/v1/catalogue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Providers []struct {
			Provider string `json:"provider"`
			Models   []struct {
				ID            string `json:"id"`
				Input         int64  `json:"input_per_mtok_micro_usd"`
				Output        int64  `json:"output_per_mtok_micro_usd"`
				ContextTokens int64  `json:"context_tokens"`
			} `json:"models"`
		} `json:"providers"`
		Limits struct {
			MaxRunTokens int64 `json:"max_run_tokens"`
			Timeout      int64 `json:"run_timeout_seconds"`
		} `json:"limits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 1 || body.Providers[0].Provider != "fake" || len(body.Providers[0].Models) != 1 {
		t.Fatalf("providers = %+v", body.Providers)
	}
	if m := body.Providers[0].Models[0]; m.ID != "fake-1" || m.Input != 1_000_000 || m.Output != 1_000_000 || m.ContextTokens != 100_000 {
		t.Errorf("model = %+v", m)
	}
	if body.Limits.MaxRunTokens != 12345 || body.Limits.Timeout != 90 {
		t.Errorf("limits = %+v", body.Limits)
	}
}

func TestTest_ReportsProviderErrorKind(t *testing.T) {
	type answer struct {
		OK           bool   `json:"ok"`
		Kind         string `json:"kind"`
		Message      string `json:"message"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		CostMicroUSD int64  `json:"cost_micro_usd"`
	}
	call := func(p provider.Provider) (answer, []provider.Request) {
		h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": p}})
		resp := h.post(t, "/v1/test", h.token, []byte(`{"provider":"fake","model":"fake-1"}`))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var a answer
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
		return a, nil
	}

	good := fake.New(fake.Text("ok", provider.Usage{InputTokens: 10, OutputTokens: 2}))
	a, _ := call(good)
	if !a.OK || a.InputTokens != 10 || a.OutputTokens != 2 || a.CostMicroUSD != 12 {
		t.Errorf("success = %+v", a)
	}
	req := good.Requests()[0]
	if len(req.Tools) != 0 || req.MaxOutputTokens != 16 || req.Model != "fake-1" {
		t.Errorf("the test call sent tools=%d max=%d model=%q", len(req.Tools), req.MaxOutputTokens, req.Model)
	}

	a, _ = call(fake.New(fake.Fail(&provider.Error{Kind: "auth"})))
	if a.OK || a.Kind != "auth" || a.Message == "" {
		t.Errorf("auth failure = %+v", a)
	}
	a, _ = call(fake.New(fake.Fail(&provider.Error{Kind: "rate_limited", Retryable: true, RetryAfter: time.Hour})))
	if a.OK || a.Kind != "rate_limited" {
		t.Errorf("rate limit = %+v (the test call must not wait to retry)", a)
	}
	a, _ = call(fake.New(fake.Fail(errors.New("dial tcp 10.0.0.1: refused"))))
	if a.OK || a.Kind != "unavailable" || strings.Contains(a.Message, "10.0.0.1") {
		t.Errorf("transport failure = %+v", a)
	}
}

func TestLogs_NeverContainMessageTextOrToken(t *testing.T) {
	const question = "QUESTION-TEXT-MARKER how are my clusters"
	const toolResult = "TOOL-RESULT-MARKER"
	h := newHarness(t, harnessOptions{upstreamAPI: func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"note":"`+toolResult+`"}`) }})
	events := readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(func(b map[string]any) {
		b["messages"] = []map[string]string{{"role": "user", "text": question}}
		b["page"] = map[string]string{"path": "/PAGE-PATH-MARKER", "service_id": "", "cluster_id": ""}
	})))
	if lastEvent(t, events).Type != "done" {
		t.Fatalf("events = %+v", events)
	}
	h.post(t, "/v1/test", h.token, []byte(`{"provider":"fake","model":"fake-1"}`))
	h.post(t, "/v1/runs", "hma_"+strings.Repeat("a", 40), h.runBody(nil))

	logs := h.log.String()
	parsed, _ := apitoken.Parse(h.token)
	for _, secret := range []string{h.token, parsed.Secret, question, toolResult, "PAGE-PATH-MARKER", "canned answer"} {
		if strings.Contains(logs, secret) {
			t.Errorf("a log line contains %q", secret)
		}
	}
	for _, want := range []string{`"run_id"`, `"role":"chat"`, `"provider":"fake"`, `"model":"fake-1"`, `"tool":"get_overview"`, `"duration_ms"`, `"input_tokens"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the logs lack %s:\n%s", want, logs)
		}
	}
	runLines, toolLines := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, `"tool":"get_overview"`) {
			toolLines++
		}
		if strings.Contains(line, `"msg":"run finished"`) {
			runLines++
		}
	}
	if runLines != 1 || toolLines != 1 {
		t.Errorf("%d run lines and %d tool lines, want one each", runLines, toolLines)
	}
}

// panickingProvider panics with content in the panic value.
type panickingProvider struct{}

func (panickingProvider) Name() string { return "fake" }
func (panickingProvider) Generate(context.Context, provider.Request, func(string)) (provider.Response, error) {
	panic("PANIC-CONTENT-MARKER")
}

func TestRuns_APanicBecomesAnErrorEventAndIsLoggedWithoutContent(t *testing.T) {
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": panickingProvider{}}, edit: func(c *Config) { c.MaxConcurrentRuns = 1 }})
	for i := 0; i < 2; i++ { // twice: the slot must have been released
		events := readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(nil)))
		last := lastEvent(t, events)
		if last.Type != "error" || last.Code != "internal" || strings.Contains(last.Message, "PANIC") {
			t.Fatalf("run %d: last event = %+v", i, last)
		}
	}
	logs := h.log.String()
	if strings.Contains(logs, "PANIC-CONTENT-MARKER") || !strings.Contains(logs, "panic") {
		t.Errorf("logs = %s", logs)
	}
}

func TestEveryResponseIsNoStore(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	check := func(name string, resp *http.Response) {
		t.Helper()
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", name, got)
		}
	}
	for _, path := range []string{"/healthz", "/metrics", "/v1/catalogue", "/nope"} {
		resp, err := http.Get(h.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		check("GET "+path, resp)
	}
	check("wrong method", h.post(t, "/v1/catalogue", "", nil))
	check("401", h.post(t, "/v1/runs", "", nil))
	check("400", h.post(t, "/v1/runs", h.token, []byte("{")))
	check("stream", h.post(t, "/v1/runs", h.token, h.runBody(nil)))
	check("test", h.post(t, "/v1/test", h.token, []byte(`{"provider":"fake","model":"fake-1"}`)))
}

func TestHealthzAndMetrics(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp, err := http.Get(h.server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
	readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(nil)))
	upstreamBefore := len(h.upstream.Seen())
	resp, err = http.Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `harbormaster_agent_runs_total{status="done"} 1`) || !strings.Contains(string(body), "harbormaster_agent_runs_active 0") {
		t.Errorf("metrics = %d %s", resp.StatusCode, body)
	}
	if n := len(h.upstream.Seen()); n != upstreamBefore {
		t.Errorf("metrics reached upstream: %d new requests", n-upstreamBefore)
	}
}
