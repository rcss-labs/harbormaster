package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// cdkNames are n different names that look random, of 32 characters each.
func cdkNames(t *testing.T, n int) []string {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("Shop-%c%02dF3A9B2C1D4E5-Xy7KpQ2mNvR", 'A'+i%26, i)
		if len(names[i]) != 32 || secretshape.ScrubText(names[i]) != secretshape.Redacted {
			t.Fatalf("test setup: %q is not hidden, or has %d characters", names[i], len(names[i]))
		}
	}
	return names
}

func serviceIDOf(cluster model.ClusterKey, service string) string {
	return model.ServiceKey{ClusterKey: cluster, Service: service}.ID()
}

// listBody is a list_services answer.
func listBody(cluster model.ClusterKey, services ...string) string {
	var rows []string
	for _, service := range services {
		rows = append(rows, `{"id":"`+serviceIDOf(cluster, service)+`","name":"`+service+`","cluster":{"id":"`+cluster.ID()+`","name":"`+cluster.Cluster+`"},"running":1}`)
	}
	return `{"items":[` + strings.Join(rows, ",") + `],"polled_at":"2026-11-25T09:30:00Z"}`
}

func callWith(id, name string, args map[string]any) provider.ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return provider.ToolCall{ID: id, Name: name, Arguments: args}
}

func cdkServiceCall(id, tool string) provider.ToolCall {
	return callWith(id, tool, map[string]any{"service_id": cdkServiceID})
}

// bigLoop leaves room for lists of many services.
func bigLoop(p provider.Provider, runner ToolRunner) *Loop {
	return newLoop(p, runner, func(l *Limits) { l.MaxResultBytes = 60_000 })
}

func TestInvestigator_SeesAndNamesItsSubjectsRandomLookingService(t *testing.T) {
	body := `{"name":"` + cdkService + `","running":0,"cluster":{"id":"` + cdkCluster.ID() + `","name":"` + cdkCluster.Cluster + `"},` +
		`"recent_events":[{"text":"(service ` + cdkService + `) was unable to place a task"}],"tags":[{"key":"password","value":"` + cdkWorker + `"}]}`
	summary := "The service " + cdkService + " in " + cdkCluster.Cluster + " cannot place its tasks."
	p := fake.New(calls(cdkServiceCall("c1", "get_service")), calls(submitCall(summary, cite("e1"), cite("e4"))))
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_service": body})), p, withTask(investigatorInput(), cdkTask())))
	want := envelope("get_service", `[e1] name: "`+cdkService+`"`+"\n[e2] running: 0\n"+
		`[e3] cluster: {"id":"`+cdkCluster.ID()+`","name":"`+cdkCluster.Cluster+`"}`+"\n"+
		`[e4] recent_events: {"text":"(service `+cdkService+`) was unable to place a task"}`+"\n"+
		`[e5] tags: {"key":"password","value":"[redacted]"}`)
	if got := resultText(t, requestAt(t, p, 1), "c1"); got != want {
		t.Errorf("sent\n%s\nwant\n%s", got, want)
	}
	if res.Summary != summary || res.SummaryReplaced {
		t.Errorf("summary = %q, replaced %v", res.Summary, res.SummaryReplaced)
	}
	if len(res.Evidence) != 2 || res.Evidence[0].Excerpt != `name: "`+cdkService+`"` || !strings.Contains(res.Evidence[1].Excerpt, cdkService) {
		t.Errorf("evidence = %+v", res.Evidence)
	}
	if res.NameIDs != nil {
		t.Errorf("an investigator reports ids: %q", res.NameIDs)
	}
}

