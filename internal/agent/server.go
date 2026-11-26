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
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	maxRequestBodyBytes = 1 << 20
	maxMessages         = 500
	// What a task may hold.
	maxTaskServices       = 20
	maxTaskServiceIDBytes = 512
	maxTaskTextBytes      = 2000
	// testCallMaxOutputTokens and testCallTimeout bound the settings page's "Test model" call.
	testCallMaxOutputTokens = 16
	testCallTimeout         = 45 * time.Second
	testPrompt              = "Reply with the single word: ok"
)

const (
	// pastRunDeadline is how long after the run deadline its last events may still be written.
	pastRunDeadline = 5 * time.Second
)

// Limits on token confirmations: per caller address, overall, and how long a request waits for a slot.
// Variables so tests can shrink them.
var (
	whoamiPerAddress = 16
	whoamiGlobal     = 256
	whoamiWait       = time.Second
)

// whoamiTimeout bounds one token confirmation. A variable so a test can shorten it.
var whoamiTimeout = 10 * time.Second

// writeStallTimeout is how long one write to a run's reader may block. A
// variable so a test can shorten it.
var writeStallTimeout = 20 * time.Second

// whoamiRoute is how a run token is confirmed with Harbormaster.
var whoamiRoute = hmtools.Route{Method: http.MethodGet, Pattern: "/api/tokens/current"}

type server struct {
	cfg       Config
	up        *hmtools.Upstream
	tools     ToolRunner
	providers map[string]provider.Provider
	log       *slog.Logger
	slots     chan struct{}
	checks    *checkLimiter

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
		tools:  NewUpstreamToolRunner(up, cfg.MaxResultBytes),
		slots:  make(chan struct{}, max(cfg.MaxConcurrentRuns, 1)),
		checks: newCheckLimiter(whoamiGlobal, perAddressBound(cfg.MaxConcurrentRuns)),
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

// guard marks every response uncacheable and turns a panic outside a run into
// a 500 when nothing has been sent yet. After the status line is out, a second
// header would only corrupt the stream, so the panic is just logged.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.log.Error("request panicked", "path", r.URL.Path, "panic_type", fmt.Sprintf("%T", p), "frames", panicFrames())
				if !sw.wrote {
					writeError(w, http.StatusInternalServerError, "internal", "The agent failed unexpectedly.")
				}
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// statusWriter remembers whether the response has started.
type statusWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *statusWriter) WriteHeader(status int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the real writer's flush and deadlines.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
		Roles     []string        `json:"roles"`
		// ReviewTypes are the kinds of review the reviewer role knows.
		ReviewTypes []string `json:"review_types"`
		Limits      struct {
			MaxRunTokens int64 `json:"max_run_tokens"`
			Timeout      int64 `json:"run_timeout_seconds"`
		} `json:"limits"`
	}{Providers: []providerEntry{}, Roles: []string{roleChat, roleInvestigator, roleReviewer, "test"}}
	for _, rt := range reviewTypes {
		out.ReviewTypes = append(out.ReviewTypes, rt.id)
	}
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
	// Confirmations are limited per caller address and overall, so one host inventing tokens cannot
	// starve the real callers. A request waits briefly for a slot.
	releaseCheck, ok := s.checks.acquire(r.Context(), hmtools.ClientAddr(r, s.cfg.TrustedProxy))
	if !ok {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusTooManyRequests, "agent_busy", "The agent is busy. Try again shortly.")
		return "", false
	}
	defer releaseCheck()
	// Harbormaster's slowness is the upstream's failure (502), never a refusal of the caller.
	ctx, cancel := context.WithTimeout(r.Context(), whoamiTimeout)
	defer cancel()
	resp, err := s.up.Do(ctx, bearer, "", hmtools.Call{Route: whoamiRoute})
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
		kind, valid := whoamiTokenKind(resp.Body)
		if !valid {
			s.log.Warn("token check failed", "reason", "upstream_answer_not_json")
			writeError(w, http.StatusBadGateway, "upstream_unavailable", "Harbormaster could not confirm the token.")
		} else if kind != string(apitoken.KindRun) {
			unauthenticated(w)
		} else {
			return bearer, true
		}
	}
	return "", false
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster-agent"`)
	writeError(w, http.StatusUnauthorized, "unauthenticated", "A valid run token is required.")
}

