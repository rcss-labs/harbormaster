package agent

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

// limitLine is the loop's own instruction for the final call.
const limitLine = "You have reached the limit for this question. Answer now from what you have, say what is missing, and do not call tools."

const (
	// minShare is the least of a cut result worth sending; below it the result
	// becomes a placeholder.
	minShare = 512
	// maxHeadRoom is the most room, beyond placeholders, that older rounds are
	// dropped to make: a full turn's results at about minShare each.
	maxHeadRoom = maxToolCallsPerTurn * minShare
)

// entry is a message of the run and, for a tool message, the result it shows.
type entry struct {
	msg    provider.Message
	result *toolResult
}

// fitFinal returns the messages of a final call whose request, with system and
// tools, is at most budgetBytes, or nil when not even the latest user message
// fits. It drops older turns first, whole. Then the results of this run's
// rounds (an assistant turn and the results of its calls) share what is left:
// small ones stay whole, the others are cut to equal shares, and where a share
// would be under minShare the newest get one and the rest become placeholders.
// Rounds are dropped, oldest first and always whole, only when that would
// still leave the newest results no room.
func fitFinal(system string, tools []provider.ToolDef, history []entry, budgetBytes int64) []provider.Message {
	room := budgetBytes - frameBytes(system, tools)
	question := -1
	for i, e := range history {
		if e.msg.Role == provider.RoleUser {
			question = i
		}
	}
	if question < 0 {
		return nil
	}
	for start := 0; start <= question; start++ {
		if history[start].msg.Role != provider.RoleUser {
			continue
		}
		if msgs := entryMessages(history[start:]); messageBytes(msgs...) <= room {
			return msgs
		}
	}

	room -= messageBytes(history[question].msg)
	if room < 0 {
		return nil
	}
	rounds := splitRounds(history[question+1:])
	var least, heads int64 // of all rounds: every result a placeholder, every result at minShare
	for _, round := range rounds {
		least += round.least
		heads += round.heads
	}
	for len(rounds) > 0 {
		need := least
		if len(rounds) > 1 {
			need = min(heads, least+maxHeadRoom)
		}
		if need <= room {
			break
		}
		least -= rounds[0].least
		heads -= rounds[0].heads
		rounds = rounds[1:]
	}
	var kept []entry
	for _, round := range rounds {
		kept = append(kept, round.entries...)
	}
	return append([]provider.Message{history[question].msg}, shareRoom(kept, room-least)...)
}

type round struct {
	entries      []entry
	least, heads int64
}

func splitRounds(entries []entry) []round {
	var rounds []round
	for _, e := range entries {
		if e.msg.Role == provider.RoleAssistant || len(rounds) == 0 {
			rounds = append(rounds, round{})
		}
		r := &rounds[len(rounds)-1]
		r.entries = append(r.entries, e)
		textless := messageBytes(e.msg) - int64(len(e.msg.Text))
		r.least += textless + int64(e.leastSize())
		r.heads += textless + int64(e.sizeAt(minShare))
	}
	return rounds
}

// leastSize is the size of the shortest text that can stand for the entry.
func (e entry) leastSize() int {
	if e.result == nil {
		return len(e.msg.Text)
	}
	return min(len(e.msg.Text), len(resultPlaceholder))
}

// sizeAt is the size of the entry's text when its result is cut to n bytes.
func (e entry) sizeAt(n int) int {
	if e.result == nil {
		return len(e.msg.Text)
	}
	return min(len(e.msg.Text), e.result.renderSize(n))
}

// textAt is that text; a cut that saves nothing is not made.
func (e entry) textAt(n int) string {
	if e.sizeAt(n) == len(e.msg.Text) {
		return e.msg.Text
	}
	return e.result.render(n)
}

// shareRoom returns entries as messages, spending spare bytes beyond the
// placeholders on their results.
func shareRoom(entries []entry, spare int64) []provider.Message {
	msgs := entryMessages(entries)
	var sharing []int
	longest := minShare
	left := spare
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.leastSize() == len(e.msg.Text) {
			continue
		}
		if extra := int64(e.sizeAt(minShare) - e.leastSize()); extra <= left {
			left -= extra
			sharing = append(sharing, i)
			longest = max(longest, len(e.result.data))
		} else {
			msgs[i].Text = resultPlaceholder
		}
	}
	// The largest equal share that fits; minShare is known to.
	share := minShare + sort.Search(longest-minShare, func(n int) bool {
		var extra int64
		for _, i := range sharing {
			extra += int64(entries[i].sizeAt(minShare+n+1) - entries[i].leastSize())
		}
		return extra > spare
	})
	for _, i := range sharing {
		msgs[i].Text = entries[i].textAt(share)
	}
	return msgs
}

func entryMessages(entries []entry) []provider.Message {
	msgs := make([]provider.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.msg
	}
	return msgs
}

// finalInstruction is the loop's instruction for the final call. It is a
// message of its own, never part of a tool result, and carries the run's
// random code so that nothing a tool returned can imitate it.
func (r *session) finalInstruction() provider.Message {
	return provider.Message{Role: provider.RoleUser, Text: "[Message from the loop, code " + r.nonce + "] " + limitLine}
}

// planGathering plans an ordinary call with the messages as they stand, or
// says that the next call must be the final one.
func (r *session) planGathering() (call, bool) {
	bytes := r.requestBytes()
	lastStep := r.steps+1 >= r.l.Limits.MaxSteps
	if lastStep || r.lowTime() || bytes > r.contextBytes(charsPerTokenTrim) {
		return call{}, false
	}
	return r.ledger.admitGathering(bytes)
}

// planFinal builds the final call: whatever fits its money and the model's
// context. It reports false when not even the question fits.
func (r *session) planFinal() (call, []provider.Message, bool) {
	instruction := r.finalInstruction()
	history := make([]entry, len(r.messages))
	for i, m := range r.messages {
		history[i] = entry{msg: m, result: r.results[i]}
	}
	most := min(r.requestBytes()+messageBytes(instruction), r.contextBytes(bytesPerToken))
	money := r.ledger.finalMoney(r.notified) // notified by the first attempt, so this is the retry
	msgs := fitFinal(r.system, r.tools, history, r.ledger.finalBytes(most, money)-messageBytes(instruction))
	if msgs == nil {
		return call{}, nil, false
	}
	msgs = append(msgs, instruction)
	c, ok := r.ledger.admitFinal(r.frameBytes+messageBytes(msgs...), money)
	return c, msgs, ok
}

func (r *session) requestBytes() int64 { return r.frameBytes + messageBytes(r.messages...) }

// contextBytes is how large a request may be for the model's context, at the
// given bytes per token.
func (r *session) contextBytes(perToken int) int64 {
	return int64(float64(r.in.Model.ContextTokens)*trimFraction) * int64(perToken)
}