func TestInvestigator_LogAnswersAndAlertAreScrubbedAsBefore(t *testing.T) {
	bodies := map[string]string{
		"get_service":      `{"name":"` + cdkService + `"}`,
		"get_service_logs": `{"lines":[{"message":"started ` + cdkService + `"},{"message":"on ` + cdkCluster.Cluster + `"}]}`,
		"search_logs":      `{"rows":[{"service_id":"` + cdkServiceID + `","message":"` + cdkWorker + ` is gone"}]}`,
	}
	task := cdkTask()
	task.Alert.Kind, task.Alert.Title, task.Alert.Text = cdkWorker, cdkService+" is down", "It is "+cdkService+" on "+cdkCluster.Cluster
	p := fake.New(calls(cdkServiceCall("c1", "get_service")), calls(cdkServiceCall("c2", "get_service_logs")),
		calls(callWith("c3", "search_logs", map[string]any{"service_ids": []any{cdkServiceID}, "query": "error"})), calls(submitCall(plainSummary, cite("e2"), cite("e4"))))
	res := requireResult(t, investigate(t, newLoop(p, replies(bodies)), p, withTask(investigatorInput(), task)))
	last := lastRequest(p)
	if got := resultText(t, last, "c1"); !strings.Contains(got, cdkService) {
		t.Fatalf("the service's own answer is %q", got)
	}
	next := 2
	for id, name := range map[string]string{"c2": "get_service_logs", "c3": "search_logs"} {
		if id == "c3" {
			next = 4
		} else {
			next = 2
		}
		shown, units := numberUnits(name, map[string]any{"service_id": cdkServiceID}, secretshape.ScrubText(bodies[name]), next)
		if len(units) == 0 || !strings.Contains(shown, secretshape.Redacted) {
			t.Fatalf("test setup: %s gives %d units: %s", name, len(units), shown)
		}
		if got := resultText(t, last, id); got != envelope(name, shown) {
			t.Errorf("%s: sent\n%s\nwant\n%s", name, got, envelope(name, shown))
		}
	}
	first := last.Messages[0].Text
	_, alert, _ := strings.Cut(first, "The alert:")
	if want := "\n" + envelope("alert", "kind: [redacted]\nopened_at: 2026-11-25T09:30:00Z\ntitle: [redacted] is down\ntext: It is [redacted] on [redacted]"); alert != want {
		t.Errorf("alert = %q, want %q", alert, want)
	}
	if len(res.Evidence) != 2 {
		t.Fatalf("evidence = %+v", res.Evidence)
	}
	for _, e := range res.Evidence {
		if e.Action != "logs" || !strings.Contains(e.Excerpt, secretshape.Redacted) || strings.Contains(e.Excerpt, cdkService) || strings.Contains(e.Excerpt, cdkWorker) {
			t.Errorf("item = %+v", e)
		}
	}
}

func TestInvestigator_NamesOutsideTheSubjectStayRedacted(t *testing.T) {
	body := listBody(cdkCluster, cdkService, cdkForeign)
	for summary, replaced := range map[string]bool{"It is " + cdkForeign + ".": true, "It is " + cdkService + ".": false} {
		p := fake.New(calls(callWith("c1", "list_services", nil)), calls(callWith("c2", "get_service", map[string]any{"service_id": cdkForeignID})), calls(submitCall(summary, cite("e2"))))
		runner := replies(map[string]string{"list_services": body, "get_service": `{"name":"` + cdkForeign + `"}`})
		res := requireResult(t, investigate(t, newLoop(p, runner), p, withTask(investigatorInput(), cdkTask())))
		for _, id := range []string{"c1", "c2"} {
			if got := resultText(t, lastRequest(p), id); strings.Contains(got, cdkForeign) || !strings.Contains(got, secretshape.Redacted) {
				t.Errorf("%s: sent %s", id, got)
			}
		}
		if got := resultText(t, lastRequest(p), "c1"); strings.Count(got, cdkService) != 1 {
			t.Errorf("the subject's own name is not shown once: %s", got)
		}
		if res.SummaryReplaced != replaced || res.NameIDs != nil || len(res.Evidence) != 1 || strings.Contains(res.Evidence[0].Excerpt, cdkForeign) {
			t.Errorf("%s: result = %+v", summary, res)
		}
	}

	// A subject gives names by the ids of its clusters and services and by nothing else.
	taskID := model.TaskID("123456789012", "eu-west-1", "arn:aws:ecs:eu-west-1:123456789012:task/"+cdkService+"/0123456789abcdef0123456789abcdef")
	if cluster, _, ok := secretshape.IDNames(taskID); !ok || cluster != cdkService {
		t.Fatal("test setup: no task id")
	}
	for label, subject := range map[string]TaskSubject{
		"names":        {Kind: "service", ServiceIDs: []string{cdkService}, ClusterID: cdkService, AccountID: cdkService, Region: cdkService},
		"a task id":    {Kind: "service", ServiceIDs: []string{taskID}, ClusterID: taskID},
		"another task": {Kind: "service", ServiceIDs: []string{checkoutID}, ClusterID: prodCluster.ID()},
	} {
		task := cdkTask()
		task.Subject = subject
		p := fake.New(calls(serviceCall("c1", "get_service")), calls(submitCall("It is "+cdkService+".")))
		res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_service": `{"name":"` + cdkService + `"}`})), p, withTask(investigatorInput(), task)))
		if got := resultText(t, lastRequest(p), "c1"); got != envelope("get_service", `[e1] name: "[redacted]"`) || !res.SummaryReplaced {
			t.Errorf("%s: sent %s, result %+v", label, got, res)
		}
	}
}

