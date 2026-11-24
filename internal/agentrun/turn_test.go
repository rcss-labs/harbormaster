package agentrun

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
)

// endOrder is the documented order, strongest first.
var endOrder = []Reason{Done, AgentFailed, TimedOut, BrowserGone, Draining, RunLost, Panicked}

type eventCase struct {
	name      string
	ev        agentclient.Event
	effects   []Effect // while streaming
	reason    Reason   // after it, from streaming
	answer    string
	tools     []string
	code, msg string
}

var eventCases = []eventCase{
	{name: "text", ev: agentclient.Event{Type: "text", Delta: "hi"}, effects: []Effect{{Kind: SendText, Text: "hi"}}, answer: "hi"},
	{name: "text of control characters", ev: agentclient.Event{Type: "text", Delta: "a\x00b\x01\tc\n\xff\xfe"},
		effects: []Effect{{Kind: SendText, Text: "ab\tc\n�"}}, answer: "ab\tc\n�"},
	{name: "empty text", ev: agentclient.Event{Type: "text", Delta: "\x00"}},
	{name: "known tool start", ev: agentclient.Event{Type: "tool_start", Tool: "get_service"},
		effects: []Effect{{Kind: SendToolStart, Tool: "get_service"}}, tools: []string{"get_service"}},
	{name: "known tool end", ev: agentclient.Event{Type: "tool_end", Tool: "get_service", OK: true},
		effects: []Effect{{Kind: SendToolEnd, Tool: "get_service", OK: true}}},
	{name: "unknown_tool start", ev: agentclient.Event{Type: "tool_start", Tool: "unknown_tool"},
		effects: []Effect{{Kind: SendToolStart, Tool: "unknown_tool"}}, tools: []string{"unknown_tool"}},
	{name: "unknown_tool end", ev: agentclient.Event{Type: "tool_end", Tool: "unknown_tool"},
		effects: []Effect{{Kind: SendToolEnd, Tool: "unknown_tool"}}},
	{name: "unheard-of tool start", ev: agentclient.Event{Type: "tool_start", Tool: "<script>"},
		effects: []Effect{{Kind: SendToolStart, Tool: "unknown_tool"}}, tools: []string{"unknown_tool"}},
	{name: "unheard-of tool end", ev: agentclient.Event{Type: "tool_end", Tool: "", OK: true},
		effects: []Effect{{Kind: SendToolEnd, Tool: "unknown_tool", OK: true}}},
	{name: "context notice", ev: agentclient.Event{Type: "notice", Notice: "context_trimmed"}, effects: []Effect{{Kind: SendNotice, Notice: "context_trimmed"}}},
	{name: "limit notice", ev: agentclient.Event{Type: "notice", Notice: "limit_reached"}, effects: []Effect{{Kind: SendNotice, Notice: "limit_reached"}}},
	{name: "made-up notice", ev: agentclient.Event{Type: "notice", Notice: "made_up"}},
	{name: "usage", ev: agentclient.Event{Type: "usage", InputTokens: 1, OutputTokens: 2, CostMicroUSD: 3},
		effects: []Effect{{Kind: RecordUsage, Usage: Usage{1, 2, 3}}}},
	{name: "done", ev: agentclient.Event{Type: "done"}, effects: []Effect{{Kind: Cancel}}, reason: Done},
	{name: "error", ev: agentclient.Event{Type: "error", Code: "provider_error", Message: "m"}, effects: []Effect{{Kind: Cancel}},
		reason: AgentFailed, code: "provider_error", msg: "m"},
	{name: "unknown type", ev: agentclient.Event{Type: "shout", Delta: "x", Tool: "get_service"}},
}

func sameEffects(a, b []Effect) bool { return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b) }

func TestTurn_EveryEventInEveryState(t *testing.T) {
	for _, tc := range eventCases {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			var turn Turn
			got := turn.Apply(tc.ev)
			if !sameEffects(got, tc.effects) {
				t.Errorf("effects = %+v, want %+v", got, tc.effects)
			}
			code, msg := turn.Failure()
			if turn.Reason() != tc.reason || turn.Answer() != tc.answer || !reflect.DeepEqual(turn.Tools(), tc.tools) || code != tc.code || msg != tc.msg {
				t.Errorf("turn = %v answer %q tools %v failure %q %q", turn.Reason(), turn.Answer(), turn.Tools(), code, msg)
			}
		})
		for _, ended := range endOrder {
			t.Run(fmt.Sprintf("ended by %v/%s", ended, tc.name), func(t *testing.T) {
				turn := endedBy(ended)
				var want []Effect // only usage still counts, and is kept rather than written at once
				if tc.ev.Type == "usage" {
					want = []Effect{{Kind: KeepUsage, Usage: Usage{1, 2, 3}}}
				}
				if got := turn.Apply(tc.ev); !sameEffects(got, want) {
					t.Errorf("effects = %+v, want %+v", got, want)
				}
				if turn.Reason() != ended || turn.Answer() != "before" || !reflect.DeepEqual(turn.Tools(), []string{"get_service"}) {
					t.Errorf("turn changed after its end: %v %q %v", turn.Reason(), turn.Answer(), turn.Tools())
				}
			})
		}
	}
}

