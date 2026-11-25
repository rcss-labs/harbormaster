package agentresult

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

func readOf(answers ...[2]string) *ReadCorpus {
	c := &ReadCorpus{}
	for _, a := range answers {
		c.Add(a[0], a[1])
	}
	return c
}

func encodeID(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }

func requireKept(t *testing.T, label, summary string, read *ReadCorpus) {
	t.Helper()
	if got, replaced := CheckReadSummary(summary, read); replaced || got != summary {
		t.Errorf("%s: %q was replaced", label, summary)
	}
	if note := CheckReadNote(summary, read); len(summary) <= NoteCap && note != summary {
		t.Errorf("%s: as a note %q was replaced", label, summary)
	}
}

func requireReplaced(t *testing.T, label, summary string, read *ReadCorpus) {
	t.Helper()
	if got, replaced := CheckReadSummary(summary, read); !replaced || got != ReplacementSummary {
		t.Errorf("%s: %q was kept", label, summary)
	}
	if note := CheckReadNote(summary, read); note != ReplacementNote {
		t.Errorf("%s: as a note %q was kept", label, summary)
	}
}

// What an investigation of checkout-api reads, in the shapes Harbormaster answers with.
const (
	serviceAnswer = `{"id":"c2VydmljZQ","name":"checkout-api","cluster":{"id":"Y2x1c3Rlcg","name":"payments-prod"},"account_id":"482109375512","region":"us-east-1",
"status":"down","task_definition_arn":"arn:aws:ecs:us-east-1:482109375512:task-definition/checkout-api:42",
"console_url":"https://console.aws.amazon.com/ecs/v2/clusters/payments-prod/services/checkout-api/health",
"deployments":[{"id":"ecs-svc/2233445566778899001","status":"PRIMARY","rollout_state":"IN_PROGRESS","rollout_state_reason":"ECS deployment ecs-svc/2233445566778899001 in progress."}],
"recent_events":[{"actor":"","source":"ecs","ts":"2026-11-25T14:02:00Z","text":"(service checkout-api) has started 1 tasks: (task 0f3a9c2b7d1e4f60a1b2c3d4e5f60718)."},
{"actor":"","source":"ecs","ts":"2026-11-25T14:01:00Z","text":"(service checkout-api) has reached a steady state."}],
"task_definition":{"family":"checkout-api","containers":[{"name":"app","image":"482109375512.dkr.ecr.us-east-1.amazonaws.com/checkout-api:2026.11.25-3f9c2ab"}]}}`
	tasksAnswer = `{"items":[{"id":"dGFzaw","short_id":"0f3a9c2b7d1e4f60a1b2c3d4e5f60718","arn":"arn:aws:ecs:us-east-1:482109375512:task/payments-prod/0f3a9c2b7d1e4f60a1b2c3d4e5f60718",
"service":{"id":"c2VydmljZQ","name":"checkout-api","status":"down"},"group":"service:checkout-api","stopped_reason":"Task failed container health checks","stop_code":"ServiceSchedulerInitiated"}],
"summary":{"stop_reasons":[{"reason":"Task failed container health checks","count":14}]}}`
	definitionAnswer = `{"family":"checkout-api","task_definition_arn":"arn:aws:ecs:us-east-1:482109375512:task-definition/checkout-api:42","main_container":"app",
"definition":{"family":"checkout-api","executionRoleArn":"arn:aws:iam::482109375512:role/checkout-api-execution","containerDefinitions":[{"name":"app",
"image":"482109375512.dkr.ecr.us-east-1.amazonaws.com/checkout-api:2026.11.25-3f9c2ab",
"command":["/app/serve","--upstream=inventory.payments-internal.example.net","--retries=5"],"entryPoint":["/bin/sh","-c","exec /app/bootstrap.sh --warm-cache --region"],
"environment":[{"name":"LEDGER_REPLICA_ENDPOINT_FOR_THE_SETTLEMENT_JOB","value":"ledger-replica-two.payments-internal.example.net"}],
"healthCheck":{"command":["CMD-SHELL","curl -f http://localhost:8080/internal/healthz || exit 1"]},
"dockerLabels":{"traefik.http.routers.checkout.rule":"Host(checkout.payments-internal.example.net)"},
"logConfiguration":{"logDriver":"awslogs","options":{"awslogs-group":"/ecs/payments-prod/checkout-api/application","vendor-endpoint":"collector.payments-internal.example.net:8088"}}}]}}`
	scalingAnswer = `{"policies":[{"name":"cpu","type":"TargetTrackingScaling"}],"activities":[{"id":"a","status_code":"Successful",
"description":"Setting desired count to 3.","cause":"monitor alarm TargetTracking-service/payments-prod/checkout-api-AlarmHigh-1f2e in state ALARM triggered policy cpu"}]}`
	logsAnswer = `{"group":"/ecs/checkout-api","region":"us-east-1","containers":[{"name":"app","driver":"awslogs"}],
"lines":[{"t":"2026-11-25T14:02:40Z","container":"app","task_id":"0f3a9c2b7d1e4f60a1b2c3d4e5f60718","message":"GET /healthz 503 migrations pending: three migrations have not been applied yet"}]}`
	searchAnswer = `{"rows":[{"t":"2026-11-25T14:02:40Z","service_id":"c2VydmljZQ","container":"app","task_id":"0f3a9c2b7d1e4f60a1b2c3d4e5f60718","message":"could not open the order ledger for writing: permission denied"}],
"targets":[{"account_id":"482109375512","region":"us-east-1","state":"failed","message":"the query was refused by the log service because of its size"}]}`
)