func TestReviewer_LearnsNamesFromAttributedIDsOnly(t *testing.T) {
	taskID := model.TaskID("123456789012", "eu-west-1", "arn:aws:ecs:eu-west-1:123456789012:task/"+cdkCluster.Cluster+"/0123456789abcdef0123456789abcdef")
	row := func(id, rest string) string { return `{"items":[{"id":"` + id + `"` + rest + `}]}` }
	for label, c := range map[string]struct {
		call    provider.ToolCall
		body    string
		failed  bool
		learned bool
	}{
		"a row":               {callWith("c1", "list_services", nil), row(cdkServiceID, `,"name":"`+cdkService+`"`), false, true},
		"a later row":         {callWith("c1", "list_services", nil), `{"items":[{"name":"` + cdkService + `"},{"id":"` + cdkServiceID + `"}]}`, false, true},
		"a nested row":        {costCall("c1"), `{"spot":{"services":[{"service_id":"` + cdkServiceID + `","service":"` + cdkService + `"}]}}`, false, true},
		"the argument":        {cdkServiceCall("c1", "get_service"), `{"id":"` + cdkServiceID + `","name":"` + cdkService + `"}`, false, true},
		"a tag value":         {callWith("c1", "list_services", nil), row(checkoutID, `,"tags":{"owner":"`+cdkServiceID+`"},"name":"`+cdkService+`"`), false, false},
		"an environment":      {serviceCall("c1", "get_task_definition"), `{"definition":{"containerDefinitions":[{"environment":[{"name":"SVC","value":"` + cdkServiceID + `"}]}]},"name":"` + cdkService + `"}`, false, false},
		"a docker label":      {serviceCall("c1", "get_task_definition"), `{"definition":{"containerDefinitions":[{"dockerLabels":{"service_id":"` + cdkServiceID + `"}}]},"name":"` + cdkService + `"}`, false, false},
		"an id in the body":   {serviceCall("c1", "get_service"), `{"id":"` + cdkServiceID + `","service_id":"` + cdkServiceID + `","name":"` + cdkService + `"}`, false, false},
		"outside the rows":    {callWith("c1", "list_services", nil), `{"id":"` + cdkServiceID + `","items":[],"extra":[{"id":"` + cdkServiceID + `","name":"` + cdkService + `"}]}`, false, false},
		"no attribution":      {callWith("c1", "list_security", nil), `{"images":[{"service_id":"` + cdkServiceID + `","service":"` + cdkService + `"}]}`, false, false},
		"a failed list":       {callWith("c1", "list_services", nil), row(cdkServiceID, `,"name":"`+cdkService+`"`), true, false},
		"a failed service":    {cdkServiceCall("c1", "get_service"), `{"id":"` + cdkServiceID + `","name":"` + cdkService + `"}`, true, false},
		"cut by the read cap": {callWith("c1", "list_services", nil), row(cdkServiceID, `,"name":"`+cdkService+`"`) + answerTooLargeNote, false, false},
		"a cluster id":        {callWith("c1", "list_services", nil), row(cdkCluster.ID(), `,"name":"`+cdkCluster.Cluster+`"`), false, false},
		"a task id":           {callWith("c1", "list_services", nil), row(taskID, `,"name":"`+cdkCluster.Cluster+`"`), false, false},
		"a name, no id":       {callWith("c1", "list_services", nil), row(cdkService, ""), false, false},
	} {
		t.Run(label, func(t *testing.T) {
			name := cdkService
			if !strings.Contains(c.body, name) {
				name = cdkCluster.Cluster
			}
			p := fake.New(calls(c.call), calls(callWith("c2", "get_overview", nil)), calls(submitCall("It is "+name+".")))
			runner := &recordingRunner{reply: func(tool string) (string, bool) {
				if tool == "get_overview" {
					return `{"text":"` + name + ` grew"}`, false
				}
				return c.body, c.failed
			}}
			res := requireResult(t, investigate(t, newLoop(p, runner), p, reviewerInput()))
			if len(runner.Calls()) != 2 {
				t.Fatalf("%d calls were run", len(runner.Calls()))
			}
			for _, id := range []string{"c1", "c2"} {
				if got := resultText(t, lastRequest(p), id); strings.Contains(got, name) != c.learned || strings.Contains(got, secretshape.Redacted) == c.learned {
					t.Errorf("%s: sent %s", id, got)
				}
			}
			if res.SummaryReplaced == c.learned || (len(res.NameIDs) > 0) != c.learned {
				t.Errorf("result = %+v", res)
			}
		})
	}

	// A log answer, which no review gets, would give none either.
	in := reviewerInput()
	r := &session{l: &Loop{}, in: in, found: newFindings(), tagCode: "aaa111", codes: regexp.MustCompile("aaa111")}
	r.readResult("search_logs", nil, `{"rows":[{"service_id":"`+cdkServiceID+`"}]}`)
	if res := r.readResult("get_overview", nil, `{"text":"`+cdkService+`"}`); res.data != `{"text":"[redacted]"}` || len(r.found.learned) != 0 {
		t.Errorf("learned from a log answer: %q, shown %q", r.found.learned, res.data)
	}
}

