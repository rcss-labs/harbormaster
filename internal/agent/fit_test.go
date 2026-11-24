package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
)

var fitTools = []provider.ToolDef{{Name: "get_overview", Description: "Overview.", InputSchema: map[string]any{"type": "object"}}}

const fitSystem = "You are read-only."

func fitBytes(msgs []provider.Message) int64 {
	return wireBytes(provider.Request{System: fitSystem, Tools: fitTools, Messages: msgs})
}

func userEntry(text string) entry {
	return entry{msg: provider.Message{Role: provider.RoleUser, Text: text}}
}

// fitRound is an assistant turn with one call per result text, and the results.
func fitRound(id string, opaque int, results ...string) []entry {
	turn := provider.Message{Role: provider.RoleAssistant, Text: "Looking."}
	if opaque > 0 {
		turn.Opaque = json.RawMessage(`"` + strings.Repeat("o", opaque) + `"`)
	}
	entries := []entry{{}}
	for i, data := range results {
		callID := fmt.Sprintf("%s-%d", id, i)
		turn.ToolCalls = append(turn.ToolCalls, provider.ToolCall{ID: callID, Name: "get_overview", Arguments: map[string]any{"q": "<é>"}})
		res := &toolResult{tag: "tool_result_abc123", tool: "get_overview", data: data}
		entries = append(entries, entry{result: res, msg: provider.Message{
			Role: provider.RoleTool, ToolCallID: callID, ToolName: "get_overview", Text: res.render(len(data)),
		}})
	}
	entries[0].msg = turn
	return entries
}

func multibyte(rng *rand.Rand, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString([]string{"a", "é", "€", "😀", "\n"}[rng.Intn(5)])
	}
	return b.String()
}

// checkFit checks everything a fitted final call must hold, whatever its
// history, and returns the kinds of tool results in it.
func checkFit(t *testing.T, history []entry, budget int64) (whole, cut, left int) {
	t.Helper()
	before := fmt.Sprintf("%+v", history)
	msgs := fitFinal(fitSystem, fitTools, history, budget)
	if fmt.Sprintf("%+v", history) != before {
		t.Fatal("the history was changed")
	}
	question, all := -1, entryMessages(history)
	for i, m := range all {
		if m.Role == provider.RoleUser {
			question = i
		}
	}
	if msgs == nil {
		if fitBytes(all[question:question+1]) <= budget {
			t.Fatal("no request although the question fits")
		}
		return
	}
	size := fitBytes(msgs)
	if size > budget {
		t.Fatalf("%d bytes, over the budget of %d", size, budget)
	}
	requireVendorValid(t, msgs)
	if fitBytes(all) <= budget {
		if !reflect.DeepEqual(msgs, all) {
			t.Fatal("a history that fits was changed")
		}
	}

	// What is kept is a suffix of the older turns, the question, and a suffix
	// of the rounds. No result is cut while an older turn is kept.
	at := -1
	for i, m := range msgs {
		if m.Role == provider.RoleUser {
			at = i
		}
	}
	if at < 0 || !reflect.DeepEqual(msgs[at], all[question]) || !reflect.DeepEqual(msgs[:at], all[question-at:question]) && at > 0 {
		t.Fatal("the question or the turns before it were changed")
	}
	tail := all[len(all)-(len(msgs)-at-1):]
	var shares []int
	var largestWhole int
	for i, m := range msgs[at+1:] {
		original := tail[i]
		kept := m
		kept.Text = original.Text
		if !reflect.DeepEqual(kept, original) {
			t.Fatalf("message %d was changed beyond its text", i)
		}
		if m.Role != provider.RoleTool {
			if m.Text != original.Text {
				t.Fatal("an assistant turn was changed")
			}
			continue
		}
		const open = "<tool_result_abc123 tool=\"get_overview\">\n"
		const end = "\n</tool_result_abc123>"
		switch {
		case m.Text == original.Text:
			whole++
			largestWhole = max(largestWhole, len(m.Text)-len(open)-len(end))
		case m.Text == resultPlaceholder:
			left++
		default:
			cut++
			body, noteAt := strings.TrimSuffix(strings.TrimPrefix(m.Text, open), end), strings.LastIndex(m.Text, "\n[truncated: ")
			if noteAt < 0 || !strings.HasPrefix(m.Text, open) || !strings.HasSuffix(m.Text, " bytes were left out]"+end) {
				t.Fatalf("a cut result lost its envelope or note: %.60q", m.Text)
			}
			data := body[:strings.LastIndex(body, "\n[truncated: ")]
			if !strings.HasPrefix(original.Text, open+data) || len(data) < minShare-3 {
				t.Fatalf("a cut result keeps %d bytes, not a head of at least %d", len(data), minShare)
			}
			shares = append(shares, len(data))
		}
		if len(m.Text) > len(original.Text) {
			t.Fatal("a result grew")
		}
	}
	if cut+left > 0 && at > 0 {
		t.Fatal("a result was cut while an older turn was kept")
	}
	for _, s := range shares {
		if s < shares[0]-3 || s > shares[0]+3 {
			t.Fatalf("cut results keep %v bytes, want equal shares", shares)
		}
		if largestWhole > s+400 {
			t.Fatalf("a result of %d bytes stayed whole beside a share of %d", largestWhole, s)
		}
	}
	// Nothing worth sending is left unused.
	if unused := budget - size; cut > 0 && left == 0 && unused > int64(8*cut) || left > 0 && unused > int64(minShare+400+8*cut) {
		t.Fatalf("%d bytes unused with %d results cut and %d left out", unused, cut, left)
	}
	return whole, cut, left
}

