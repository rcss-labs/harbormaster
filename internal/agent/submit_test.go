package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

// submitted runs an investigator that reads the service and then submits.
func submitted(t *testing.T, body string, list ...provider.ToolCall) (agentresult.Result, *recordingRunner) {
	t.Helper()
	p := fake.New(tool("c1", "get_service"), calls(list...))
	runner := replies(map[string]string{"get_service": body})
	return requireResult(t, investigate(t, newLoop(p, runner), p, investigatorInput())), runner
}

func TestInvestigator_ResultCarriesCodeDerivedExcerptAndLabel(t *testing.T) {
	long := strings.Repeat("ä", 2000)
	res, _ := submitted(t, `{"running":0,"reason":"`+long+`"}`, submitCall(plainSummary, cite("e2"), map[string]any{"ref": "e1", "note": "No task runs."}))
	if res.Summary != plainSummary || res.SummaryReplaced || res.EvidenceDropped != 0 || len(res.Evidence) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if want := (agentresult.Evidence{Tool: "get_service", ServiceID: checkoutID, Action: "view", Note: "No task runs.", Excerpt: "running: 0"}); res.Evidence[1] != want {
		t.Errorf("item = %+v, want %+v", res.Evidence[1], want)
	}
	if got, want := res.Evidence[0].Excerpt, agentresult.CutExcerpt(`reason: "`+long+`"`); got != want || len(got) > agentresult.ExcerptCap {
		t.Errorf("excerpt of %d bytes, want %d", len(got), len(want))
	}

	// No evidence is an empty list, never null.
	res, _ = submitted(t, serviceBody, provider.ToolCall{ID: "s", Name: "submit_result", Arguments: map[string]any{"summary": plainSummary}})
	if raw, _ := json.Marshal(res); !strings.Contains(string(raw), `"evidence":[]`) {
		t.Errorf("result = %s", raw)
	}
}

func TestInvestigator_UnknownAndInventedRefsAreDropped(t *testing.T) {
	bad := []any{"e999", "e0", "E1", "e1; drop table", float64(1), "e01", "[e1]", " e1", "", nil, map[string]any{"ref": "e1"}, "e" + strings.Repeat("1", 500)}
	var evidence []any
	for _, ref := range bad {
		evidence = append(evidence, cite(ref))
	}
	evidence = append(evidence, map[string]any{"note": plainNote}, cite("e1"), cite("e1"))
	res, _ := submitted(t, serviceBody, submitCall(plainSummary, evidence...))
	if len(res.Evidence) != 1 || res.EvidenceDropped != len(bad)+2 || res.Evidence[0].Excerpt != serviceUnits[0] {
		t.Errorf("evidence %+v, dropped %d, want %d", res.Evidence, res.EvidenceDropped, len(bad)+2)
	}
}

func TestInvestigator_ModelCannotSupplyExcerptOrLabel(t *testing.T) {
	for _, extra := range []string{"excerpt", "action", "service_id", "tool"} {
		forged := cite("e1")
		forged[extra] = paymentsID
		p := fake.New(tool("c1", "get_service"), calls(submitCall(plainSummary, forged)), calls(submitCall(plainSummary, cite("e1"))))
		events := investigate(t, newLoop(p, replies(map[string]string{"get_service": serviceBody})), p, investigatorInput())
		reqs := p.Requests()
		if len(reqs) != 3 {
			t.Fatalf("%s: %d calls, want 3: the forged submit is a tool error", extra, len(reqs))
		}
		if m := lastMessage(reqs[2]); m.Role != provider.RoleTool || !m.IsError {
			t.Errorf("%s: answered with %+v", extra, m)
		}
		res := requireResult(t, events)
		if len(res.Evidence) != 1 || res.Evidence[0].ServiceID != checkoutID || res.Evidence[0].Action != "view" || res.Evidence[0].Excerpt != serviceUnits[0] {
			t.Errorf("%s: %+v", extra, res)
		}
	}
}

func TestInvestigator_AtMostTwelveItems(t *testing.T) {
	var members []string
	var evidence []any
	for i := range 15 {
		members = append(members, fmt.Sprintf(`"m%d":%d`, i, i))
		evidence = append(evidence, cite(fmt.Sprintf("e%d", i+1)))
	}
	res, _ := submitted(t, "{"+strings.Join(members, ",")+"}", submitCall(plainSummary, evidence...))
	if len(res.Evidence) != 12 || res.EvidenceDropped != 3 || res.Evidence[11].Excerpt != "m11: 11" {
		t.Errorf("%d items, %d dropped", len(res.Evidence), res.EvidenceDropped)
	}
}

