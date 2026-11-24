package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// settleStepTimeout bounds each step that ends a turn. They run on a context detached from the request, so an
	// aborted request still settles its run, deletes its token and saves what was said.
	settleStepTimeout = 10 * time.Second
	// sseWriteTimeout bounds one write to the browser, so a stalled client cannot hold the turn or its slot open.
	sseWriteTimeout = 30 * time.Second
	// runTimeoutSlack is how long past the agent's own run timeout the turn waits before giving up on the stream.
	runTimeoutSlack = 30 * time.Second
	// maxSavedAnswerBytes caps the answer text kept for one turn.
	maxSavedAnswerBytes = 256 << 10
	maxErrorMessageLen  = 300
)

var errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// sseStream writes server-sent events. Every data line is JSON, which never holds a raw newline, so text from the
// model cannot end an event early or start a forged one. The comment heartbeat shares the lock.
type sseStream struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	rc     *http.ResponseController
	broken bool
}

func startSSE(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // keep nginx-style proxies from holding the stream back
	h.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	s := &sseStream{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

// event sends one event. A failed write marks the stream broken and returns the error.
func (s *sseStream) event(name string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, raw))
}

func (s *sseStream) comment() error { return s.write(": keep-alive\n\n") }

func (s *sseStream) write(frame string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return errors.New("stream closed")
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := s.w.Write([]byte(frame)); err != nil {
		s.broken = true
		return err
	}
	if err := s.rc.Flush(); err != nil {
		s.broken = true
		return err
	}
	return nil
}

func (s *sseStream) isBroken() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// heartbeat sends a comment line every interval until the returned stop function is called; stop returns after
// the last write, so nothing is written once the handler returns.
func (s *sseStream) heartbeat(interval time.Duration) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = s.comment()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
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
	resultStopped      = turnResult{outcome: "interrupted", code: "agent_unavailable", message: "This question was stopped before it finished. Try again."}
	resultUnexpected   = turnResult{outcome: "error", code: "agent_unavailable", message: agentUnavailableMsg}
)

// chatTurn is one question and its answer. Whatever way it ends, finish runs exactly once: it saves the answer
// (or the part said so far, marked interrupted), settles the run, deletes the run token, writes the audit row and
// frees the user's slot, in that order, so a browser that aborts and refetches finds the part answer as soon as it
// can. The turn never logs or audits what was said.
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
	sse     *sseStream

	answer              []byte
	tools               []string
	input, output, cost int64
	ended               bool // a done or error event arrived: nothing after it counts
	succeeded           bool
	agentErr            *agentclient.Event
	stopped             bool // the run was swept or its usage could not be recorded, so it was cancelled
	cancelRun           context.CancelFunc

	finishOnce sync.Once
}

// execute prepares the turn, streams it and finishes it. It owns every exit, a panic included.
func (t *chatTurn) execute(w http.ResponseWriter) {
	defer func() {
		rec := recover()
		if rec != nil {
			t.s.log().Error("panic in chat turn", "run_id", t.run.ID, "panic", rec, "stack", string(debug.Stack()))
		}
		t.finish(resultUnexpected) // a no-op when the turn already finished
		// Before the stream starts a panic still answers 500 through the recover middleware; a stream cannot take one.
		if rec != nil && (t.sse == nil || rec == http.ErrAbortHandler) {
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

// prepare saves the user's message and mints the run token.
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
	for _, m := range saved {
		if m.Text != "" { // an interrupted turn with nothing said leaves no empty message to replay
			t.history = append(t.history, agentclient.Message{Role: m.Role, Text: m.Text})
		}
	}
	t.token, err = t.s.Auth.MintRunToken(ctx, t.user, t.run.ID, t.redactEnv)
	return err
}

func (t *chatTurn) stream(w http.ResponseWriter) {
	t.sse = startSSE(w)
	defer t.sse.heartbeat(t.s.AgentHeartbeat)()

	ctx, cancel := context.WithCancel(t.r.Context())
	defer cancel()
	t.cancelRun = cancel
	if t.timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, t.timeout+runTimeoutSlack)
		defer cancelTimeout()
	}
	err := t.s.Agent.Run(ctx, t.token, agentclient.RunRequest{
		RunID: t.run.ID, Role: t.run.Role, Provider: t.run.Provider, Model: t.run.Model,
		Messages: t.history, Page: t.page, MaxCostMicroUSD: t.run.ReservedMicroUSD,
	}, t.onEvent)
	t.finish(t.resultOf(err))
}