// decodeBody reads one JSON object of at most 1 MiB into v and returns it as it came. On refusal it has already answered.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) (raw []byte, ok bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request is larger than 1 MiB.")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		}
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil || decoder.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not one JSON object.")
		return nil, false
	}
	return raw, true
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
	Task            *Task `json:"task"`

	validUTF8 bool // the body as it came, before decoding replaced any byte
}

// wireEvent is Event as it travels: flat, with empty fields left out. Only a
// result event carries the fields of a result, all of them.
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
	*agentresult.Result
}

func toWire(ev Event) wireEvent {
	out := wireEvent{
		Type: ev.Type, Delta: ev.Delta, Tool: ev.Tool, OK: ev.OK, CostMicroUSD: ev.CostMicroUSD,
		Provisional: ev.Provisional, Notice: ev.Notice, Code: ev.Code, Message: ev.Message,
		Result: ev.Result,
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
	raw, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	req.validUTF8 = utf8.Valid(raw)
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
		RunID: req.RunID, Role: req.Role, Model: model, Bearer: bearer, MaxCostMicroUSD: req.MaxCostMicroUSD, Task: req.Task,
		Page: PageContext{Path: req.Page.Path, ServiceID: req.Page.ServiceID, ClusterID: req.Page.ClusterID},
	}
	for _, m := range req.Messages {
		input.Messages = append(input.Messages, provider.Message{Role: provider.Role(m.Role), Text: m.Text})
	}
	s.stream(w, r, prov, input, req.Provider)
}

