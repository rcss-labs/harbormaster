package agentjob

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Names as CDK makes them: the scrubber takes them for random tokens.
var (
	cdkCluster = model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"}
	cdkService = model.ServiceKey{ClusterKey: cdkCluster, Service: "CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt"}
	cdkWorker  = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"}
)

// cdkKeys are n services of an ordinary cluster with different names that look random.
func cdkKeys(t *testing.T, n int) []model.ServiceKey {
	t.Helper()
	keys := make([]model.ServiceKey, n)
	for i := range keys {
		keys[i] = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: fmt.Sprintf("Shop-%c%02dF3A9B2C1D4E5-Xy7KpQ2mNvR", 'A'+i%26, i)}
		if secretshape.ScrubText(keys[i].Service) != secretshape.Redacted {
			t.Fatalf("test setup: %q is not hidden", keys[i].Service)
		}
	}
	return keys
}

func idsOf(keys []model.ServiceKey) []string {
	var ids []string
	for _, key := range keys {
		ids = append(ids, key.ID())
	}
	return ids
}

// requireSendKeeps requires that the check at send time, which has the stored ids only, lets the stored summary stand.
func requireSendKeeps(t *testing.T, label string, got store.InvestigationResult) {
	t.Helper()
	if again, replaced := agentresult.RecheckSummaryNaming(got.Summary, agentresult.ReplacementSummary, secretshape.NamesOf(got.NameIDs...)); replaced || again != got.Summary {
		t.Errorf("%s: with the stored ids %q the summary %q is replaced", label, got.NameIDs, got.Summary)
	}
}

func TestAccept_KeepsASummaryNamingATaskService(t *testing.T) {
	both := []string{cdkCluster.ID(), cdkService.ID()}
	inJSON := `{"finding":"one\n` + cdkService.Service + ` is down"}`
	if !(len(secretshape.NamesOf(both...).NamedIn(inJSON, both...)) == 0 && secretshape.ScrubTextKeeping(inJSON, secretshape.NamesOf(both...)) == inJSON) {
		t.Fatal("test setup: the name after the escape is found")
	}
	for label, c := range map[string]struct {
		summary string
		ids     []string
		want    []string
	}{
		"the service":           {"The service " + cdkService.Service + " cannot place its tasks.", both, both[1:]},
		"the cluster":           {"Nothing runs in " + cdkCluster.Cluster + ".", both, both},
		"a cluster alone":       {"Nothing runs in " + cdkCluster.Cluster + ".", both[:1], both[:1]},
		"both":                  {cdkService.Service + " in " + cdkCluster.Cluster + " is down.", both, both},
		"no name":               {"The service stopped.", both, nil},
		"an id given twice":     {cdkService.Service + " is down.", []string{both[1], both[0], both[1]}, both[1:]},
		"after a JSON escape":   {inJSON, both, both},
		"an escape, ids twice":  {inJSON, append(slices.Clone(both), both...), both},
		"one of each":           {strings.ReplaceAll(inJSON, cdkService.Service, cdkWorker.Service) + "\nIn " + cdkCluster.Cluster + ".", []string{cdkService.ID(), cdkWorker.ID(), cdkCluster.ID()}, []string{cdkService.ID(), cdkWorker.ID(), cdkCluster.ID()}},
		"an ordinary id beside": {cdkService.Service + " is down.", append([]string{checkoutKey.ID()}, both...), both[1:]},
	} {
		for name, opts := range map[string]AcceptOptions{"investigation": investigationOptions, "review": reviewOptions} {
			opts.TaskIDs = c.ids
			got := Accept(agentresult.Result{Summary: c.summary}, nil, opts) // the task's own ids need no answer
			if got.Summary != c.summary || got.SummaryReplaced || !slices.Equal(got.NameIDs, c.want) {
				t.Errorf("%s, %s: summary %q, replaced %v, ids %q; want ids %q", label, name, got.Summary, got.SummaryReplaced, got.NameIDs, c.want)
			}
			requireSendKeeps(t, label, got)
		}
	}
	// An id of the task that is reported too is stored once.
	opts := investigationOptions
	opts.TaskIDs = both
	if got := Accept(agentresult.Result{Summary: inJSON, NameIDs: both}, readAll, opts); !slices.Equal(got.NameIDs, both) {
		t.Errorf("reported too: ids %q", got.NameIDs)
	}
	// More ids than can be stored: what could not be sent as it is stored is replaced.
	opts.TaskIDs = nil
	for i := range store.MaxSummaryNameIDs + 1 {
		opts.TaskIDs = append(opts.TaskIDs, model.ServiceKey{ClusterKey: cdkCluster, Service: fmt.Sprint("api-", i)}.ID())
	}
	if got := Accept(agentresult.Result{Summary: "Nothing runs in " + cdkCluster.Cluster + "."}, nil, opts); !got.SummaryReplaced || got.Summary != agentresult.ReplacementSummary || got.NameIDs != nil {
		t.Errorf("too many ids: %+v", got)
	}
	// What the task holds says nothing about what the answer claims, and the other way round.
	for label, opts := range map[string]AcceptOptions{"no ids": reviewOptions, "another task": {Replacement: agentresult.ReplacementReviewSummary, TaskIDs: []string{cdkWorker.ID()}}} {
		got := Accept(agentresult.Result{Summary: "It is " + cdkService.Service + "."}, readAll, opts)
		if got.Summary != agentresult.ReplacementReviewSummary || !got.SummaryReplaced || got.NameIDs != nil {
			t.Errorf("%s: %+v", label, got)
		}
	}
}