func TestFitFinal_AlwaysFitsAndStaysValid(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var whole, cut, left, dropped int
	for n := 0; n < 600; n++ {
		var history []entry
		for i := rng.Intn(4); i > 0; i-- {
			history = append(history, userEntry(multibyte(rng, 1+rng.Intn(3000))),
				entry{msg: provider.Message{Role: provider.RoleAssistant, Text: multibyte(rng, 1+rng.Intn(3000))}})
		}
		history = append(history, userEntry(multibyte(rng, 1+rng.Intn(2000))))
		results := rng.Intn([]int{1, 10, 40, 301}[rng.Intn(4)])
		for results > 0 {
			in := min(results, 1+rng.Intn(12))
			results -= in
			var texts []string
			for i := 0; i < in; i++ {
				texts = append(texts, multibyte(rng, rng.Intn([]int{60, 700, 4000, 30_000}[rng.Intn(4)])))
			}
			history = append(history, fitRound(fmt.Sprint("r", len(history)), rng.Intn(2)*rng.Intn(2000), texts...)...)
		}
		full := fitBytes(entryMessages(history))
		budget := []int64{full, full - 1, full / 2, full / 5, 40_000, rng.Int63n(full + 1)}[rng.Intn(6)]
		w, c, l := checkFit(t, history, budget)
		whole, cut, left = whole+w, cut+c, left+l
		if msgs := fitFinal(fitSystem, fitTools, history, budget); msgs != nil && len(msgs) < len(history) {
			dropped++
		}
	}
	if whole == 0 || cut == 0 || left == 0 || dropped == 0 {
		t.Errorf("%d whole, %d cut, %d left out, %d with parts dropped: the cases do not cover all", whole, cut, left, dropped)
	}
}

