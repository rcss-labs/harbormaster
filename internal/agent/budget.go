package agent

import (
	"encoding/json"
	"math"
	"sort"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

const (
	// A gathering call is given between minGatherOutput and maxGatherOutput
	// tokens of reply, a final call between minFinalOutput and maxFinalOutput.
	minGatherOutput = 1024
	maxGatherOutput = 8192
	minFinalOutput  = 2048
	maxFinalOutput  = 4096

	// reserveDataBytes is the room for tool results and history that the
	// answer reserve pays for.
	reserveDataBytes = 32 * 1024

	// Money assumes one token per byte, plus framingTokens per request. The
	// token allowance uses an estimate of bytesPerToken instead.
	framingTokens = 1000
	bytesPerToken = 3
)

// ledger holds what a run has spent and decides what the next call may use.
// Its figures are those the vendor reported; a call that reported none counts
// at its worst case. It does no I/O.
type ledger struct {
	model     provider.Model
	ceiling   int64 // micro-dollars
	maxTokens int64
	reserve   int64 // micro-dollars kept for the final answer, fixed at the start of the run

	spentMicroUSD   int64
	usedIn, usedOut int64
}

// call is what one model call is planned to use.
type call struct {
	bytes    int64 // of the request
	estimate int64 // of its input tokens, for the token allowance
	out      int64 // output cap
	cost     int64 // worst case, in micro-dollars
}

// worstCost is the most a call of requestBytes with out tokens of reply can cost.
func (l *ledger) worstCost(requestBytes, out int64) int64 {
	return l.model.CostMicroUSD(provider.Usage{InputTokens: requestBytes + framingTokens, OutputTokens: out})
}

// keepForAnswer fixes the reserve: the worst case of the smallest final call
// that still has room for something to answer from.
func (l *ledger) keepForAnswer(smallestFinalBytes int64) {
	l.reserve = l.worstCost(smallestFinalBytes+reserveDataBytes, minFinalOutput)
}

func (l *ledger) left() int64 { return max(l.ceiling-l.spentMicroUSD, 0) }

func (l *ledger) holdsReserve() bool { return l.reserve <= l.left() }

// admitGathering plans an ordinary call, or refuses it: after its worst case
// the reserve must still be there, and its estimated input must fit the
// token allowance.
func (l *ledger) admitGathering(requestBytes int64) (call, bool) {
	unused := l.maxTokens - addCapped(l.usedIn, l.usedOut)
	estimate := estimateTokens(requestBytes)
	if estimate > unused {
		return call{}, false
	}
	most := min(maxGatherOutput, max(minGatherOutput, unused-estimate))
	return l.plan(requestBytes, minGatherOutput, most, l.left()-l.reserve)
}

// admitFinal plans the final call, which only the money left can refuse.
func (l *ledger) admitFinal(requestBytes int64) (call, bool) {
	return l.plan(requestBytes, minFinalOutput, maxFinalOutput, l.left())
}

// plan gives a call the largest output cap in [least, most] whose worst case
// is at most budget.
func (l *ledger) plan(requestBytes, least, most, budget int64) (call, bool) {
	if l.worstCost(requestBytes, least) > budget {
		return call{}, false
	}
	out := least + int64(sort.Search(int(most-least), func(i int) bool {
		return l.worstCost(requestBytes, least+int64(i)+1) > budget
	}))
	return call{bytes: requestBytes, estimate: estimateTokens(requestBytes), out: out, cost: l.worstCost(requestBytes, out)}, true
}

// finalBytes is the largest final request, up to most bytes, that the money
// left pays for. It is negative when not even an empty one fits.
func (l *ledger) finalBytes(most int64) int64 {
	return int64(sort.Search(int(most)+1, func(i int) bool {
		return l.worstCost(int64(i), minFinalOutput) > l.left()
	})) - 1
}

// provisional is the run's usage and cost if c uses everything it may.
func (l *ledger) provisional(c call) (provider.Usage, int64) {
	return provider.Usage{InputTokens: addCapped(l.usedIn, c.estimate), OutputTokens: addCapped(l.usedOut, c.out)},
		addCapped(l.spentMicroUSD, c.cost)
}

// settle books a finished call. A figure the vendor did not report stays at
// its worst case: a reply is never free. A call that failed books the larger
// of the two, because what a broken stream reported may be only a part.
func (l *ledger) settle(c call, reported provider.Usage, failed bool) {
	in, costIn := reported.InputTokens, reported.InputTokens
	if in <= 0 || failed {
		in, costIn = max(in, c.estimate), max(costIn, c.bytes+framingTokens)
	}
	out := reported.OutputTokens
	if out <= 0 || failed {
		out = max(out, c.out)
	}
	l.usedIn = addCapped(l.usedIn, in)
	l.usedOut = addCapped(l.usedOut, out)
	l.spentMicroUSD = addCapped(l.spentMicroUSD, l.model.CostMicroUSD(provider.Usage{InputTokens: costIn, OutputTokens: out}))
}

func estimateTokens(bytes int64) int64 { return (bytes + bytesPerToken - 1) / bytesPerToken }

// addCapped adds two figures that are not negative and stays at the largest
// int64 instead of wrapping: a vendor may report anything.
func addCapped(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// frameBytes is the part of every request that is not messages.
func frameBytes(system string, tools []provider.ToolDef) int64 {
	encoded, _ := json.Marshal(tools)
	return int64(len(system) + len(encoded))
}

func messageBytes(msgs ...provider.Message) int64 {
	n := 0
	for _, m := range msgs {
		n += len(m.Text) + len(m.ToolName) + len(m.ToolCallID) + len(m.Opaque) + 8
		for _, c := range m.ToolCalls {
			args, _ := json.Marshal(c.Arguments)
			n += len(c.ID) + len(c.Name) + len(args) + len(c.ArgError) + 8
		}
	}
	return int64(n)
}
