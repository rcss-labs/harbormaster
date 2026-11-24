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
	// Each finishing step has its own deadline, from a fresh context; together they stay under 8 seconds, well
	// inside the 15 second shutdown. Credentials and money come first so a slow later step cannot starve them.
	tokenStepTimeout  = 1500 * time.Millisecond
	settleStepBudget  = 2 * time.Second
	usageStepTimeout  = time.Second // inside the settle step, so settling always keeps at least this much
	saveStepTimeout   = 2500 * time.Millisecond
	auditStepTimeout  = time.Second
	drainUsageTimeout = 2 * time.Second
	// finalFlush is how long the last events get to reach a browser that is still reading.
	finalFlush = 2 * time.Second
	// maxSavedAnswerBytes caps the answer text kept for one turn; the marker is added once when it is passed.
	maxSavedAnswerBytes = 256 << 10
	answerCapMarker     = "\n\n[The answer was cut short because it grew too long.]"
	maxErrorMessageLen  = 300
	maxToolNameLen      = 64
	// The agent refuses a request of more than 500 messages or 1 MiB; these leave margin.
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
// the reading of the agent's events (which carry the usage the budget depends on). Adjacent text deltas that the
// browser has not been sent yet are merged into one pending frame, so a burst of tiny deltas costs one write, and
// the notice, error and done frames are always accepted: a browser that is merely slow loses nothing. Only a
// write that does not finish in writeTimeout, a failed write, or a runaway amount of unsent text marks the stream
// broken and calls onBroken. Every data line is JSON, which never holds a raw newline, so text from the model
// cannot end an event early or start a forged one. The writer also sends the comment heartbeat.
type browserWriter struct {
	rc           *http.ResponseController
	w            http.ResponseWriter
	writeTimeout time.Duration
	onBroken     func()

	mu      sync.Mutex // guards items, pending and closed
	items   []outItem
	pending int // bytes of unsent text
	closed  bool
	wake    chan struct{}

	broken        atomic.Bool
	flushDeadline atomic.Int64 // unix nanos; set once, by finish
	done          chan struct{}
}

// outItem is one unsent event; a text item holds the merged delta text instead of data.
type outItem struct {
	name string
	data any
	text []byte
}

const (
	// maxPendingText is the most unsent text kept for one browser; past it the stream is broken.
	maxPendingText = 16 << 20
	// maxTextFrame is the largest text event written at once.
	maxTextFrame = 64 << 10
)

// startBrowserWriter starts the writer goroutine. onBroken is called, once, when the stream can no longer be
// trusted to reach the browser.
func startBrowserWriter(w http.ResponseWriter, writeTimeout, heartbeat time.Duration, onBroken func()) *browserWriter {
	b := &browserWriter{
		w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout, onBroken: onBroken,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go b.loop(heartbeat)
	return b
}

func (b *browserWriter) loop(heartbeat time.Duration) {
	defer close(b.done)
	defer func() {
		if recover() != nil {
			b.markBroken() // a write that panics must not take the process down, or leave the turn waiting
		}
	}()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		frame, ok, finished := b.next()
		if ok {
			if !b.write(frame) {
				return
			}
			continue
		}
		if finished {
			return
		}
		select {
		case <-b.wake:
		case <-ticker.C:
			if !b.write(": keep-alive\n\n") {
				return
			}
		}
	}
}

// next pops the first unsent event as a frame. finished is true once the writer is closed and nothing is left.
func (b *browserWriter) next() (frame string, ok, finished bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return "", false, b.closed
	}
	it := &b.items[0]
	var data any = it.data
	if it.name == "text" && it.text != nil {
		n := len(it.text)
		if n > maxTextFrame {
			n = maxTextFrame
			for n > 0 && !utf8.RuneStart(it.text[n]) {
				n--
			}
		}
		data = map[string]string{"delta": string(it.text[:n])}
		b.pending -= n
		if n < len(it.text) {
			it.text = it.text[n:]
		} else {
			b.items = b.items[1:]
		}
	} else {
		b.items = b.items[1:]
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	name := it.name
	if len(b.items) == 0 {
		b.items = nil
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw), true, false
}