func validateRun(req *runRequest) string {
	// Canonical form only: the id goes into log lines.
	if id, err := uuid.Parse(req.RunID); err != nil || id.String() != req.RunID {
		return "run_id must be a lowercase UUID."
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
	switch req.Role {
	case roleInvestigator:
		return validateInvestigation(req)
	case roleReviewer:
		return validateReview(req)
	}
	if req.Task != nil {
		return "This kind of run takes no task."
	}
	if len(req.Messages) == 0 || len(req.Messages) > maxMessages {
		return "messages must hold between 1 and 500 entries."
	}
	for _, m := range req.Messages {
		if m.Role != string(provider.RoleUser) && m.Role != string(provider.RoleAssistant) {
			return "Each message role must be user or assistant."
		}
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != string(provider.RoleUser) || strings.TrimSpace(last.Text) == "" {
		return "The last message must be a question from the user."
	}
	return ""
}

// validateInvestigation checks what an investigator run needs in place of messages.
func validateInvestigation(req *runRequest) string {
	if len(req.Messages) != 0 || req.Page.Path != "" || req.Page.ServiceID != "" || req.Page.ClusterID != "" {
		return "This kind of run takes a task, and no messages or page."
	}
	if req.Task == nil || req.Task.Kind != "investigate" {
		return "task must be of kind investigate."
	}
	if req.Task.Review != nil || req.Task.Scope != nil {
		return "An investigation takes no review or scope."
	}
	subject, alert := req.Task.Subject, req.Task.Alert
	switch subject.Kind {
	case "service", "cluster", "account":
	default:
		return "subject kind must be service, cluster or account."
	}
	if len(subject.ServiceIDs) == 0 || len(subject.ServiceIDs) > maxTaskServices {
		return "service_ids must hold between 1 and 20 entries."
	}
	for _, id := range subject.ServiceIDs {
		if strings.TrimSpace(id) == "" || len(id) > maxTaskServiceIDBytes {
			return "Each service id must hold between 1 and 512 bytes."
		}
	}
	for _, text := range []string{subject.ClusterID, subject.AccountID, subject.Region, alert.Kind, alert.Title, alert.Text} {
		if len(text) > maxTaskTextBytes {
			return "Each text of a task must be at most 2000 bytes."
		}
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
	controller := http.NewResponseController(w)

	// A reader that stops reading must not hold the slot: each write has a
	// deadline, and a failed write ends the run.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	runDeadline := started.Add(s.cfg.RunTimeout)
	writeFailed := false
	write := func(line []byte) {
		if writeFailed {
			return
		}
		deadline := time.Now().Add(writeStallTimeout)
		if limit := runDeadline.Add(pastRunDeadline); limit.Before(deadline) {
			deadline = limit
		}
		_ = controller.SetWriteDeadline(deadline)
		_, err := w.Write(line)
		if err == nil {
			err = controller.Flush()
		}
		if err != nil {
			writeFailed = true
			cancel()
		}
	}
	write(nil)

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
		write(append(line, '\n'))
	}

	loop := &Loop{
		Provider: prov, Tools: s.tools, Log: s.log,
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
		_ = loop.Run(ctx, input, emit)
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
	if _, ok := decodeBody(w, r, &req); !ok {
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
	system := "You are a connectivity check."
	resp, err := prov.Generate(ctx, provider.Request{
		Model: model.ID, System: system, MaxOutputTokens: testCallMaxOutputTokens,
		Messages: []provider.Message{{Role: provider.RoleUser, Text: testPrompt}},
	}, nil)

	// What the vendor reported is charged, also beside an error. A call that
	// reports nothing is charged a provisional: one token per request byte plus
	// 1000, and the whole output allowance.
	usage := resp.Usage
	if usage.InputTokens <= 0 && usage.OutputTokens <= 0 {
		usage = provider.Usage{InputTokens: int64(len(system)+len(testPrompt)) + 1000, OutputTokens: testCallMaxOutputTokens}
	}
	answer := testAnswer{
		OK: err == nil, InputTokens: max(usage.InputTokens, 0), OutputTokens: max(usage.OutputTokens, 0),
		CostMicroUSD: model.CostMicroUSD(usage),
	}
	if err != nil {
		var perr *provider.Error
		if errors.As(err, &perr) {
			answer.Kind = perr.Kind
			logModelFailure(s.log, "", req.Provider, model.ID, perr)
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

// whoamiTokenKind reads token.kind from Harbormaster's whoami answer, decoding
// only as far as "token": the rest (a user's thousands of tags) is never parsed,
// and an answer cut off after it by the read cap still counts.
func whoamiTokenKind(body []byte) (kind string, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if first, err := decoder.Token(); err != nil || first != json.Delim('{') {
		return "", false
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", false
		}
		if name, _ := key.(string); name == "token" {
			var token struct {
				Kind string `json:"kind"`
			}
			if decoder.Decode(&token) != nil {
				return "", false
			}
			return token.Kind, true
		}
		var skipped json.RawMessage
		if decoder.Decode(&skipped) != nil {
			return "", false
		}
	}
	return "", true // a whole object that names no token
}

// perAddressBound is how many token confirmations one caller address may have
// in flight: whoamiPerAddress, or twice the run limit when that is larger, so a
// caller can always fill every run slot it is allowed.
func perAddressBound(maxConcurrentRuns int) int {
	return max(whoamiPerAddress, 2*maxConcurrentRuns)
}

// checkLimiter bounds token confirmations in flight: a per-address bound and a
// global total. It never keys on a token.
type checkLimiter struct {
	global     chan struct{}
	perAddress int
	mu         sync.Mutex
	addrs      map[string]*addrChecks
}

type addrChecks struct {
	slots chan struct{}
	users int // holders and waiters
}

func newCheckLimiter(global, perAddress int) *checkLimiter {
	return &checkLimiter{global: make(chan struct{}, max(global, 1)), perAddress: max(perAddress, 1), addrs: map[string]*addrChecks{}}
}

// acquire waits up to whoamiWait (and no longer than ctx) for a slot for addr.
// A true answer comes with the function that gives the slot back.
func (l *checkLimiter) acquire(ctx context.Context, addr string) (release func(), ok bool) {
	if ctx.Err() != nil { // select picks among ready cases at random: a done context must not win a slot
		return nil, false
	}
	l.mu.Lock()
	entry := l.addrs[addr]
	if entry == nil {
		entry = &addrChecks{slots: make(chan struct{}, l.perAddress)}
		l.addrs[addr] = entry
	}
	entry.users++
	l.mu.Unlock()
	leave := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if entry.users--; entry.users == 0 {
			delete(l.addrs, addr)
		}
	}

	timer := time.NewTimer(whoamiWait)
	defer timer.Stop()
	select {
	case entry.slots <- struct{}{}:
	case <-timer.C:
		leave()
		return nil, false
	case <-ctx.Done():
		leave()
		return nil, false
	}
	select {
	case l.global <- struct{}{}:
	case <-timer.C:
		<-entry.slots
		leave()
		return nil, false
	case <-ctx.Done():
		<-entry.slots
		leave()
		return nil, false
	}
	return func() {
		<-l.global
		<-entry.slots
		leave()
	}, true
}