// The scope's clusters are the task's own.
func TestReviewer_SeesAndNamesTheClusterOfItsScope(t *testing.T) {
	body := `{"id":"` + cdkCluster.ID() + `","name":"` + cdkCluster.Cluster + `"}`
	summary := "The cluster " + cdkCluster.Cluster + " is close to full."
	for _, reviewType := range []string{"cost", "custom"} {
		for scoped, task := range map[bool]Task{true: cdkReview(reviewType), false: reviewTask(reviewType)} {
			if reviewType == "custom" {
				task.Review.Instructions = growthRequest
			}
			task.Subject = TaskSubject{ServiceIDs: []string{serviceIDOf(cdkCluster, "api")}} // a review has none
			p := fake.New(calls(callWith("c1", "get_cluster", map[string]any{"cluster_id": cdkCluster.ID()})), calls(submitCall(summary)))
			res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_cluster": body})), p, withTask(reviewerInput(), task)))
			want := strings.ReplaceAll(body, cdkCluster.Cluster, secretshape.Redacted)
			if scoped {
				want = body
			}
			if got := resultText(t, lastRequest(p), "c1"); got != envelope("get_cluster", want) {
				t.Errorf("%s, scoped %v: sent %s", reviewType, scoped, got)
			}
			if res.SummaryReplaced == scoped || res.NameIDs != nil {
				t.Errorf("%s, scoped %v: result %+v", reviewType, scoped, res)
			}
		}
	}
}

// listed runs a review of task that lists the services of cluster, reads the overview and submits.
func listed(t *testing.T, task Task, cluster model.ClusterKey, services []string, submit provider.ToolCall) agentresult.Result {
	t.Helper()
	p := fake.New(calls(callWith("c1", "list_services", nil)), calls(submit))
	return requireResult(t, investigate(t, bigLoop(p, replies(map[string]string{"list_services": listBody(cluster, services...)})), p, withTask(reviewerInput(), task)))
}