func TestAccept_KeepsASummaryNamingAVerifiedReportedService(t *testing.T) {
	summary := "The service " + cdkService.Service + " costs more than " + cdkWorker.Service + "."
	res := agentresult.Result{Summary: summary, NameIDs: []string{cdkWorker.ID(), cdkService.ID()}}
	var asked []string
	viewOnly := func(key model.ServiceKey, action authz.Action) bool {
		asked = append(asked, key.Service)
		return action == authz.View
	}
	opts := reviewOptions
	opts.TaskIDs = []string{cdkCluster.ID()}
	got := Accept(res, viewOnly, opts)
	if got.Summary != summary || got.SummaryReplaced || !slices.Equal(got.NameIDs, res.NameIDs) {
		t.Errorf("summary %q, replaced %v, ids %q", got.Summary, got.SummaryReplaced, got.NameIDs)
	}
	if !slices.Equal(asked, []string{cdkWorker.Service, cdkService.Service}) {
		t.Errorf("asked about %q, want each service once", asked)
	}
	requireSendKeeps(t, "two services", got)

	// Only the ids of the stored summary are stored, whatever else was reported.
	res.Summary = "It is " + cdkWorker.Service + "."
	res.Evidence = []agentresult.Evidence{viewItem(`name: "` + cdkService.Service + `"`)}
	res.Evidence[0].Note = "It is " + cdkService.Service
	got = Accept(res, viewOnly, opts)
	if !slices.Equal(got.NameIDs, []string{cdkWorker.ID()}) || len(got.Evidence) != 1 || got.Evidence[0].Note != res.Evidence[0].Note || got.Evidence[0].Excerpt != res.Evidence[0].Excerpt {
		t.Errorf("result = %+v", got)
	}

	// Thirty-two is the most the agent reports.
	keys := cdkKeys(t, 32)
	var names []string
	for _, key := range keys {
		names = append(names, key.Service)
	}
	got = Accept(agentresult.Result{Summary: strings.Join(names, ", "), NameIDs: idsOf(keys)}, viewOnly, reviewOptions)
	if got.SummaryReplaced || !slices.Equal(got.NameIDs, idsOf(keys)) {
		t.Errorf("32 names: %+v", got)
	}
}

