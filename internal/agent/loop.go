// Package agent runs the bounded tool-use loop: it asks a model for a turn,
// runs the read-only tools the model asks for, and stops at a step, token,
// cost or time limit.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	// Test seams: nonce makes the data tag's random code, newTimer makes the
	// wait before a retry. Nil means crypto/rand and time.NewTimer.
	nonce    func() string
	newTimer func(time.Duration) *time.Timer
}

const (
	noticeContextTrimmed = "context_trimmed"
	noticeLimitReached   = "limit_reached"

	// maxOutputTokensPerCall caps one reply; below minOutputTokensPerCall of
	// room the run stops instead of asking for a uselessly short answer.
	maxOutputTokensPerCall = 8192
	minOutputTokensPerCall = 256

	// charsPerTokenBudget is the estimate for the token allowance. Money does
	// not use it: cost assumes one token per byte.
	charsPerTokenBudget = 3
	charsPerTokenTrim   = 4
	trimFraction        = 0.8

	// framingTokens is added to the worst-case input of every call.
	framingTokens = 1000

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
	nonce    string         // random per run: names the data tag
	marker   *regexp.Regexp // the tag's name, in any letter case

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
	r.nonce = l.newNonce()
	r.marker = regexp.MustCompile(`(?i)tool_result_` + regexp.QuoteMeta(r.nonce))
	system, ok := systemText(in.Role, in.Page, r.nonce)
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
			if resp.StopReason == "max_tokens" {
				r.stop("The reply was cut short by the output limit.")
				return nil
			}
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
			if runCtx.Err() != nil {
				r.stop("The run took too long and was stopped.")
				return nil
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

// callPlan is what one model call may use: the request to send, the token
// estimate for the allowance, and the worst-case cost for the money ceiling.
type callPlan struct {
	req     provider.Request
	estIn   int64 // 3 bytes per token, for the token allowance
	maxOut  int64
	worstIn int64 // one token per byte plus framing, for the cost
	cost    int64 // provisional cost of this call alone
}

// plan sizes the next call. Money is held to the worst case (every byte a
// token); tokens use the estimate. When the full output cap does not fit the
// cost ceiling it is lowered, down to the floor, before the run stops. A
// non-empty reason means the call may not be made.
func (r *session) plan() (callPlan, string) {
	bytes := int64(r.toolSize + len(r.system) + messageChars(r.messages))
	p := callPlan{
		estIn:   (bytes + charsPerTokenBudget - 1) / charsPerTokenBudget,
		worstIn: bytes + framingTokens,
	}
	p.maxOut = min(maxOutputTokensPerCall, r.l.Limits.MaxRunTokens-r.tokensIn-r.tokensOut-p.estIn)
	if p.maxOut < minOutputTokensPerCall {
		return p, "The run reached its token limit and was stopped."
	}
	costOf := func(out int64) int64 {
		return r.in.Model.CostMicroUSD(provider.Usage{InputTokens: p.worstIn, OutputTokens: out})
	}
	fits := func(out int64) bool { return r.cost+costOf(out) <= r.in.MaxCostMicroUSD }
	if !fits(minOutputTokensPerCall) {
		return p, "The run reached its cost limit and was stopped."
	}
	if !fits(p.maxOut) {
		low, high := int64(minOutputTokensPerCall), p.maxOut
		for low < high {
			mid := (low + high + 1) / 2
			if fits(mid) {
				low = mid
			} else {
				high = mid - 1
			}
		}
		p.maxOut = low
	}
	p.cost = costOf(p.maxOut)
	p.req = provider.Request{
		Model: r.in.Model.ID, System: r.system, Messages: slices.Clone(r.messages), Tools: r.tools,
		MaxOutputTokens: int(p.maxOut),
	}
	return p, ""
}

// callModel makes one model call, with at most one retry. Before each attempt
// it emits a provisional usage event whose figures already include the call's
// worst case; a call that fails or reports nothing keeps that figure.
func (r *session) callModel(ctx, runCtx context.Context) (provider.Response, outcome) {
	for attempt := 0; ; attempt++ {
		p, why := r.plan()
		if why != "" {
			r.stop(why)
			return provider.Response{}, outcomeEnded
		}
		r.emitUsage(r.tokensIn+p.estIn, r.tokensOut+p.maxOut, r.cost+p.cost, true)

		streamed := false
		resp, err := r.l.Provider.Generate(runCtx, p.req, func(delta string) {
			streamed = true
			r.emit(Event{Type: "text", Delta: delta})
		})
		if err == nil {
			if !streamed && resp.Text != "" {
				r.emit(Event{Type: "text", Delta: resp.Text})
			}
			// A field the vendor left at zero keeps its provisional figure: a
			// reply is never free.
			in, out, costIn, costOut := resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.InputTokens, resp.Usage.OutputTokens
			if in <= 0 {
				in, costIn = p.estIn, p.worstIn
			}
			if out <= 0 {
				out, costOut = p.maxOut, p.maxOut
			}
			r.tokensIn += in
			r.tokensOut += out
			r.cost += r.in.Model.CostMicroUSD(provider.Usage{InputTokens: costIn, OutputTokens: costOut})
			r.emitUsage(r.tokensIn, r.tokensOut, r.cost, false)
			return resp, outcomeOK
		}

		// A failed call keeps the larger of its provisional figure and what
		// the vendor reported, field by field: it may have done and billed
		// the work.
		r.tokensIn += max(p.estIn, resp.Usage.InputTokens)
		r.tokensOut += max(p.maxOut, resp.Usage.OutputTokens)
		r.cost += r.in.Model.CostMicroUSD(provider.Usage{
			InputTokens: max(p.worstIn, resp.Usage.InputTokens), OutputTokens: max(p.maxOut, resp.Usage.OutputTokens),
		})
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
		if !perr.Retryable || attempt > 0 || streamed || (hasDeadline && time.Until(deadline) <= wait) {
			r.fail(perr.Kind, providerMessage(perr))
			return provider.Response{}, outcomeEnded
		}
		// The retry must fit the limits as they now stand; if it does not,
		// the user learns what went wrong, not that a limit was hit.
		if _, why := r.plan(); why != "" {
			r.fail(perr.Kind, providerMessage(perr))
			return provider.Response{}, outcomeEnded
		}
		timer := r.timer(wait)
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

func (r *session) timer(d time.Duration) *time.Timer {
	if r.l.newTimer != nil {
		return r.l.newTimer(d)
	}
	return time.NewTimer(d)
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

// newNonce makes the random code that names this run's data tag, so tool
// output cannot write the tag that closes its own envelope.
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

// envelope wraps tool output as data inside the run's random tag. The text is
// scrubbed first, then the tag's name is removed from it, and only then is it
// cut, so a cut cannot split a secret and the cap holds for what is sent.
func (r *session) envelope(tool, text string) string {
	limit := r.l.Limits.MaxResultBytes
	if limit <= 0 {
		limit = defaultResultBytes
	}
	text = secretshape.ScrubText(text)
	text = r.marker.ReplaceAllString(text, "tool_result_[removed]")
	note := ""
	if total := len(text); total > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
		note = "\n[truncated: " + strconv.Itoa(total-cut) + " of " + strconv.Itoa(total) + " bytes were left out]"
	}
	tag := "tool_result_" + r.nonce
	return "<" + tag + " tool=\"" + tool + "\">\n" + text + note + "\n</" + tag + ">"
}
