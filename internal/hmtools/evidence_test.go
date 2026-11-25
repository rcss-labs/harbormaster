package hmtools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const svcA = "svc-a"

func serviceArgs(id any) map[string]any { return map[string]any{"service_id": id} }

func unitTexts(units []Unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Text)
	}
	return out
}

func TestEveryToolHasAnExplicitEvidenceRule(t *testing.T) {
	counts := map[string]int{}
	tools := Tools()
	if len(tools) != 39 {
		t.Fatalf("%d tools, want 39", len(tools))
	}
	for _, tool := range tools {
		rule, ok := EvidenceRuleFor(tool.Name)
		if !ok {
			t.Errorf("tool %s has no evidence rule", tool.Name)
			continue
		}
		switch rule.Kind {
		case EvidenceNone:
			counts["none"]++
			if rule.Action != "" || rule.Items != "" || rule.IDField != "" {
				t.Errorf("%s: a none rule carries %+v", tool.Name, rule)
			}
		case EvidenceServiceArg:
			counts["arg-"+rule.Action]++
			if rule.Items != "" || rule.IDField != "" {
				t.Errorf("%s: a service-arg rule carries %+v", tool.Name, rule)
			}
			if !hasParam(tool, "service_id") {
				t.Errorf("%s: a service-arg rule needs a service_id parameter", tool.Name)
			}
		case EvidencePerItem:
			counts["item-"+rule.Action]++
			if rule.Items == "" || rule.IDField == "" {
				t.Errorf("%s: a per-item rule needs Items and IDField, got %+v", tool.Name, rule)
			}
		default:
			t.Errorf("%s: unknown kind %d", tool.Name, rule.Kind)
		}
	}
	want := map[string]int{"arg-view": 15, "arg-logs": 1, "item-view": 3, "item-logs": 1, "none": 19}
	for key, n := range want {
		if counts[key] != n {
			t.Errorf("%s: %d tools, want %d (all counts %v)", key, counts[key], n, counts)
		}
	}
	for name, rule := range map[string]EvidenceRule{
		"get_service_logs": {Kind: EvidenceServiceArg, Action: "logs"},
		"search_logs":      {Kind: EvidencePerItem, Action: "logs", Items: "rows", IDField: "service_id"},
		"list_alerts":      {Kind: EvidencePerItem, Action: "view", Items: "items", IDField: "service_id"},
		"list_services":    {Kind: EvidencePerItem, Action: "view", Items: "items", IDField: "id"},
		"search_config":    {Kind: EvidencePerItem, Action: "view", Items: "items", IDField: "id"},
		"get_service":      {Kind: EvidenceServiceArg, Action: "view"},
	} {
		if got, _ := EvidenceRuleFor(name); got != rule {
			t.Errorf("%s: rule %+v, want %+v", name, got, rule)
		}
	}
	if rule, ok := EvidenceRuleFor("no_such_tool"); ok || rule != (EvidenceRule{}) {
		t.Errorf("unknown tool got a rule: %+v", rule)
	}
}

func hasParam(tool Tool, name string) bool {
	for _, p := range tool.Params {
		if p.Name == name {
			return true
		}
	}
	return false
}

func TestEvidenceAction_NoneAndUnknownAreNotOK(t *testing.T) {
	for _, name := range []string{"whoami", "get_cluster", "list_storage", "no_such_tool", ""} {
		if action, ok := EvidenceAction(name); ok || action != "" {
			t.Errorf("%q: got %q, %v; want no action", name, action, ok)
		}
	}
	for name, want := range map[string]string{"get_service": "view", "list_alerts": "view", "get_service_logs": "logs", "search_logs": "logs"} {
		if action, ok := EvidenceAction(name); !ok || action != want {
			t.Errorf("%q: got %q, %v; want %q", name, action, ok, want)
		}
	}
}