func (b *browserWriter) write(frame string) bool {
	if b.broken.Load() {
		return false
	}
	b.setDeadline()
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

// setDeadline gives the next write writeTimeout, or less once finish has set its flush deadline. It looks at the
// flush deadline again after setting, so a deadline finish set in between is never overwritten by a later one.
func (b *browserWriter) setDeadline() {
	deadline := time.Now().Add(b.writeTimeout)
	if fd := b.flushDeadline.Load(); fd != 0 && time.Unix(0, fd).Before(deadline) {
		deadline = time.Unix(0, fd)
	}
	_ = b.rc.SetWriteDeadline(deadline)
	if fd := b.flushDeadline.Load(); fd != 0 && time.Unix(0, fd).Before(deadline) {
		_ = b.rc.SetWriteDeadline(time.Unix(0, fd))
	}
}

func (b *browserWriter) markBroken() {
	if b.broken.CompareAndSwap(false, true) && b.onBroken != nil {
		b.onBroken()
	}
}

func (b *browserWriter) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// text queues a text delta, merging it into the last unsent text event. It is refused only when the stream is
// broken or closed, or far more text is waiting than any browser could be reading (which breaks the stream).
func (b *browserWriter) text(delta string) bool {
	b.mu.Lock()
	if b.closed || b.broken.Load() {
		b.mu.Unlock()
		return false
	}
	if b.pending+len(delta) > maxPendingText {
		b.mu.Unlock()
		b.markBroken()
		return false
	}
	if n := len(b.items); n > 0 && b.items[n-1].name == "text" && b.items[n-1].text != nil {
		b.items[n-1].text = append(b.items[n-1].text, delta...)
	} else {
		b.items = append(b.items, outItem{name: "text", text: []byte(delta)})
	}
	b.pending += len(delta)
	b.mu.Unlock()
	b.signal()
	return true
}

// event queues any other event. It is always accepted unless the stream is broken or closed.
func (b *browserWriter) event(name string, data any) bool {
	b.mu.Lock()
	if b.closed || b.broken.Load() {
		b.mu.Unlock()
		return false
	}
	b.items = append(b.items, outItem{name: name, data: data})
	b.mu.Unlock()
	b.signal()
	return true
}

func (b *browserWriter) isBroken() bool { return b.broken.Load() }

// pendingItems is how many events wait to be written.
func (b *browserWriter) pendingItems() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// finish stops accepting events, gives what is queued flush to reach the browser, and returns once the writer has
// stopped, so nothing is written after the handler returns. The flush deadline is set once and cannot be moved.
func (b *browserWriter) finish(flush time.Duration) {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.flushDeadline.CompareAndSwap(0, time.Now().Add(flush).UnixNano())
	_ = b.rc.SetWriteDeadline(time.Unix(0, b.flushDeadline.Load())) // also cuts short a write waiting on a stalled browser
	b.signal()
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
	resultUnknownTool  = turnResult{outcome: "unknown_tool", code: "unknown_tool", message: "The agent used a tool this version does not know. Ask an admin to update Harbormaster or the agent."}
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
	badTool              bool // the agent named a tool this version does not know
	stopped              bool // the run was swept or its usage could not be recorded, so it was cancelled
	drained              atomic.Bool
	cancelRun            context.CancelFunc

	finishOnce sync.Once
}

type usageFigures struct{ input, output, cost int64 }

// finishDeadlineKey carries the instant by which one finishing step must be done.
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
	if err != nil && t.r.Context().Err() == nil && !t.bw.isBroken() && !t.stopped && !t.badTool && !t.drained.Load() {
		t.s.log().Warn("agent run failed", "run_id", t.run.ID, "error", err) // never any model text
	}
	t.finish(t.resultOf(err))
}

