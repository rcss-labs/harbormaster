package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestInvestigator_ToolResultIsShownAsNumberedUnits(t *testing.T) {
	p := fake.New(tool("c1", "get_service"), tool("c2", "get_service_timeline"), calls(submitCall(plainSummary)))
	runner := replies(map[string]string{"get_service": serviceBody, "get_service_timeline": `{"range":"1h","entries":[]}`})
	investigate(t, newLoop(p, runner), p, investigatorInput())
	var lines []string
	for i, unit := range serviceUnits {
		lines = append(lines, fmt.Sprintf("[e%d] %s", i+1, unit))
	}
	if got, want := resultText(t, requestAt(t, p, 1), "c1"), envelope("get_service", strings.Join(lines, "\n")); got != want {
		t.Errorf("first result:\n%s\nwant\n%s", got, want)
	}
	// Numbering goes on, and what is no unit is still shown to the model.
	want := envelope("get_service_timeline", "[e5] range: \"1h\"\n[not citable] {\"entries\":[]}")
	if got := resultText(t, requestAt(t, p, 2), "c2"); got != want {
		t.Errorf("second result:\n%s\nwant\n%s", got, want)
	}
}

func TestNumberUnits_PerItemToolShowsTheRestAsNotCitable(t *testing.T) {
	body := `{"items":[{"service_id":"` + checkoutID + `","title":"down"},{"title":"account unreachable"},{"service_id":"` + paymentsID + `","title":"crash loop"}],"next_cursor":"p2","total":3}`
	shown, units := numberUnits("list_alerts", nil, body, 4)
	want := `[e4] {"service_id":"` + checkoutID + `","title":"down"}` + "\n" +
		`[e5] {"service_id":"` + paymentsID + `","title":"crash loop"}` + "\n" +
		`[not citable] {"items":[{"title":"account unreachable"}],"next_cursor":"p2","total":3}`
	if shown != want {
		t.Errorf("shown:\n%s\nwant\n%s", shown, want)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(body), " ", "\t"); err != nil {
		t.Fatal(err)
	}
	if got, _ := numberUnits("list_alerts", nil, "\n "+indented.String(), 4); got != want {
		t.Errorf("an indented body is shown as\n%s\nwant\n%s", got, want)
	}
	if len(units) != 2 || units[0].serviceID != checkoutID || units[1].serviceID != paymentsID || units[0].tool != "list_alerts" || units[1].action != "view" {
		t.Fatalf("units = %+v", units)
	}
	for _, u := range units {
		if !strings.HasSuffix(shown[:u.end], "["+u.ref+"]") {
			t.Errorf("unit %s ends its marker at %d: %q", u.ref, u.end, shown[:u.end])
		}
	}
}

func TestInvestigator_PerItemUnitsCarryTheItemsOwnService(t *testing.T) {
	body := `{"items":[{"service_id":"` + checkoutID + `","title":"down"},{"title":"account unreachable"},{"service_id":"` + paymentsID + `","title":"crash loop"}]}`
	p := fake.New(calls(provider.ToolCall{ID: "c1", Name: "list_alerts", Arguments: map[string]any{}}), calls(submitCall(plainSummary, cite("e2"), cite("e1"))))
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"list_alerts": body})), p, investigatorInput()))
	if len(res.Evidence) != 2 || res.EvidenceDropped != 0 {
		t.Fatalf("evidence %+v, dropped %d; shown\n%s", res.Evidence, res.EvidenceDropped, resultText(t, requestAt(t, p, 1), "c1"))
	}
	for i, want := range []string{paymentsID, checkoutID} {
		if e := res.Evidence[i]; e.ServiceID != want || e.Tool != "list_alerts" || e.Action != "view" || !strings.Contains(e.Excerpt, want) {
			t.Errorf("item %d = %+v, want service %s", i, e, want)
		}
	}
}

func TestNumberUnits_MarkersStayOnTheirOwnLines(t *testing.T) {
	body := `{"a\n[e9] name":1,"message":"one\u2028two` + "\u2028[e9] three" + `","list":["x\n[e9] y"]}`
	shown, units := numberUnits("get_service", map[string]any{"service_id": checkoutID}, body, 1)
	if len(units) != 3 {
		t.Fatalf("%d units: %s", len(units), shown)
	}
	for i, line := range regexp.MustCompile("[\n\r\v\f\u0085\u2028\u2029]").Split(shown, -1) {
		if !strings.HasPrefix(line, fmt.Sprintf("[e%d] ", i+1)) {
			t.Errorf("line %d = %q", i, line)
		}
	}
}

