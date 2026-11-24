package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	maxErrorMessageLen = 300
	// The agent refuses a request of more than 500 messages or 1 MiB; these leave margin.
	historyMaxMessages = 400
	historyMaxBytes    = 700 << 10
	// defaultRunTimeout stands in when the catalogue states none.
	defaultRunTimeout = 5 * time.Minute
	// A reservation that may have been made is looked for this many times, this far apart.
	lostReservationLooks     = 4
	lostReservationLookAgain = 250 * time.Millisecond
)

// setUpLimit bounds each database step before a run starts, the reservation included.
var setUpLimit = agentrun.Limit{Normal: 10 * time.Second, Draining: 2 * time.Second}

var errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

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

// turnResult is what the end of a turn means outside it.
type turnResult struct {
	success bool
	// outcome is the audit label: done, error or interrupted.
	outcome string
	// code and message become the error event; an empty code sends none (the browser is gone).
	code, message string
}

func resultOf(turn *agentrun.Turn) turnResult {
	switch turn.Reason() {
	case agentrun.Done:
		return turnResult{success: true, outcome: "done"}
	case agentrun.AgentFailed:
		code, message := turn.Failure()
		return turnResult{outcome: "error", code: errorCodeFrom(code), message: errorMessageFrom(message)}
	case agentrun.TimedOut:
		return turnResult{outcome: "error", code: "timeout", message: "The agent took too long and was stopped."}
	case agentrun.BrowserGone:
		return turnResult{outcome: "interrupted"}
	case agentrun.Draining, agentrun.RunLost:
		return turnResult{outcome: "interrupted", code: "interrupted", message: "This question was stopped before it finished. Try again."}
	}
	return turnResult{outcome: "error", code: "agent_unavailable", message: agentUnavailableMsg}
}

// chatTurn is one question and its answer. One goroutine, the handler's, owns it: it alone decides why the turn
// ends (through turn) and cancels the agent run. The agent's events are read on a second goroutine and the browser
// is written by a third, so neither ever waits for the other. However the turn ends, finish runs once. The turn
// never logs or audits what was said.
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

	start    turnStart
	turn     agentrun.Turn
	meter    *agentrun.Meter
	bw       *browserWriter
	cancel   context.CancelFunc
	finished bool
}

// turnStart is what a turn needs before it can call the agent.
type turnStart struct {
	token   string
	history []agentclient.Message
	trimmed bool
}

// execute prepares the turn, streams it and finishes it. It owns every exit, a panic included.
func (t *chatTurn) execute(w http.ResponseWriter) {
	t.meter = &agentrun.Meter{Store: t.s.Store, RunID: t.run.ID, Now: t.s.now, Draining: t.s.draining}
	defer func() { // last, and whatever happens before it: nothing writes to the browser after the handler
		if t.bw != nil {
			t.bw.finish()
		}
	}()
	defer func() {
		rec := recover()
		if rec != nil {
			t.s.logRunError(t.run.ID, "panic in chat turn", agentrun.NewPanicError(rec))
			t.turn.End(agentrun.Panicked)
		}
		t.finish()
		// Before the stream starts a panic still answers 500 through the recover middleware; a stream cannot take one.
		if rec != nil && (t.bw == nil || rec == http.ErrAbortHandler) {
			panic(rec)
		}
	}()
	var err error
	if t.start, err = agentrun.Within(t.r.Context(), setUpLimit, t.s.draining, t.prepare); err != nil {
		t.turn.Fail("internal", "")
		t.finish()
		t.s.fail(w, err)
		return
	}
	t.stream(w)
}

// prepare saves the user's message, picks the history to send and mints the run token.
func (t *chatTurn) prepare(ctx context.Context) (start turnStart, err error) {
	convID := *t.run.ConversationID
	if _, err := t.s.Store.AppendAgentMessage(ctx, t.user.ID, store.AgentMessage{ConversationID: convID, Role: "user", Text: t.text}); err != nil {
		return start, err
	}
	if err := t.s.Store.SetAgentConversationTitleIfEmpty(ctx, t.user.ID, convID, conversationTitle(t.text)); err != nil {
		t.s.log().Warn("agent conversation title not set", "error", err)
	}
	saved, err := t.s.Store.ListAgentMessages(ctx, t.user.ID, convID)
	if err != nil {
		return start, err
	}
	start.history, start.trimmed = boundedHistory(saved)
	start.token, err = t.s.Auth.MintRunToken(ctx, t.user, t.run.ID, t.redactEnv)
	return start, err
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

// stream runs the agent and relays its events until the agent's stream has closed. Everything that can end the turn
// arrives here: the agent's events, the request's end, a browser that stopped reading, the shutdown and the clock.
func (t *chatTurn) stream(w http.ResponseWriter) {
	// Detached from the request: only this turn cancels the run, once it knows why.
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.r.Context()))
	defer cancel()
	t.cancel = cancel
	overdue, stopClock := context.WithTimeout(context.Background(), t.timeout+t.s.AgentRunSlack)
	defer stopClock()
	startSSE(w)
	t.bw = startBrowserWriter(w, t.s.AgentWriteTimeout, t.s.AgentHeartbeat, t.s.draining)
	if t.start.trimmed {
		t.bw.event("notice", map[string]string{"kind": "context_trimmed"})
	}
	stops := []stop{{agentrun.TimedOut, overdue.Done()}, {agentrun.BrowserGone, t.r.Context().Done()},
		{agentrun.BrowserGone, t.bw.broken}, {agentrun.Draining, t.s.draining}}

	if reasons := known(stops); len(reasons) > 0 { // nothing is paid for that nobody will see
		t.perform(t.turn.End(reasons...), stops)
		return
	}

	done := make(chan struct{})
	defer close(done)
	events, runErr := t.readAgent(ctx, done)
	for !t.turn.Ended() {
		select {
		case ev, open := <-events:
			if !open {
				t.perform(t.failed(*runErr), stops)
				return
			}
			t.apply(ev, stops)
			continue
		case <-stops[0].when:
		case <-stops[1].when:
		case <-stops[2].when:
		case <-stops[3].when:
		}
		select { // an event that has been read came before the reason to stop
		case ev, open := <-events:
			if open {
				t.apply(ev, stops)
			}
		default:
		}
		t.perform(t.turn.End(known(stops)...), stops)
	}
	for ev := range events { // what the run costs counts until its stream closes
		t.apply(ev, stops)
	}
}

