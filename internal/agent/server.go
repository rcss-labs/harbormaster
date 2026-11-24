package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	maxRequestBodyBytes = 1 << 20
	maxMessages         = 500
	// testCallMaxOutputTokens and testCallTimeout bound the settings page's "Test model" call.
	testCallMaxOutputTokens = 16
	testCallTimeout         = 45 * time.Second
	testPrompt              = "Reply with the single word: ok"
)

// whoamiRoute is how a run token is confirmed with Harbormaster.
var whoamiRoute = hmtools.Route{Method: http.MethodGet, Pattern: "/api/tokens/current"}

type server struct {
	cfg       Config
	up        *hmtools.Upstream
	tools     ToolRunner
	providers map[string]provider.Provider
	log       *slog.Logger
	slots     chan struct{}

	active   atomic.Int64
	finished sync.Map // status -> *atomic.Int64
}

// NewHandler serves the agent's API: GET /healthz, /metrics and /v1/catalogue,
// POST /v1/runs and /v1/test. Only providers present in providers are offered.
func NewHandler(cfg Config, up *hmtools.Upstream, providers map[string]provider.Provider, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		cfg: cfg, up: up, providers: providers, log: log,
		tools: NewUpstreamToolRunner(up, cfg.MaxResultBytes),
		slots: make(chan struct{}, max(cfg.MaxConcurrentRuns, 1)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /metrics", s.serveMetrics)
	mux.HandleFunc("GET /v1/catalogue", s.serveCatalogue)
	mux.HandleFunc("POST /v1/runs", s.serveRun)
	mux.HandleFunc("POST /v1/test", s.serveTest)
	return s.guard(mux)
}

// guard marks every response uncacheable and turns a panic outside a run into a 500.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("request panicked", "path", r.URL.Path, "panic_type", fmt.Sprintf("%T", p), "frames", panicFrames())
				writeError(w, http.StatusInternalServerError, "internal", "The agent failed unexpectedly.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// panicFrames names the functions on the stack of a panic. Names only: no
// arguments and no panic value, which may carry content.
func panicFrames() []string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		frame, more := frames.Next()
		out = append(out, frame.Function+":"+strconv.Itoa(frame.Line))
		if !more {
			return out
		}
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) serveCatalogue(w http.ResponseWriter, _ *http.Request) {
	type model struct {
		ID            string `json:"id"`
		InputPerMTok  int64  `json:"input_per_mtok_micro_usd"`
		OutputPerMTok int64  `json:"output_per_mtok_micro_usd"`
		ContextTokens int64  `json:"context_tokens"`
	}
	type providerEntry struct {
		Provider string  `json:"provider"`
		Models   []model `json:"models"`
	}
	out := struct {
		Providers []providerEntry `json:"providers"`
		Limits    struct {
			MaxRunTokens int64 `json:"max_run_tokens"`
			Timeout      int64 `json:"run_timeout_seconds"`
		} `json:"limits"`
	}{Providers: []providerEntry{}}
	for _, entry := range s.cfg.Catalogue {
		if _, ok := s.providers[entry.Provider]; !ok {
			continue
		}
		listed := providerEntry{Provider: entry.Provider, Models: []model{}}
		for _, m := range entry.Models {
			listed.Models = append(listed.Models, model{m.ID, m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok, m.ContextTokens})
		}
		out.Providers = append(out.Providers, listed)
	}
	out.Limits.MaxRunTokens = s.cfg.MaxRunTokens
	out.Limits.Timeout = int64(s.cfg.RunTimeout / time.Second)
	writeJSON(w, out)
}

// runStatuses are the labels of harbormaster_agent_runs_total.
var runStatuses = []string{"done", "error", "cancelled", "panic"}

func (s *server) countFinished(status string) {
	counter, _ := s.finished.LoadOrStore(status, new(atomic.Int64))
	counter.(*atomic.Int64).Add(1)
}

func (s *server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder
	b.WriteString("# TYPE harbormaster_agent_runs_total counter\n")
	for _, status := range runStatuses {
		var n int64
		if counter, ok := s.finished.Load(status); ok {
			n = counter.(*atomic.Int64).Load()
		}
		fmt.Fprintf(&b, "harbormaster_agent_runs_total{status=%q} %d\n", status, n)
	}
	b.WriteString("# TYPE harbormaster_agent_runs_active gauge\n")
	fmt.Fprintf(&b, "harbormaster_agent_runs_active %d\n", s.active.Load())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// authenticate accepts only a run token that Harbormaster confirms right now.
// On refusal it has already answered.
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (bearer string, ok bool) {
	header := r.Header.Get("Authorization")
	if len(header) > len("Bearer ") && strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		bearer = strings.TrimSpace(header[len("Bearer "):])
	}
	parsed, valid := apitoken.Parse(bearer)
	if !valid || parsed.Kind != apitoken.KindRun {
		unauthenticated(w)
		return "", false
	}
	resp, err := s.up.Do(r.Context(), bearer, "", hmtools.Call{Route: whoamiRoute})
	switch {
	case err != nil:
		if r.Context().Err() == nil {
			s.log.Warn("token check failed", "reason", "upstream_unreachable")
		}
		writeError(w, http.StatusBadGateway, "upstream_unavailable", "Harbormaster could not be reached.")
	case resp.Status == http.StatusUnauthorized:
		unauthenticated(w)
	case resp.Status < 200 || resp.Status > 299:
		s.log.Warn("token check failed", "reason", "upstream_status", "status", resp.Status)
		writeError(w, http.StatusBadGateway, "upstream_unavailable", "Harbormaster could not confirm the token.")
	default:
		return bearer, true
	}
	return "", false
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster-agent"`)
	writeError(w, http.StatusUnauthorized, "unauthenticated", "A valid run token is required.")
}

// decodeBody reads one JSON object of at most 1 MiB into v. On refusal it has already answered.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request is larger than 1 MiB.")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		}
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(v); err != nil || decoder.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not one JSON object.")
		return false
	}
	return true
}