func TestFitFinal_SharesTheRoomBetweenResults(t *testing.T) {
	base := func(entries ...[]entry) []entry {
		history := []entry{userEntry("old question"), {msg: provider.Message{Role: provider.RoleAssistant, Text: "old answer"}}, userEntry("question")}
		for _, e := range entries {
			history = append(history, e...)
		}
		return history
	}
	x := func(n int) string { return strings.Repeat("x", n) }

	t.Run("older turns go before results are cut", func(t *testing.T) {
		history := base(fitRound("a", 0, x(5000)))
		msgs := fitFinal(fitSystem, fitTools, history, fitBytes(entryMessages(history))-1)
		if len(msgs) != 3 || msgs[0].Text != "question" || msgs[2].Text != history[4].msg.Text {
			t.Errorf("%d messages, first %q", len(msgs), msgs[0].Text)
		}
	})
	t.Run("only the oldest turns that must go are dropped", func(t *testing.T) {
		history := append([]entry{{msg: provider.Message{Role: provider.RoleAssistant, Text: "a welcome"}}, userEntry("oldest question")}, base(fitRound("a", 0, x(5000)))...)
		msgs := fitFinal(fitSystem, fitTools, history, fitBytes(entryMessages(history))-30)
		if len(msgs) != 5 || msgs[0].Text != "old question" {
			t.Errorf("%d messages, first %q, want the turn before the question kept", len(msgs), msgs[0].Text)
		}
		msgs = fitFinal(fitSystem, fitTools, history, fitBytes(entryMessages(history)))
		if len(msgs) != 6 || msgs[0].Text != "oldest question" {
			t.Errorf("%d messages, first %q: a request must start with a user message", len(msgs), msgs[0].Text)
		}
		if msgs := fitFinal(fitSystem, fitTools, history[:1], 1<<20); msgs != nil {
			t.Error("a final call without a question")
		}
	})
	t.Run("the room is used to the last byte", func(t *testing.T) {
		msgs := fitFinal(fitSystem, fitTools, base(fitRound("a", 0, x(50_000))), 9000)
		if got := fitBytes(msgs); got != 9000 {
			t.Errorf("%d of 9000 bytes used", got)
		}
	})
	t.Run("small results stay whole and large ones get equal shares", func(t *testing.T) {
		history := base(fitRound("a", 0, x(700), x(50_000)), fitRound("b", 0, x(90_000)))
		if w, c, l := checkFit(t, history, 30_000); w != 1 || c != 2 || l != 0 {
			t.Errorf("%d whole, %d cut, %d left out, want 1, 2, 0", w, c, l)
		}
	})
	t.Run("the newest keep something when there is not enough for all", func(t *testing.T) {
		var texts []string
		for i := 0; i < 40; i++ {
			texts = append(texts, x(5000))
		}
		history := base(fitRound("a", 0, texts...), fitRound("b", 0, texts[:2]...))
		msgs := fitFinal(fitSystem, fitTools, history, 20_000)
		if _, c, l := checkFit(t, history, 20_000); c == 0 || l == 0 || c+l != 42 {
			t.Fatalf("%d cut, %d left out, want some of each and no round dropped", c, l)
		}
		seenCut := false
		for _, m := range msgs {
			if m.Role != provider.RoleTool {
				continue
			}
			if m.Text != resultPlaceholder {
				seenCut = true
			} else if seenCut {
				t.Fatal("a newer result was left out while an older one was shown")
			}
		}
	})
	t.Run("rounds are dropped only when the newest results would have no room", func(t *testing.T) {
		var rounds [][]entry
		for i := 0; i < 30; i++ {
			rounds = append(rounds, fitRound(fmt.Sprint("r", i), 0, x(9000), x(9000)))
		}
		history := base(rounds...)
		var placeholders int64
		for _, r := range splitRounds(history[3:]) {
			placeholders += r.least
		}
		exact := fitBytes([]provider.Message{{Role: provider.RoleUser, Text: "question"}}) + placeholders + 4096
		if msgs := fitFinal(fitSystem, fitTools, history, exact); len(msgs) != 1+30*3 {
			t.Errorf("%d messages, want every round kept", len(msgs))
		}
		if msgs := fitFinal(fitSystem, fitTools, history, exact-1); len(msgs) != 1+29*3 || msgs[1].ToolCalls[0].ID != "r1-0" {
			t.Errorf("%d messages, want the oldest round dropped", len(msgs))
		}
	})
	t.Run("the last round stays while its placeholders fit", func(t *testing.T) {
		history := base(fitRound("a", 0, x(5000), x(5000)))
		least := fitBytes([]provider.Message{{Role: provider.RoleUser, Text: "question"}}) + splitRounds(history[3:])[0].least
		if msgs := fitFinal(fitSystem, fitTools, history, least); len(msgs) != 4 {
			t.Errorf("%d messages, want the round kept", len(msgs))
		}
		if msgs := fitFinal(fitSystem, fitTools, history, least-1); len(msgs) != 1 {
			t.Errorf("%d messages, want the question alone", len(msgs))
		}
	})
	t.Run("no round is dropped when every result gets a head", func(t *testing.T) {
		history := base(fitRound("a", 0, x(5000)), fitRound("b", 0, x(5000)))
		heads := fitBytes([]provider.Message{{Role: provider.RoleUser, Text: "question"}})
		for _, r := range splitRounds(history[3:]) {
			heads += r.heads
		}
		if _, c, l := checkFit(t, history, heads); c != 2 || l != 0 {
			t.Errorf("%d cut, %d left out, want both cut", c, l)
		}
		three := base(fitRound("z", 0, x(5000)), fitRound("a", 0, x(5000)), fitRound("b", 0, x(5000)))
		if msgs := fitFinal(fitSystem, fitTools, three, heads); len(msgs) != 5 {
			t.Errorf("%d messages, want the two newest of three rounds", len(msgs))
		}
		if msgs := fitFinal(fitSystem, fitTools, history, heads-1); len(msgs) != 3 {
			t.Errorf("%d messages, want the newest round", len(msgs))
		}
	})
	t.Run("a result cut to the cap still says how much there was", func(t *testing.T) {
		round := fitRound("a", 0, x(5000))
		round[1].result.omitted, round[1].result.readCap = 1000, true
		round[1].msg.Text = round[1].result.render(5000)
		msgs := fitFinal(fitSystem, fitTools, base(round), 3000)
		if text := msgs[2].Text; !strings.Contains(text, "of at least 6000 bytes were left out; the answer was larger than could be read]") {
			t.Errorf("cut result ends %q", text[max(0, len(text)-150):])
		}
	})
	t.Run("the question alone must fit", func(t *testing.T) {
		history := base(fitRound("a", 0, x(5000)))
		alone := fitBytes([]provider.Message{{Role: provider.RoleUser, Text: "question"}})
		if msgs := fitFinal(fitSystem, fitTools, history, alone); len(msgs) != 1 || msgs[0].Text != "question" {
			t.Errorf("%d messages, want the question alone", len(msgs))
		}
		if msgs := fitFinal(fitSystem, fitTools, history, alone-1); msgs != nil {
			t.Errorf("%d messages that cannot fit", len(msgs))
		}
	})
}

