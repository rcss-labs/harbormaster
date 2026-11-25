package review

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent"
	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const namesBase = "https://hm.example.com"

// Names as CDK makes them: the scrubber takes them for random tokens.
var (
	cdkCluster = model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"}
	// A random token that is no name, assembled so that secret scanners do not flag it.
	randomToken = "Qm4Zx81LtVb7" + "RkPw29HsJd6Ny3Fc5Ge0UaTi"
)

// cdkKeys are n services of the cluster with different names that look random.
func cdkKeys(t *testing.T, cluster model.ClusterKey, n int) []model.ServiceKey {
	t.Helper()
	keys := make([]model.ServiceKey, n)
	for i := range keys {
		keys[i] = model.ServiceKey{ClusterKey: cluster, Service: fmt.Sprintf("Shop-%c%02dF3A9B2C1D4E5-Xy7KpQ2mNvR", 'A'+i%26, i)}
		if secretshape.ScrubText(keys[i].Service) != secretshape.Redacted {
			t.Fatalf("test setup: %q is not hidden", keys[i].Service)
		}
	}
	return keys
}

type toolBodies map[string]string

func (b toolBodies) Run(_ context.Context, _, name string, _ map[string]any) (string, bool) {
	return b[name], false
}

// listBody is a list_services answer.
func listBody(keys []model.ServiceKey) string {
	var rows []string
	for _, key := range keys {
		rows = append(rows, `{"id":"`+key.ID()+`","name":"`+key.Service+`","cluster":{"id":"`+key.ClusterKey.ID()+`","name":"`+key.Cluster+`"},"running":1}`)
	}
	return `{"items":[` + strings.Join(rows, ",") + `]}`
}

// agentRun is what the agent service's own loop makes of a task whose model lists the services and submits summary, with
// one piece of evidence.
func agentRun(t *testing.T, role string, task agentclient.Task, listed []model.ServiceKey, summary, ref, note string) agentresult.Result {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var given agent.Task
	if err := json.Unmarshal(raw, &given); err != nil {
		t.Fatal(err)
	}
	usage := provider.Usage{InputTokens: 100, OutputTokens: 20}
	loop := &agent.Loop{
		Provider: fake.New(fake.ToolCalls(usage, provider.ToolCall{ID: "c1", Name: "list_services", Arguments: map[string]any{}}),
			fake.ToolCalls(usage, provider.ToolCall{ID: "c2", Name: "submit_result", Arguments: map[string]any{"summary": summary, "evidence": []any{map[string]any{"ref": ref, "note": note}}}})),
		Tools:  toolBodies{"list_services": listBody(listed)},
		Limits: agent.Limits{MaxSteps: 10, MaxRunTokens: 1_000_000, Timeout: time.Minute, MaxResultBytes: 60_000},
	}
	var result *agentresult.Result
	if err := loop.Run(context.Background(), agent.RunInput{RunID: "run-1", Role: role, Bearer: "run-token", Task: &given, MaxCostMicroUSD: 100_000_000,
		Model: provider.Model{ID: "fake-model", InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000, ContextTokens: 1_000_000}},
		func(ev agent.Event) {
			if ev.Result != nil {
				result = ev.Result
			}
		}); err != nil || result == nil {
		t.Fatalf("the agent's run gave no result: %v", err)
	}
	// Over the wire.
	if raw, err = json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	var received agentresult.Result
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatal(err)
	}
	return received
}

// agentScript answers a run of the fixture's fake agent with what the agent service's own loop makes of it.
func agentScript(t *testing.T, listed []model.ServiceKey, summary string) func(c *call) {
	return func(c *call) {
		c.send(usage(4200), resultEvent(agentRun(t, c.body.Role, *c.body.Task, listed, summary, "e1", "It is listed.")), doneEvent)
	}
}

// sentReview is the text of every destination for a stored run.
func sentReview(t *testing.T, v store.ReviewRun) string {
	t.Helper()
	m := notify.Message{Event: notify.EventReview, BaseURL: namesBase, Review: &notify.Review{RunID: v.ID, Name: v.ReviewName, Type: v.ReviewType, PeriodLabel: v.PeriodLabel, Summary: v.Summary, NameIDs: v.SummaryNameIDs}}
	slack, err := notify.SlackBody(m)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Blocks []struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(slack, &body); err != nil || len(body.Blocks) < 2 {
		t.Fatalf("%s: %v", slack, err)
	}
	return body.Blocks[1].Text.Text
}