func TestUnits_ServiceArgSplitsMembersAndArrays(t *testing.T) {
	body := `{"name":"web", "tasks": [ {"id":1}, {"id": 2} ], "count": 3, "empty": []}`
	want := []string{`name: "web"`, `tasks: {"id":1}`, `tasks: {"id":2}`, `count: 3`}
	for repeat := 0; repeat < 5; repeat++ {
		units := Units("get_service", serviceArgs(svcA), body)
		if got := unitTexts(units); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
			t.Fatalf("repeat %d: units %q, want exactly %q", repeat, got, want)
		}
		for _, u := range units {
			if u.ServiceID != svcA || u.Action != "view" {
				t.Errorf("unit %+v mislabelled", u)
			}
		}
	}

	top := Units("list_revisions", serviceArgs(svcA), `[ {"r":1}, "two", 3 ]`)
	if got := unitTexts(top); fmt.Sprint(got) != fmt.Sprint([]string{`{"r":1}`, `"two"`, `3`}) {
		t.Errorf("top-level array units %q", got)
	}
	scalar := Units("get_desired_count", serviceArgs(svcA), ` 42 `)
	if got := unitTexts(scalar); fmt.Sprint(got) != "[42]" {
		t.Errorf("scalar units %q", got)
	}
	logs := Units("get_service_logs", serviceArgs(svcA), `{"lines":[{"m":"x"}]}`)
	if len(logs) != 1 || logs[0].Action != "logs" || logs[0].ServiceID != svcA {
		t.Errorf("log units %+v", logs)
	}
}

func TestUnits_ServiceArgNeedsAValidServiceIDArgument(t *testing.T) {
	body := `{"a":1}`
	for name, args := range map[string]map[string]any{
		"missing": {}, "nil map": nil, "empty": serviceArgs(""), "number": serviceArgs(7),
		"object": serviceArgs(map[string]any{"x": 1}), "oversized": serviceArgs(strings.Repeat("a", 513)),
	} {
		if units := Units("get_service", args, body); len(units) != 0 {
			t.Errorf("%s: got %d units", name, len(units))
		}
	}
	if units := Units("get_service", serviceArgs(strings.Repeat("a", 512)), body); len(units) != 1 {
		t.Errorf("512-byte id: got %d units, want 1", len(units))
	}
}

func TestUnits_ServiceIDComesFromTheArgumentNotTheBody(t *testing.T) {
	body := `{"service_id":"svc-other","id":"svc-other","tasks":[{"service_id":"svc-other"}]}`
	for _, tool := range []string{"get_service", "get_service_logs"} {
		units := Units(tool, serviceArgs(svcA), body)
		if len(units) == 0 {
			t.Fatalf("%s: no units", tool)
		}
		for _, u := range units {
			if u.ServiceID != svcA {
				t.Errorf("%s: unit attributed to %q, want the argument %q", tool, u.ServiceID, svcA)
			}
		}
	}
}

func TestUnits_PerItemAttributesEachElement(t *testing.T) {
	body := `{"items":[{"id":"s1","name":"a"},{"id":"s2","name":"b"}],"total":2}`
	units := Units("list_services", nil, body)
	if len(units) != 2 {
		t.Fatalf("units %+v", units)
	}
	if units[0] != (Unit{ServiceID: "s1", Action: "view", Text: `{"id":"s1","name":"a"}`}) ||
		units[1] != (Unit{ServiceID: "s2", Action: "view", Text: `{"id":"s2","name":"b"}`}) {
		t.Errorf("units %+v", units)
	}
	rows := Units("search_logs", nil, `{"rows":[{"service_id":"s9","message":"boom"}]}`)
	if len(rows) != 1 || rows[0].ServiceID != "s9" || rows[0].Action != "logs" {
		t.Errorf("log rows %+v", rows)
	}
	alerts := Units("list_alerts", map[string]any{"service_id": "ignored"}, `{"items":[{"service_id":"s3","id":"not-this"}]}`)
	if len(alerts) != 1 || alerts[0].ServiceID != "s3" {
		t.Errorf("alerts %+v", alerts)
	}
}

func TestUnits_PerItemDropsElementsWithoutAnID(t *testing.T) {
	body := fmt.Sprintf(`{"items":[
		{"name":"missing"},
		{"id":""},
		{"id":7},
		{"id":{"x":"y"}},
		{"id":null},
		{"id":["s1"]},
		"not an object",
		{"id":%q},
		{"id":"keep"}
	]}`, strings.Repeat("a", 513))
	units := Units("list_services", nil, body)
	if len(units) != 1 || units[0].ServiceID != "keep" {
		t.Errorf("units %+v, want only the element with id keep", units)
	}
	if units := Units("list_services", nil, fmt.Sprintf(`{"items":[{"id":%q}]}`, strings.Repeat("a", 512))); len(units) != 1 {
		t.Errorf("512-byte id dropped")
	}
}