func realisticRun() *ReadCorpus {
	return readOf([2]string{"get_service", serviceAnswer}, [2]string{"list_service_tasks", tasksAnswer}, [2]string{"get_task_definition", definitionAnswer}, [2]string{"get_scaling", scalingAnswer},
		[2]string{"get_service_logs", logsAnswer}, [2]string{"search_logs", searchAnswer})
}

func TestReadCorpus_RealisticDiagnosesAreKept(t *testing.T) {
	read := realisticRun()
	for _, summary := range []string{
		"Task 0f3a9c2b7d1e4f60a1b2c3d4e5f60718 was stopped one minute after it started.",
		"The new image checkout-api:2026.11.25-3f9c2ab does not exist in the registry.",
		"The service payments-prod/checkout-api has no running tasks.",
		"Since 14:02 the service checkout-api has started 14 tasks and none stayed up.",
		"After the rollback the service checkout-api has reached a steady state again.",
		"The task failed container health checks one minute after start.",
		// Names of settings, log groups and roles are not values.
		"The variable LEDGER_REPLICA_ENDPOINT_FOR_THE_SETTLEMENT_JOB is wrong.",
		"Read the group /ecs/payments-prod/checkout-api/application for more.",
	} {
		requireKept(t, "diagnosis", summary, read)
	}
}

func TestReadCorpus_LogEnvironmentAndCommandTextStaysStrict(t *testing.T) {
	read := realisticRun()
	for label, summary := range map[string]string{
		"log line":           "It says migrations pending: three migrations have not been applied.",
		"log search row":     "It could not open the order ledger for writing at all.",
		"environment value":  "It connects to ledger-replica-two.payments-internal.example.net and fails.",
		"command":            "It runs with --upstream=inventory.payments-internal.example.net set.",
		"entry point":        "It starts with exec /app/bootstrap.sh --warm-cache --region first.",
		"health check":       "The check is curl -f http://localhost:8080/internal/healthz so it fails.",
		"label":              "The label is Host(checkout.payments-internal.example.net) here.",
		"unlisted option":    "It ships to collector.payments-internal.example.net:8088 instead.",
		"search error text":  "The query was refused by the log service because of it.",
		"log line, rendered": "MIGRATIONS pending -- three `migrations` have\nnot been applied",
	} {
		requireReplaced(t, label, summary, read)
	}
	// The same texts through a revision.
	revision := readOf([2]string{"get_revision", definitionAnswer})
	requireReplaced(t, "environment value of a revision", "It connects to ledger-replica-two.payments-internal.example.net and fails.", revision)
	requireReplaced(t, "command of a revision", "It runs with --upstream=inventory.payments-internal.example.net set.", revision)
	requireKept(t, "image of a revision", "The image checkout-api:2026.11.25-3f9c2ab does not exist.", readOf([2]string{"get_revision", `{"definition":{"containerDefinitions":[{"image":"482109375512.dkr.ecr.us-east-1.amazonaws.com/checkout-api:2026.11.25-3f9c2ab"}]}}`}))
}