// Half of the twenty runs that named such a service, all of which were replaced before; the investigations are beside
// their runner.
func TestNamedSummary_TenReviewsThroughTheWholePath(t *testing.T) {
	f := newFixture(t)
	keys := cdkKeys(t, cdkCluster, 10)
	f.services(keys...)
	kept := 0
	for i, key := range keys {
		summary := []string{
			"The service " + key.Service + " cannot start its tasks, so nothing serves traffic.",
			"In " + cdkCluster.Cluster + ", " + key.Service + " is the least available: check its task definition.",
			"Tasks of \"" + key.Service + "\" stop after a minute (" + key.Service + ").",
		}[i%3]
		scoped := i%2 == 0
		f.agent.set(agentScript(t, keys, summary))
		v := f.runTo(f.due(fmt.Sprint("Review ", i), func(r *store.Review) {
			if scoped {
				r.Scope = []store.NotificationSelector{{Kind: "cluster", AccountID: cdkCluster.AccountID, Region: cdkCluster.Region, Cluster: cdkCluster.Cluster}}
			}
		}), "done")
		if v.Summary == summary && !v.SummaryReplaced && sentReview(t, v) == summary {
			kept++
		} else {
			t.Errorf("scoped %v: %q was stored as %q with %q and sent as %q", scoped, summary, v.Summary, v.SummaryNameIDs, sentReview(t, v))
		}
		f.clock.advance(time.Second)
	}
	if kept != 10 {
		t.Errorf("%d of 10 summaries kept", kept)
	}
}

func TestNamedSummary_ReplacedWhenTheServiceIsGoneAtAccept(t *testing.T) {
	keys := cdkKeys(t, checkoutKey.ClusterKey, 3)
	summary := "The service " + keys[1].Service + " cannot start its tasks."
	for label, change := range map[string]func(f *fixture){
		"still there":             func(*fixture) {},
		"gone":                    func(f *fixture) { f.exec(`delete from services where service_name = $1`, keys[1].Service) },
		"no longer visible":       func(f *fixture) { f.tagService(keys[1], f.tag("hidden")) },
		"another service is gone": func(f *fixture) { f.exec(`delete from services where service_name = $1`, keys[0].Service) },
	} {
		t.Run(label, func(t *testing.T) {
			f := newFixture(t)
			f.services(keys...)
			id := f.due("Daily spend")
			script := agentScript(t, keys, summary)
			f.agent.set(func(c *call) { change(f); script(c) })
			v := f.runTo(id, "done")
			want, ids := summary, []string{keys[1].ID()}
			if label == "gone" || label == "no longer visible" {
				want, ids = agentresult.ReplacementReviewSummary, nil
			}
			if v.Summary != want || v.SummaryReplaced != (ids == nil) || !slices.Equal(v.SummaryNameIDs, ids) || sentReview(t, v) != want {
				t.Errorf("stored %q (replaced %v) with %q, sent %q; want %q with %q", v.Summary, v.SummaryReplaced, v.SummaryNameIDs, sentReview(t, v), want, ids)
			}
		})
	}
}

// The cluster of the scope is the task's own: the agent reports no id for it, and none is needed.
func TestNamedSummary_OfTheScopesClusterAlone(t *testing.T) {
	f := newFixture(t)
	keys := cdkKeys(t, cdkCluster, 2)
	f.services(keys...)
	summary := "The cluster " + cdkCluster.Cluster + " is close to full."
	f.agent.set(func(c *call) { // nothing is cited, so no service's own name is used
		c.send(usage(4200), resultEvent(agentRun(t, c.body.Role, *c.body.Task, keys, summary, "e99", "")), doneEvent)
	})
	for _, scoped := range []bool{true, false} {
		v := f.runTo(f.due(fmt.Sprint("Scoped ", scoped), func(r *store.Review) {
			if scoped {
				r.Scope = []store.NotificationSelector{{Kind: "cluster", AccountID: cdkCluster.AccountID, Region: cdkCluster.Region, Cluster: cdkCluster.Cluster}}
			}
		}), "done")
		ids := []string{keys[0].ID()} // learned from the list
		if scoped {
			ids = []string{cdkCluster.ID()}
		}
		if v.Summary != summary || v.SummaryReplaced || !slices.Equal(v.SummaryNameIDs, ids) || sentReview(t, v) != summary {
			t.Errorf("scoped %v: stored %q with %q, sent %q", scoped, v.Summary, v.SummaryNameIDs, sentReview(t, v))
		}
		f.clock.advance(time.Second)
	}
}

