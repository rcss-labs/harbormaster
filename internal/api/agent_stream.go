package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// settleStepTimeout bounds each step that ends a turn. They run on a context detached from the request, so an
	// aborted request still settles its run, deletes its token and saves what was said.
	settleStepTimeout = 10 * time.Second
	// finishBudget caps all the finishing steps of a turn together, so a turn always ends well inside the 15 second
	// shutdown.
	finishBudget = 8 * time.Second
	// finalFlush is how long the last events get to reach a browser that is still reading.
	finalFlush = 2 * time.Second
	// streamQueueSize is how many events may wait for the browser before it counts as stalled.
	streamQueueSize = 1024
	// maxSavedAnswerBytes caps the answer text kept for one turn; the marker is added once when it is passed.
	maxSavedAnswerBytes = 256 << 10
	answerCapMarker     = "\n\n[The answer was cut short because it grew too long.]"
	maxErrorMessageLen  = 300
	maxToolNameLen      = 64
	// The agent refuses more than 400 messages or about 1 MiB of them; these leave margin.
	historyMaxMessages = 400
	historyMaxBytes    = 700 << 10
	// defaultRunTimeout stands in when the catalogue states none.
	defaultRunTimeout = 5 * time.Minute
)

var errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// toolNames is the set of tools the agent may call: the shared table.
var toolNames = func() map[string]bool {
	names := map[string]bool{}
	for _, tool := range hmtools.Tools() {
		names[tool.Name] = true
	}
	return names
}()

// cleanModelText makes text from the model safe to store and relay: valid UTF-8 and no control characters but
// newline and tab (Postgres refuses NUL, and the rest would only confuse a log or a terminal).
func cleanModelText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// startSSE sends the response head of an event stream.
func startSSE(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // keep nginx-style proxies from holding the stream back
	h.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
}

// browserWriter sends server-sent events to the browser from a goroutine of its own, so a slow browser never slows
// the reading of the agent's events (which carry the usage the budget depends on). Events wait in a bounded queue;
// a write that does not finish in writeTimeout, a failed write, or a full queue marks the stream broken and calls
// onBroken. Every data line is JSON, which never holds a raw newline, so text from the model cannot end an event
// early or start a forged one. The writer also sends the comment heartbeat.
type browserWriter struct {
	rc           *http.ResponseController
	w            http.ResponseWriter
	writeTimeout time.Duration
	onBroken     func()

	mu     sync.Mutex // guards queue sends and close
	closed bool
	queue  chan string

	broken        atomic.Bool
	flushDeadline atomic.Int64 // unix nanos; zero until finish
	done          chan struct{}
}

// startBrowserWriter starts the writer goroutine. onBroken is called, once, from the producer or the writer, when the
// stream can no longer be trusted to reach the browser.
func startBrowserWriter(w http.ResponseWriter, writeTimeout, heartbeat time.Duration, onBroken func()) *browserWriter {
	b := &browserWriter{
		w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout, onBroken: onBroken,
		queue: make(chan string, streamQueueSize), done: make(chan struct{}),
	}
	go b.loop(heartbeat)
	return b
}

func (b *browserWriter) loop(heartbeat time.Duration) {
	defer close(b.done)
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case frame, ok := <-b.queue:
			if !ok {
				return
			}
			if !b.write(frame) {
				return
			}
		case <-ticker.C:
			if !b.write(": keep-alive\n\n") {
				return
			}
		}
	}
}

func (b *browserWriter) write(frame string) bool {
	if b.broken.Load() {
		return false
	}
	deadline := time.Now().Add(b.writeTimeout)
	if fd := b.flushDeadline.Load(); fd != 0 && time.Unix(0, fd).Before(deadline) {
		deadline = time.Unix(0, fd)
	}
	_ = b.rc.SetWriteDeadline(deadline)
	_, err := b.w.Write([]byte(frame))
	if err == nil {
		if err = b.rc.Flush(); errors.Is(err, http.ErrNotSupported) {
			err = nil
		}
	}
	if err != nil {
		b.markBroken()
		return false
	}
	return true
}

func (b *browserWriter) markBroken() {
	if b.broken.CompareAndSwap(false, true) && b.onBroken != nil {
		b.onBroken()
	}
}