// letters is n characters of prose that normalise to themselves and repeat no run.
func letters(n int) string {
	var out strings.Builder
	for i := 0; out.Len() < n; i++ {
		out.WriteString("w" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "x ")
	}
	text := []byte(out.String()[:n])
	if text[n-1] == ' ' {
		text[n-1] = 'y'
	}
	return string(text)
}

func TestReadCorpus_RunLengthsAtTheEdges(t *testing.T) {
	value := letters(200)
	logs := readOf([2]string{"get_service_logs", `{"lines":[{"message":"` + value + `"}]}`})
	search := readOf([2]string{"search_logs", `{"rows":[{"message":"` + value + `"}]}`})
	environment := readOf([2]string{"get_task_definition", `{"definition":{"containerDefinitions":[{"environment":[{"name":"A","value":"` + value + `"}]}]}}`})
	command := readOf([2]string{"get_task_definition", `{"definition":{"containerDefinitions":[{"command":["` + value + `"]}]}}`})
	loose := readOf([2]string{"get_service", `{"recent_events":[{"text":"` + value + `"}]}`})
	for label, c := range map[string]struct {
		read *ReadCorpus
		run  int
	}{"log line": {logs, 32}, "log search row": {search, 32}, "environment": {environment, 32}, "command": {command, 32}, "event": {loose, 80}} {
		for _, n := range []int{c.run - 1, c.run, c.run + 1} {
			// Alone, so that no character beside the copy can make the run longer.
			from := 4
			for strings.TrimSpace(value[from:from+n]) != value[from:from+n] {
				from++
			}
			copied := value[from : from+n]
			if n < c.run {
				requireKept(t, label, copied, c.read)
			} else {
				requireReplaced(t, label, copied, c.read)
			}
		}
	}
	if LooseRun != 80 || VerbatimRun != 32 {
		t.Errorf("runs are %d and %d", VerbatimRun, LooseRun)
	}
}

func TestReadCorpus_TheMemberDecidesNotTheText(t *testing.T) {
	text := "the order ledger refused a new connection" // 40 characters are copied
	copied := "It seems the order ledger refused a new connection today."
	event := readOf([2]string{"get_service", `{"recent_events":[{"text":"` + text + `"}]}`})
	requireKept(t, "an event of get_service", copied, event)
	logLine := readOf([2]string{"get_service_logs", `{"lines":[{"message":"` + text + `"}]}`})
	requireReplaced(t, "the message of a log answer", copied, logLine)
	// Read both ways, the strict one decides, whichever came first.
	event.Add("get_service_logs", `{"lines":[{"message":"`+text+`"}]}`)
	logLine.Add("get_service", `{"recent_events":[{"text":"`+text+`"}]}`)
	requireReplaced(t, "event, then log", copied, event)
	requireReplaced(t, "log, then event", copied, logLine)
}