func TestReviewer_ReportsTheIDsItsResultUses(t *testing.T) {
	names := cdkNames(t, 40)
	ids := make([]string, len(names))
	inCDK := make([]string, len(names))
	for i, name := range names {
		ids[i], inCDK[i] = serviceIDOf(prodCluster, name), serviceIDOf(cdkCluster, name)
	}
	noteOf := func(ref, note string) map[string]any { return map[string]any{"ref": ref, "note": note} }
	for label, c := range map[string]struct {
		task     Task
		cluster  model.ClusterKey
		submit   provider.ToolCall
		want     []string
		replaced bool
	}{
		"nothing":                 {reviewTask("cost"), prodCluster, submitCall(plainSummary), nil, false},
		"in the order read":       {reviewTask("cost"), prodCluster, submitCall("First " + names[4] + ", then " + names[1] + " and again " + names[4] + "."), []string{ids[1], ids[4]}, false},
		"an excerpt":              {reviewTask("cost"), prodCluster, submitCall(plainSummary, cite("e6")), []string{ids[5]}, false},
		"an excerpt that is none": {reviewTask("cost"), prodCluster, submitCall(plainSummary, cite("e41")), nil, false},
		"a glued name":            {reviewTask("cost"), prodCluster, submitCall("It is " + names[4] + "-2 and " + names[1] + "."), []string{ids[1]}, true},
		"a note":                  {reviewTask("cost"), prodCluster, submitCall("It is "+names[1]+".", noteOf("e6", "It is "+names[8])), []string{ids[1], ids[5], ids[8]}, false},
		"a replaced note":         {reviewTask("cost"), prodCluster, submitCall("It is "+names[1]+".", noteOf("e6", "It is "+names[8]+strings.Repeat(" again", 50))), []string{ids[1], ids[5]}, false},
		"thirty-two":              {reviewTask("cost"), prodCluster, submitCall(strings.Join(names[:32], ", ")), ids[:32], false},
		"thirty-three":            {reviewTask("cost"), prodCluster, submitCall(strings.Join(names[:33], ", ")), ids[:32], true},
		"the last of them":        {reviewTask("cost"), prodCluster, submitCall("It is " + names[39] + "."), ids[39:], false},
		"a cluster, unscoped":     {reviewTask("cost"), cdkCluster, submitCall("It is " + names[39] + " in " + cdkCluster.Cluster + "."), []string{inCDK[0], inCDK[39]}, false},
		"a cluster, scoped":       {cdkReview("cost"), cdkCluster, submitCall("It is " + names[39] + " in " + cdkCluster.Cluster + "."), inCDK[39:], false},
		"a cluster's row":         {reviewTask("cost"), cdkCluster, submitCall("It is "+names[39]+".", cite("e8")), []string{inCDK[0], inCDK[7], inCDK[39]}, false},
		"the cluster alone":       {cdkReview("cost"), cdkCluster, submitCall("It is " + cdkCluster.Cluster + "."), nil, false},
	} {
		t.Run(label, func(t *testing.T) {
			res := listed(t, c.task, c.cluster, names, c.submit)
			if !slices.Equal(res.NameIDs, c.want) || res.SummaryReplaced != c.replaced {
				t.Errorf("ids %q\nwant %q\nreplaced %v, summary %q", res.NameIDs, c.want, res.SummaryReplaced, res.Summary)
			}
			raw, err := json.Marshal(res)
			if err != nil || strings.Contains(string(raw), "name_ids") != (c.want != nil) {
				t.Errorf("result %s, error %v", raw, err)
			}
			if label == "a replaced note" && (len(res.Evidence) != 1 || res.Evidence[0].Note != agentresult.ReplacementNote) {
				t.Errorf("evidence = %+v", res.Evidence)
			}
		})
	}

	// Services with ordinary names need no id: nothing hides them.
	plain := listed(t, cdkReview("cost"), cdkCluster, []string{"checkout-api", "payments-worker"}, submitCall("Both checkout-api and payments-worker run in "+cdkCluster.Cluster+"."))
	if plain.SummaryReplaced || plain.NameIDs != nil {
		t.Errorf("result = %+v", plain)
	}

	// Reading an id by its argument is enough; the name itself need not be read.
	p := fake.New(calls(cdkServiceCall("c1", "get_desired_count")), calls(submitCall(plainSummary, noteOf("e1", "It is "+cdkService))))
	res := requireResult(t, investigate(t, newLoop(p, replies(map[string]string{"get_desired_count": `{"desired":3}`})), p, reviewerInput()))
	if len(res.Evidence) != 1 || res.Evidence[0].Note != "It is "+cdkService || !slices.Equal(res.NameIDs, []string{cdkServiceID}) {
		t.Errorf("result = %+v", res)
	}
}

