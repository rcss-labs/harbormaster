package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	// answerOutputTokens is the output the final call must be able to use.
	answerOutputTokens = 2048
	// minResultBytes is the least a shrunk tool result keeps.
	minResultBytes = 2048
)

// limitLine is the loop's own instruction for the final call.
const limitLine = "You have reached the limit for this question. Answer now from what you have, say what is missing, and do not call tools."

// answerTooLargeNote ends a tool answer that reached the upstream read cap.
const answerTooLargeNote = "\n[The answer was larger than could be read; the rest was left out.]"

const (
	// resultSlack is what the data tag and the truncation note add around a result.
	resultSlack = 256
	// outputBytesPerToken is how many bytes of a reply are assumed per output
	// token when sizing the input of the call that follows it.
	outputBytesPerToken = 4
)

// toolResult is a tool answer as the loop keeps it, so that it can be shown
// in full, cut to the result cap, or shrunk later to make room for an answer.
type toolResult struct {
	tool    string
	data    string // scrubbed, tag-neutralised, cut to the result cap
	omitted int    // bytes of the scrubbed answer that are not in data
	readCap bool   // the answer reached the upstream read cap: more existed than was read
	shown   int    // how much of data is currently shown
}

// render is the tool message text: the shown data inside the run's tag,
// followed by a note in the loop's own words when anything was left out.
func (r *session) render(res *toolResult) string {
	left := res.omitted + len(res.data) - res.shown
	total := res.omitted + len(res.data)
	note := ""
	switch {
	case left > 0 && res.readCap:
		note = "\n[truncated: at least " + strconv.Itoa(left) + " of at least " + strconv.Itoa(total) +
			" bytes were left out; the answer was larger than could be read]"
	case left > 0:
		note = "\n[truncated: " + strconv.Itoa(left) + " of " + strconv.Itoa(total) + " bytes were left out]"
	case res.readCap:
		note = answerTooLargeNote
	}
	tag := "tool_result_" + r.nonce
	return "<" + tag + " tool=\"" + res.tool + "\">\n" + res.data[:res.shown] + note + "\n</" + tag + ">"
}

// newResult prepares a tool answer: scrub, then remove the tag's name, and
// only then cut, so a cut cannot split a secret and the cap holds for what is
// sent.
func (r *session) newResult(tool, text string) *toolResult {
	limit := r.l.Limits.MaxResultBytes
	if limit <= 0 {
		limit = defaultResultBytes
	}
	res := &toolResult{tool: tool}
	if strings.HasSuffix(text, answerTooLargeNote) {
		text = strings.TrimSuffix(text, answerTooLargeNote)
		res.readCap = true
	}
	text = secretshape.ScrubText(text)
	text = r.marker.ReplaceAllString(text, "tool_result_[removed]")
	cut := cutAt(text, limit)
	res.data, res.omitted, res.shown = text[:cut], len(text)-cut, cut
	return res
}