func TestAccept_ReportedIDsAreVerified(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	only := func(allowed ...model.ServiceKey) func(model.ServiceKey, authz.Action) bool {
		return func(key model.ServiceKey, action authz.Action) bool {
			return action == authz.View && slices.Contains(allowed, key)
		}
	}
	elsewhere := cdkService
	elsewhere.AccountID = "731904428817"
	long := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: cdkCluster.AccountID, Region: cdkCluster.Region, Cluster: strings.Repeat("Shop-F3A9B2C1D4E5", 13)}, Service: cdkService.Service + strings.Repeat("-Xy7KpQ2mNvRt", 13)}
	if len(long.ID()) <= 512 || secretshape.NamesOf(long.ID()).Empty() {
		t.Fatalf("test setup: an id of %d bytes", len(long.ID()))
	}
	keys := cdkKeys(t, 33)
	canonical := cdkService.ID()
	// The last character of this id carries bits that decode to nothing.
	spelled := model.ServiceKey{ClusterKey: cdkCluster, Service: cdkService.Service}
	if n := len(spelled.ID()) % 4; n != 2 && n != 3 {
		spelled.Service += "x"
	}
	loose := spelled.ID()
	loose = loose[:len(loose)-1] + string(loose[len(loose)-1]+1)
	if key, err := model.ParseServiceID(loose); err != nil || key != spelled {
		t.Fatalf("test setup: %q decodes to %+v, %v", loose, key, err)
	}
	for label, c := range map[string]struct {
		ids  []string
		name string
		can  func(model.ServiceKey, authz.Action) bool
	}{
		"nothing reported":     {nil, cdkService.Service, readAll},
		"a name":               {[]string{cdkService.Service}, cdkService.Service, readAll},
		"not an id":            {[]string{"", "not an id!", canonical + "!", encode("482109375512|us-east-1|" + cdkCluster.Cluster + "|" + cdkService.Service + "|x")}, cdkService.Service, readAll},
		"plain fields":         {[]string{"482109375512|us-east-1|" + cdkCluster.Cluster + "|" + cdkService.Service}, cdkService.Service, readAll},
		"a cluster id":         {[]string{cdkCluster.ID()}, cdkCluster.Cluster, readAll},
		"a task id":            {[]string{model.TaskID("482109375512", "us-east-1", "arn:aws:ecs:us-east-1:482109375512:task/"+cdkCluster.Cluster+"/0123456789abcdef0123456789abcdef")}, cdkCluster.Cluster, readAll},
		"with a line break":    {[]string{canonical[:8] + "\n" + canonical[8:]}, cdkService.Service, readAll},
		"another spelling":     {[]string{loose}, spelled.Service, readAll},
		"another account":      {[]string{elsewhere.ID()}, cdkService.Service, only(cdkService)},
		"an unknown service":   {[]string{canonical}, cdkService.Service, only(checkoutKey)},
		"its cluster too":      {[]string{canonical}, cdkCluster.Cluster, only(checkoutKey)},
		"no View":              {[]string{canonical}, cdkService.Service, func(_ model.ServiceKey, action authz.Action) bool { return action == authz.Logs }},
		"no permissions":       {[]string{canonical}, cdkService.Service, nil},
		"the 33rd":             {idsOf(keys), keys[32].Service, readAll},
		"after 32 unknown ids": {append(idsOf(keys[:32]), canonical), cdkService.Service, only(cdkService)},
		"an id too long":       {[]string{long.ID()}, long.Service, readAll},
		"a name of no id":      {[]string{canonical}, cdkWorker.Service, readAll},
	} {
		t.Run(label, func(t *testing.T) {
			item := viewItem(`name: "` + c.name + `"`)
			item.Note = "It is " + c.name
			res := agentresult.Result{Summary: "The service " + c.name + " is down.", NameIDs: c.ids, Evidence: []agentresult.Evidence{item}}
			can := c.can
			if can != nil {
				can = func(key model.ServiceKey, action authz.Action) bool { return key == checkoutKey || c.can(key, action) }
			}
			opts := reviewOptions
			opts.TaskIDs = []string{checkoutKey.ID()}
			got := Accept(res, can, opts)
			if got.Summary != agentresult.ReplacementReviewSummary || !got.SummaryReplaced || got.NameIDs != nil {
				t.Errorf("summary %q, replaced %v, ids %q", got.Summary, got.SummaryReplaced, got.NameIDs)
			}
			if can == nil {
				return
			}
			if len(got.Evidence) != 1 || got.Evidence[0].Note != agentresult.ReplacementNote || got.Evidence[0].Excerpt != `name: "[redacted]"` {
				t.Errorf("evidence = %+v", got.Evidence)
			}
		})
	}

	// An id that verifies is not lost for those that do not, or are given twice.
	ids := []string{"not an id!", elsewhere.ID(), canonical, canonical}
	got := Accept(agentresult.Result{Summary: "It is " + cdkService.Service + ".", NameIDs: slices.Concat(ids, ids, idsOf(keys[:29]), ids, []string{cdkWorker.ID()})}, only(cdkService, cdkWorker), reviewOptions)
	if got.SummaryReplaced || !slices.Equal(got.NameIDs, []string{canonical}) {
		t.Errorf("beside others: %+v", got)
	}
	// The lookups of a decision are bounded; ids are looked at before evidence, and no more than 32 of them.
	asked := map[model.ServiceKey]int{}
	Accept(agentresult.Result{Summary: "x", NameIDs: slices.Concat(idsOf(cdkKeys(t, 90)), idsOf(cdkKeys(t, 90)))},
		func(key model.ServiceKey, _ authz.Action) bool { asked[key]++; return true }, reviewOptions)
	if len(asked) != 32 {
		t.Errorf("asked about %d services, want 32", len(asked))
	}
}