const copiedLine = "panic: could not open the migrations directory for reading"

func TestInvestigator_CopiedLogLineReplacesTheSummary(t *testing.T) {
	for _, summary := range []string{
		"The log says " + copiedLine,
		"The log says PANIC -- could not\nopen the 'migrations' directory",
	} {
		res, _ := submitted(t, `{"message":"`+copiedLine+`"}`, submitCall(summary, cite("e1")))
		if !res.SummaryReplaced || res.Summary != agentresult.ReplacementSummary || len(res.Evidence) != 1 {
			t.Errorf("%q: result = %+v", summary, res)
		}
	}
	// Text of a result that gives no units counts as read as well.
	p := fake.New(calls(overviewCall("c1")), calls(submitCall(copiedLine)))
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_overview": `{"note":"` + copiedLine + `"}`})), p, investigatorInput()))
	if !res.SummaryReplaced {
		t.Errorf("result = %+v", res)
	}
}

func TestInvestigator_CopiedTextInANoteReplacesTheNote(t *testing.T) {
	res, _ := submitted(t, `{"message":"`+copiedLine+`"}`, submitCall(plainSummary, map[string]any{"ref": "e1", "note": copiedLine}))
	if res.SummaryReplaced || len(res.Evidence) != 1 || res.Evidence[0].Note != agentresult.ReplacementNote {
		t.Errorf("result = %+v", res)
	}
}

func TestInvestigator_TooMuchReadTextReplacesTheSummary(t *testing.T) {
	p := fake.New(tool("c1", "get_service"), calls(submitCall(plainSummary)))
	runner := replies(map[string]string{"get_service": `{"a":"` + strings.Repeat("log line ok ", agentresult.CorpusCap/12+1) + `"}`})
	if res := requireResult(t, investigate(t, newLoop(p, runner), p, investigatorInput())); !res.SummaryReplaced {
		t.Errorf("result = %+v", res)
	}
}

func TestInvestigator_SubmitResultNeverReachesTheToolRunner(t *testing.T) {
	bad := provider.ToolCall{ID: "s1", Name: "submit_result", ArgError: "cut off"}
	p := fake.New(tool("c1", "get_service"), calls(bad), calls(submitCall(plainSummary)))
	runner := replies(map[string]string{"get_service": serviceBody})
	requireResult(t, investigate(t, newLoop(p, runner), p, investigatorInput()))
	for _, c := range runner.Calls() {
		if c.Name != "get_service" {
			t.Errorf("the tool runner was asked for %s", c.Name)
		}
	}
}

func TestInvestigator_OtherCallsBesideSubmitResultAreNotRun(t *testing.T) {
	_, runner := submitted(t, serviceBody, serviceCall("c2", "get_service_logs"), submitCall(plainSummary), serviceCall("c3", "get_service_logs"))
	if n := len(runner.Calls()); n != 1 {
		t.Errorf("%d tool calls, want only the one before the submit", n)
	}

	// Beside a refused submit they are not run either, and each call is answered.
	p := fake.New(
		calls(serviceCall("c2", "get_service_logs"), submitCall(nil), serviceCall("c3", "get_service_logs")),
		calls(submitCall(plainSummary)),
	)
	runner = replies(map[string]string{})
	requireResult(t, investigate(t, newLoop(p, runner), p, investigatorInput()))
	if n := len(runner.Calls()); n != 0 {
		t.Errorf("%d tool calls", n)
	}
	if text := resultText(t, requestAt(t, p, 1), "c2"); !strings.Contains(text, "was not run") {
		t.Errorf("c2 was answered with %q", text)
	}
	answered := 0
	for _, m := range requestAt(t, p, 1).Messages {
		if m.Role == provider.RoleTool {
			answered++
			if !m.IsError {
				t.Errorf("call %s beside a refused submit is not answered with an error", m.ToolCallID)
			}
		}
	}
	if answered != 3 {
		t.Errorf("%d calls were answered, want 3", answered)
	}
}

func TestInvestigator_NullEvidenceAndNullNoteCountAsAbsent(t *testing.T) {
	none := provider.ToolCall{ID: "submit", Name: "submit_result", Arguments: map[string]any{"summary": plainSummary, "evidence": nil}}
	if res, _ := submitted(t, serviceBody, none); res.Summary != plainSummary || len(res.Evidence) != 0 || res.EvidenceDropped != 0 {
		t.Errorf("null evidence: %+v", res)
	}
	res, _ := submitted(t, serviceBody, submitCall(plainSummary, map[string]any{"ref": "e1", "note": nil}, map[string]any{"ref": "e2"}))
	if len(res.Evidence) != 2 || res.Evidence[0].Note != agentresult.ReplacementNote || res.Evidence[1].Note != agentresult.ReplacementNote {
		t.Errorf("null note: %+v", res)
	}
}