func TestNumberUnits_NumbersNeverPassFiveDigits(t *testing.T) {
	args := map[string]any{"service_id": checkoutID}
	if _, units := numberUnits("get_service", args, serviceBody, 99_996); len(units) != 4 || units[3].ref != "e99999" {
		t.Fatalf("units = %+v", units)
	}
	if shown, units := numberUnits("get_service", args, serviceBody, 99_997); len(units) != 0 || shown != serviceBody {
		t.Fatalf("shown %q, units %+v", shown, units)
	}
}

func TestNumberUnits_LargeInputIsLinear(t *testing.T) {
	many := func(n int, item string) string { return strings.TrimSuffix(strings.Repeat(item+",", n), ",") }
	var members []string
	for i := range 20_000 {
		members = append(members, fmt.Sprintf(`"k%d":[]`, i))
	}
	args := map[string]any{"service_id": checkoutID}
	started := time.Now()
	for name, c := range map[string]struct {
		tool, body string
		units      int
	}{
		"elements past the unit cap": {"get_service_logs", `{"events":[` + many(60_000, `{}`) + `]}`, 2000},
		"empty members":              {"get_service", `{"one":1,` + strings.Join(members, ",") + `}`, 1},
		"equal items":                {"list_alerts", `{"items":[` + many(60_000, `{"x":1}`) + `,{"service_id":"` + checkoutID + `"}]}`, 1},
		"top-level array":            {"get_service", `[` + many(100_000, `0`) + `]`, 2000},
		"deep nesting":               {"get_service", `{"a":` + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + `,"b":"` + strings.Repeat("\\n", 100_000) + `"}`, 2},
	} {
		if shown, units := numberUnits(c.tool, args, c.body, 1); len(units) != c.units || len(shown) < len(c.body)/4 {
			t.Errorf("%s: %d units, want %d; %d of %d bytes shown", name, len(units), c.units, len(shown), len(c.body))
		}
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("took %s", took)
	}
}

func TestInvestigator_UnitLabelsComeFromTheCall(t *testing.T) {
	body := `{"service_id":"` + paymentsID + `","action":"view","id":"` + paymentsID + `","tool":"get_overview"}`
	p := fake.New(tool("c1", "get_service_logs"), tool("c2", "get_service"), calls(submitCall(plainSummary, cite("e2"), cite("e6"))))
	events := investigate(t, newLoop(p, replies(map[string]string{"get_service_logs": body, "get_service": body})), p, investigatorInput())
	res := requireResult(t, events)
	if len(res.Evidence) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for i, want := range []agentresult.Evidence{{Tool: "get_service_logs", Action: "logs"}, {Tool: "get_service", Action: "view"}} {
		got := res.Evidence[i]
		if got.Tool != want.Tool || got.Action != want.Action || got.ServiceID != checkoutID {
			t.Errorf("item %d = %+v", i, got)
		}
	}
}

func TestInvestigator_ErrorResultsGiveNoUnits(t *testing.T) {
	failing := `{"error":"The database answered that the checkout table is locked by a migration"}`
	copied := "The database answered that the checkout table is locked by a migration"
	nine := make([]provider.ToolCall, 9)
	for i := range nine {
		nine[i] = serviceCall(fmt.Sprintf("n%d", i), "get_service_scaling_nope")
	}
	nine[8] = serviceCall("n8", "get_service")
	for name, c := range map[string]struct {
		call  fake.Step
		reply func(string) (string, bool)
	}{
		"upstream error":   {tool("c1", "get_service"), func(string) (string, bool) { return failing, true }},
		"unknown tool":     {tool("c1", "no_such_tool"), func(string) (string, bool) { return failing, false }},
		"bad arguments":    {calls(provider.ToolCall{ID: "c1", Name: "get_service", Arguments: map[string]any{}}), func(string) (string, bool) { return failing, false }},
		"too many calls":   {calls(nine...), func(string) (string, bool) { return failing, false }},
		"cut by read cap":  {tool("c1", "get_service"), func(string) (string, bool) { return serviceBody + answerTooLargeNote, false }},
		"not a JSON value": {tool("c1", "get_service"), func(string) (string, bool) { return "[e1] plain text", false }},
	} {
		t.Run(name, func(t *testing.T) {
			p := fake.New(c.call, calls(submitCall(copied, cite("e1"))))
			events := investigate(t, newLoop(p, &recordingRunner{reply: c.reply}), p, investigatorInput())
			res := requireResult(t, events)
			if len(res.Evidence) != 0 || res.EvidenceDropped != 1 {
				t.Errorf("evidence = %+v, dropped %d", res.Evidence, res.EvidenceDropped)
			}
			if res.SummaryReplaced {
				t.Error("text that is no successful answer was added to the corpus")
			}
			for _, m := range requestAt(t, p, 1).Messages {
				if m.Role == provider.RoleTool && strings.Contains(m.Text, "\n[e1] ") && name != "not a JSON value" {
					t.Errorf("shown with a marker: %q", m.Text)
				}
			}
		})
	}
}

