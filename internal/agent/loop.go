// Package agent runs the bounded tool-use loop: it asks a model for a turn,
// runs the read-only tools the model asks for, and stops at a step, token,
// cost or time limit.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
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
}

const (
	noticeContextTrimmed = "context_trimmed"
	noticeLimitReached   = "limit_reached"

	// maxOutputTokensPerCall caps one reply; below minOutputTokensPerCall of
	// room the run stops instead of asking for a uselessly short answer.
	maxOutputTokensPerCall = 8192
	minOutputTokensPerCall = 256

	// charsPerTokenBudget is deliberately pessimistic: the budget must hold
	// even when the real count is higher than a 4-characters estimate.
	charsPerTokenBudget = 3
	charsPerTokenTrim   = 4
	trimFraction        = 0.8

	maxRetryWait        = 20 * time.Second
	maxToolCallsPerTurn = 8
	defaultResultBytes  = 100_000
)

// session is the state of one Loop.Run.
type session struct {
	l        *Loop
	in       RunInput
	emit     func(Event)
	system   string
	tools    []provider.ToolDef
	toolSize int
	messages []provider.Message

	steps   int
	trimmed bool

	// Figures of finished calls. A call that failed, or reported no usage,
	// stays in here at its provisional figure.
	tokensIn, tokensOut, cost int64
}

// Run drives one conversation turn to its end, reporting through emit. It
// returns nil once it has emitted done or error, and ctx.Err() without either
// when ctx is cancelled.
func (l *Loop) Run(ctx context.Context, in RunInput, emit func(Event)) error {
	r := &session{l: l, in: in, emit: emit}
	system, ok := systemText(in.Role, in.Page)
	if !ok {
		r.fail("invalid_request", "This kind of run is not supported.")
		return nil
	}
	r.system = system
	r.messages = textOnly(in.Messages)
	if len(r.messages) == 0 || r.messages[len(r.messages)-1].Role != provider.RoleUser {
		r.fail("invalid_request", "There is no question to answer.")
		return nil
	}
	for _, tool := range hmtools.Tools() {
		r.tools = append(r.tools, provider.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema()})
	}
	encoded, _ := json.Marshal(r.tools)
	r.toolSize = len(encoded)

	runCtx, cancel := context.WithTimeout(ctx, l.Limits.Timeout)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if runCtx.Err() != nil {
			r.stop("The run took too long and was stopped.")
			return nil
		}
		if r.steps >= l.Limits.MaxSteps {
			r.stop("The run reached its step limit and was stopped.")
			return nil
		}
		r.trim()

		resp, outcome := r.callModel(ctx, runCtx)
		switch outcome {
		case outcomeCancelled:
			return ctx.Err()
		case outcomeEnded:
			return nil
		}
		r.steps++

		r.messages = append(r.messages, provider.Message{
			Role: provider.RoleAssistant, Text: resp.Text, ToolCalls: resp.ToolCalls, Opaque: resp.Opaque,
		})
		if len(resp.ToolCalls) == 0 {
			r.emit(Event{Type: "done"})
			return nil
		}
		if r.steps >= l.Limits.MaxSteps || r.tokensIn+r.tokensOut >= l.Limits.MaxRunTokens || r.cost >= in.MaxCostMicroUSD {
			r.stop("The run reached its limit and was stopped.")
			return nil
		}
		for i, call := range resp.ToolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.messages = append(r.messages, r.runTool(runCtx, call, i >= maxToolCallsPerTurn))
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

func (r *session) stop(message string) {
	r.emit(Event{Type: "notice", Notice: noticeLimitReached, Message: message})
	r.emit(Event{Type: "done"})
}

// fail ends the run with an error event; the message is scrubbed as a last guard.
func (r *session) fail(code, message string) {
	r.emit(Event{Type: "error", Code: code, Message: secretshape.ScrubText(message)})
}

// trim drops the oldest turns while the conversation would use more than 80%
// of the model's context window, and says so once.
func (r *session) trim() {
	limit := int64(float64(r.in.Model.ContextTokens) * trimFraction)
	overhead := len(r.system) + r.toolSize
	size := func(msgs []provider.Message) int64 {
		return int64((overhead + messageChars(msgs) + charsPerTokenTrim - 1) / charsPerTokenTrim)
	}
	msgs, trimmed := trimTurns(r.messages, size, limit)
	if !trimmed {
		return
	}
	r.messages = msgs
	if !r.trimmed {
		r.trimmed = true
		r.emit(Event{Type: "notice", Notice: noticeContextTrimmed, Message: "The oldest part of this conversation was left out to fit the model's context."})
	}
}

func messageChars(msgs []provider.Message) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Text) + len(m.ToolName) + len(m.ToolCallID) + len(m.Opaque) + 8
		for _, c := range m.ToolCalls {
			args, _ := json.Marshal(c.Arguments)
			chars += len(c.ID) + len(c.Name) + len(args) + len(c.ArgError) + 8
		}
	}
	return chars
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeEnded
	outcomeCancelled
)