// stop is a reason to end the turn and the channel that is closed when it is there.
type stop struct {
	reason agentrun.Reason
	when   <-chan struct{}
}

// known returns the reasons there are right now, and also.
func known(stops []stop, also ...agentrun.Reason) []agentrun.Reason {
	for _, s := range stops {
		select {
		case <-s.when:
			also = append(also, s.reason)
		default:
		}
	}
	return also
}

// readAgent calls the agent and hands over its events one at a time. The channel closes when the call has ended, its
// error then being in *err. Nothing is handed over once done is closed.
func (t *chatTurn) readAgent(ctx context.Context, done <-chan struct{}) (<-chan agentclient.Event, *error) {
	events, err := make(chan agentclient.Event), new(error)
	go func() {
		defer close(events)
		defer func() {
			if rec := recover(); rec != nil {
				*err = agentrun.NewPanicError(rec)
			}
		}()
		*err = t.s.Agent.Run(ctx, t.start.token, agentclient.RunRequest{
			RunID: t.run.ID, Role: t.run.Role, Provider: t.run.Provider, Model: t.run.Model,
			Messages: t.start.history, Page: t.page, MaxCostMicroUSD: t.run.ReservedMicroUSD,
		}, func(ev agentclient.Event) {
			select {
			case events <- ev:
			case <-done:
			}
		})
	}()
	return events, err
}

// failed ends the turn for an agent call that ended without an answer.
func (t *chatTurn) failed(err error) []agentrun.Effect {
	t.s.logRunError(t.run.ID, "agent run failed", err) // never any model text
	switch {
	case errors.Is(err, agentclient.ErrAgentBusy):
		return t.turn.Fail("agent_busy", "The agent is busy right now. Try again shortly.")
	case errors.Is(err, agentclient.ErrUnknownModel):
		return t.turn.Fail(errNotConfigured.Code, errNotConfigured.Message)
	}
	return t.turn.Fail("agent_unavailable", agentUnavailableMsg)
}

// apply takes one event. A panic ends the turn but not the reading: the usage still to come counts.
func (t *chatTurn) apply(ev agentclient.Event, stops []stop) {
	defer func() {
		if rec := recover(); rec != nil {
			t.s.logRunError(t.run.ID, "panic in chat turn", agentrun.NewPanicError(rec))
			t.perform(t.turn.End(agentrun.Panicked), stops)
		}
	}()
	if hook := t.s.agentEventHook; hook != nil {
		hook(t.r.Context(), ev)
	}
	t.perform(t.turn.Apply(ev), stops)
}

// perform does what the turn asks for. Usage goes to the run row and never to the browser.
func (t *chatTurn) perform(effects []agentrun.Effect, stops []stop) {
	for _, e := range effects {
		switch e.Kind {
		case agentrun.SendText:
			t.bw.text(e.Text)
		case agentrun.SendToolStart:
			t.bw.event("tool_start", map[string]string{"name": e.Tool})
		case agentrun.SendToolEnd:
			t.bw.event("tool_end", map[string]any{"name": e.Tool, "ok": e.OK})
		case agentrun.SendNotice:
			t.bw.event("notice", map[string]string{"kind": e.Notice})
		case agentrun.KeepUsage:
			t.meter.See(e.Usage)
		case agentrun.RecordUsage:
			t.meter.See(e.Usage)
			if err := t.meter.Record(t.r.Context()); err != nil { // it can no longer be billed, so it must stop
				t.s.logRunError(t.run.ID, "agent usage not recorded; stopping the run", err)
				t.perform(t.turn.End(known(stops, agentrun.RunLost)...), stops)
			}
		case agentrun.Cancel:
			t.cancel()
		}
	}
}