func TestReviewer_FinalCheckUsesOnlyTaskAndReportedIDs(t *testing.T) {
	names := cdkNames(t, 40)
	var learned []string
	for _, name := range names {
		learned = append(learned, serviceIDOf(prodCluster, name))
	}
	units := map[string]citedUnit{"e1": {tool: "list_services", serviceID: checkoutID, action: "view", excerpt: "running: 1"}}
	task := []string{cdkCluster.ID()}
	for label, c := range map[string]struct {
		summary, note string
		replaced      bool
		noteKept      bool
	}{
		"the 33rd name in a note":  {strings.Join(names[:32], ", "), "It is " + names[32], false, false},
		"the 32nd name in a note":  {strings.Join(names[:31], ", "), "It is " + names[31], false, true},
		"the 33rd in the summary":  {strings.Join(names[:33], ", "), plainNote, true, true},
		"beside the task's":        {cdkCluster.Cluster + " runs " + names[7], "It is " + cdkCluster.Cluster, false, true},
		"a name that is not known": {"It is " + cdkForeign, "It is " + cdkForeign, true, false},
	} {
		result := buildResult(submission{summary: c.summary, evidence: []citation{{ref: "e1", note: c.note}}}, units, &agentresult.ReadCorpus{}, agentresult.ReplacementReviewSummary, task, learned)
		if result.SummaryReplaced != c.replaced || len(result.Evidence) != 1 || (result.Evidence[0].Note == c.note) != c.noteKept || len(result.NameIDs) > 32 {
			t.Errorf("%s: result = %+v", label, result)
		}
		// What the reader of the result repeats with the ids it was given comes to the same verdict.
		final := secretshape.NamesOf(append(slices.Clone(task), result.NameIDs...)...)
		if clean, replaced := agentresult.RecheckSummaryNaming(c.summary, agentresult.ReplacementReviewSummary, final); clean != result.Summary || replaced != result.SummaryReplaced {
			t.Errorf("%s: checked again: %q, replaced %v", label, clean, replaced)
		}
		if note := agentresult.RecheckNoteNaming(c.note, final); note != result.Evidence[0].Note {
			t.Errorf("%s: note checked again: %q, result %q", label, note, result.Evidence[0].Note)
		}
	}
}

// Thousands of ids of one cluster, whose name each of them gives, must not cost more than their number.
func TestReviewer_ManyLearnedIDsAreLinear(t *testing.T) {
	var learned []string
	for i := range 20_000 {
		learned = append(learned, serviceIDOf(cdkCluster, fmt.Sprintf("Shop-%05dF3A9B2C1D4-Xy7KpQ2mNvR", i)))
	}
	started := time.Now()
	result := buildResult(submission{summary: "It is " + cdkCluster.Cluster}, nil, &agentresult.ReadCorpus{}, agentresult.ReplacementReviewSummary, nil, learned)
	if took := time.Since(started); took > 15*time.Second {
		t.Errorf("took %s", took)
	}
	if !slices.Equal(result.NameIDs, learned[:1]) || result.SummaryReplaced {
		t.Errorf("%d ids, result %+v", len(result.NameIDs), result)
	}
}

func TestChat_IsUnchanged(t *testing.T) {
	bodies := map[string]string{"list_services": listBody(cdkCluster, cdkService), "get_service": `{"name":"` + cdkService + `"}`}
	p := fake.New(calls(callWith("c1", "list_services", nil)), calls(cdkServiceCall("c2", "get_service")), fake.Text("It runs.", smallUsage))
	in := baseInput()
	in.Page = PageContext{Path: "/services/" + cdkServiceID, ServiceID: cdkServiceID}
	if _, err := run(t, fixedNonces(newLoop(p, replies(bodies))), in); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"c1": "list_services", "c2": "get_service"} {
		want := envelope(name, secretshape.ScrubText(bodies[name]))
		if got := resultText(t, lastRequest(p), id); got != want || strings.Contains(got, cdkService) || !strings.Contains(got, secretshape.Redacted) {
			t.Errorf("%s: sent %s, want %s", name, got, want)
		}
	}
}