// acquire takes a concurrency slot or answers 429.
func (s *server) acquire(w http.ResponseWriter) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "agent_busy", "The agent is running its maximum number of runs. Try again shortly.")
		return false
	}
}

func (s *server) release() { <-s.slots }

// lookup finds a model that is in the catalogue and whose provider is built.
func (s *server) lookup(providerID, modelID string) (provider.Provider, provider.Model, bool) {
	p, ok := s.providers[providerID]
	if !ok {
		return nil, provider.Model{}, false
	}
	model, ok := s.cfg.Catalogue.Find(providerID, modelID)
	return p, model, ok
}

type runRequest struct {
	RunID    string `json:"run_id"`
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Messages []struct {
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"messages"`
	Page struct {
		Path      string `json:"path"`
		ServiceID string `json:"service_id"`
		ClusterID string `json:"cluster_id"`
	} `json:"page"`
	MaxCostMicroUSD int64 `json:"max_cost_micro_usd"`
}

// wireEvent is Event as it travels: flat, with empty fields left out.
type wireEvent struct {
	Type         string `json:"type"`
	Delta        string `json:"delta,omitempty"`
	Tool         string `json:"tool,omitempty"`
	OK           bool   `json:"ok,omitempty"`
	InputTokens  int64  `json:"input_tokens,omitempty"`
	OutputTokens int64  `json:"output_tokens,omitempty"`
	CostMicroUSD int64  `json:"cost_micro_usd,omitempty"`
	Provisional  bool   `json:"provisional,omitempty"`
	Notice       string `json:"notice,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
}

func toWire(ev Event) wireEvent {
	out := wireEvent{
		Type: ev.Type, Delta: ev.Delta, Tool: ev.Tool, OK: ev.OK, CostMicroUSD: ev.CostMicroUSD,
		Provisional: ev.Provisional, Notice: ev.Notice, Code: ev.Code, Message: ev.Message,
	}
	if ev.Usage != nil {
		out.InputTokens, out.OutputTokens = ev.Usage.InputTokens, ev.Usage.OutputTokens
	}
	return out
}

func (s *server) serveRun(w http.ResponseWriter, r *http.Request) {
	bearer, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var req runRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if message := validateRun(&req); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	prov, model, found := s.lookup(req.Provider, req.Model)
	if !found {
		writeError(w, http.StatusNotFound, "unknown_model", "The agent does not offer that model.")
		return
	}
	if !s.acquire(w) {
		return
	}
	defer s.release()

	input := RunInput{
		RunID: req.RunID, Role: req.Role, Model: model, Bearer: bearer, MaxCostMicroUSD: req.MaxCostMicroUSD,
		Page: PageContext{Path: req.Page.Path, ServiceID: req.Page.ServiceID, ClusterID: req.Page.ClusterID},
	}
	for _, m := range req.Messages {
		input.Messages = append(input.Messages, provider.Message{Role: provider.Role(m.Role), Text: m.Text})
	}
	s.stream(w, r, prov, input, req.Provider)
}

func validateRun(req *runRequest) string {
	if _, err := uuid.Parse(req.RunID); err != nil {
		return "run_id must be a UUID."
	}
	if _, ok := roleFrames[req.Role]; !ok {
		return "This kind of run is not supported."
	}
	if req.Provider == "" || req.Model == "" {
		return "provider and model are required."
	}
	if req.MaxCostMicroUSD <= 0 {
		return "max_cost_micro_usd must be positive."
	}
	if len(req.Messages) == 0 || len(req.Messages) > maxMessages {
		return "messages must hold between 1 and 500 entries."
	}
	for _, m := range req.Messages {
		if m.Role != string(provider.RoleUser) && m.Role != string(provider.RoleAssistant) {
			return "Each message role must be user or assistant."
		}
	}
	if req.Messages[len(req.Messages)-1].Role != string(provider.RoleUser) {
		return "The last message must be from the user."
	}
	return ""
}

// stream runs one conversation turn and writes its events as NDJSON, one
// flushed line each. The run ends when the client goes away.
func (s *server) stream(w http.ResponseWriter, r *http.Request, prov provider.Provider, input RunInput, providerID string) {
	started := time.Now()
	s.active.Add(1)
	defer s.active.Add(-1)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	var (
		mu                   sync.Mutex
		tokensIn, tokensOut  int64
		cost                 int64
		toolStart            time.Time
		finalType, finalCode string
	)
	emit := func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case "usage":
			if ev.Usage != nil {
				tokensIn, tokensOut, cost = ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.CostMicroUSD
			}
		case "tool_start":
			toolStart = time.Now()
		case "tool_end":
			s.log.Info("tool call",
				"run_id", input.RunID, "role", input.Role, "provider", providerID, "model", input.Model.ID,
				"tool", ev.Tool, "status", toolStatus(ev.OK), "duration_ms", time.Since(toolStart).Milliseconds(),
				"input_tokens", tokensIn, "output_tokens", tokensOut)
		case "done", "error":
			finalType, finalCode = ev.Type, ev.Code
		}
		line, _ := json.Marshal(toWire(ev))
		_, _ = w.Write(append(line, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}

	loop := &Loop{
		Provider: prov, Tools: s.tools,
		Limits: Limits{MaxSteps: s.cfg.MaxSteps, MaxRunTokens: s.cfg.MaxRunTokens, Timeout: s.cfg.RunTimeout, MaxResultBytes: s.cfg.MaxResultBytes},
	}
	panicked := false
	func() {
		defer func() {
			if p := recover(); p != nil {
				panicked = true
				s.log.Error("run panicked", "run_id", input.RunID, "panic_type", fmt.Sprintf("%T", p), "frames", panicFrames())
				emit(Event{Type: "error", Code: "internal", Message: "The run failed unexpectedly."})
			}
		}()
		_ = loop.Run(r.Context(), input, emit)
	}()

	status := "cancelled"
	switch {
	case panicked:
		status = "panic"
	case finalType == "done":
		status = "done"
	case finalType == "error":
		status = "error"
	}
	s.countFinished(status)
	mu.Lock()
	defer mu.Unlock()
	s.log.Info("run finished",
		"run_id", input.RunID, "role", input.Role, "provider", providerID, "model", input.Model.ID,
		"status", status, "error_code", finalCode, "duration_ms", time.Since(started).Milliseconds(),
		"input_tokens", tokensIn, "output_tokens", tokensOut, "cost_micro_usd", cost)
}

func toolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

type testRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type testAnswer struct {
	OK           bool   `json:"ok"`
	Kind         string `json:"kind,omitempty"`
	Message      string `json:"message,omitempty"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
}

// serveTest makes one tiny call with no tools and reports how it went.
func (s *server) serveTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	var req testRequest
	if !decodeBody(w, r, &req) {
		return
	}
	prov, model, found := s.lookup(req.Provider, req.Model)
	if !found {
		writeError(w, http.StatusNotFound, "unknown_model", "The agent does not offer that model.")
		return
	}
	if !s.acquire(w) {
		return
	}
	defer s.release()

	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), min(testCallTimeout, s.cfg.RunTimeout))
	defer cancel()
	resp, err := prov.Generate(ctx, provider.Request{
		Model: model.ID, System: "You are a connectivity check.", MaxOutputTokens: testCallMaxOutputTokens,
		Messages: []provider.Message{{Role: provider.RoleUser, Text: testPrompt}},
	}, nil)

	answer := testAnswer{OK: err == nil}
	if err == nil {
		answer.InputTokens, answer.OutputTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
		answer.CostMicroUSD = model.CostMicroUSD(resp.Usage)
	} else {
		var perr *provider.Error
		if errors.As(err, &perr) {
			answer.Kind = perr.Kind
			answer.Message = secretshape.ScrubText(providerMessage(perr))
		} else {
			answer.Kind = "unavailable"
			answer.Message = "The model could not be reached."
		}
	}
	s.log.Info("test finished", "provider", req.Provider, "model", model.ID, "ok", answer.OK, "kind", answer.Kind,
		"duration_ms", time.Since(started).Milliseconds(), "input_tokens", answer.InputTokens, "output_tokens", answer.OutputTokens)
	writeJSON(w, answer)
}