func TestToolResult_RenderSizeIsTheSizeOfWhatIsRendered(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for n := 0; n < 500; n++ {
		res := &toolResult{tag: "tool_result_abc", tool: "whoami", data: multibyte(rng, rng.Intn(300)), omitted: rng.Intn(2) * rng.Intn(99), readCap: rng.Intn(2) == 0}
		at := rng.Intn(320)
		if text := res.render(at); len(text) != res.renderSize(at) || !utf8.ValidString(text) {
			t.Fatalf("renderSize(%d) = %d, rendered %d bytes, valid %v", at, res.renderSize(at), len(text), utf8.ValidString(text))
		}
	}
}

func TestToolResult_NotesSayWhatWasLeftOut(t *testing.T) {
	const tooLarge = "; the answer was larger than could be read]"
	for _, tc := range []struct {
		omitted, shown int
		readCap        bool
		want           string
	}{
		{0, 10, false, ""},
		{0, 10, true, answerTooLargeNote},
		{0, 9, false, "\n[truncated: 1 of 10 bytes were left out]"},
		{0, 9, true, "\n[truncated: at least 1 of at least 10 bytes were left out" + tooLarge},
		{5, 10, false, "\n[truncated: 5 of 15 bytes were left out]"},
		{5, 4, true, "\n[truncated: at least 11 of at least 15 bytes were left out" + tooLarge},
	} {
		res := &toolResult{tag: "tag", tool: "whoami", data: "0123456789", omitted: tc.omitted, readCap: tc.readCap}
		if got, want := res.render(tc.shown), "<tag tool=\"whoami\">\n"+res.data[:tc.shown]+tc.want+"\n</tag>"; got != want {
			t.Errorf("render = %q, want %q", got, want)
		}
	}
}