// A replaced summary names nothing, so nothing is stored for it.
func TestAccept_NoIDsForAReplacedSummary(t *testing.T) {
	name := "The service " + cdkService.Service + " is down"
	for label, summary := range map[string]string{
		"a secret": name + ": it uses " + fakeSecret + ".", "too long": name + strings.Repeat(" a", 600), "hidden": name + "\u202e", "a token": name + " with Qm4Zx81LtVb7" + "RkPw29HsJd6Ny3Fc5Ge0UaTi",
		"the request": name + ", and " + requestText, "a password": "Set password=" + cdkService.Service + ".", "glued": name + ", not " + cdkService.Service + "-2", "a path": name + ", see /services/" + cdkService.Service,
	} {
		opts := reviewOptions
		opts.Echo, opts.TaskIDs = requestText, []string{cdkService.ID()}
		got := Accept(agentresult.Result{Summary: summary, NameIDs: []string{cdkService.ID()}}, readAll, opts)
		if got.Summary != agentresult.ReplacementReviewSummary || !got.SummaryReplaced || got.NameIDs != nil {
			t.Errorf("%s: %+v", label, got)
		}
	}
	// What the agent replaced stays counted, and its sentence names nothing.
	opts := investigationOptions
	opts.TaskIDs = []string{cdkService.ID()}
	if got := Accept(agentresult.Result{Summary: agentresult.ReplacementSummary, SummaryReplaced: true, NameIDs: []string{cdkService.ID()}}, readAll, opts); !got.SummaryReplaced || got.NameIDs != nil {
		t.Errorf("replaced by the agent: %+v", got)
	}
}

// An excerpt follows the answer it was taken from: log text keeps no name.
func TestAccept_ExcerptsFollowTheirAction(t *testing.T) {
	text := `name: "` + cdkService.Service + `" in ` + cdkCluster.Cluster + " beside " + cdkWorker.Service
	for label, opts := range map[string]AcceptOptions{"a task's ids": {TaskIDs: []string{cdkService.ID()}}, "a reported id": {}} {
		opts.AllowLogs, opts.Replacement = true, agentresult.ReplacementSummary
		got := Accept(agentresult.Result{Summary: "Fine.", NameIDs: []string{cdkService.ID()}, Evidence: []agentresult.Evidence{viewItem(text), logsItem(text), viewItem("password=" + cdkService.Service)}}, readAll, opts)
		if len(got.Evidence) != 3 || got.NameIDs != nil {
			t.Fatalf("%s: %+v", label, got)
		}
		if want := strings.ReplaceAll(text, cdkWorker.Service, secretshape.Redacted); got.Evidence[0].Excerpt != want {
			t.Errorf("%s: view excerpt %q, want %q", label, got.Evidence[0].Excerpt, want)
		}
		if want := secretshape.ScrubText(text); got.Evidence[1].Excerpt != want || strings.Contains(want, "Stack-") {
			t.Errorf("%s: logs excerpt %q, want %q", label, got.Evidence[1].Excerpt, want)
		}
		if strings.Contains(got.Evidence[2].Excerpt, cdkService.Service) {
			t.Errorf("%s: excerpt %q", label, got.Evidence[2].Excerpt)
		}
	}
}

