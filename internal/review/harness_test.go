package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	testProvider = "anthropic"
	testModel    = "claude-sonnet-4-5-20250929"
	// The whole reservation: 200000 tokens at 15 dollars a million. All three roles have this model.
	testReservation = 3_000_000
	testLeave       = 2 * testReservation
)

// clock is a settable clock shared by the runner and the fixture.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// logBuffer is a log sink that can be read while it is written to.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// call is one run the fake agent was asked for.
type call struct {
	n    int // from 1
	w    http.ResponseWriter
	r    *http.Request
	body agentclient.RunRequest
}

// send writes events in one write, so they reach the client together.
func (c *call) send(events ...map[string]any) {
	var lines bytes.Buffer
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			panic(err)
		}
		lines.Write(append(raw, '\n'))
	}
	_, _ = c.w.Write(lines.Bytes())
	c.w.(http.Flusher).Flush()
}

// hold keeps the stream open until the client goes away, and reports whether it did in time.
func (c *call) hold() bool {
	select {
	case <-c.r.Context().Done():
		return true
	case <-time.After(15 * time.Second):
		return false
	}
}

func usage(cost int64) map[string]any {
	return map[string]any{"type": "usage", "input_tokens": cost / 10, "output_tokens": cost / 100, "cost_micro_usd": cost}
}

func provisional(cost int64) map[string]any {
	ev := usage(cost)
	ev["provisional"] = true
	return ev
}

func toolStart(tool string) map[string]any { return map[string]any{"type": "tool_start", "tool": tool} }

func resultEvent(res agentresult.Result) map[string]any {
	evidence := res.Evidence
	if evidence == nil {
		evidence = []agentresult.Evidence{}
	}
	return map[string]any{"type": "result", "summary": res.Summary, "summary_replaced": res.SummaryReplaced, "evidence": evidence, "evidence_dropped": res.EvidenceDropped}
}

func agentError(code string) map[string]any {
	return map[string]any{"type": "error", "code": code, "message": "MESSAGE-SENTINEL"}
}

var doneEvent = map[string]any{"type": "done"}

// goodRun is what an ordinary run says; it costs 4200.
func goodRun(summary string) []map[string]any {
	return []map[string]any{provisional(90_000), toolStart("get_cost_analytics"), usage(4200),
		resultEvent(agentresult.Result{Summary: summary, Evidence: []agentresult.Evidence{viewItem("desired 2, running 0")}}), doneEvent}
}

// fakeAgent speaks the agent service's wire.
type fakeAgent struct {
	srv *httptest.Server

	mu              sync.Mutex
	calls           []agentclient.RunRequest
	script          func(c *call)
	roles           []string
	reviewTypes     []string
	models          []string
	catalogueStatus int
	runTimeout      time.Duration
	maxRunTokens    int64
	outputPrice     int64
	active, most    int
	onCatalogue     func(r *http.Request) // before the catalogue is answered
	refused         []string              // why a task was answered with 400
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{roles: []string{"chat", "investigator", "reviewer", "test"}, reviewTypes: []string{"cost", "security", "reliability", "capacity"}, models: []string{testModel}, catalogueStatus: http.StatusOK, runTimeout: time.Minute, maxRunTokens: 200_000, outputPrice: 15_000_000}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/catalogue", func(w http.ResponseWriter, r *http.Request) {
		if a.onCatalogue != nil {
			a.onCatalogue(r)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.catalogueStatus != http.StatusOK {
			w.WriteHeader(a.catalogueStatus)
			return
		}
		var models []map[string]any
		for _, id := range a.models {
			models = append(models, map[string]any{"id": id, "input_per_mtok_micro_usd": a.outputPrice / 5, "output_per_mtok_micro_usd": a.outputPrice, "context_tokens": 200_000})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"providers": []map[string]any{{"provider": testProvider, "models": models}}, "roles": a.roles, "review_types": a.reviewTypes,
			"limits": map[string]any{"max_run_tokens": a.maxRunTokens, "run_timeout_seconds": int(a.runTimeout.Seconds())},
		})
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body agentclient.RunRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.calls = append(a.calls, body)
		c := &call{n: len(a.calls), w: w, r: r, body: body}
		script := a.script
		a.active++
		a.most = max(a.most, a.active)
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			a.active--
			a.mu.Unlock()
		}()
		if message := refusal(raw); message != "" {
			a.mu.Lock()
			a.refused = append(a.refused, message)
			a.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		script(c)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		a.srv.CloseClientConnections()
		a.srv.Close()
	})
	return a
}

func (a *fakeAgent) set(script func(c *call)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.script = script
}