func TestLedger_GatheringAlwaysLeavesTheReserve(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	admitted := 0
	for n := 0; n < 5000; n++ {
		m := testModel
		m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok = 1+rng.Int63n(20_000_000), 1+rng.Int63n(80_000_000)
		l := ledger{model: m, ceiling: rng.Int63n(6_000_000), maxTokens: 200_000,
			spentMicroUSD: rng.Int63n(1_000_000), usedIn: rng.Int63n(150_000), usedOut: rng.Int63n(50_000)}
		l.keepForAnswer(33_000 + rng.Int63n(9_000))
		bytes := 33_000 + rng.Int63n(300_000)
		c, ok := l.admitGathering(bytes)
		affordable := func(out int64) bool {
			return l.spentMicroUSD+worstCostOf(m, bytes, out)+l.reserve <= l.ceiling
		}
		tokens := l.usedIn + l.usedOut + (bytes+2)/3
		if want := affordable(1024) && tokens <= 200_000; ok != want {
			t.Fatalf("admitted %v, want %v: %+v, %d bytes", ok, want, l, bytes)
		}
		if !ok {
			continue
		}
		admitted++
		if l.ceiling-l.spentMicroUSD-worstCostOf(m, bytes, c.out) < l.reserve {
			t.Fatalf("a call with %d output tokens eats into the reserve: %+v", c.out, l)
		}
		most := min(8192, max(1024, 200_000-tokens))
		if c.out < 1024 || c.out > most || (c.out < most && affordable(c.out+1)) {
			t.Fatalf("%d output tokens, want the most that fits up to %d", c.out, most)
		}
		if c.cost != worstCostOf(m, bytes, c.out) || c.estimate != (bytes+2)/3 || c.bytes != bytes {
			t.Fatalf("call = %+v", c)
		}
	}
	if admitted < 500 {
		t.Fatalf("only %d admitted: the cases prove little", admitted)
	}
}

func TestLedger_FinalIsBoundByMoneyOnly(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	for n := 0; n < 2000; n++ {
		m := testModel
		m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok = 1+rng.Int63n(20_000_000), 1+rng.Int63n(80_000_000)
		l := ledger{model: m, ceiling: rng.Int63n(3_000_000), spentMicroUSD: rng.Int63n(1_000_000), usedIn: math.MaxInt64, usedOut: 5}
		left := l.ceiling - l.spentMicroUSD
		bytes := rng.Int63n(200_000)
		c, ok := l.admitFinal(bytes, left)
		if want := worstCostOf(m, bytes, 2048) <= left; ok != want {
			t.Fatalf("admitted %v, want %v", ok, want)
		}
		if ok && (c.out < 2048 || c.out > 4096 || c.cost > left || (c.out < 4096 && worstCostOf(m, bytes, c.out+1) <= left)) {
			t.Fatalf("call = %+v with %d left", c, left)
		}
		most := rng.Int63n(200_000)
		got := l.finalBytes(most, left)
		if got > most || (got >= 0 && worstCostOf(m, got, 2048) > left) || (got < most && worstCostOf(m, got+1, 2048) <= left) {
			t.Fatalf("finalBytes(%d) = %d with %d left", most, got, left)
		}
	}
}

