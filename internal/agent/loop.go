// Package agent runs the bounded tool-use loop: it asks a model for a turn,
// runs the read-only tools the model asks for, and stops at a step, token,
// cost or time limit.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// Event is one thing a run reports to its caller.
type Event struct {
	Type         string          `json:"type"`
	Delta        string          `json:"delta,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	OK           bool            `json:"ok,omitempty"`
	Usage        *provider.Usage `json:"usage,omitempty"`
	CostMicroUSD int64           `json:"cost_micro_usd,omitempty"`
	Provisional  bool            `json:"provisional"`
	Notice       string          `json:"notice,omitempty"`
	Code         string          `json:"code,omitempty"`
	Message      string          `json:"message,omitempty"`
	// Result is set on a result event only.
	Result *agentresult.Result `json:"result,omitempty"`
}

// Task is what an investigator run works on. It takes the place of a chat's
// messages.
type Task struct {
	Kind    string      `json:"kind"`
	Subject TaskSubject `json:"subject"`
	Alert   TaskAlert   `json:"alert"`
}

// TaskSubject is what an investigation is about.
type TaskSubject struct {
	Kind       string   `json:"kind"`
	ServiceIDs []string `json:"service_ids"`
	ClusterID  string   `json:"cluster_id"`
	AccountID  string   `json:"account_id"`
	Region     string   `json:"region"`
}

// TaskAlert is the alert that started an investigation.
type TaskAlert struct {
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	OpenedAt time.Time `json:"opened_at"`
}

type PageContext struct{ Path, ServiceID, ClusterID string }

type RunInput struct {
	RunID           string
	Role            string
	Model           provider.Model
	Messages        []provider.Message
	Page            PageContext
	Bearer          string
	MaxCostMicroUSD int64
	// Task is set for an investigator run, which has no Messages.
	Task *Task
}

// Limits bound a run. A zero value allows nothing: a run with MaxSteps,
// MaxRunTokens or Timeout of zero stops before its first model call.
// MaxResultBytes of zero means the default, 100000.
type Limits struct {
	MaxSteps       int
	MaxRunTokens   int64
	Timeout        time.Duration
	MaxResultBytes int
}

type Loop struct {
	Provider provider.Provider
	Tools    ToolRunner
	Limits   Limits
	// Log, when set, gets one line for each failed model call and one, with
	// counts only, at the end of an investigator run.
	Log *slog.Logger

	// Test seams: nonce makes each of the run's two random codes, newTimer makes the wait
	// before a retry, now is the clock the time margins use. Nil means
	// crypto/rand, time.NewTimer and time.Now.
	nonce    func() string
	newTimer func(time.Duration) *time.Timer
	now      func() time.Time
}

const (
	noticeContextTrimmed = "context_trimmed"
	noticeLimitReached   = "limit_reached"

	charsPerTokenTrim = 4
	trimFraction      = 0.8

	maxRetryWait        = 20 * time.Second
	maxToolCallsPerTurn = 8
	defaultResultBytes  = 100_000
)

// noAnswerText is the answer of a run that ends without one from the model.
const noAnswerText = "The agent could not finish an answer within the limit."

// noTimeText is the answer of a run whose time ran out before its first call.
const noTimeText = "The run ran out of time before it could start."

// noCallText is the answer of a run that made no call to the model.
const noCallText = "The question is too long for this model, or the limit for one question is too small."

// session is the state of one Loop.Run.
type session struct {
	l          *Loop
	in         RunInput
	emit       func(Event)
	system     string
	tools      []provider.ToolDef
	frameBytes int64 // of the system text and the tool definitions
	messages   []provider.Message
	results    map[int]*toolResult // tool results by index into messages
	tagCode    string              // random per run: names the data tag
	loopCode   string              // random per run: marks the loop's own message, never near tool text
	codes      *regexp.Regexp      // both codes, in any letter case
	started    time.Time
	ledger     ledger

	steps    int
	trimmed  bool
	final    bool // the next call is the final one
	notified bool // limit_reached was emitted for the final call
	timedOut bool // the run ended because its time ran out
	spoke    bool // the reply in progress, or the last one, has text for the user

	// For an investigator run only.
	found    *findings          // nil for a chat
	reminded bool               // the reminder turn was used
	reminder []provider.Message // extra messages of the next gathering call
	result   *agentresult.Result
}

// Run drives one conversation turn to its end, reporting through emit. It
// returns nil once it has emitted done or error, and ctx.Err() without either
// when ctx is cancelled.
func (l *Loop) Run(ctx context.Context, in RunInput, emit func(Event)) error {
	r := &session{l: l, in: in, emit: emit, results: map[int]*toolResult{}, started: l.clock()}
	r.tagCode, r.loopCode = l.newNonce(), l.newNonce()
	r.codes = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(r.tagCode) + `|` + regexp.QuoteMeta(r.loopCode))
	if in.Role == roleInvestigator {
		// Its answer is the result event: its text is for nobody.
		r.found, in.Page = newFindings(), PageContext{}
		r.emit = func(ev Event) {
			if ev.Type != "text" {
				emit(ev)
			}
		}
	}
	if (in.Task != nil) != (r.found != nil) {
		r.fail("invalid_request", "This kind of run is not supported.")
		return nil
	}
	system, ok := systemText(in.Role, in.Page, r.tagCode, r.loopCode)
	if !ok {
		r.fail("invalid_request", "This kind of run is not supported.")
		return nil
	}
	r.system = system
	if r.found != nil {
		defer r.logFindings()
		r.found.allowSubject(in.Task.Subject)
		r.messages = []provider.Message{r.taskMessage(*in.Task)}
	} else {
		r.messages = textOnly(in.Messages)
	}
	if len(r.messages) == 0 || r.messages[len(r.messages)-1].Role != provider.RoleUser {
		r.fail("invalid_request", "There is no question to answer.")
		return nil
	}
	for _, tool := range hmtools.Tools() {
		r.tools = append(r.tools, provider.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema()})
	}
	if r.found != nil {
		r.tools = append(r.tools, submitToolDef())
	}
	r.frameBytes = frameBytes(r.system, r.tools)
	r.ledger = ledger{model: in.Model, ceiling: max(in.MaxCostMicroUSD, 0), maxTokens: l.Limits.MaxRunTokens}
	question := r.messages[len(r.messages)-1]
	r.ledger.keepForAnswer(r.frameBytes + messageBytes(question, r.finalInstruction()))
	if l.Limits.MaxSteps <= 0 || l.Limits.MaxRunTokens <= 0 || !r.ledger.holdsReserve() {
		r.stop("The limit for this run is too small for an answer.")
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, l.Limits.Timeout)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if runCtx.Err() != nil {
			r.stopTimedOut()
			return nil
		}
		resp, outcome := r.callModel(ctx, runCtx)
		switch outcome {
		case outcomeCancelled:
			return ctx.Err()
		case outcomeEnded:
			return nil
		}

		submit, submitted := r.findSubmit(resp.ToolCalls)
		problem := ""
		if submitted {
			if problem = r.submit(submit); problem == "" {
				return nil
			}
		}
		if r.final {
			// Other tool calls in the final reply are ignored; its text was streamed.
			if !r.spoke {
				r.emit(Event{Type: "text", Delta: noAnswerText})
			}
			r.emit(Event{Type: "done"})
			return nil
		}
		if len(resp.ToolCalls) == 0 && r.found != nil {
			if r.remind(resp) {
				continue
			}
			return nil
		}
		if len(resp.ToolCalls) == 0 {
			switch {
			case !r.spoke:
				// Nothing to show for the call: ask for the answer outright.
				r.final = true
				continue
			case resp.StopReason == "max_tokens":
				r.stop("The reply was cut short by the output limit.")
			default:
				r.emit(Event{Type: "done"})
			}
			return nil
		}
		r.spoke = false
		r.messages = append(r.messages, provider.Message{
			Role: provider.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, Opaque: resp.Opaque,
		})
		if !r.anyCallFits() {
			r.stop("The run reached its limit and was stopped.")
			return nil
		}
		for i, call := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}
			if runCtx.Err() != nil {
				r.stopTimedOut()
				return nil
			}
			var msg provider.Message
			var res *toolResult
			if submitted {
				msg, res = r.refuse(call, problem)
			} else {
				// The final call shows its results too, so only the time kept for
				// it can stop a tool.
				msg, res = r.runTool(runCtx, call, i >= maxToolCallsPerTurn, r.lowTime())
			}
			r.messages = append(r.messages, msg)
			r.results[len(r.messages)-1] = res
		}
	}
}

// textOnly keeps the text of user and assistant messages. History comes from
// outside the run: it never gets to carry tool calls, tool results or
// adapter state.
func textOnly(history []provider.Message) []provider.Message {
	var out []provider.Message
	for _, m := range history {
		if (m.Role == provider.RoleUser || m.Role == provider.RoleAssistant) && strings.TrimSpace(m.Text) != "" {
			out = append(out, provider.Message{Role: m.Role, Text: m.Text})
		}
	}
	return out
}

// stop ends the run at a limit. A run never ends without saying anything.
func (r *session) stop(message string) {
	r.emit(Event{Type: "notice", Notice: noticeLimitReached, Message: message})
	switch {
	case r.steps == 0 && r.timedOut:
		r.emit(Event{Type: "text", Delta: noTimeText})
	case r.steps == 0:
		r.emit(Event{Type: "text", Delta: noCallText})
	case !r.spoke:
		r.emit(Event{Type: "text", Delta: noAnswerText})
	}
	r.emit(Event{Type: "done"})
}

// stopTimedOut ends the run at its time limit.
func (r *session) stopTimedOut() {
	r.timedOut = true
	r.stop("The run took too long and was stopped.")
}

// fail ends the run with an error event; the message is scrubbed as a last guard.
func (r *session) fail(code, message string) {
	r.emit(Event{Type: "error", Code: code, Message: secretshape.ScrubText(message)})
}

// trim drops the oldest turns while the conversation would use more than 80%
// of the model's context window, and says so once.
func (r *session) trim() {
	size := func(msgs []provider.Message) int64 { return r.frameBytes + messageBytes(msgs...) }
	msgs, trimmed := trimTurns(r.messages, size, r.contextBytes(charsPerTokenTrim))
	if !trimmed {
		return
	}
	dropped := len(r.messages) - len(msgs)
	shifted := map[int]*toolResult{}
	for i, res := range r.results {
		if i >= dropped {
			shifted[i-dropped] = res
		}
	}
	r.results = shifted
	r.messages = msgs
	if !r.trimmed {
		r.trimmed = true
		r.emit(Event{Type: "notice", Notice: noticeContextTrimmed, Message: "The oldest part of this conversation was left out to fit the model's context."})
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeEnded
	outcomeCancelled
)

// callModel makes the next model call, with at most one retry. Every attempt
// is planned afresh: a gathering call, or its retry, that is not admitted
// becomes the final call, which has a retry of its own.
func (r *session) callModel(ctx, runCtx context.Context) (provider.Response, outcome) {
	for retried := false; ; {
		c, msgs, ok := r.nextCall()
		if !ok {
			r.stop("The run reached its limit and was stopped.")
			return provider.Response{}, outcomeEnded
		}
		if r.final && !r.notified {
			r.notified, retried = true, false
			r.emit(Event{Type: "notice", Notice: noticeLimitReached, Message: "The run reached its limit, so this answer uses only what was found so far."})
		}
		resp, streamed, err := r.attempt(runCtx, c, msgs)
		if err == nil {
			return resp, outcomeOK
		}
		if ctx.Err() != nil {
			return provider.Response{}, outcomeCancelled
		}
		if runCtx.Err() != nil {
			r.stopTimedOut()
			return provider.Response{}, outcomeEnded
		}
		var perr *provider.Error
		if !errors.As(err, &perr) {
			r.fail("unavailable", "The model could not be reached.")
			return provider.Response{}, outcomeEnded
		}
		logModelFailure(r.l.Log, r.in.RunID, r.l.Provider.Name(), r.in.Model.ID, perr)
		wait := min(max(perr.RetryAfter, 0), maxRetryWait)
		if !perr.Retryable || retried || streamed || r.timeLeft() <= wait || !r.anyCallFits() {
			r.fail(perr.Kind, providerMessage(perr))
			return provider.Response{}, outcomeEnded
		}
		retried = true
		timer := r.timer(wait)
		select {
		case <-timer.C:
		case <-runCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return provider.Response{}, outcomeCancelled
			}
			r.stopTimedOut()
			return provider.Response{}, outcomeEnded
		}
	}
}

// nextCall plans the next call. Once a gathering call is refused, every call
// is the final one.
func (r *session) nextCall() (call, []provider.Message, bool) {
	if !r.final {
		r.trim()
		if c, ok := r.planGathering(); ok {
			return c, append(slices.Clone(r.messages), r.reminder...), true
		}
		r.final = true
	}
	return r.planFinal()
}

// anyCallFits reports whether the money left pays for another call. A
// gathering call always leaves enough for the final one, so only that counts.
func (r *session) anyCallFits() bool {
	_, _, ok := r.planFinal()
	return ok
}

// attempt makes one request. It emits a provisional usage event first, whose
// figures already include the call's worst case, and the actual usage after.
func (r *session) attempt(runCtx context.Context, c call, msgs []provider.Message) (resp provider.Response, streamed bool, err error) {
	r.steps++
	r.spoke = false
	usage, cost := r.ledger.provisional(c)
	r.emitUsage(usage, cost, true)

	say := func(delta string) {
		streamed = r.found == nil // an investigator's text is never shown, so its call may be retried
		r.spoke = r.spoke || strings.TrimSpace(delta) != ""
		r.emit(Event{Type: "text", Delta: delta})
	}
	resp, err = r.l.Provider.Generate(runCtx, provider.Request{
		Model: r.in.Model.ID, System: r.system, Messages: msgs, Tools: r.tools, MaxOutputTokens: int(c.out),
	}, say)
	r.ledger.settle(c, resp.Usage, err != nil)
	if err != nil {
		return resp, streamed, err
	}
	if !streamed && resp.Text != "" {
		say(resp.Text)
	}
	r.emitUsage(provider.Usage{InputTokens: r.ledger.usedIn, OutputTokens: r.ledger.usedOut}, r.ledger.spentMicroUSD, false)
	r.reminder = nil
	if r.found != nil {
		r.found.noteShown(msgs)
	}
	return resp, streamed, nil
}

// timeLeft is what remains of the run's time.
func (r *session) timeLeft() time.Duration {
	return r.l.Limits.Timeout - r.l.clock().Sub(r.started)
}

// lowTime reports whether only the final call's share of the time is left.
func (r *session) lowTime() bool { return r.timeLeft() <= r.l.Limits.Timeout/4 }

func (l *Loop) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (r *session) timer(d time.Duration) *time.Timer {
	if r.l.newTimer != nil {
		return r.l.newTimer(d)
	}
	return time.NewTimer(d)
}

func (r *session) emitUsage(usage provider.Usage, cost int64, provisional bool) {
	r.emit(Event{Type: "usage", Usage: &usage, CostMicroUSD: cost, Provisional: provisional})
}

// runTool answers one tool call from the model and returns the tool message
// for it. Every failure here is a result for the model, not the end of the
// run. With skip, the tool is not run at all and nothing is reported.
func (r *session) runTool(ctx context.Context, call provider.ToolCall, overLimit, skip bool) (provider.Message, *toolResult) {
	tool, known := lookupTool(call.Name)
	name := "unknown_tool"
	if known {
		name = tool.Name
	}
	if skip {
		return r.resultMessage(call, name, "This tool was not run: the run had too little time left.", true)
	}
	r.emit(Event{Type: "tool_start", Tool: name})

	var text string
	var isError bool
	switch {
	case overLimit:
		text, isError = "Too many tool calls in one turn. Ask for fewer at a time.", true
	case !known:
		text, isError = unknownToolMessage(call.Name), true
	case call.ArgError != "":
		text, isError = "The arguments were not valid: they were not a JSON object.", true
	default:
		if _, err := tool.BuildCall(call.Arguments); err != nil {
			text, isError = buildErrorMessage(err), true
		} else {
			text, isError = r.l.Tools.Run(ctx, r.in.Bearer, call.Name, call.Arguments)
		}
	}

	r.emit(Event{Type: "tool_end", Tool: name, OK: !isError})
	return r.resultMessage(call, name, text, isError)
}

func (r *session) resultMessage(call provider.ToolCall, name, text string, isError bool) (provider.Message, *toolResult) {
	var res *toolResult
	if r.found != nil && !isError {
		res = r.readResult(name, call.Arguments, text)
	} else {
		res = r.newResult(name, text)
	}
	return provider.Message{
		Role: provider.RoleTool, ToolCallID: call.ID, ToolName: call.Name, IsError: isError,
		Text: res.render(len(res.data)),
	}, res
}

// newNonce makes a random code. Each run has two, so that nothing a tool
// returns can close its own envelope or speak for the loop.
func (l *Loop) newNonce() string {
	if l.nonce != nil {
		return l.nonce()
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("agent: no randomness: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}