func (a *fakeAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *fakeAgent) call(i int) agentclient.RunRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[i]
}

// refusal is why the agent service answers a review's request with 400, after Task 7's rules, or "".
func refusal(raw []byte) string {
	var body struct {
		Role string `json:"role"`
		Task *struct {
			Kind    string          `json:"kind"`
			Subject json.RawMessage `json:"subject"`
			Alert   json.RawMessage `json:"alert"`
			Review  *struct{ Type, Range, Period string }
			Scope   *struct {
				Entries       []map[string]string `json:"entries"`
				ServicesTotal int                 `json:"services_total"`
			}
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err.Error()
	}
	task := body.Task
	switch {
	case body.Role != "reviewer":
		return "role " + body.Role
	case task == nil || task.Kind != "review" || task.Subject != nil || task.Alert != nil:
		return "not a review task"
	case task.Review == nil || task.Scope == nil:
		return "review and scope are needed"
	case !slices.Contains([]string{"cost", "security", "reliability", "capacity"}, task.Review.Type):
		return "type " + task.Review.Type
	case !slices.Contains([]string{"24h", "7d", "30d"}, task.Review.Range):
		return "range " + task.Review.Range
	case !regexp.MustCompile(`^[A-Za-z0-9 ,.:()-]{1,80}$`).MatchString(task.Review.Period):
		return "period " + task.Review.Period
	case task.Scope.ServicesTotal < 0 || task.Scope.ServicesTotal > 100_000 || len(task.Scope.Entries) > 10:
		return "scope size"
	}
	members := map[string][]string{"tag": {"tag_id"}, "cluster": {"cluster_id"}, "account": {"account_id", "region"}}
	for _, entry := range task.Scope.Entries {
		allowed, known := members[entry["kind"]]
		if !known || strings.TrimSpace(entry[allowed[0]]) == "" {
			return "entry kind or id"
		}
		for member, value := range entry {
			limit := map[string]int{"kind": 10, "name": 255, "cluster_id": 512}[member]
			if limit == 0 {
				limit = 200
			}
			if len(value) > limit || (member != "kind" && member != "name" && !slices.Contains(allowed, member)) {
				return "entry member " + member
			}
		}
	}
	return ""
}

var (
	checkoutKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "payments-prod"}, Service: "checkout-api"}
	ledgerKey   = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "eu-west-1", Cluster: "ledger-prod"}, Service: "ledger-writer"}
	batchKey    = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "731904428817", Region: "us-east-1", Cluster: "batch-prod"}, Service: "nightly-batch"}

	// Wednesday, 45 minutes after the fixture's reviews are due (08:00 in Berlin).
	testNow = time.Date(2026, 11, 25, 7, 45, 0, 0, time.UTC)
	dueAt   = time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
)

func viewItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service", ServiceID: checkoutKey.ID(), Action: "view", Note: "The service has no running tasks.", Excerpt: excerpt}
}

func logsItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service_logs", ServiceID: checkoutKey.ID(), Action: "logs", Note: "The start fails.", Excerpt: excerpt}
}