func TestReadCorpus_WhatCannotBeDecidedIsStrict(t *testing.T) {
	text := "the order ledger refused a new connection"
	copied := "It seems the order ledger refused a new connection today."
	for label, answer := range map[string][2]string{
		"invented tool":                    {"read_everything", `{"name":"` + text + `"}`},
		"no tool":                          {"", `{"name":"` + text + `"}`},
		"invented member":                  {"get_service", `{"invented":"` + text + `"}`},
		"invented member of a member":      {"get_service", `{"cluster":{"invented":"` + text + `"}}`},
		"known name in an invented member": {"get_service", `{"invented":{"name":"` + text + `","text":"` + text + `"}}`},
		"known name in an invented list":   {"get_service", `{"invented":[{"name":"` + text + `"}]}`},
		"member in another case":           {"get_service", `{"Name":"` + text + `"}`},
		"object where text was expected":   {"get_service", `{"name":{"name":"` + text + `"}}`},
		"list where text was expected":     {"get_service", `{"name":["` + text + `"]}`},
		"text where a list was expected":   {"get_service", `{"recent_events":"` + text + `"}`},
		"text in a list of objects":        {"get_service", `{"recent_events":["` + text + `"]}`},
		"object where a list was expected": {"get_service", `{"recent_events":{"text":"` + text + `"}}`},
		"list in a list":                   {"get_service", `{"recent_events":[[{"text":"` + text + `"}]]}`},
		"another tool's member":            {"get_service", `{"groups":[{"name":"` + text + `"}]}`},
		"list at the top":                  {"get_service", `[{"name":"` + text + `"}]`},
		"second value":                     {"get_service", `{"name":"x"} {"name":"` + text + `"}`},
		"plain text":                       {"get_service", text},
		"a JSON string":                    {"get_service", `"` + text + `"`},
		"cut JSON":                         {"get_service", `{"name":"` + text + `","clu`},
		"JSON with a tail":                 {"get_service", `{"name":"` + text + `"} and more`},
		"error text":                       {"get_service", `The database says ` + text + `. (internal)`},
		"unexpected log shape":             {"get_service_logs", `{"events":[{"message":"` + text + `"}]}`},
		"log line as a list":               {"get_service_logs", `{"lines":[{"message":["` + text + `"]}]}`},
		"log line as an object":            {"get_service_logs", `{"lines":[{"message":{"container":"` + text + `"}}]}`},
		"log lines under another tool":     {"list_tasks", `{"lines":[{"message":"` + text + `"}]}`},
		"unexpected definition":            {"get_task_definition", `{"definition":"` + text + `"}`},
		"unexpected container":             {"get_task_definition", `{"definition":{"containerDefinitions":{"image":"` + text + `"}}}`},
		"environment as a map":             {"get_task_definition", `{"definition":{"containerDefinitions":[{"environment":{"name":"` + text + `"}}]}}`},
		"definition member in another case": {"get_task_definition",
			`{"definition":{"containerDefinitions":[{"Environment":[{"name":"` + text + `"}]}]}}`},
		"invented definition member": {"get_task_definition", `{"definition":{"containerDefinitions":[{"invented":"` + text + `"}]}}`},
	} {
		requireReplaced(t, label, copied, readOf(answer))
	}
	for label, answer := range map[string][2]string{
		"cut after a value":          {"get_service", `{"family":"x","name":"` + text + `"`},
		"a value after an empty one": {"get_service", `{} {"name":"` + text + `"}`},
		"after a number":             {"get_service_logs", `{"lines":[{"n":3,"message":"` + text + `"}]}`},
		"after a boolean":            {"get_service_logs", `{"lines":[{"n":true,"message":"` + text + `"}]}`},
		"after a null":               {"get_service_logs", `{"lines":[null,{"n":null,"message":"` + text + `"}]}`},
		"after an empty list":        {"get_service_logs", `{"lines":[[],{"n":[],"m":{},"message":"` + text + `"}]}`},
	} {
		requireReplaced(t, label, copied, readOf(answer))
	}
	// A repeated member is read as often as it stands there.
	twice := readOf([2]string{"get_service_logs", `{"lines":[{"message":"` + text + `","message":"x"}],"group":"x"}`})
	requireReplaced(t, "repeated member", copied, twice)
	requireReplaced(t, "the list after a list", copied, readOf([2]string{"get_service_logs", `{"containers":[{"name":"a"}],"lines":[{"container":"a","message":"` + text + `"}]}`}))
	requireReplaced(t, "after a nested object", copied, readOf([2]string{"get_service", `{"cluster":{"name":"a"},"invented":"` + text + `"}`}))
	requireKept(t, "listed after a nested object", copied, readOf([2]string{"get_service", `{"invented":{"a":[1,{"b":null}]},"cluster":{"name":"` + text + `"}}`}))
}