func TestUnits_PerItemIgnoresEverythingOutsideTheArray(t *testing.T) {
	body := `{"total":3,"other":[{"id":"x"}],"items":[{"id":"s1"}],"polled_at":"now","id":"top"}`
	units := Units("list_services", nil, body)
	if len(units) != 1 || units[0].ServiceID != "s1" {
		t.Errorf("units %+v", units)
	}
	alerts := Units("list_alerts", nil, `{"items":[{"service_id":"","text":"account unreachable"},{"service_id":"s1"}],"counts":{"danger":1}}`)
	if len(alerts) != 1 || alerts[0].ServiceID != "s1" {
		t.Errorf("an account-level alert must not be citable: %+v", alerts)
	}
	for name, shaped := range map[string]string{
		"array body": `[{"id":"s1"}]`, "items is an object": `{"items":{"id":"s1"}}`,
		"items missing": `{"rows":[{"id":"s1"}]}`, "items null": `{"items":null}`, "scalar": `5`,
	} {
		if units := Units("list_services", nil, shaped); len(units) != 0 {
			t.Errorf("%s: got %+v", name, units)
		}
	}
	if units := Units("list_services", nil, `{"items":[{"ID":"s1"}]}`); len(units) != 0 {
		t.Errorf("a member name differing in case must not match: %+v", units)
	}
}

func TestUnits_NoneAndUnknownToolsGiveNothing(t *testing.T) {
	for _, tool := range []string{"whoami", "get_overview", "list_storage", "no_such_tool", "", "submit_result"} {
		if units := Units(tool, serviceArgs(svcA), `{"items":[{"id":"s1"}],"service_id":"s1"}`); len(units) != 0 {
			t.Errorf("%q gave %+v", tool, units)
		}
	}
}

func TestUnits_InvalidOrTruncatedJSONGivesNothing(t *testing.T) {
	bodies := []string{
		``, `   `, `{`, `{"a":`, `{"a":1`, `[1,2`, `{"a":1}}`, `{"a":1} {"b":2}`, `{"a":1} x`, `nul`, `{'a':1}`, `{"a":"\xff"`,
		`{"a":1,}`, "{\"a\":\"line\nbreak\"}",
	}
	for _, body := range bodies {
		if units := Units("get_service", serviceArgs(svcA), body); len(units) != 0 {
			t.Errorf("get_service %q gave %+v", body, units)
		}
		if units := Units("list_services", nil, body); len(units) != 0 {
			t.Errorf("list_services %q gave %+v", body, units)
		}
	}
	truncated := `{"items":[{"id":"s1"},{"id":"s2"`
	if units := Units("list_services", nil, truncated); len(units) != 0 {
		t.Errorf("truncated body gave %+v", units)
	}
}

func TestUnits_BodyOverTwoMiBGivesNothing(t *testing.T) {
	pad := func(n int) string { return strings.Repeat("a", n) }
	exact := `{"k":"` + pad(2<<20-len(`{"k":""}`)) + `"}`
	if len(exact) != 2<<20 {
		t.Fatalf("test body is %d bytes", len(exact))
	}
	if units := Units("get_service", serviceArgs(svcA), exact); len(units) != 1 {
		t.Errorf("a body of exactly 2 MiB gave %d units, want 1", len(units))
	}
	if units := Units("get_service", serviceArgs(svcA), exact+" "); len(units) != 0 {
		t.Errorf("a body over 2 MiB gave %d units", len(units))
	}
}

func TestUnits_CapsTheNumberOfUnits(t *testing.T) {
	var elements []string
	for i := 0; i < 2500; i++ {
		elements = append(elements, fmt.Sprintf(`{"id":"s%d"}`, i))
	}
	array := "[" + strings.Join(elements, ",") + "]"
	if units := Units("list_revisions", serviceArgs(svcA), array); len(units) != 2000 {
		t.Errorf("service-arg array: %d units, want 2000", len(units))
	}
	if units := Units("get_service", serviceArgs(svcA), `{"a":`+array+`,"b":`+array+`}`); len(units) != 2000 {
		t.Errorf("service-arg object: %d units, want 2000", len(units))
	}
	if units := Units("list_services", nil, `{"items":`+array+`}`); len(units) != 2000 || units[1999].ServiceID != "s1999" {
		t.Errorf("per-item: %d units", len(units))
	}
}