// resultOf says how the stream ended, given the error Run returned.
func (t *chatTurn) resultOf(err error) turnResult {
	switch {
	case t.stopped:
		return resultStopped
	case t.succeeded:
		return turnResult{success: true, outcome: "done"}
	case t.agentErr != nil:
		return turnResult{outcome: "error", code: errorCodeFrom(t.agentErr.Code), message: errorMessageFrom(t.agentErr.Message)}
	case t.r.Context().Err() != nil || t.sse.isBroken():
		return resultDisconnected
	case errors.Is(err, agentclient.ErrAgentBusy):
		return turnResult{outcome: "error", code: "agent_busy", message: "The agent is busy right now. Try again shortly."}
	case errors.Is(err, agentclient.ErrUnknownModel):
		return turnResult{outcome: "error", code: errNotConfigured.Code, message: errNotConfigured.Message}
	case errors.Is(err, context.DeadlineExceeded):
		return turnResult{outcome: "error", code: "agent_unavailable", message: "The agent took too long to answer. Try again."}
	}
	return resultUnexpected
}

func errorCodeFrom(code string) string {
	if errorCodePattern.MatchString(code) {
		return code
	}
	return "agent_unavailable"
}

func errorMessageFrom(msg string) string {
	runes := []rune(msg)
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
	if t.ended || t.stopped {
		return
	}
	switch ev.Type {
	case "text":
		if len(t.answer)+len(ev.Delta) <= maxSavedAnswerBytes {
			t.answer = append(t.answer, ev.Delta...)
		}
		t.send("text", map[string]string{"delta": ev.Delta})
	case "tool_start":
		t.tools = append(t.tools, ev.Tool)
		t.send("tool_start", map[string]string{"name": ev.Tool})
	case "tool_end":
		t.send("tool_end", map[string]any{"name": ev.Tool, "ok": ev.OK})
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

func (t *chatTurn) send(name string, data any) {
	if err := t.sse.event(name, data); err != nil {
		t.cancelRun() // the browser is gone: stop paying for the run
	}
}

// recordUsage stores the run's cumulative figures. Anything but success means the run can no longer be billed
// reliably (it was swept, or the store failed), so the agent run is cancelled at once.
func (t *chatTurn) recordUsage(ev agentclient.Event) {
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
	t.input, t.output, t.cost = ev.InputTokens, ev.OutputTokens, ev.CostMicroUSD
}

// finish ends the turn once; later calls do nothing. Each step runs whether or not an earlier one failed.
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
				res = turnResult{outcome: "error", code: "internal", message: "The answer could not be saved. Try again."}
			}
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
					"tools": nonNil(t.tools), "input_tokens": t.input, "output_tokens": t.output,
					"cost_micro_usd": t.cost, "outcome": res.outcome,
				},
			})
		})
		t.s.log().Info("agent run finished", "run_id", t.run.ID, "user_id", t.user.ID, "outcome", res.outcome,
			"input_tokens", t.input, "output_tokens", t.output, "cost_micro_usd", t.cost)
		// The slot is freed before the browser hears the end, so its next question is not refused as busy.
		t.release()

		if t.sse == nil {
			return
		}
		switch {
		case res.success:
			_ = t.sse.event("done", map[string]int64{"message_id": saved.ID})
		case res.code != "":
			_ = t.sse.event("error", map[string]string{"code": res.code, "message": res.message})
		}
	})
}

// step runs one finishing step of the turn; see runStep.
func (t *chatTurn) step(name string, fn func(ctx context.Context) error) error {
	return t.s.runStep(t.r, t.run.ID, name, fn)
}

// runStep runs one step that ends a run (any role) on a context detached from the request, and turns a panic or
// an error into a log line so the steps after it still run.
func (s *Server) runStep(r *http.Request, runID uuid.UUID, name string, fn func(ctx context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), settleStepTimeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
			s.log().Error("agent turn step panicked", "step", name, "run_id", runID, "panic", rec, "stack", string(debug.Stack()))
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