func TestInvestigator_ExcerptIsTheStoredTextNotTheLine(t *testing.T) {
	res, _ := submitted(t, `{"message":"one`+"\u2028"+`two"}`, submitCall(plainSummary, cite("e1")))
	if len(res.Evidence) != 1 || res.Evidence[0].Excerpt != "message: \"one\u2028two\"" {
		t.Errorf("evidence = %+v", res.Evidence)
	}
}

func TestInvestigator_MalformedSubmitIsAToolErrorAndCountsAStep(t *testing.T) {
	args := func(m map[string]any) provider.ToolCall {
		return provider.ToolCall{ID: "submit", Name: "submit_result", Arguments: m}
	}
	for name, bad := range map[string]provider.ToolCall{
		"arguments not an object": {ID: "submit", Name: "submit_result", ArgError: "arguments were not a JSON object"},
		"no arguments":            args(nil),
		"no summary":              args(map[string]any{"evidence": []any{}}),
		"blank summary":           args(map[string]any{"summary": " \n"}),
		"summary is a number":     args(map[string]any{"summary": float64(3)}),
		"unknown member":          args(map[string]any{"summary": plainSummary, "title": "x"}),
		"evidence is a string":    args(map[string]any{"summary": plainSummary, "evidence": "e1"}),
		"entry is a string":       args(map[string]any{"summary": plainSummary, "evidence": []any{"e1"}}),
		"note is a number":        args(map[string]any{"summary": plainSummary, "evidence": []any{map[string]any{"ref": "e1", "note": float64(1)}}}),
		"extra member past the cap": args(map[string]any{"summary": plainSummary, "evidence": func() []any {
			list := make([]any, 13)
			for i := range list {
				list[i] = cite("e1")
			}
			list[12] = map[string]any{"ref": "e1", "note": "n", "excerpt": "x"}
			return list
		}()}),
	} {
		t.Run(name, func(t *testing.T) {
			p := fake.New(calls(bad), calls(submitCall(plainSummary)))
			events := investigate(t, newLoop(p, &recordingRunner{}), p, investigatorInput())
			requireResult(t, events)
			reqs := p.Requests()
			if len(reqs) != 2 {
				t.Fatalf("%d calls, want 2", len(reqs))
			}
			if m := lastMessage(reqs[1]); m.Role != provider.RoleTool || !m.IsError || m.ToolCallID != "submit" || !strings.Contains(m.Text, "submit_result") {
				t.Errorf("answered with %+v", m)
			}
		})
	}
	// Three bad ones use up three steps; the last is the final call.
	p := fake.New(calls(submitCall(nil)), calls(submitCall(nil)), calls(submitCall(nil)), calls(submitCall(plainSummary)))
	events := investigate(t, newLoop(p, &recordingRunner{}, func(l *Limits) { l.MaxSteps = 3 }), p, investigatorInput())
	requireNoResult(t, events)
	if len(p.Requests()) != 3 || countNotices(events, "limit_reached") != 1 {
		t.Errorf("%d calls, events %v", len(p.Requests()), eventTypes(events))
	}
}

func TestSubmission_LargeInputIsLinear(t *testing.T) {
	evidence := make([]any, 0, 20_000)
	for i := range 20_000 {
		evidence = append(evidence, map[string]any{"ref": fmt.Sprintf("e%d", i), "note": strings.Repeat("n ", 20)})
	}
	evidence[0].(map[string]any)["note"] = strings.Repeat("long note ", 30_000)
	units := map[string]citedUnit{}
	for i := range 20_000 {
		units[fmt.Sprintf("e%d", i+1)] = citedUnit{tool: "get_service", serviceID: checkoutID, action: "view", excerpt: "x"}
	}
	corpus := &agentresult.Corpus{}
	corpus.Add(strings.Repeat("read text ", 100_000))
	started := time.Now()
	sub, problem := parseSubmission(submitCall(strings.Repeat("summary ", 30_000), evidence...))
	if problem != "" {
		t.Fatal(problem)
	}
	res := buildResult(sub, units, corpus)
	if len(res.Evidence) != 12 || res.EvidenceDropped != 20_000-12 || !res.SummaryReplaced {
		t.Errorf("%d items, %d dropped, replaced %v", len(res.Evidence), res.EvidenceDropped, res.SummaryReplaced)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("took %s", took)
	}
}