func TestUnits_LinearOnLargeBodies(t *testing.T) {
	const rowCount = 50_000
	rows := make([]string, rowCount)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"service_id":"s%d","m":"line %d"}`, i%7, i)
	}
	body := `{"rows":[` + strings.Join(rows, ",") + `],"capped":false}`
	if len(body) > 2<<20 || strings.Count(body, `"service_id"`) != rowCount {
		t.Fatalf("test body is %d bytes with %d rows", len(body), strings.Count(body, `"service_id"`))
	}
	members := make([]string, 100_000)
	for i := range members {
		members[i] = fmt.Sprintf(`"k%d":1`, i)
	}
	adversarial := map[string]string{
		"rows":          body,
		"deep nesting":  strings.Repeat("[", 5000) + strings.Repeat("]", 5000),
		"many members":  "{" + strings.Join(members, ",") + "}",
		"long string":   `{"m":"` + strings.Repeat(`\"`, 400000) + `"}`,
		"wide id array": `{"items":[` + strings.Repeat(`{"id":"x"},`, 90000) + `{"id":"y"}]}`,
	}
	if len(adversarial["many members"]) > 2<<20 {
		t.Fatalf("many members body is %d bytes", len(adversarial["many members"]))
	}
	for name, input := range adversarial {
		for _, tool := range []string{"search_logs", "get_service", "list_services"} {
			start := time.Now()
			Units(tool, serviceArgs(svcA), input)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("%s through %s took %v", name, tool, elapsed)
			}
		}
	}
	start := time.Now()
	units := Units("search_logs", nil, body)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("50,000 rows took %v", elapsed)
	}
	if len(units) != 2000 {
		t.Errorf("log rows: %d units, want the 2000 cap", len(units))
	}
}

// FuzzUnits seeds the corpus that the normal run checks as plain cases; `go test -fuzz FuzzUnits` explores further.
func FuzzUnits(f *testing.F) {
	seeds := []string{
		``, `null`, `0`, `"s"`, `[]`, `{}`, `[[]]`, `{"items":null}`, `{"items":[null,1,"x",[],{}]}`, `{"items":[{"id":null}]}`,
		`{"rows":[{"service_id":"s"}]}`, `{"a":[{"b":[1]}]}`, `{"items":[{"id":"\u0000"}]}`, `{"\ud800":1}`, `[1e999]`,
		`{"a":1}{"b":2}`, strings.Repeat("[", 20000), `{"items":[` + strings.Repeat(`{"id":"s"},`, 3000) + `{}]}`,
		"\xff\xfe", `{"service_id":"x","items":[{"id":"y"}]}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	tools := Tools()
	f.Fuzz(func(t *testing.T, body string) {
		for _, tool := range tools {
			units := Units(tool.Name, serviceArgs(svcA), body)
			rule, _ := EvidenceRuleFor(tool.Name)
			if rule.Kind == EvidenceNone && len(units) != 0 {
				t.Fatalf("%s: none rule gave units", tool.Name)
			}
			if len(units) > 2000 {
				t.Fatalf("%s: %d units", tool.Name, len(units))
			}
			for _, u := range units {
				if u.ServiceID == "" || len(u.ServiceID) > 512 || u.Action == "" || !json.Valid([]byte(body)) {
					t.Fatalf("%s: bad unit %+v for %q", tool.Name, u, body)
				}
				if rule.Kind == EvidenceServiceArg && u.ServiceID != svcA {
					t.Fatalf("%s: unit attributed to %q", tool.Name, u.ServiceID)
				}
			}
		}
	})
}