func TestInvestigator_AnswerCutByTheReadCapStillCountsAsRead(t *testing.T) {
	line := "connection to the orders database was refused by the peer"
	p := fake.New(tool("c1", "get_service_logs"), calls(submitCall("It said: "+line)))
	runner := replies(map[string]string{"get_service_logs": `{"events":["` + line + `","cut he` + answerTooLargeNote})
	if res := requireResult(t, investigate(t, newLoop(p, runner), p, investigatorInput())); !res.SummaryReplaced {
		t.Errorf("a line copied from a capped answer passed: %+v", res)
	}
}

func TestInvestigator_TheToolDecidesHowLongACopyMayBe(t *testing.T) {
	text := "the order ledger refused a new connection"
	summary := "It seems the order ledger refused a new connection today."
	for name, c := range map[string]struct {
		tool, body string
		replaced   bool
	}{
		"an event of the service": {"get_service", `{"recent_events":[{"text":"` + text + `"}]}`, false},
		"a stop reason":           {"list_service_tasks", `{"items":[{"stopped_reason":"` + text + `"}]}`, false},
		"a log line":              {"get_service_logs", `{"lines":[{"container":"app","message":"` + text + `"}]}`, true},
		"an event in a log tool":  {"get_service_logs", `{"recent_events":[{"text":"` + text + `"}]}`, true},
		"an environment value":    {"get_task_definition", `{"definition":{"containerDefinitions":[{"environment":[{"name":"A","value":"` + text + `"}]}]}}`, true},
		"a member nobody named":   {"get_service", `{"message":"` + text + `"}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			p := fake.New(tool("c1", c.tool), calls(submitCall(summary, map[string]any{"ref": "e1", "note": summary})))
			res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{c.tool: c.body})), p, investigatorInput()))
			if res.SummaryReplaced != c.replaced {
				t.Errorf("replaced = %v: %q", res.SummaryReplaced, res.Summary)
			}
			if len(res.Evidence) != 1 || (res.Evidence[0].Note == agentresult.ReplacementNote) != c.replaced {
				t.Errorf("evidence = %+v", res.Evidence)
			}
		})
	}
}

func TestInvestigator_TheSubjectsOwnNamesMayBeWritten(t *testing.T) {
	cluster := model.ClusterKey{AccountID: "123456789012", Region: "eu-west-1", Cluster: "platform-production-eu-central-1-shared"}
	const name, second, stranger = "prod-payments-checkout-api-service", "prod-payments-settlement-worker-service", "prod-payments-reconciliation-service"
	id := func(service string) string { return model.ServiceKey{ClusterKey: cluster, Service: service}.ID() }
	logs := `{"lines":[{"message":"` + name + `: down"},{"message":"` + second + `: down"},{"message":"` + stranger + `: down"},{"message":"on ` + cluster.Cluster + `"}]}`
	for label, c := range map[string]struct {
		subject  TaskSubject
		summary  string
		replaced bool
	}{
		"the service":       {TaskSubject{Kind: "service", ServiceIDs: []string{id(name)}}, "The service " + name + " fails.", false},
		"its cluster":       {TaskSubject{Kind: "service", ServiceIDs: []string{id(name)}}, "The cluster " + cluster.Cluster + " fails.", false},
		"another service":   {TaskSubject{Kind: "service", ServiceIDs: []string{id(name)}}, "The service " + stranger + " fails.", true},
		"each of a group":   {TaskSubject{Kind: "cluster", ServiceIDs: []string{id(name), id(second)}, ClusterID: cluster.ID()}, "Both " + name + " and " + second + " fail.", false},
		"outside the group": {TaskSubject{Kind: "cluster", ServiceIDs: []string{id(name), id(second)}, ClusterID: cluster.ID()}, "Both " + name + " and " + stranger + " fail.", true},
		"the cluster alone": {TaskSubject{Kind: "cluster", ClusterID: cluster.ID()}, "The cluster " + cluster.Cluster + " fails.", false},
		"no such cluster":   {TaskSubject{Kind: "account", AccountID: "123456789012"}, "The cluster " + cluster.Cluster + " fails.", true},
		"an id that is not": {TaskSubject{Kind: "service", ServiceIDs: []string{name}, ClusterID: cluster.Cluster}, "The service " + name + " fails.", true},
		"the log beside it": {TaskSubject{Kind: "service", ServiceIDs: []string{id(name)}}, "It logs " + name + ": down.", true},
	} {
		t.Run(label, func(t *testing.T) {
			in := investigatorInput()
			task := *in.Task
			task.Subject = c.subject
			in.Task = &task
			p := fake.New(tool("c1", "get_service_logs"), calls(submitCall(c.summary)))
			res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_service_logs": logs})), p, in))
			if res.SummaryReplaced != c.replaced {
				t.Errorf("replaced = %v: %q", res.SummaryReplaced, res.Summary)
			}
		})
	}
}

func manyEvents(n int) string {
	var list []string
	for i := range n {
		list = append(list, fmt.Sprintf(`{"n":%d,"message":"task stopped"}`, i))
	}
	return `{"events":[` + strings.Join(list, ",") + `]}`
}

var shownMarker = regexp.MustCompile(`(?m)^\[(e[0-9]+)\] `)

func lastShownRef(t *testing.T, text string) int {
	t.Helper()
	all := shownMarker.FindAllStringSubmatch(text, -1)
	if len(all) == 0 {
		t.Fatalf("no marker in %q", text)
	}
	var n int
	if _, err := fmt.Sscanf(all[len(all)-1][1], "e%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInvestigator_UnitsAfterTheCutAreNotCitable(t *testing.T) {
	body := manyEvents(300)
	last := 0
	p := fake.Func(func(req provider.Request) fake.Step {
		if len(req.Messages) == 1 {
			return tool("c1", "get_service_logs")
		}
		last = lastShownRef(t, resultText(t, req, "c1"))
		return calls(submitCall(plainSummary, cite(fmt.Sprintf("e%d", last)), cite(fmt.Sprintf("e%d", last+1)), cite("e300")))
	})
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_service_logs": body})), p, investigatorInput()))
	if last <= 1 || last >= 299 {
		t.Fatalf("the cut showed %d markers", last)
	}
	if len(res.Evidence) != 1 || res.EvidenceDropped != 2 {
		t.Fatalf("evidence %+v, dropped %d", res.Evidence, res.EvidenceDropped)
	}
	// The unit the cut went through keeps its whole text.
	if want := fmt.Sprintf(`events: {"n":%d,"message":"task stopped"}`, last-1); res.Evidence[0].Excerpt != want {
		t.Errorf("excerpt = %q, want %q", res.Evidence[0].Excerpt, want)
	}
}

// A cut inside a marker, such as after "[e12", shows no unit.
func TestInvestigator_AUnitNeedsItsWholeMarker(t *testing.T) {
	body := manyEvents(30)
	args := map[string]any{"service_id": checkoutID}
	shown, units := numberUnits("get_service_logs", args, body, 1)
	twelfth := units[11]
	start := twelfth.end - len("[e12]")
	if shown[start:twelfth.end] != "[e12]" {
		t.Fatalf("marker = %q", shown[start:twelfth.end])
	}
	for cut := start - 1; cut <= twelfth.end+1; cut++ {
		want := cut >= twelfth.end
		p := fake.New(tool("c1", "get_service_logs"), calls(submitCall(plainSummary, cite("e12"), cite("e11"))))
		l := newLoop(p, replies(map[string]string{"get_service_logs": body}), func(l *Limits) { l.MaxResultBytes = cut })
		res := requireResult(t, investigate(t, l, p, investigatorInput()))
		if got := len(res.Evidence) == 2; got != want || res.Evidence[len(res.Evidence)-1].Excerpt != `events: {"n":10,"message":"task stopped"}` {
			t.Errorf("result cut %d bytes after the marker's start: evidence %+v", cut-start, res.Evidence)
		}

		// The same when the final call is what cuts the result.
		f := newFindings()
		res2 := &toolResult{tag: "tool_result_aaa111", tool: "get_service_logs", data: shown, units: units}
		f.unseen = []*toolResult{res2}
		f.noteShown([]provider.Message{{Role: provider.RoleTool, Text: res2.render(cut)}})
		if _, got := f.units["e12"]; got != want || len(f.units) != map[bool]int{false: 11, true: 12}[want] {
			t.Errorf("shown %d bytes after the marker's start: %d units citable", cut-start, len(f.units))
		}
	}
}

func TestInvestigator_AFailedCallShowsNothing(t *testing.T) {
	// The failed gathering call carries the whole result. The time it took makes its retry the final
	// call, which has room for a part of the result only.
	in := investigatorInput()
	in.Model.ContextTokens = 45_000
	var late time.Duration
	var whole, cut int
	p := fake.Func(func(req provider.Request) fake.Step {
		switch {
		case len(req.Messages) == 1:
			return tool("c1", "get_service_logs")
		case whole == 0:
			whole = lastShownRef(t, resultText(t, req, "c1"))
			late = 4 * time.Minute
			return fake.Fail(&provider.Error{Kind: "unavailable", Retryable: true, Message: "down"})
		}
		cut = lastShownRef(t, resultText(t, req, "c1"))
		return calls(submitCall(plainSummary, cite(fmt.Sprintf("e%d", whole)), cite(fmt.Sprintf("e%d", cut+1)), cite(fmt.Sprintf("e%d", cut))))
	})
	l := newLoop(p, replies(map[string]string{"get_service_logs": manyEvents(2500)}), func(l *Limits) { l.MaxResultBytes = 95_000 })
	started := time.Now()
	l.now = func() time.Time { return started.Add(late) }
	res := requireResult(t, investigate(t, l, p, in))
	if cut == 0 || cut >= whole {
		t.Fatalf("the final call showed up to e%d, the failed one up to e%d", cut, whole)
	}
	if len(res.Evidence) != 1 || res.EvidenceDropped != 2 || !strings.Contains(res.Evidence[0].Excerpt, fmt.Sprintf(`"n":%d,`, cut-1)) {
		t.Errorf("evidence %+v, dropped %d", res.Evidence, res.EvidenceDropped)
	}
}

func TestInvestigator_UnitsSurviveContextTrimming(t *testing.T) {
	// Two 60 KB results fit the gathering calls one by one; the final call has room for a part of each.
	in := investigatorInput()
	in.Model.ContextTokens = 45_000
	body := manyEvents(1400)
	var first, unseen int
	p := fake.Func(func(req provider.Request) fake.Step {
		switch {
		case len(req.Messages) == 1:
			return tool("c1", "get_service_logs")
		case first == 0:
			first = lastShownRef(t, resultText(t, req, "c1"))
			return tool("c2", "get_service_timeline")
		}
		cut := resultText(t, req, "c1")
		if !strings.Contains(lastMessage(req).Text, submitLimit) || strings.Contains(cut, fmt.Sprintf("\n[e%d] ", first)) {
			t.Errorf("the early result was not cut for the final call")
		}
		unseen = lastShownRef(t, resultText(t, req, "c2")) + 1
		return calls(submitCall(plainSummary, cite(fmt.Sprintf("e%d", first)), cite(fmt.Sprintf("e%d", unseen)), cite(fmt.Sprintf("e%d", unseen-1))))
	})
	l := newLoop(p, replies(map[string]string{"get_service_logs": body, "get_service_timeline": body}), func(l *Limits) { l.MaxSteps, l.MaxResultBytes = 3, 60_000 })
	res := requireResult(t, investigate(t, l, p, in))
	if len(res.Evidence) != 2 || res.EvidenceDropped != 1 {
		t.Fatalf("evidence %+v, dropped %d", res.Evidence, res.EvidenceDropped)
	}
	if want := fmt.Sprintf(`events: {"n":%d,"message":"task stopped"}`, first-1); res.Evidence[0].Excerpt != want || res.Evidence[0].Tool != "get_service_logs" {
		t.Errorf("item 0 = %+v, want excerpt %q", res.Evidence[0], want)
	}
	if res.Evidence[1].Tool != "get_service_timeline" {
		t.Errorf("item 1 = %+v", res.Evidence[1])
	}
}

func TestInvestigator_UncitableToolsAreShownUnchanged(t *testing.T) {
	if rule, ok := hmtools.EvidenceRuleFor("get_overview"); !ok || rule.Kind != hmtools.EvidenceNone {
		t.Fatalf("get_overview: rule %+v", rule)
	}
	body := "{\n  \"clusters\": 2\n}"
	p := fake.New(calls(overviewCall("c1")), calls(submitCall(plainSummary, cite("e1"))))
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_overview": body})), p, investigatorInput()))
	if got, want := resultText(t, requestAt(t, p, 1), "c1"), envelope("get_overview", body); got != want {
		t.Errorf("shown %q, want %q", got, want)
	}
	if len(res.Evidence) != 0 || res.EvidenceDropped != 1 {
		t.Errorf("result = %+v", res)
	}
}