// event queues one event without waiting. It reports false when the stream is broken or the browser has fallen so
// far behind that the queue is full (which breaks it).
func (b *browserWriter) event(name string, data any) bool {
	raw, err := json.Marshal(data)
	if err != nil {
		return true // nothing the model sends can fail to encode; drop rather than break the stream
	}
	frame := fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.broken.Load() {
		return false
	}
	select {
	case b.queue <- frame:
		return true
	default:
		b.markBroken()
		return false
	}
}

func (b *browserWriter) isBroken() bool { return b.broken.Load() }

// finish stops accepting events, gives what is queued flush to reach the browser, and returns once the writer has
// stopped, so nothing is written after the handler returns.
func (b *browserWriter) finish(flush time.Duration) {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		deadline := time.Now().Add(flush)
		b.flushDeadline.Store(deadline.UnixNano())
		_ = b.rc.SetWriteDeadline(deadline) // also cuts short a write already waiting on a stalled browser
		close(b.queue)
	}
	b.mu.Unlock()
	<-b.done
}

// turnResult is how a chat turn ended.
type turnResult struct {
	success bool
	// outcome is the audit label: done, error or interrupted.
	outcome string
	// code and message become the error event; an empty code sends none (the browser is gone).
	code, message string
}

var (
	resultDisconnected = turnResult{outcome: "interrupted"}
	resultStopped      = turnResult{outcome: "interrupted", code: "interrupted", message: "This question was stopped before it finished. Try again."}
	resultTimedOut     = turnResult{outcome: "error", code: "timeout", message: "The agent took too long and was stopped."}
	resultUnexpected   = turnResult{outcome: "error", code: "agent_unavailable", message: agentUnavailableMsg}
	resultSaveFailed   = turnResult{outcome: "error", code: "internal", message: "The answer could not be saved. Try again."}
)

// chatTurn is one question and its answer. The goroutine that runs it reads the agent's events and never waits for
// the browser: browser writes go through browserWriter. Whatever way the turn ends, finish runs exactly once: it
// saves the answer (or the part said so far, marked interrupted), records the last usage seen, settles the run,
// deletes the run token, writes the audit row and frees the user's slot, in that order, so a browser that aborts and
// refetches finds the part answer as soon as it can. The turn never logs or audits what was said.
type chatTurn struct {
	s         *Server
	r         *http.Request
	user      store.User
	run       store.AgentRun
	text      string
	page      agentclient.Page
	redactEnv bool
	timeout   time.Duration
	release   func()

	token   string
	history []agentclient.Message
	trimmed bool
	bw      *browserWriter

	answer    []byte
	answerCut bool
	tools     []string
	// The usage last seen is kept even when writing it failed, and written again in finish before the run settles.
	seen                 usageFigures
	haveSeen, seenStored bool
	ended                bool // a done or error event arrived: nothing after it counts
	succeeded            bool
	agentErr             *agentclient.Event
	stopped              bool // the run was swept or its usage could not be recorded, so it was cancelled
	drained              atomic.Bool
	cancelRun            context.CancelFunc

	finishOnce sync.Once
	finishAt   sync.Once
	deadline   time.Time
}

type usageFigures struct{ input, output, cost int64 }

// finishDeadlineKey carries the instant by which all finishing steps must be done.
type finishDeadlineKey struct{}

// execute prepares the turn, streams it and finishes it. It owns every exit, a panic included.
func (t *chatTurn) execute(w http.ResponseWriter) {
	defer func() {
		rec := recover()
		if rec != nil {
			// The type only: a panic value could carry text.
			t.s.log().Error("panic in chat turn", "run_id", t.run.ID, "panic_type", fmt.Sprintf("%T", rec), "stack", string(debug.Stack()))
		}
		t.finish(resultUnexpected) // a no-op when the turn already finished
		// Before the stream starts a panic still answers 500 through the recover middleware; a stream cannot take one.
		if rec != nil && (t.bw == nil || rec == http.ErrAbortHandler) {
			panic(rec)
		}
	}()
	if err := t.prepare(); err != nil {
		t.finish(resultUnexpected)
		t.s.fail(w, err)
		return
	}
	t.stream(w)
}