func TestReadCorpus_ARunNeverSpansValuesOrClasses(t *testing.T) {
	value := letters(160)
	half, other := strings.TrimSpace(value[:30]), strings.TrimSpace(value[30:60])
	read := readOf([2]string{"get_service_logs", `{"group":"` + half + `","lines":[{"container":"` + half + `","message":"` + other + `"}]}`})
	requireKept(t, "loose then strict", half+" "+other, read)
	requireKept(t, "strict then loose", other+" "+half, read)
	requireKept(t, "two values of a list", half+" "+other, readOf([2]string{"invented", `["` + half + `","` + other + `"]`}))
	long, rest := strings.TrimSpace(value[:75]), strings.TrimSpace(value[75:150])
	requireKept(t, "two loose values", long+" "+rest, readOf([2]string{"get_service", `{"name":"` + long + `","family":"` + rest + `"}`}))
	// A long run of a loose value is a copy, escapes and all.
	requireReplaced(t, "one loose value", long+" "+rest, readOf([2]string{"get_service", `{"name":"` + long + " " + rest + `"}`}))
	escaped := strings.ReplaceAll(long+" "+rest, " ", `\\n`)
	requireReplaced(t, "a loose value with escapes", long+" "+rest, readOf([2]string{"get_service", `{"name":"` + escaped + `"}`}))
}

func subjectID(t *testing.T, cluster, service string) (id string) {
	t.Helper()
	id = encodeID("482109375512|us-east-1|" + cluster + "|" + service)
	if _, name, ok := secretshape.IDNames(id); !ok || name != service {
		t.Fatalf("setup: %q is no id", id)
	}
	return id
}

func TestReadCorpus_TheSubjectsNamesAreNoCopy(t *testing.T) {
	const (
		name34  = "prod-payments-checkout-api-service"                           // 34
		name60  = "CheckoutStack-CheckoutServiceDefinition-Xy7KpQ2mNvRtAbCdEf12" // 60
		cluster = "platform-production-eu-central-1-shared"
	)
	if len(name34) != 34 || len(name60) != 60 {
		t.Fatalf("setup: %d, %d", len(name34), len(name60))
	}
	for _, name := range []string{name34, name60} {
		// The log itself names the service and the cluster, which makes the names strict text.
		logs := `{"lines":[{"message":"` + name + `: refused"},{"message":"in ` + cluster + `"}]}`
		read := readOf([2]string{"get_service_logs", logs})
		summary := "The service " + name + " of " + cluster + " refuses connections."
		requireNamesReplaced(t, name, summary, read)
		clusterName, service, _ := secretshape.IDNames(subjectID(t, cluster, name))
		read.AllowNames(clusterName, service)
		if got, replaced := CheckReadSummary(summary, read); replaced {
			t.Errorf("%s: the subject's own name was replaced: %q", name, got)
		}
		// A name with the log text beside it is still a copy.
		requireNamesReplaced(t, name, "It logs in "+cluster+" here.", read)
		requireNamesReplaced(t, name, "It logs "+name+": refused by it.", read)
		// Another service's name from another log line is not the subject's.
		other := "billing-" + name
		read.Add("get_service_logs", `{"lines":[{"message":"calling `+other+` failed"}]}`)
		requireNamesReplaced(t, name, "It calls "+other+" in vain.", read)
		if _, replaced := CheckReadSummary(summary, read); replaced {
			t.Errorf("%s: replaced after another answer", name)
		}
	}
	// A name over the loose run, in values of the loose class.
	long := strings.Repeat("settlement-worker-", 6) // 108
	long = strings.TrimSuffix(long, "-")
	read := readOf([2]string{"get_service", `{"name":"` + long + `"}`})
	requireNamesReplaced(t, "long name", "The service "+long+" is down.", read)
	read.AllowNames(long)
	if _, replaced := CheckReadSummary("The service "+long+" is down.", read); replaced {
		t.Error("the subject's own long name was replaced")
	}
}