// callModel makes one model call, with at most one retry. Before each attempt
// it emits a provisional usage event whose figures already include the call's
// worst case; a call that fails or reports nothing keeps that figure.
func (r *session) callModel(ctx, runCtx context.Context) (provider.Response, outcome) {
	for attempt := 0; ; attempt++ {
		req := provider.Request{Model: r.in.Model.ID, System: r.system, Messages: slices.Clone(r.messages), Tools: r.tools}
		estIn := int64((r.toolSize + len(r.system) + messageChars(r.messages) + charsPerTokenBudget - 1) / charsPerTokenBudget)
		maxOut := min(maxOutputTokensPerCall, r.l.Limits.MaxRunTokens-r.tokensIn-r.tokensOut-estIn)
		if maxOut < minOutputTokensPerCall {
			r.stop("The run reached its token limit and was stopped.")
			return provider.Response{}, outcomeEnded
		}
		provisional := provider.Usage{InputTokens: estIn, OutputTokens: maxOut}
		provisionalCost := r.in.Model.CostMicroUSD(provisional)
		if r.cost+provisionalCost > r.in.MaxCostMicroUSD {
			r.stop("The run reached its cost limit and was stopped.")
			return provider.Response{}, outcomeEnded
		}
		req.MaxOutputTokens = int(maxOut)
		r.emitUsage(r.tokensIn+estIn, r.tokensOut+maxOut, r.cost+provisionalCost, true)

		streamed := false
		resp, err := r.l.Provider.Generate(runCtx, req, func(delta string) {
			streamed = true
			r.emit(Event{Type: "text", Delta: delta})
		})
		if err == nil {
			if !streamed && resp.Text != "" {
				r.emit(Event{Type: "text", Delta: resp.Text})
			}
			actual := resp.Usage
			if actual.InputTokens <= 0 && actual.OutputTokens <= 0 {
				actual = provisional
			}
			r.tokensIn += max(actual.InputTokens, 0)
			r.tokensOut += max(actual.OutputTokens, 0)
			r.cost += r.in.Model.CostMicroUSD(actual)
			r.emitUsage(r.tokensIn, r.tokensOut, r.cost, false)
			return resp, outcomeOK
		}

		// The failed call's provisional figure stands: the vendor may have
		// done and billed the work.
		r.tokensIn += provisional.InputTokens
		r.tokensOut += provisional.OutputTokens
		r.cost += provisionalCost
		if ctx.Err() != nil {
			return provider.Response{}, outcomeCancelled
		}
		if runCtx.Err() != nil {
			r.stop("The run took too long and was stopped.")
			return provider.Response{}, outcomeEnded
		}
		var perr *provider.Error
		if !errors.As(err, &perr) {
			r.fail("unavailable", "The model could not be reached.")
			return provider.Response{}, outcomeEnded
		}
		wait := min(max(perr.RetryAfter, 0), maxRetryWait)
		deadline, hasDeadline := runCtx.Deadline()
		if !perr.Retryable || attempt > 0 || (hasDeadline && time.Until(deadline) <= wait) {
			r.fail(perr.Kind, providerMessage(perr))
			return provider.Response{}, outcomeEnded
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-runCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return provider.Response{}, outcomeCancelled
			}
			r.stop("The run took too long and was stopped.")
			return provider.Response{}, outcomeEnded
		}
	}
}

func providerMessage(e *provider.Error) string {
	if e.Message != "" {
		return e.Message
	}
	switch e.Kind {
	case "auth":
		return "The model provider refused our credentials."
	case "rate_limited":
		return "The model provider is limiting requests. Try again shortly."
	case "context_too_long":
		return "The conversation is too long for the model."
	case "invalid_request":
		return "The model provider refused the request."
	}
	return "The model provider is unavailable. Try again shortly."
}

func (r *session) emitUsage(in, out, cost int64, provisional bool) {
	r.emit(Event{Type: "usage", Usage: &provider.Usage{InputTokens: in, OutputTokens: out}, CostMicroUSD: cost, Provisional: provisional})
}

// runTool answers one tool call from the model and returns the tool message
// for it. Every failure here is a result for the model, not the end of the run.
func (r *session) runTool(ctx context.Context, call provider.ToolCall, overLimit bool) provider.Message {
	tool, known := lookupTool(call.Name)
	name := "unknown_tool"
	if known {
		name = tool.Name
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
	return provider.Message{
		Role: provider.RoleTool, ToolCallID: call.ID, ToolName: call.Name, IsError: isError,
		Text: r.envelope(name, text),
	}
}

var envelopeTag = regexp.MustCompile(`(?i)<(/?)tool_result`)

// envelope wraps tool output as data. It is scrubbed first and cut second, so
// a cut cannot split a secret, and text that looks like the envelope's own
// tags is defanged so the output cannot close it early.
func (r *session) envelope(tool, text string) string {
	limit := r.l.Limits.MaxResultBytes
	if limit <= 0 {
		limit = defaultResultBytes
	}
	text = secretshape.ScrubText(text)
	note := ""
	if total := len(text); total > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
		note = "\n[truncated: " + strconv.Itoa(total-cut) + " of " + strconv.Itoa(total) + " bytes were left out]"
	}
	text = envelopeTag.ReplaceAllString(text, "<$1 tool_result")
	return "<tool_result tool=\"" + tool + "\">\n" + text + note + "\n</tool_result>"
}