func TestUnits_NeverPanics(t *testing.T) {
	// The fuzz seed corpus runs as plain cases in every normal test run; the seeds are the targets above.
	bodies := []string{
		``, `null`, `0`, `"s"`, `[]`, `{}`, `[[]]`, `{"items":null}`, `{"items":[null,1,"x",[],{}]}`, `{"items":[{"id":null}]}`,
		`{"rows":[{"service_id":"s"}]}`, `{"a":[{"b":[1]}]}`, `{"items":[{"id":"\u0000"}]}`, `{"\ud800":1}`, `[1e999]`,
		`{"a":1}{"b":2}`, strings.Repeat("[", 20000), "\xff\xfe", `{"service_id":"x","items":[{"id":"y"}]}`,
	}
	argSets := []map[string]any{nil, {}, {"service_id": nil}, {"service_id": 1.5}, {"service_id": []any{"x"}}, serviceArgs("s")}
	for _, tool := range append(Tools(), Tool{Name: "unknown"}) {
		for _, body := range bodies {
			for _, args := range argSets {
				Units(tool.Name, args, body)
			}
		}
	}
}

func TestEvidenceRules_TableCoversExactlyTheTools(t *testing.T) {
	tools := Tools()
	if len(evidenceRules) != len(tools) {
		t.Errorf("%d rules for %d tools", len(evidenceRules), len(tools))
	}
	for _, tool := range tools {
		if _, ok := evidenceRules[tool.Name]; !ok {
			t.Errorf("tool %s has no rule", tool.Name)
		}
	}
}

func TestEvidence_ABadActionOrAnUnknownKindFailsClosed(t *testing.T) {
	bad := map[string]EvidenceRule{
		"tmp_bad_action":   {Kind: EvidenceServiceArg, Action: "operate"},
		"tmp_empty_action": {Kind: EvidencePerItem, Action: "", Items: "items", IDField: "id"},
		"tmp_bad_kind":     {Kind: EvidenceKind(99), Action: "view", Items: "items", IDField: "id"},
	}
	for name, rule := range bad {
		evidenceRules[name] = rule
	}
	defer func() {
		for name := range bad {
			delete(evidenceRules, name)
		}
	}()
	for name := range bad {
		if action, ok := EvidenceAction(name); ok || action != "" {
			t.Errorf("%s: EvidenceAction gave %q, %v", name, action, ok)
		}
		if units := Units(name, serviceArgs(svcA), `{"items":[{"id":"s1"}],"a":1}`); len(units) != 0 {
			t.Errorf("%s: gave %+v", name, units)
		}
	}
}

func TestUnits_DuplicateKeysFailClosed(t *testing.T) {
	// A duplicate id key inside an element: Go keeps the last, other readers keep the first, so drop the element.
	body := `{"items":[{"id":"s1","id":"s2"},{"id":"ok","name":"a","name":"b"},{"id":"fine"}]}`
	units := Units("list_services", nil, body)
	if len(units) != 1 || units[0].ServiceID != "fine" {
		t.Errorf("duplicate keys in an element: %+v", units)
	}
	// A duplicate top-level list key: no unit is citable.
	if units := Units("list_services", nil, `{"items":[{"id":"s1"}],"items":[{"id":"s2"}]}`); len(units) != 0 {
		t.Errorf("duplicate list key: %+v", units)
	}
	if units := Units("get_service", serviceArgs(svcA), `{"a":1,"a":2}`); len(units) != 0 {
		t.Errorf("duplicate top-level member of a service-arg body: %+v", units)
	}
}

func TestUnits_PerItemDropsIDsThatAreScrubberMarkersOrUnusable(t *testing.T) {
	for _, id := range []string{"[redacted]", "abc[1]", "[", " lead", "trail ", `a\u0000b`, `a\nb`} {
		body := `{"items":[{"id":"` + id + `"},{"id":"keep"}]}`
		units := Units("list_services", nil, body)
		if len(units) != 1 || units[0].ServiceID != "keep" {
			t.Errorf("id %q: units %+v, want only keep", id, units)
		}
	}
	if units := Units("get_service", serviceArgs("[redacted]"), `{"a":1}`); len(units) != 0 {
		t.Errorf("a marker as the service argument gave %+v", units)
	}
}

func TestUnits_PerItemElementWithWhitespaceIsCompacted(t *testing.T) {
	body := "{ \"items\" : [ {\n\t\"id\" :  \"s1\" ,\r\n \"tags\" : [ 1 , 2 ] , \"n\" : \"a b\" } ] }"
	units := Units("list_services", nil, body)
	want := `{"id":"s1","tags":[1,2],"n":"a b"}`
	if len(units) != 1 || units[0].ServiceID != "s1" || units[0].Text != want {
		t.Errorf("units %+v, want text %s", units, want)
	}
}