// prepare saves the user's message, picks the history to send and mints the run token.
func (t *chatTurn) prepare() error {
	ctx := t.r.Context()
	convID := *t.run.ConversationID
	if _, err := t.s.Store.AppendAgentMessage(ctx, t.user.ID, store.AgentMessage{ConversationID: convID, Role: "user", Text: t.text}); err != nil {
		return err
	}
	if err := t.s.Store.SetAgentConversationTitleIfEmpty(ctx, t.user.ID, convID, conversationTitle(t.text)); err != nil {
		t.s.log().Warn("agent conversation title not set", "error", err)
	}
	saved, err := t.s.Store.ListAgentMessages(ctx, t.user.ID, convID)
	if err != nil {
		return err
	}
	t.history, t.trimmed = boundedHistory(saved)
	t.token, err = t.s.Auth.MintRunToken(ctx, t.user, t.run.ID, t.redactEnv)
	return err
}

// boundedHistory returns the newest messages that fit the agent's limits with margin, starting at a user message,
// and whether any were left out. Messages with no text (a turn that said nothing) are not replayed.
func boundedHistory(saved []store.AgentMessage) (history []agentclient.Message, trimmed bool) {
	var all []agentclient.Message
	for _, m := range saved {
		if m.Text != "" {
			all = append(all, agentclient.Message{Role: m.Role, Text: m.Text})
		}
	}
	size, from := 0, len(all)
	for from > 0 && len(all)-from < historyMaxMessages {
		raw, err := json.Marshal(all[from-1])
		if err != nil || (from < len(all) && size+len(raw)+1 > historyMaxBytes) {
			break // the newest message is always sent, whatever its size
		}
		size += len(raw) + 1
		from--
	}
	for from < len(all) && all[from].Role != "user" {
		from++
	}
	return all[from:], from > 0
}

func (t *chatTurn) stream(w http.ResponseWriter) {
	ctx, cancel := context.WithCancel(t.r.Context())
	defer cancel()
	t.cancelRun = cancel
	if t.timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, t.timeout+t.s.AgentRunSlack)
		defer cancelTimeout()
	}
	startSSE(w)
	t.bw = startBrowserWriter(w, t.s.AgentWriteTimeout, t.s.AgentHeartbeat, cancel)
	if t.trimmed {
		t.send("notice", map[string]string{"kind": "context_trimmed"})
	}

	// The server's shutdown stops the run; the turn then ends as interrupted with time to spare.
	watching := make(chan struct{})
	defer close(watching)
	go func() {
		select {
		case <-t.s.draining:
			t.drained.Store(true)
			cancel()
		case <-watching:
		}
	}()

	err := t.s.Agent.Run(ctx, t.token, agentclient.RunRequest{
		RunID: t.run.ID, Role: t.run.Role, Provider: t.run.Provider, Model: t.run.Model,
		Messages: t.history, Page: t.page, MaxCostMicroUSD: t.run.ReservedMicroUSD,
	}, t.onEvent)
	if err != nil && t.r.Context().Err() == nil && !t.bw.isBroken() && !t.stopped && !t.drained.Load() {
		t.s.log().Warn("agent run failed", "run_id", t.run.ID, "error", err) // never any model text
	}
	t.finish(t.resultOf(err))
}

// resultOf says how the stream ended, given the error Run returned.
func (t *chatTurn) resultOf(err error) turnResult {
	switch {
	case t.stopped || t.drained.Load():
		return resultStopped
	case t.succeeded:
		return turnResult{success: true, outcome: "done"}
	case t.agentErr != nil:
		return turnResult{outcome: "error", code: errorCodeFrom(t.agentErr.Code), message: errorMessageFrom(t.agentErr.Message)}
	case t.r.Context().Err() != nil || t.bw.isBroken():
		return resultDisconnected
	case errors.Is(err, agentclient.ErrAgentBusy):
		return turnResult{outcome: "error", code: "agent_busy", message: "The agent is busy right now. Try again shortly."}
	case errors.Is(err, agentclient.ErrUnknownModel):
		return turnResult{outcome: "error", code: errNotConfigured.Code, message: errNotConfigured.Message}
	case errors.Is(err, context.DeadlineExceeded):
		return resultTimedOut
	}
	return resultUnexpected
}

func errorCodeFrom(code string) string {
	if errorCodePattern.MatchString(code) {
		return code
	}
	return "agent_unavailable"
}