// A run without names that look random stores what it did: no id, not even one that was reported.
func TestNamedSummary_OrdinaryRunsStoreNoIDs(t *testing.T) {
	f := newFixture(t)
	res := agentresult.Result{Summary: "The service checkout-api in payments-prod is down.", NameIDs: []string{checkoutKey.ID(), cdkKeys(t, cdkCluster, 1)[0].ID()}}
	f.agent.set(func(c *call) { c.send(usage(4200), resultEvent(res), doneEvent) })
	v := f.runTo(f.due("Daily spend"), "done")
	if v.Summary != res.Summary || v.SummaryReplaced || len(v.SummaryNameIDs) != 0 || sentReview(t, v) != res.Summary {
		t.Errorf("run = %+v", v)
	}
}

// The agent, the check when the result is stored and the check at each send come to one verdict. HM_NAMES_DIFF=full
// runs all 200,000 generated summaries.
func TestNamedSummary_SameVerdictAtAgentAcceptAndSend(t *testing.T) {
	const batches, size = 40, 5000
	run, texts := batches, 100
	if os.Getenv("HM_NAMES_DIFF") == "full" {
		texts = size
	}
	for batch := range run {
		t.Run(fmt.Sprintf("seed_%02d", batch), func(t *testing.T) {
			t.Parallel()
			agreement(t, int64(batch), texts)
		})
	}
}