// acceptBefore is Accept as it was before names could be kept.
func acceptBefore(res agentresult.Result, canRead func(key model.ServiceKey, action authz.Action) bool, opts AcceptOptions) store.InvestigationResult {
	summary, replaced := agentresult.RecheckSummaryAs(res.Summary, opts.Replacement)
	if !replaced && repeats(summary, opts) {
		summary, replaced = agentresult.RecheckSummaryAs("", opts.Replacement)
	}
	out := store.InvestigationResult{Summary: summary, SummaryReplaced: res.SummaryReplaced || replaced}
	dropped := min(max(res.EvidenceDropped, 0), maxDroppedCount)
	for _, item := range res.Evidence {
		derived, known := hmtools.EvidenceAction(item.Tool)
		action, allowed := actionOf(derived)
		key, err := model.ParseServiceID(item.ServiceID)
		excerpt := agentresult.CutExcerpt(secretshape.ScrubText(item.Excerpt))
		if len(out.Evidence) == agentresult.EvidenceCap || !known || derived != item.Action || derived != hmtools.EvidenceView && !opts.AllowLogs || !allowed ||
			err != nil || !storable(key) || canRead == nil || !canRead(key, action) || strings.TrimSpace(excerpt) == "" {
			dropped++
			continue
		}
		note := agentresult.RecheckNote(item.Note)
		if repeats(note, opts) {
			note = agentresult.ReplacementNote
		}
		out.Evidence = append(out.Evidence, store.InvestigationEvidence{Ordinal: len(out.Evidence) + 1, Tool: item.Tool, Action: derived,
			AccountID: key.AccountID, Region: key.Region, ClusterName: key.Cluster, ServiceName: key.Service, Note: note, Excerpt: excerpt})
	}
	out.EvidenceDropped = min(dropped, maxDroppedCount)
	return out
}