// errorMessageFrom is the agent's error text with secret-shaped spans scrubbed, control characters removed and
// the length capped.
func errorMessageFrom(msg string) string {
	runes := []rune(cleanModelText(secretshape.ScrubText(msg)))
	if len(runes) == 0 {
		return agentUnavailableMsg
	}
	if len(runes) > maxErrorMessageLen {
		runes = runes[:maxErrorMessageLen]
	}
	return string(runes)
}

// onEvent handles one agent event. Usage goes to the run row and never to the browser.
func (t *chatTurn) onEvent(ev agentclient.Event) {
	if hook := t.s.agentEventHook; hook != nil {
		hook(t.r.Context(), ev)
	}
	if t.ended || t.stopped {
		return
	}
	switch ev.Type {
	case "text":
		delta := cleanModelText(ev.Delta)
		if delta == "" {
			return
		}
		t.keepAnswer(delta)
		t.send("text", map[string]string{"delta": delta})
	case "tool_start":
		if name, ok := knownTool(ev.Tool); ok {
			t.tools = append(t.tools, name)
			t.send("tool_start", map[string]string{"name": name})
		}
	case "tool_end":
		if name, ok := knownTool(ev.Tool); ok {
			t.send("tool_end", map[string]any{"name": name, "ok": ev.OK})
		}
	case "notice":
		if ev.Notice == "context_trimmed" || ev.Notice == "limit_reached" {
			t.send("notice", map[string]string{"kind": ev.Notice})
		}
	case "usage":
		t.recordUsage(ev)
	case "done":
		t.ended, t.succeeded = true, true
	case "error":
		t.ended, t.agentErr = true, &ev
	}
}

func knownTool(name string) (string, bool) {
	name = cleanModelText(name)
	return name, len(name) <= maxToolNameLen && toolNames[name]
}

// keepAnswer adds delta to the saved answer until the cap, then adds the marker once and stops.
func (t *chatTurn) keepAnswer(delta string) {
	if t.answerCut {
		return
	}
	if len(t.answer)+len(delta) > maxSavedAnswerBytes {
		t.answerCut = true
		t.answer = append(t.answer, answerCapMarker...)
		return
	}
	t.answer = append(t.answer, delta...)
}

// send queues an event for the browser. A browser that is gone or too far behind breaks the stream, and the break
// cancels the agent run (see startBrowserWriter's onBroken), so a failed send needs nothing more here.
func (t *chatTurn) send(name string, data any) { t.bw.event(name, data) }

// recordUsage stores the run's cumulative figures. The figures are remembered first, so that a failure here (or a
// panic) still lets finish record them. Anything but success means the run can no longer be billed reliably, so
// the agent run is cancelled at once.
func (t *chatTurn) recordUsage(ev agentclient.Event) {
	t.seen, t.haveSeen, t.seenStored = usageFigures{ev.InputTokens, ev.OutputTokens, ev.CostMicroUSD}, true, false
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.r.Context()), settleStepTimeout)
	defer cancel()
	if err := t.s.Store.RecordAgentRunUsage(ctx, t.run.ID, ev.InputTokens, ev.OutputTokens, ev.CostMicroUSD, t.s.now()); err != nil {
		if errors.Is(err, store.ErrAgentRunNotRunning) {
			t.s.log().Warn("agent run was settled while running; stopping it", "run_id", t.run.ID)
		} else {
			t.s.log().Error("agent usage not recorded; stopping the run", "run_id", t.run.ID, "error", err)
		}
		t.stopped = true
		t.cancelRun()
		return
	}
	t.seenStored = true
}

