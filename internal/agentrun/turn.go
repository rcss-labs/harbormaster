package agentrun

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// Reason is why a turn ended.
//
// The first reason to be known wins and later ones change nothing: an answer that was complete stays complete when
// the server starts to shut down a moment later, and a turn cut short by the shutdown does not become complete
// because the agent says so afterwards. When several are known at the same moment (passed to End together) the one
// listed first here wins.
type Reason int

const (
	Streaming   Reason = iota // not ended
	Done                      // the agent finished its answer
	AgentFailed               // the agent reported an error, refused the run, or its stream broke
	TimedOut                  // the run took longer than it may
	BrowserGone               // the user stopped it, left, or stopped reading
	Draining                  // this server is shutting down
	RunLost                   // the run was swept or settled elsewhere, or its usage cannot be recorded
	Panicked                  // our own code failed
)

// Usage is a run's cumulative figures.
type Usage struct{ InputTokens, OutputTokens, CostMicroUSD int64 }

// EffectKind says what an Effect asks for.
type EffectKind int

const (
	SendText EffectKind = iota + 1
	SendToolStart
	SendToolEnd
	SendNotice
	RecordUsage // remember the figures and store them now
	KeepUsage   // remember the figures; they are stored when the run is settled
	Cancel      // stop the agent run
)

// Effect is one thing the owner of a Turn must do.
type Effect struct {
	Kind               EffectKind
	Text, Tool, Notice string
	OK                 bool
	Usage              Usage
}

const (
	// MaxAnswerBytes caps the answer kept to be saved; AnswerCutMarker is added once when it is passed.
	MaxAnswerBytes  = 256 << 10
	AnswerCutMarker = "\n\n[The answer was cut short because it grew too long.]"

	// unknownTool stands for any tool name outside the shared table, which is also what the agent itself sends when
	// a model asks for a tool that does not exist. It is shown, saved and audited like any other; it ends nothing.
	unknownTool = "unknown_tool"
)

// toolNames is the set of tools the agent may call: the shared table.
var toolNames = func() map[string]bool {
	names := map[string]bool{}
	for _, tool := range hmtools.Tools() {
		names[tool.Name] = true
	}
	return names
}()

// Turn is what an agent's events and the reasons to stop add up to. It does no I/O: Apply, End and Fail return what
// the owner must do. How the turn ends and how long the stream is read are separate matters: after the end nothing
// is relayed or added to the answer, but usage counts until the stream closes. Not safe for concurrent use.
type Turn struct {
	reason        Reason
	code, message string
	answer        []byte
	cut           bool
	tools         []string
}

// Apply takes the agent's next event.
func (t *Turn) Apply(ev agentclient.Event) []Effect {
	if ev.Type == "usage" {
		kind := RecordUsage
		if t.Ended() {
			kind = KeepUsage
		}
		return []Effect{{Kind: kind, Usage: Usage{ev.InputTokens, ev.OutputTokens, ev.CostMicroUSD}}}
	}
	if t.Ended() {
		return nil
	}
	switch ev.Type {
	case "text":
		delta := CleanText(ev.Delta)
		if delta == "" {
			return nil
		}
		t.keep(delta)
		return []Effect{{Kind: SendText, Text: delta}}
	case "tool_start":
		name := toolName(ev.Tool)
		t.tools = append(t.tools, name)
		return []Effect{{Kind: SendToolStart, Tool: name}}
	case "tool_end":
		return []Effect{{Kind: SendToolEnd, Tool: toolName(ev.Tool), OK: ev.OK}}
	case "notice":
		if ev.Notice == "context_trimmed" || ev.Notice == "limit_reached" {
			return []Effect{{Kind: SendNotice, Notice: ev.Notice}}
		}
	case "done":
		return t.End(Done)
	case "error":
		return t.Fail(ev.Code, ev.Message)
	}
	return nil
}

// End ends the turn for the strongest of the reasons, unless it has ended already.
func (t *Turn) End(reasons ...Reason) []Effect {
	if t.Ended() || len(reasons) == 0 {
		return nil
	}
	t.reason = slices.Min(reasons)
	return []Effect{{Kind: Cancel}}
}

// Fail ends the turn as AgentFailed, with the agent's code and message as they came.
func (t *Turn) Fail(code, message string) []Effect {
	if !t.Ended() {
		t.code, t.message = code, message
	}
	return t.End(AgentFailed)
}

// Ended reports whether the turn has ended.
func (t *Turn) Ended() bool { return t.reason != Streaming }

// Reason is why the turn ended, or Streaming.
func (t *Turn) Reason() Reason { return t.reason }

// Failure is the agent's code and message when the turn ended as AgentFailed.
func (t *Turn) Failure() (code, message string) { return t.code, t.message }

// Answer is what was said up to the end.
func (t *Turn) Answer() string { return string(t.answer) }

// Tools are the names of the tools started up to the end, in order.
func (t *Turn) Tools() []string { return t.tools }

// keep adds delta to the answer until the cap, then adds the marker once and stops.
func (t *Turn) keep(delta string) {
	if t.cut {
		return
	}
	if len(t.answer)+len(delta) > MaxAnswerBytes {
		t.cut = true
		delta = AnswerCutMarker
	}
	t.answer = append(t.answer, delta...)
}

func toolName(name string) string {
	if toolNames[name] {
		return name
	}
	return unknownTool
}

// CleanText makes text from the agent safe to store and relay: valid UTF-8 and no control characters but newline
// and tab (Postgres refuses NUL, and the rest would only confuse a log or a terminal).
func CleanText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