func requireNamesReplaced(t *testing.T, label, summary string, read *ReadCorpus) {
	t.Helper()
	if got, replaced := CheckReadSummary(summary, read); !replaced || got != ReplacementSummary {
		t.Errorf("%s: %q was kept", label, summary)
	}
}

func TestReadCorpus_OneBudgetForBothClasses(t *testing.T) {
	const plain = "The newest deployment cannot start its tasks."
	half := strings.Repeat("log line ok ", CorpusCap/24-10)
	logs, service := `{"lines":[{"message":"`+half+`"}]}`, `{"name":"`+half+`"}`
	read := readOf([2]string{"get_service_logs", logs}, [2]string{"get_service", service})
	requireKept(t, "under the cap", plain, read)
	read.Add("get_service", `{"name":"`+strings.Repeat("a ", 300)+`"}`)
	requireReplaced(t, "over the cap by a loose value", plain, read)
	read.Add("get_service", `{"name":"x"}`)
	requireReplaced(t, "after the cap", plain, read)
	read = readOf([2]string{"get_service", service}, [2]string{"get_service_logs", logs})
	read.Add("get_service_logs", `{"lines":[{"message":"`+strings.Repeat("a ", 300)+`"}]}`)
	requireReplaced(t, "over the cap by a strict value", plain, read)
	// Exactly at the cap is not over it, and the raw bytes count, not what is stored.
	read = readOf([2]string{"get_service", strings.Repeat(" ", CorpusCap-1)}, [2]string{"get_service_logs", "x"})
	requireKept(t, "at the cap", plain, read)
	read.Add("no_tool", "x")
	requireReplaced(t, "one byte over", plain, read)
	requireReplaced(t, "no corpus", plain, nil)
	requireKept(t, "nothing read", plain, &ReadCorpus{})
}

func TestReadCorpus_EveryOtherCheckStillRuns(t *testing.T) {
	read := realisticRun()
	for label, summary := range map[string]string{
		"secret":     "It used " + "AKIA" + "IOSFODNN7EXAMPLE" + " to call.",
		"markup":     "See ![x](https://example.com/a.png) here.",
		"hidden":     "The ser\u200bvice is down.",
		"empty":      " ",
		"over a cap": strings.Repeat("ab ", SummaryCap),
	} {
		requireReplaced(t, label, summary, read)
		requireReplaced(t, label+", nothing read", summary, &ReadCorpus{})
	}
	if got := CheckReadNote("one\ntwo", read); got != "one two" {
		t.Errorf("note = %q", got)
	}
}

func TestReadCorpus_LinearOnLargeHostileAnswers(t *testing.T) {
	const size = 512 << 10
	repeat := func(s string) string { return strings.Repeat(s, size/len(s)) }
	for label, answer := range map[string]string{
		"deep lists":    repeat("["),
		"deep objects":  repeat(`{"definition":`),
		"deep children": `{"root":` + repeat(`{"children":[`),
		"many members":  `{` + repeat(`"name":"a b",`) + `"name":""}`,
		"many values":   `{"recent_events":[` + repeat(`{"text":"ab"},`) + `{"text":""}]}`,
		"near misses":   `{"name":"` + repeat(strings.Repeat("a", 79)+"b ") + `"}`,
		"one long name": `{"` + repeat("a") + `":"x"}`,
		"escapes":       `{"name":"` + repeat(`\\`) + `"}`,
	} {
		for _, tool := range []string{"get_service", "get_task_definition", "get_vpc_tree", "invented"} {
			started := time.Now()
			read := readOf([2]string{tool, answer}, [2]string{"get_service_logs", answer})
			_, _ = CheckReadSummary(strings.Repeat("a", 79)+" "+strings.Repeat("ab ", 300), read)
			if took := time.Since(started); took > 5*time.Second {
				t.Errorf("%s, %s: %v", tool, label, took)
			}
		}
	}
}