// finish ends the turn once; later calls do nothing. See agentrun.Finisher for the steps and their order. Only the
// browser hears of an answer that could not be saved: the run itself was done, and its row and audit say so.
func (t *chatTurn) finish() {
	if t.finished {
		return
	}
	t.finished = true
	res := resultOf(&t.turn)
	tools, answer := nonNil(slices.Clone(t.turn.Tools())), t.turn.Answer()
	ops := t.s.runOps(t.r, t.run, t.user, t.meter, res)
	ops.Tools, ops.SlotToRelease = tools, t.release
	ops.SaveAnswer = func(ctx context.Context) (int64, error) {
		if !res.success && answer == "" && len(tools) == 0 {
			return 0, nil
		}
		saved, err := t.s.Store.AppendAgentMessage(ctx, t.user.ID, store.AgentMessage{
			ConversationID: *t.run.ConversationID, Role: "assistant", Text: answer, Tools: tools, Interrupted: !res.success,
		})
		return saved.ID, err
	}
	ended := t.s.finishRun(t.r, ops)

	if t.bw == nil {
		return
	}
	switch {
	case res.success && ended.SaveErr != nil:
		t.bw.event("error", map[string]string{"code": "internal", "message": "The answer could not be saved. Try again."})
	case res.success:
		t.bw.event("done", map[string]int64{"message_id": ended.MessageID})
	case res.code != "":
		t.bw.event("error", map[string]string{"code": res.code, "message": res.message})
	}
}

// runOps is how a run of any role ends in the store.
func (s *Server) runOps(r *http.Request, run store.AgentRun, user store.User, meter *agentrun.Meter, res turnResult) *agentrun.StoreOps {
	state := "failed"
	if res.success {
		state = "done"
	}
	return &agentrun.StoreOps{Store: s.Store, Run: run, Meter: meter, State: state, Outcome: res.outcome,
		Tools: []string{}, Username: user.Username, IP: s.clientIP(r), Now: s.now}
}

// finishRun ends a run of any role.
func (s *Server) finishRun(r *http.Request, ops *agentrun.StoreOps) agentrun.Result {
	f := agentrun.Finisher{Ops: ops, Draining: s.draining, Log: func(step string, err error) {
		s.logRunError(ops.Run.ID, "agent run: could not "+step, err)
	}}
	ended := f.Finish(r.Context())
	u, _ := ops.Meter.Last()
	s.log().Info("agent run finished", "run_id", ops.Run.ID, "role", ops.Run.Role, "user_id", ops.Run.UserID, "outcome", ops.Outcome,
		"input_tokens", u.InputTokens, "output_tokens", u.OutputTokens, "cost_micro_usd", u.CostMicroUSD)
	return ended
}

// reserveRun reserves a run. The reservation commits on a context of its own: a browser that leaves mid-call must not
// leave a half-known state behind, and a run that was reserved is settled by its turn, or here when the reservation
// failed without saying whether it was made (or at the latest by the reconcile pass).
func (s *Server) reserveRun(r *http.Request, run store.AgentRun, runTimeout time.Duration) error {
	_, err := agentrun.Within(context.WithoutCancel(r.Context()), setUpLimit, s.draining, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.Store.ReserveAgentRun(ctx, run, max(2*runTimeout, minRunStaleAfter), s.now())
	})
	switch {
	case err == nil || errors.Is(err, store.ErrNotFound):
		return err
	case errors.Is(err, store.ErrBudgetExhausted):
		return errBudget
	}
	s.settleLostReservation(r, run.ID)
	return err
}

// settleLostReservation settles a run whose reservation failed in a way that does not say whether it was made. The
// commit may still be under way, so a run that is not there is looked for a few more times. Its audit row is the
// reconcile pass's to write.
func (s *Server) settleLostReservation(r *http.Request, runID uuid.UUID) {
	_, err := agentrun.Within(context.WithoutCancel(r.Context()), setUpLimit, s.draining, func(ctx context.Context) (struct{}, error) {
		for looks := 1; ; looks++ {
			err := s.Store.SettleAgentRun(ctx, runID, "failed", nil, nil, s.now())
			if !errors.Is(err, store.ErrNotFound) || looks == lostReservationLooks {
				return struct{}{}, err
			}
			select {
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			case <-time.After(lostReservationLookAgain):
			}
		}
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrAgentRunNotRunning) {
		s.logRunError(runID, "agent run: could not settle a reservation that may have been made", err)
	}
}

// logRunError logs err against a run, with the stack when it was a panic.
func (s *Server) logRunError(runID uuid.UUID, what string, err error) {
	var p *agentrun.PanicError
	if errors.As(err, &p) {
		s.log().Error(what, "run_id", runID, "panic_type", p.Type, "stack", p.Stack)
		return
	}
	s.log().Warn(what, "run_id", runID, "error", err)
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
	runes := []rune(agentrun.CleanText(secretshape.ScrubText(msg)))
	if len(runes) == 0 {
		return agentUnavailableMsg
	}
	if len(runes) > maxErrorMessageLen {
		runes = runes[:maxErrorMessageLen]
	}
	return string(runes)
}