// cutAt returns where to cut text to at most n bytes without splitting a character.
func cutAt(text string, n int) int {
	if len(text) <= n {
		return len(text)
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return n
}

// finalMessage is the loop's instruction for the final call. It is a message
// of its own, never part of a tool result, and carries the run's random code
// so that nothing a tool returned can imitate it.
func (r *session) finalMessage() provider.Message {
	return provider.Message{Role: provider.RoleUser, Text: "[Message from the loop, code " + r.nonce + "] " + limitLine}
}

// callPlan is what one model call may use: the request to send, the token
// estimate for the allowance, and the worst-case cost for the money ceiling.
type callPlan struct {
	req     provider.Request
	estIn   int64 // 3 bytes per token, for the token allowance
	maxOut  int64
	worstIn int64 // one token per byte plus framing, for the cost
	cost    int64 // provisional cost of this call alone
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// floorMessageChars is the size the messages could be shrunk to: every tool
// result cut to its minimum.
func (r *session) floorMessageChars() int64 {
	var chars int64
	for i, m := range r.messages {
		if res := r.results[i]; res != nil {
			shrunk := *res
			shrunk.shown = min(res.shown, cutAt(res.data, minResultBytes))
			m.Text = r.render(&shrunk)
		}
		chars += int64(messageChars([]provider.Message{m}))
	}
	return chars
}

// answerBytes is the input of a final call made after one more ordinary call
// that produces up to out tokens and up to results tool results: everything
// shrunk to its minimum, which is the least the final call can need.
func (r *session) answerBytes(out int64, results int) int64 {
	line := int64(messageChars([]provider.Message{r.finalMessage()}))
	return int64(r.toolSize+len(r.system)) + r.floorMessageChars() + out*outputBytesPerToken +
		int64(results)*(minResultBytes+resultSlack) + line
}

// fits reports whether a call with input worstIn/estIn and output cap out
// leaves the run inside its limits; for an ordinary call it also keeps what a
// final answer would need afterwards (reserveBytes of input).
func (r *session) fits(estIn, worstIn, out, reserveBytes int64) (tokensOK, costOK bool) {
	tokens := r.tokensIn + r.tokensOut + estIn + out
	cost := r.cost + r.in.Model.CostMicroUSD(provider.Usage{InputTokens: worstIn, OutputTokens: out})
	if reserveBytes > 0 {
		tokens += ceilDiv(reserveBytes, charsPerTokenBudget) + answerOutputTokens
		cost += r.in.Model.CostMicroUSD(provider.Usage{InputTokens: reserveBytes + framingTokens, OutputTokens: answerOutputTokens})
	}
	return tokens <= r.l.Limits.MaxRunTokens, cost <= r.in.MaxCostMicroUSD
}

// plan sizes the next call. Money is held to the worst case (every byte a
// token); tokens use the estimate. An ordinary call must leave room for a
// final answer (answerOutputTokens plus the input of one last call); the final
// call must itself fit with at least answerOutputTokens. The output cap is
// lowered, down to the floor, before the call is refused; a non-empty reason
// means the call may not be made.
func (r *session) plan(final bool) (callPlan, string) {
	msgs := r.messages
	if final {
		msgs = append(slices.Clone(msgs), r.finalMessage())
	}
	bytes := int64(r.toolSize + len(r.system) + messageChars(msgs))
	p := callPlan{estIn: ceilDiv(bytes, charsPerTokenBudget), worstIn: bytes + framingTokens}
	floor := int64(minOutputTokensPerCall)
	if final {
		floor = answerOutputTokens
	}
	fitsAt := func(out int64) (bool, bool) {
		reserve := int64(0)
		if !final {
			reserve = r.answerBytes(out, maxToolCallsPerTurn)
		}
		return r.fits(p.estIn, p.worstIn, out, reserve)
	}
	if tokensOK, costOK := fitsAt(floor); !tokensOK || !costOK {
		if !tokensOK {
			return p, "The run reached its token limit and was stopped."
		}
		return p, "The run reached its cost limit and was stopped."
	}
	low, high := floor, int64(maxOutputTokensPerCall)
	for low < high {
		mid := (low + high + 1) / 2
		if tokensOK, costOK := fitsAt(mid); tokensOK && costOK {
			low = mid
		} else {
			high = mid - 1
		}
	}
	p.maxOut = low
	p.cost = r.in.Model.CostMicroUSD(provider.Usage{InputTokens: p.worstIn, OutputTokens: p.maxOut})
	p.req = provider.Request{
		Model: r.in.Model.ID, System: r.system, Messages: slices.Clone(msgs), Tools: r.tools,
		MaxOutputTokens: int(p.maxOut),
	}
	return p, ""
}

// planFinal plans the final call, shrinking tool results until it fits: this
// turn's first, largest first, then older turns', each down to minResultBytes.
func (r *session) planFinal() (callPlan, string) {
	for {
		p, why := r.plan(true)
		if why == "" || !r.shrinkOne() {
			return p, why
		}
	}
}

func (r *session) planFor(final bool) (callPlan, string) {
	if final {
		return r.planFinal()
	}
	return r.plan(false)
}

// shrinkOne cuts the largest result that is above the minimum, preferring the
// results of the turn in progress, and reports whether there was one.
func (r *session) shrinkOne() bool {
	turn := 0
	for i, m := range r.messages {
		if m.Role == provider.RoleAssistant {
			turn = i + 1
		}
	}
	pick := func(from, to int) *toolResult {
		var best *toolResult
		bestIndex := -1
		for i := from; i < to; i++ {
			res := r.results[i]
			if res == nil || res.shown <= cutAt(res.data, minResultBytes) {
				continue
			}
			if best == nil || res.shown > best.shown {
				best, bestIndex = res, i
			}
		}
		if best != nil {
			best.shown = cutAt(best.data, minResultBytes)
			r.messages[bestIndex].Text = r.render(best)
		}
		return best
	}
	return pick(turn, len(r.messages)) != nil || pick(0, turn) != nil
}

// canUseResults reports whether n more tool results, even at their minimum,
// leave room for a final answer.
func (r *session) canUseResults(n int) bool {
	bytes := r.answerBytes(0, n)
	tokensOK, costOK := r.fits(ceilDiv(bytes, charsPerTokenBudget), bytes+framingTokens, answerOutputTokens, 0)
	return tokensOK && costOK
}

// timeLow reports whether less than a fifth of the run's time is left, which
// makes the next call the final one while there is still time to answer.
func (r *session) timeLow(runCtx context.Context) bool {
	deadline, ok := runCtx.Deadline()
	return ok && r.steps > 0 && time.Until(deadline) < r.l.Limits.Timeout/5
}