func TestNames_AreNotLogged(t *testing.T) {
	var log bytes.Buffer
	names := cdkNames(t, 3)
	p := fake.New(calls(callWith("c1", "list_services", nil)), calls(submitCall("It is "+names[1]+".")))
	l := newLoop(p, replies(map[string]string{"list_services": listBody(cdkCluster, names...)}))
	l.Log = slog.New(slog.NewJSONHandler(&log, nil))
	if res := requireResult(t, investigate(t, l, p, withTask(reviewerInput(), cdkReview("cost")))); len(res.NameIDs) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(log.String(), "review finished") {
		t.Fatal("nothing was logged")
	}
	for _, name := range append(names, cdkCluster.Cluster) {
		if strings.Contains(log.String(), name) {
			t.Errorf("the log names %s", name)
		}
	}
	for _, name := range names {
		if strings.Contains(log.String(), serviceIDOf(cdkCluster, name)) {
			t.Errorf("the log holds the id of %s", name)
		}
	}
}

// Before names were kept, 20 of 20 such summaries were replaced.
func TestSummariesNamingCDKServices_NoneReplaced(t *testing.T) {
	names := cdkNames(t, 10)
	fixture := func(reviewer bool, name, summary string) agentresult.Result {
		id := serviceIDOf(cdkCluster, name)
		body := `{"name":"` + name + `","running":0,"cluster":{"id":"` + cdkCluster.ID() + `","name":"` + cdkCluster.Cluster + `"},"recent_events":[{"text":"(service ` + name + `) has stopped 1 running tasks"}]}`
		get := callWith("c2", "get_service", map[string]any{"service_id": id})
		in := withTask(reviewerInput(), reviewTask("reliability"))
		if !reviewer {
			task := cdkTask()
			task.Subject.ServiceIDs = []string{id}
			in = withTask(investigatorInput(), task)
		}
		p := fake.New(calls(callWith("c1", "list_services", nil)), calls(get), calls(submitCall(summary, cite("e1"), cite("e3"))))
		return requireResult(t, investigate(t, bigLoop(p, replies(map[string]string{"list_services": listBody(cdkCluster, names...), "get_service": body})), p, in))
	}
	kept, runs := 0, 0
	for i, name := range names {
		for _, reviewer := range []bool{false, true} {
			summary := []string{
				"The service " + name + " cannot start its tasks, so nothing serves traffic.",
				"In " + cdkCluster.Cluster + ", " + name + " is the least available: check its task definition.",
				"Tasks of \"" + name + "\" stop after a minute (" + name + ").",
			}[i%3]
			res := fixture(reviewer, name, summary)
			runs++
			if !res.SummaryReplaced && res.Summary == summary {
				kept++
			} else {
				t.Errorf("reviewer %v: replaced %q", reviewer, summary)
			}
			for label, control := range map[string]string{
				"glued before": "The service x" + name + " fails.", "glued after": "The service " + name + "-2 fails.",
				"in a path": "Open /services/" + name + "/logs first.", "two names": "The services " + name + "/" + cdkCluster.Cluster + " fail.",
				"another case": "The service " + strings.Replace(name, "Xy7", "xY7", 1) + " fails.", "assigned": "The service " + name + "= fails.",
				"a password": "Set password=" + name + " again.", "a token beside": "The service " + name + " uses " + randomToken + ".",
				"forged": "The service " + cdkForeign + " fails.",
			} {
				if res := fixture(reviewer, name, control); !res.SummaryReplaced {
					t.Errorf("reviewer %v, %s: kept %q", reviewer, label, res.Summary)
				}
			}
		}
	}
	if runs != 20 || kept != 20 {
		t.Errorf("%d of %d summaries kept", kept, runs)
	}
}