func TestAccept_WithoutNamesIsUnchanged(t *testing.T) {
	lying := logsItem("connection refused")
	lying.Action = "view"
	badID := viewItem("x")
	badID.ServiceID = checkoutKey.ClusterKey.ID()
	secret := logsItem("login with " + fakeSecret + " refused\x00here " + strings.Repeat("é", 2000))
	secret.Note = "Key " + fakeSecret
	named := viewItem(`name: "` + cdkService.Service + `"`)
	named.Note = "It is " + cdkService.Service
	lines := viewItem("ok")
	lines.Note = "two\nlines"
	echoes := viewItem("ok")
	echoes.Note = requestText
	var many []agentresult.Evidence
	for i := range 15 {
		many = append(many, viewItem(fmt.Sprint("row ", i)))
	}
	results := map[string]agentresult.Result{
		"items":            resultWith(lying, badID, secret, named, lines, echoes, viewItem(" \x00\n"), logsItem("connection refused"), viewItem("desired 2")),
		"many items":       resultWith(many...),
		"a CDK name":       {Summary: "The service " + cdkService.Service + " is down."},
		"a secret":         {Summary: "It uses " + fakeSecret + " to sign."},
		"the request":      {Summary: requestText},
		"empty":            {},
		"too long":         {Summary: strings.Repeat("a ", 601)},
		"hidden":           {Summary: "It failed\u202e."},
		"not UTF-8":        {Summary: "It failed\xff."},
		"links":            {Summary: "Open https://elsewhere.example/page now."},
		"ids":              {Summary: "Compare " + checkoutKey.ID() + " and " + cdkService.ID() + "."},
		"agent's sentence": {Summary: agentresult.ReplacementReviewSummary, SummaryReplaced: true, EvidenceDropped: 3},
		"a bad count":      {Summary: "Fine.", EvidenceDropped: -7},
	}
	// Ids that give no name: ordinary ones, and what is no id.
	ordinary := []string{checkoutKey.ID(), ledgerKey.ID(), checkoutKey.ClusterKey.ID(), cdkService.Service, ""}
	hidden := func(key model.ServiceKey, action authz.Action) bool { return key != ledgerKey && action == authz.View }
	for label, res := range results {
		for name, opts := range map[string]AcceptOptions{"investigation": investigationOptions, "review": reviewOptions, "custom review": {Replacement: agentresult.ReplacementReviewSummary, Echo: requestText}, "none": {}} {
			for sight, can := range map[string]func(model.ServiceKey, authz.Action) bool{"all": readAll, "some": hidden, "nil": nil} {
				want := acceptBefore(res, can, opts)
				for ids, with := range map[string][]string{"no ids": nil, "ordinary ids": ordinary} {
					res, opts := res, opts
					res.NameIDs, opts.TaskIDs = with, with
					if got := Accept(res, can, opts); !reflect.DeepEqual(got, want) {
						t.Errorf("%s, %s, %s, %s: %+v, want %+v", label, name, sight, ids, got, want)
					}
				}
			}
		}
	}
	// An id that gives no name is not looked up.
	Accept(agentresult.Result{Summary: "Fine.", NameIDs: ordinary}, func(key model.ServiceKey, _ authz.Action) bool {
		t.Errorf("asked about %+v", key)
		return true
	}, reviewOptions)
}

func TestTaskIDs_AreTheIDsTheAgentTakes(t *testing.T) {
	subject := agentclient.TaskSubject{Kind: "service", ClusterID: cdkCluster.ID(), ServiceIDs: []string{cdkService.ID(), cdkWorker.ID()}, AccountID: cdkCluster.AccountID}
	entries := []agentclient.ScopeEntry{
		{Kind: "tag", TagID: "12", Name: cdkService.Service}, {Kind: "cluster", ClusterID: cdkCluster.ID(), Name: "x"},
		{Kind: "account", AccountID: cdkCluster.AccountID, Region: "us-east-1"}, {Kind: "cluster", ClusterID: checkoutKey.ClusterKey.ID()},
	}
	taskID := model.TaskID("482109375512", "us-east-1", "arn:aws:ecs:us-east-1:482109375512:task/"+cdkCluster.Cluster+"/0123456789abcdef0123456789abcdef")
	for label, c := range map[string]struct {
		task agentclient.Task
		want []string
	}{
		"an investigation":    {agentclient.Task{Kind: "investigate", Subject: subject}, []string{cdkCluster.ID(), cdkService.ID(), cdkWorker.ID()}},
		"an account":          {agentclient.Task{Kind: "investigate", Subject: agentclient.TaskSubject{Kind: "account", ServiceIDs: []string{cdkService.ID()}}}, []string{cdkService.ID()}},
		"a review":            {agentclient.Task{Kind: "review", Subject: subject, Scope: &agentclient.TaskScope{Entries: entries}}, []string{cdkCluster.ID(), checkoutKey.ClusterKey.ID()}},
		"an unscoped review":  {agentclient.Task{Kind: "review", Scope: &agentclient.TaskScope{Entries: []agentclient.ScopeEntry{}}}, nil},
		"a review, no scope":  {agentclient.Task{Kind: "review"}, nil},
		"what is no resource": {agentclient.Task{Kind: "investigate", Subject: agentclient.TaskSubject{ClusterID: taskID, ServiceIDs: []string{taskID, "", cdkService.Service}}}, nil},
		"an empty task":       {agentclient.Task{}, nil},
	} {
		if got := TaskIDs(c.task); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", label, got, c.want)
		}
	}
}