// agreement makes n summaries of one seed and requires the same verdict for each at the agent, when stored and when sent.
func agreement(t *testing.T, seed int64, n int) {
	rng := rand.New(rand.NewSource(seed))
	own := cdkKeys(t, cdkCluster, 40)
	elsewhere := cdkCluster
	elsewhere.AccountID, elsewhere.Cluster = "731904428817", "Other-ClusterC40D77E2-qZ4nTb8WxYk6Lr"
	// A request may hold a name that looks random only where the scrubber lets it stand, as in an ARN.
	inARN := "arn:aws:ecs:us-east-1:482109375512:service/" + cdkCluster.Cluster + "/LedgerStack-ReportServiceE81D52A7-hJ6mVc3RzTq9"
	foreign := model.ServiceKey{ClusterKey: elsewhere, Service: "LedgerStack-LedgerServiceC40D77E2-qZ4nTb8WxYk6"}
	count := map[string]int{}
	pick := func(list []string) string { return list[rng.Intn(len(list))] }
	for range n {
		review := rng.Intn(2) == 0
		listed := own[:3+rng.Intn(4)]
		if rng.Intn(20) == 0 {
			listed = own
		}
		gone := model.ServiceKey{}
		if rng.Intn(4) == 0 {
			gone = listed[rng.Intn(len(listed))]
		}
		task := agentclient.Task{Kind: "investigate", Subject: agentclient.TaskSubject{Kind: "service", ClusterID: cdkCluster.ID(), ServiceIDs: []string{own[0].ID(), own[1].ID()}, AccountID: cdkCluster.AccountID, Region: cdkCluster.Region}}
		role, replacement, clean := "investigator", agentresult.ReplacementSummary, notify.CleanSummary
		if review {
			role, replacement, clean = "reviewer", agentresult.ReplacementReviewSummary, notify.CleanReviewSummary
			task = agentclient.Task{Kind: "review", Review: &agentclient.TaskReview{Type: "cost", Range: "7d", Period: "Week 48, 2026"}, Scope: &agentclient.TaskScope{Entries: []agentclient.ScopeEntry{}, ServicesTotal: 42}}
			if rng.Intn(2) == 0 {
				task.Scope.Entries = append(task.Scope.Entries, agentclient.ScopeEntry{Kind: "cluster", ClusterID: cdkCluster.ID(), Name: "shop"})
			}
		}

		request, requestWords := "", []string{"The service"}
		if review && rng.Intn(3) == 0 {
			task.Review.Type = "custom"
			request = "Compare the running cost of every service this week with the week before, " + pick([]string{"", "of " + inARN + " too, "}) +
				"and say which of them moved the most and since when that has been so."
			if agentresult.CheckInstructions(request) != agentresult.InstructionsOK {
				t.Fatalf("test setup: the request is refused: %q", request)
			}
			task.Review.Instructions = request
			requestWords = []string{request, request[:100], request[len(request)-90:], request[20:90], inARN, strings.ToUpper(request)}
		}

		names := []string{cdkCluster.Cluster}
		for _, key := range listed {
			names = append(names, key.Service)
		}
		piece := func() string {
			name := pick(names)
			switch rng.Intn(24) {
			case 0:
				return pick([]string{"x", "_", "-", "/", "9"}) + name
			case 1:
				return name + pick([]string{"x", "_", "-2", "/logs", "="})
			case 2:
				return pick(names) + pick([]string{"/", "-", "", "_"}) + name
			case 3:
				return pick([]string{foreign.Service, elsewhere.Cluster, own[39].Service})
			case 4:
				return pick([]string{randomToken, "AKIA" + "IOSFODNN7EXAMPLE", "ghp_" + strings.Repeat("a1B2", 9), name[1:], name[:len(name)-1], strings.ToLower(name)})
			case 5:
				return pick([]string{"password=", "token: ", "Bearer ", `"secret":"`, "https://ops:"}) + name
			case 6:
				return pick([]string{"https://evil.example/x ", "[" + name + "](https://evil.example/x) ", "https://hm.example.com/services/x ", "https://evil.example/" + name, "www.evil.io/ "})
			case 7, 8:
				return `{"finding":"one` + pick([]string{`\n`, `\t`, `: `, ` `}) + name + pick([]string{"", " fails"}) + `"}`
			case 9:
				return strings.Repeat("again and ", rng.Intn(140))
			case 10:
				return pick([]string{"\n", "\t", "\u202e", "故障 ", "é"})
			}
			return pick([]string{"", "\"", "("}) + name + pick([]string{"", "\"", ")", ",", ".", ":"})
		}
		var parts []string
		for k := rng.Intn(5); k >= 0; k-- {
			parts = append(parts, pick([]string{"The service", "It is", "fails in", "and", "costs more than", "", pick(requestWords)}), piece())
		}
		if rng.Intn(60) == 0 {
			parts = nil
			for _, key := range own[:20+rng.Intn(20)] {
				parts = append(parts, key.Service)
			}
		}
		summary := strings.Join(parts, pick([]string{" ", " ", "\n"}))
		note := "It is " + piece()
		if strings.TrimSpace(summary) == "" {
			continue
		}

		got := agentRun(t, role, task, listed, summary, fmt.Sprint("e", 1+rng.Intn(len(listed))), note)
		visible := func(key model.ServiceKey, action authz.Action) bool {
			return key != gone && slices.Contains(own, key) && action == authz.View
		}
		taskIDs := agentjob.TaskIDs(task)
		accepted := agentjob.Accept(got, visible, agentjob.AcceptOptions{AllowLogs: !review, Replacement: replacement, Echo: request, TaskIDs: taskIDs})
		sent := clean(accepted.Summary, namesBase, secretshape.NamesOf(accepted.NameIDs...))
		fail := func(what string) {
			t.Helper()
			t.Fatalf("%s: %s of %q\nagent %q (replaced %v) ids %q\nstored %q (replaced %v) ids %q\nsent %q", what, role, summary, got.Summary, got.SummaryReplaced, got.NameIDs, accepted.Summary, accepted.SummaryReplaced, accepted.NameIDs, sent)
		}

		allowed, verify := map[string]bool{}, true
		for _, id := range append(slices.Clone(taskIDs), got.NameIDs...) {
			if key, err := model.ParseServiceID(id); err == nil && key == gone && !slices.Contains(taskIDs, id) {
				verify = false
				continue
			}
			cluster, service, _ := secretshape.IDNames(id)
			allowed[cluster], allowed[service] = true, true
		}
		for _, id := range accepted.NameIDs {
			if !slices.Contains(taskIDs, id) && (!slices.Contains(got.NameIDs, id) || id == gone.ID()) {
				fail("an id that does not verify is stored")
			}
		}
		for _, key := range append(slices.Clone(own), foreign) {
			for _, name := range []string{key.Service, key.Cluster} {
				if allowed[name] {
					continue
				}
				for _, text := range append([]string{accepted.Summary, sent}, evidenceTexts(accepted)...) {
					if strings.Contains(text, name) {
						fail("a name outside the task and the verified ids, " + name + ", is shown in " + text)
					}
				}
			}
		}
		for _, text := range append([]string{accepted.Summary, sent}, evidenceTexts(accepted)...) {
			if strings.Contains(text, randomToken) || strings.Contains(text, "AKIA") || strings.Contains(text, "ghp_") {
				fail("a secret is shown in " + text)
			}
		}
		stripped := agentresult.StripForeignURLs(summary, namesBase)
		// What a send with every id that Accept used makes of the summary; without a foreign link, the summary itself.
		fullSend := func() string {
			full := slices.DeleteFunc(append(slices.Clone(taskIDs), got.NameIDs...), func(id string) bool { return id == gone.ID() && !slices.Contains(taskIDs, id) })
			want := clean(summary, namesBase, secretshape.NamesOf(full...))
			if stripped == summary && want != summary {
				fail("a send with every id replaces a summary without links")
			}
			return want
		}
		named := !got.SummaryReplaced && secretshape.ScrubText(summary) != summary
		switch {
		case got.SummaryReplaced:
			if got.Summary != replacement || accepted.Summary != replacement || !accepted.SummaryReplaced || sent != replacement || accepted.NameIDs != nil {
				fail("replaced at the agent and not after")
			}
			count["replaced"]++
			if request != "" && agentresult.Echoes(summary, request) {
				count["replaced for the request"]++
			}
		case verify:
			if got.Summary != summary || accepted.Summary != summary || accepted.SummaryReplaced {
				fail("kept at the agent and not when stored")
			}
			if sent != fullSend() {
				fail("stored and not sent")
			}
			if named {
				count["kept for its names"]++
			}
			if named && stripped != summary && sent == stripped {
				count["kept beside a link"]++
			}
			if named && request != "" && strings.Contains(summary, inARN) {
				count["kept beside part of the request"]++
			}
			if named && strings.Contains(summary, `\n`) {
				count["kept after an escape"]++
			}
			cited := slices.DeleteFunc(slices.Clone(got.Evidence), func(e agentresult.Evidence) bool { return e.ServiceID == gone.ID() })
			if len(accepted.Evidence) != len(cited) {
				fail("evidence lost")
			}
			for i, item := range accepted.Evidence {
				// A name beyond the 32 ids an answer reports is hidden in an excerpt; every other excerpt is the agent's.
				if item.Note != cited[i].Note || item.Excerpt != cited[i].Excerpt && len(got.NameIDs) < 32 {
					fail("evidence kept at the agent and not when stored: " + item.Note + " " + item.Excerpt)
				}
			}
		case accepted.SummaryReplaced:
			if accepted.Summary != replacement || sent != replacement || accepted.NameIDs != nil {
				fail("replaced when stored and not when sent")
			}
			count["replaced when stored"]++
		default:
			if accepted.Summary != summary || sent != fullSend() {
				fail("stored without the id that is gone and not sent")
			}
			count["kept without the id that is gone"]++
		}
	}
	t.Log(count)
	for _, kind := range []string{"replaced", "kept for its names", "kept beside a link", "kept after an escape", "replaced when stored", "replaced for the request"} {
		if n >= 5000 && count[kind] < n/1000 {
			t.Errorf("%s: only %d of %d", kind, count[kind], n)
		}
	}
}