func TestLedger_FirstFinalAttemptLeavesTheReserveForARetry(t *testing.T) {
	for _, tc := range []struct {
		left  int64
		retry bool
		want  int64
	}{
		{1500, false, 1000}, {1000, false, 500}, {999, false, 999}, {500, false, 500}, {0, false, 0},
		{1500, true, 1500}, {500, true, 500},
	} {
		l := ledger{model: testModel, ceiling: tc.left + 70, spentMicroUSD: 70, reserve: 500}
		if got := l.finalMoney(tc.retry); got != tc.want {
			t.Errorf("finalMoney(%v) with %d left = %d, want %d", tc.retry, tc.left, got, tc.want)
		}
	}
}

func TestLedger_SettleBooksWhatWasReported(t *testing.T) {
	c := call{bytes: 3000, estimate: 1000, out: 500}
	price := func(in, out int64) int64 {
		return testModel.CostMicroUSD(provider.Usage{InputTokens: in, OutputTokens: out})
	}
	for _, tc := range []struct {
		name             string
		reported         provider.Usage
		failed           bool
		in, out, costsIn int64
	}{
		{"reported", provider.Usage{InputTokens: 700, OutputTokens: 20}, false, 700, 20, 700},
		{"nothing reported", provider.Usage{}, false, 1000, 500, 4000},
		{"negative", provider.Usage{InputTokens: -5, OutputTokens: -5}, false, 1000, 500, 4000},
		{"smallest report", provider.Usage{InputTokens: 1, OutputTokens: 1}, false, 1, 1, 1},
		{"failed with a partial report", provider.Usage{InputTokens: 700, OutputTokens: 20}, true, 1000, 500, 4000},
		{"failed with a larger report", provider.Usage{InputTokens: 9000, OutputTokens: 600}, true, 9000, 600, 9000},
		{"failed between estimate and worst case", provider.Usage{InputTokens: 2000}, true, 2000, 500, 4000},
	} {
		l := ledger{model: testModel, usedIn: 1, usedOut: 2, spentMicroUSD: 3}
		l.settle(c, tc.reported, tc.failed)
		if l.usedIn != 1+tc.in || l.usedOut != 2+tc.out || l.spentMicroUSD != 3+price(tc.costsIn, tc.out) {
			t.Errorf("%s: %d in, %d out, %d µ$, want %d, %d, %d", tc.name, l.usedIn, l.usedOut, l.spentMicroUSD, 1+tc.in, 2+tc.out, 3+price(tc.costsIn, tc.out))
		}
	}
}

func TestLedger_AbsurdUsageNeverWrapsAround(t *testing.T) {
	l := ledger{model: testModel, ceiling: 1 << 50, maxTokens: 1 << 50}
	for i := 0; i < 3; i++ {
		l.settle(call{}, provider.Usage{InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64}, false)
		if l.usedIn != math.MaxInt64 || l.usedOut != math.MaxInt64 || l.spentMicroUSD != math.MaxInt64 {
			t.Fatalf("after %d calls: %+v", i+1, l)
		}
		if _, ok := l.admitGathering(1); ok {
			t.Fatal("a gathering call was admitted")
		}
		if _, ok := l.admitFinal(1, l.finalMoney(true)); ok {
			t.Fatal("a final call was admitted")
		}
	}
	usage, cost := l.provisional(call{estimate: 1, out: 1, cost: 1})
	if usage.InputTokens != math.MaxInt64 || usage.OutputTokens != math.MaxInt64 || cost != math.MaxInt64 {
		t.Errorf("provisional figures = %+v, %d", usage, cost)
	}
	// Overspent is nothing left, not a little.
	cheap := ledger{model: provider.Model{InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1}, ceiling: 5, spentMicroUSD: 9}
	if _, ok := cheap.admitFinal(1, cheap.finalMoney(false)); ok {
		t.Error("a call was admitted with nothing left")
	}
	// Tokens alone: spending is fine, the allowance is gone.
	l = ledger{model: testModel, ceiling: 1 << 50, maxTokens: 100, usedIn: math.MaxInt64, usedOut: math.MaxInt64}
	if _, ok := l.admitGathering(1); ok {
		t.Error("a gathering call was admitted with the allowance long gone")
	}
}