// resultOf says how the stream ended, given the error Run returned.
func (t *chatTurn) resultOf(err error) turnResult {
	switch {
	case t.succeeded: // an answer that was complete stays complete, whatever happened after it
		return turnResult{success: true, outcome: "done"}
	case t.badTool:
		return resultUnknownTool
	case t.stopped || t.drained.Load():
		return resultStopped
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
		if !t.bw.text(delta) {
			t.cancelRun()
		}
	case "tool_start", "tool_end":
		name, ok := knownTool(ev.Tool)
		if !ok { // the agent and this server disagree about the tools: say so rather than hide it
			t.ended, t.badTool = true, true
			t.cancelRun()
			return
		}
		if ev.Type == "tool_start" {
			t.tools = append(t.tools, name)
			t.send("tool_start", map[string]string{"name": name})
		} else {
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
	timeout := settleStepTimeout
	if t.drained.Load() {
		timeout = drainUsageTimeout // the server is shutting down: do not hold it up
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.r.Context()), timeout)
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

// finish ends the turn once; later calls do nothing. The steps run in order of what must not be lost: the run token
// goes first, then the cost is recorded and the run settled, then the answer is saved, then the slot is freed, then
// the audit row is written. Each has its own deadline on a fresh context detached from the request, so one that
// hangs cannot starve the others, and each runs whether or not an earlier one failed.
func (t *chatTurn) finish(res turnResult) {
	t.finishOnce.Do(func() {
		state := "failed"
		if res.success {
			state = "done"
		}
		_ = t.stepWithin("delete the run token", tokenStepTimeout, func(ctx context.Context) error {
			return t.s.dropRunToken(ctx, t.run.ID)
		})
		_ = t.stepWithin("record usage and settle the run", settleStepBudget, func(ctx context.Context) error {
			if t.haveSeen && !t.seenStored {
				// The figures last seen are written even if writing them failed earlier; a swept run keeps the higher ones.
				uctx, cancel := context.WithTimeout(ctx, usageStepTimeout)
				err := t.s.Store.RecordAgentRunUsage(uctx, t.run.ID, t.seen.input, t.seen.output, t.seen.cost, t.s.now())
				cancel()
				if err != nil && !errors.Is(err, store.ErrAgentRunNotRunning) {
					t.s.log().Error("agent turn step failed", "step", "record the last usage", "run_id", t.run.ID, "error", err)
				}
			}
			return t.s.settleRun(ctx, t.run.ID, state, t.tools)
		})

		var saved store.AgentMessage
		saveErr := error(nil)
		if res.success || len(t.answer) > 0 || len(t.tools) > 0 {
			saveErr = t.stepWithin("save the answer", saveStepTimeout, func(ctx context.Context) (err error) {
				saved, err = t.s.Store.AppendAgentMessage(ctx, t.user.ID, store.AgentMessage{
					ConversationID: *t.run.ConversationID, Role: "assistant", Text: string(t.answer),
					Tools: nonNil(t.tools), Interrupted: !res.success,
				})
				return err
			})
			if res.success && saveErr != nil {
				res = resultSaveFailed // the run is settled and costed; only the browser's answer changes
			}
		}
		// The slot is freed before the browser hears the end, so its next question is not refused as busy.
		t.release()

		_ = t.stepWithin("write the audit row", auditStepTimeout, func(ctx context.Context) error {
			return t.s.Store.Audit(ctx, store.AuditEntry{
				ActorUserID: &t.user.ID, ActorUsername: t.user.Username,
				Action: "agent_run", Target: t.run.ID.String(), Summary: "AI chat run", IP: t.s.clientIP(t.r),
				Details: map[string]any{
					"run_id": t.run.ID.String(), "role": t.run.Role, "provider": t.run.Provider, "model": t.run.Model,
					"tools": nonNil(t.tools), "input_tokens": t.seen.input, "output_tokens": t.seen.output,
					"cost_micro_usd": t.seen.cost, "outcome": res.outcome, "saved": saveErr == nil,
				},
			})
		})
		t.s.log().Info("agent run finished", "run_id", t.run.ID, "user_id", t.user.ID, "outcome", res.outcome,
			"input_tokens", t.seen.input, "output_tokens", t.seen.output, "cost_micro_usd", t.seen.cost)

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

// stepWithin runs one finishing step with its own deadline; see runStep.
func (t *chatTurn) stepWithin(name string, within time.Duration, fn func(ctx context.Context) error) error {
	r := t.r.WithContext(context.WithValue(t.r.Context(), finishDeadlineKey{}, time.Now().Add(within)))
	return t.s.runStep(r, t.run.ID, name, fn)
}

// runStep runs one step that ends a run (any role) on a context detached from the request, and turns a panic or
// an error into a log line so the steps after it still run. The step gets settleStepTimeout, or less when the
// request carries a finishDeadlineKey.
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