// fixture is a real store with services the identity may View, a fake agent and a runner.
type fixture struct {
	t        *testing.T
	st       *store.Store
	agent    *fakeAgent
	clock    *clock
	logs     *logBuffer
	kicks    atomic.Int64
	identity store.User
	admin    store.User
	dev      authz.TagID
	routeID  int64
	runner   *Runner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, st: storetest.Open(t), agent: newFakeAgent(t), logs: &logBuffer{}, clock: &clock{at: testNow}}
	ctx := context.Background()
	var err error
	if f.admin, err = f.st.CreateUser(ctx, "dana", "h", true, false); err != nil {
		t.Fatal(err)
	}
	if f.identity, err = f.st.EnsureAgentIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	dev, err := f.st.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.dev = dev.ID
	f.grant(f.dev)
	f.services(checkoutKey, ledgerKey)
	f.settings(func(*store.AgentSettings) {})
	route, err := f.st.CreateNotificationRoute(ctx, store.NotificationRoute{Name: "hook", Destination: "webhook",
		Config: store.NotificationConfig{URLHost: "hooks.example"}, SecretEnc: []byte("sealed"), MinSeverity: "warning",
		Kinds: []string{"service_failing"}, RepeatSeconds: 1800, QuietTimezone: "Europe/Berlin", NotifyResolved: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.routeID = route.ID
	f.agent.set(func(c *call) { c.send(goodRun("Spend rose by 12 percent.")...) })
	f.runner = f.newRunner(f.agent)
	return f
}

// newRunner is another process's runner on the same store.
func (f *fixture) newRunner(agent *fakeAgent) *Runner {
	f.t.Helper()
	client, err := agentclient.New(agent.srv.URL, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return &Runner{
		Store: f.st, Scopes: f.st,
		Driver: &agentjob.Driver{Store: f.st, Agent: client,
			Auth: auth.NewService(f.st, config.Config{AgentURL: agent.srv.URL, AgentRunTokenTTL: 15 * time.Minute})},
		PerDay: 24, MaxLate: 6 * time.Hour, InvestigationMaxWait: 15 * time.Minute, Interval: 20 * time.Millisecond, Now: f.clock.now,
		KickNotify: func() { f.kicks.Add(1) }, Log: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// grant gives the identity View and Logs on exactly these tags.
func (f *fixture) grant(tags ...authz.TagID) {
	f.t.Helper()
	perms := map[authz.TagID]authz.ActionSet{}
	for _, tag := range tags {
		perms[tag] = authz.ActionSet(authz.View | authz.Logs)
	}
	if err := f.st.SetAgentIdentityPermissions(context.Background(), perms, f.admin.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) tag(name string) authz.TagID {
	f.t.Helper()
	tag, err := f.st.CreateTag(context.Background(), name, "red", false)
	if err != nil {
		f.t.Fatal(err)
	}
	return tag.ID
}

func (f *fixture) tagService(key model.ServiceKey, tag authz.TagID) {
	f.t.Helper()
	if err := f.st.SetServiceTag(context.Background(), key, &tag); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) services(keys ...model.ServiceKey) {
	f.t.Helper()
	var rows []model.Service
	for _, k := range keys {
		rows = append(rows, model.Service{Key: k, Status: "ACTIVE", Raw: []byte(`{"Tags":[]}`), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(f.st).UpsertServices(context.Background(), rows); err != nil {
		f.t.Fatal(err)
	}
}

// settings stores the models of all three roles and a budget, after change.
func (f *fixture) settings(change func(*store.AgentSettings)) {
	f.t.Helper()
	model := store.AgentModelRef{Provider: testProvider, Model: testModel}
	in := store.AgentSettings{MonthlyBudgetMicroUSD: 100_000_000,
		RoleModels: map[string]store.AgentModelRef{"reviewer": model, "chat": model, "investigator": model}}
	change(&in)
	if err := storetest.PutAgentSettings(context.Background(), f.st, in); err != nil {
		f.t.Fatal(err)
	}
}

// review creates a daily cost review due at 08:00 in Berlin, after change. Its schedule was last changed long ago.
func (f *fixture) review(name string, change ...func(*store.Review)) store.Review {
	f.t.Helper()
	ctx := context.Background()
	in := store.Review{Name: name, Type: "cost", Enabled: true, Cadence: "daily", AtMinute: 480, Timezone: "Europe/Berlin", RouteIDs: []int64{f.routeID}}
	for _, c := range change {
		c(&in)
	}
	created, err := f.st.CreateReview(ctx, in, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	f.scheduleChanged(created.ID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if created, err = f.st.GetReview(ctx, created.ID); err != nil {
		f.t.Fatal(err)
	}
	return created
}

func (f *fixture) scheduleChanged(reviewID int64, at time.Time) {
	f.t.Helper()
	f.exec(`update agent_reviews set schedule_changed_at = $2 where id = $1`, reviewID, at)
}

// due records the waiting run of a new review, as the due scan does, and returns its id.
func (f *fixture) due(name string, change ...func(*store.Review)) int64 {
	f.t.Helper()
	rv := f.review(name, change...)
	ctx := context.Background()
	if n, err := f.st.RecordDueReviewRuns(ctx, []store.DueReview{{ReviewID: rv.ID, Version: rv.Version, PeriodKey: "d:2026-11-25",
		PeriodLabel: "25 Nov 2026", ScheduledFor: dueAt}}, f.clock.now()); err != nil || n != 1 {
		f.t.Fatalf("recorded %d, %v", n, err)
	}
	runs := f.reviewRuns(rv.ID)
	return runs[0].ID
}

// reviewRuns are the run rows of a review, newest first.
func (f *fixture) reviewRuns(reviewID int64) []store.ReviewRun {
	f.t.Helper()
	runs, err := f.st.ListReviewRuns(context.Background(), store.ReviewRunFilter{ReviewID: &reviewID})
	if err != nil {
		f.t.Fatal(err)
	}
	return runs
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Error(err)
	}
}

func (f *fixture) row(id int64) store.ReviewRun {
	f.t.Helper()
	v, err := f.st.GetReviewRun(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

// investigation records an investigation that was requested that long ago and waits.
func (f *fixture) investigation(ago time.Duration) int64 {
	f.t.Helper()
	var id int64
	if err := f.st.Pool.QueryRow(context.Background(), `insert into agent_investigations
		(subject_kind, subject_key, account_id, region, cluster_name, service_name, alert_kind, alert_title, alert_text, alert_opened_at, requested_at)
		values ('service', 'service/482109375512/us-east-1/payments-prod/checkout-api', '482109375512', 'us-east-1', 'payments-prod', 'checkout-api',
		'service_failing', 't', 't', $1, $1) returning id`, f.clock.now().Add(-ago)).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// ticks lets r tick several times, for what must not happen to have had the time to.
func (f *fixture) ticks(r *Runner) {
	f.t.Helper()
	stop := f.start(r)
	time.Sleep(200 * time.Millisecond)
	stop()
}

// runRow is what is stored of one run.
type runRow struct {
	id             uuid.UUID
	state          string
	reserved, cost int64
	userID         *int64
	tokens, audits int
	tools          []string
}

func (f *fixture) runs() []runRow {
	f.t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), `select r.id, r.state, r.reserved_micro_usd, r.cost_micro_usd, r.user_id, r.tools,
		(select count(*) from api_tokens where run_id = r.id),
		(select count(*) from audit_log where target = r.id::text and action = 'agent_run')
		from agent_runs r order by r.started_at, r.id`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.id, &r.state, &r.reserved, &r.cost, &r.userID, &r.tools, &r.tokens, &r.audits); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// settled asserts the runs there are, in order, as "state cost", each with no token left and one audit row.
func (f *fixture) settled(want ...string) {
	f.t.Helper()
	var got []string
	for _, r := range f.runs() {
		got = append(got, fmt.Sprintf("%s %d", r.state, r.cost))
		if r.tokens != 0 || r.audits != 1 {
			f.t.Errorf("run %s: %d tokens and %d audit rows, want none and one", r.state, r.tokens, r.audits)
		}
	}
	if !slices.Equal(got, want) {
		f.t.Errorf("runs = %v, want %v", got, want)
	}
}

// start runs r until stop, which waits for it to end.
func (f *fixture) start(r *Runner) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				f.t.Error("the runner did not stop")
			}
		})
	}
	f.t.Cleanup(stop)
	return stop
}

func (f *fixture) wait(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

// runTo runs the fixture's runner until the review run is in state, then stops it and waits for what it started.
func (f *fixture) runTo(id int64, state string) store.ReviewRun {
	f.t.Helper()
	return f.runnerTo(f.runner, id, state)
}

func (f *fixture) runnerTo(r *Runner, id int64, state string) store.ReviewRun {
	f.t.Helper()
	stop := f.start(r)
	f.wait(fmt.Sprintf("review run %d to be %s", id, state), func() bool { return f.row(id).State == state && !f.anyRunning() })
	stop()
	return f.row(id)
}

// runToRelease runs the runner until the review run was claimed and given back.
func (f *fixture) runToRelease(id int64) store.ReviewRun {
	f.t.Helper()
	before := f.row(id).ClaimSeq
	stop := f.start(f.runner)
	f.wait(fmt.Sprintf("review run %d to be given back", id), func() bool {
		v := f.row(id)
		return v.State == "requested" && v.ClaimSeq > before && !f.anyRunning()
	})
	stop()
	return f.row(id)
}

// waitLong is wait for what takes as long as the limits of the steps that end a run.
func (f *fixture) waitLong(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) anyRunning() bool {
	return slices.ContainsFunc(f.runs(), func(r runRow) bool { return r.state == "running" || r.tokens != 0 })
}

// audit returns the agent_run audit rows, oldest first, as stored.
func (f *fixture) audit() []store.AuditEntry {
	f.t.Helper()
	all, err := f.st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range slices.Backward(all) {
		if e.Action == "agent_run" {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) want(v store.ReviewRun, state, reason string) {
	f.t.Helper()
	if v.State != state || v.Reason != reason {
		f.t.Errorf("review run = %s %q, want %s %q", v.State, v.Reason, state, reason)
	}
}

// unspent asserts that nothing was reserved and the agent not called.
func (f *fixture) unspent() {
	f.t.Helper()
	if n, runs := f.agent.count(), f.runs(); n != 0 || len(runs) != 0 {
		f.t.Errorf("%d agent calls, runs %+v; want none", n, runs)
	}
}

var errNoLease = errors.New("lease not confirmed")

func containsAny(text string, words ...string) string {
	for _, w := range words {
		if strings.Contains(text, w) {
			return w
		}
	}
	return ""
}