func evidenceTexts(res store.InvestigationResult) (texts []string) {
	for _, item := range res.Evidence {
		texts = append(texts, item.Note, item.Excerpt)
	}
	return texts
}

// The agent compares a result with the request as it was shown, scrubbed, and Harbormaster with the request as it is
// stored. The two are one text: a request that holds a name that looks random never reaches the agent, though the name
// is the scope's own.
func TestNamedSummary_ARequestWithAKeptNameNeverRuns(t *testing.T) {
	f := newCustomFixture(t)
	key := cdkKeys(t, cdkCluster, 1)[0]
	f.services(key)
	text := "Say how the cost of " + key.Service + " in " + cdkCluster.Cluster + " moved."
	if agentresult.CheckInstructions(text) != agentresult.InstructionsSecret {
		t.Fatal("the text can be saved")
	}
	id := f.due("Weekly words", custom("Say how the cost moved."), func(r *store.Review) {
		r.Scope = []store.NotificationSelector{{Kind: "cluster", AccountID: cdkCluster.AccountID, Region: cdkCluster.Region, Cluster: cdkCluster.Cluster}}
	})
	f.edit(id, text) // saved under older rules, or not through the API
	f.want(f.runTo(id, "skipped"), "skipped", "instructions")
	f.unspent()
}