// finish ends the turn once; later calls do nothing. Each step runs whether or not an earlier one failed, and all
// of them together are bounded by finishBudget.
func (t *chatTurn) finish(res turnResult) {
	t.finishOnce.Do(func() {
		var saved store.AgentMessage
		var saveErr error
		if res.success || len(t.answer) > 0 || len(t.tools) > 0 {
			saveErr = t.step("save the answer", func(ctx context.Context) (err error) {
				saved, err = t.s.Store.AppendAgentMessage(ctx, t.user.ID, store.AgentMessage{
					ConversationID: *t.run.ConversationID, Role: "assistant", Text: string(t.answer),
					Tools: nonNil(t.tools), Interrupted: !res.success,
				})
				return err
			})
			if res.success && saveErr != nil {
				res = resultSaveFailed
			}
		}
		if t.haveSeen && !t.seenStored {
			_ = t.step("record the last usage", func(ctx context.Context) error {
				err := t.s.Store.RecordAgentRunUsage(ctx, t.run.ID, t.seen.input, t.seen.output, t.seen.cost, t.s.now())
				if errors.Is(err, store.ErrAgentRunNotRunning) {
					return nil // a swept run keeps the higher figures
				}
				return err
			})
		}
		state := "failed"
		if res.success {
			state = "done"
		}
		_ = t.step("settle the run", func(ctx context.Context) error {
			return t.s.settleRun(ctx, t.run.ID, state, t.tools)
		})
		_ = t.step("delete the run token", func(ctx context.Context) error {
			return t.s.dropRunToken(ctx, t.run.ID)
		})
		_ = t.step("write the audit row", func(ctx context.Context) error {
			return t.s.Store.Audit(ctx, store.AuditEntry{
				ActorUserID: &t.user.ID, ActorUsername: t.user.Username,
				Action: "agent_run", Target: t.run.ID.String(), Summary: "AI chat run", IP: t.s.clientIP(t.r),
				Details: map[string]any{
					"run_id": t.run.ID.String(), "role": t.run.Role, "provider": t.run.Provider, "model": t.run.Model,
					"tools": nonNil(t.tools), "input_tokens": t.seen.input, "output_tokens": t.seen.output,
					"cost_micro_usd": t.seen.cost, "outcome": res.outcome,
				},
			})
		})
		t.s.log().Info("agent run finished", "run_id", t.run.ID, "user_id", t.user.ID, "outcome", res.outcome,
			"input_tokens", t.seen.input, "output_tokens", t.seen.output, "cost_micro_usd", t.seen.cost)
		// The slot is freed before the browser hears the end, so its next question is not refused as busy.
		t.release()

		if t.bw == nil {
			return
		}
		switch {
		case res.success:
			t.bw.event("done", map[string]int64{"message_id": saved.ID})
		case res.code != "":
			t.bw.event("error", map[string]string{"code": res.code, "message": res.message})
		}
		t.bw.finish(finalFlush)
	})
}

// step runs one finishing step of the turn; see runStep.
func (t *chatTurn) step(name string, fn func(ctx context.Context) error) error {
	r := t.r.WithContext(context.WithValue(t.r.Context(), finishDeadlineKey{}, t.finishBy()))
	return t.s.runStep(r, t.run.ID, name, fn)
}

// finishBy is the instant the finishing steps must be done by, fixed when the first one starts.
func (t *chatTurn) finishBy() time.Time {
	t.finishAt.Do(func() { t.deadline = time.Now().Add(finishBudget) })
	return t.deadline
}

// runStep runs one step that ends a run (any role) on a context detached from the request, and turns a panic or
// an error into a log line so the steps after it still run. The step gets settleStepTimeout, and never runs past the
// finishing deadline when the request carries one.
func (s *Server) runStep(r *http.Request, runID uuid.UUID, name string, fn func(ctx context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), settleStepTimeout)
	defer cancel()
	if by, ok := r.Context().Value(finishDeadlineKey{}).(time.Time); ok {
		var cancelBy context.CancelFunc
		ctx, cancelBy = context.WithDeadline(ctx, by)
		defer cancelBy()
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %T", rec)
			s.log().Error("agent turn step panicked", "step", name, "run_id", runID, "panic_type", fmt.Sprintf("%T", rec), "stack", string(debug.Stack()))
		}
	}()
	if err = fn(ctx); err != nil {
		s.log().Error("agent turn step failed", "step", name, "run_id", runID, "error", err)
	}
	return err
}

// settleRun finishes a run as done or failed. A run the stale sweep already failed is not an error: the sweep
// kept the figures and the tools.
func (s *Server) settleRun(ctx context.Context, runID uuid.UUID, state string, tools []string) error {
	err := s.Store.SettleAgentRun(ctx, runID, state, nonNil(tools), s.now())
	if errors.Is(err, store.ErrAgentRunNotRunning) {
		return nil
	}
	return err
}

// dropRunToken deletes the run token minted for runID.
func (s *Server) dropRunToken(ctx context.Context, runID uuid.UUID) error {
	return s.Store.DeleteRunToken(ctx, runID)
}