// endedBy is a turn that said something, used a tool and then ended for reason.
func endedBy(reason Reason) *Turn {
	var turn Turn
	turn.Apply(agentclient.Event{Type: "text", Delta: "before"})
	turn.Apply(agentclient.Event{Type: "tool_start", Tool: "get_service"})
	switch reason {
	case Done:
		turn.Apply(agentclient.Event{Type: "done"})
	case AgentFailed:
		turn.Fail("first", "first message")
	default:
		turn.End(reason)
	}
	return &turn
}

func TestTurn_TheFirstReasonWins(t *testing.T) {
	for _, first := range endOrder {
		for _, then := range endOrder {
			turn := endedBy(first)
			var got []Effect
			if then == AgentFailed {
				got = turn.Fail("second", "second message")
			} else {
				got = turn.End(then)
			}
			code, _ := turn.Failure()
			if len(got) != 0 || turn.Reason() != first || code == "second" {
				t.Errorf("%v then %v: effects %v, reason %v, code %q", first, then, got, turn.Reason(), code)
			}
		}
	}
}

func TestTurn_ReasonsKnownTogetherFollowTheDocumentedOrder(t *testing.T) {
	for i, strong := range endOrder {
		for _, weak := range endOrder[i:] {
			for _, given := range [][]Reason{{strong, weak}, {weak, strong}} {
				var turn Turn
				if got := turn.End(given...); !sameEffects(got, []Effect{{Kind: Cancel}}) || turn.Reason() != strong {
					t.Errorf("End(%v): %v, reason %v, want %v", given, got, turn.Reason(), strong)
				}
			}
		}
	}
	var turn Turn
	if got := turn.End(); len(got) != 0 || turn.Reason() != Streaming {
		t.Errorf("End with no reason ended the turn: %v %v", got, turn.Reason())
	}
}

func TestTurn_AnswerIsCutOnceAtTheCapButStillRelayed(t *testing.T) {
	var turn Turn
	chunk := strings.Repeat("a", 1000)
	relayed := 0
	for range 2 * MaxAnswerBytes / 1000 {
		for _, e := range turn.Apply(agentclient.Event{Type: "text", Delta: chunk}) {
			relayed += len(e.Text)
		}
	}
	if len(turn.Answer()) > MaxAnswerBytes+len(AnswerCutMarker) || strings.Count(turn.Answer(), AnswerCutMarker) != 1 || !strings.HasSuffix(turn.Answer(), AnswerCutMarker) {
		t.Errorf("answer is %d bytes with %d markers", len(turn.Answer()), strings.Count(turn.Answer(), AnswerCutMarker))
	}
	if relayed != 2*MaxAnswerBytes/1000*1000 {
		t.Errorf("relayed %d bytes", relayed)
	}
}

// Whatever the order of events and reasons: the last usage is the one kept, the turn ends once with the reason that
// came first, cancels the run once, and relays nothing after that.
func TestTurn_RandomOrders(t *testing.T) {
	for seed := range uint64(3000) {
		rnd := rand.New(rand.NewPCG(seed, 11))
		var turn Turn
		var lastSent, lastSeen Usage
		var want Reason
		cancels, wantAnswer := 0, ""
		for step := range 40 {
			wasOver := want != Streaming
			var effects []Effect
			switch n := rnd.IntN(20); {
			case n < 2: // a reason to stop
				r := endOrder[2+rnd.IntN(len(endOrder)-2)]
				effects = turn.End(r)
				if !wasOver {
					want = r
				}
			case n == 2 && step > 5:
				effects = turn.Apply(agentclient.Event{Type: "done"})
				if !wasOver {
					want = Done
				}
			case n == 3 && step > 5:
				effects = turn.Apply(agentclient.Event{Type: "error", Code: "c"})
				if !wasOver {
					want = AgentFailed
				}
			case n < 10:
				lastSent = Usage{int64(step + 1), int64(seed), int64(step)}
				effects = turn.Apply(agentclient.Event{Type: "usage", InputTokens: lastSent.InputTokens, OutputTokens: lastSent.OutputTokens, CostMicroUSD: lastSent.CostMicroUSD})
			case n < 15:
				effects = turn.Apply(agentclient.Event{Type: "tool_start", Tool: "nobody_knows"})
			default:
				effects = turn.Apply(agentclient.Event{Type: "text", Delta: "x"})
				if !wasOver {
					wantAnswer += "x"
				}
			}
			for _, e := range effects {
				switch e.Kind {
				case Cancel:
					cancels++
				case RecordUsage, KeepUsage:
					lastSeen = e.Usage
				default:
					if wasOver {
						t.Fatalf("seed %d: %+v relayed after the end", seed, e)
					}
				}
			}
			if turn.Reason() != want {
				t.Fatalf("seed %d step %d: reason %v, want %v", seed, step, turn.Reason(), want)
			}
		}
		wantCancels := 0
		if want != Streaming {
			wantCancels = 1
		}
		if cancels != wantCancels || lastSeen != lastSent || turn.Answer() != wantAnswer {
			t.Fatalf("seed %d: %d cancels (want %d), usage %v (want %v), answer %q (want %q)", seed, cancels, wantCancels, lastSeen, lastSent, turn.Answer(), wantAnswer)
		}
	}
}
