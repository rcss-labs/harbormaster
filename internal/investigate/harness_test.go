package investigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
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
	// The whole reservation: 200000 tokens at 15 dollars a million.
	testReservation = 3_000_000
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

// gate is a channel a script may wait on and what opens it. The end of the test opens it too, before the fake agent of
// the fixture is closed: Close waits for its handlers, so a test that failed first would hang instead. Call it after
// the fixture is made.
func gate(t *testing.T) (opened <-chan struct{}, open func()) {
	ch := make(chan struct{})
	open = sync.OnceFunc(func() { close(ch) })
	t.Cleanup(open)
	return ch, open
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
	ev := map[string]any{"type": "result", "summary": res.Summary, "summary_replaced": res.SummaryReplaced, "evidence": evidence, "evidence_dropped": res.EvidenceDropped}
	if len(res.NameIDs) > 0 {
		ev["name_ids"] = res.NameIDs
	}
	return ev
}

func agentError(code string) map[string]any {
	return map[string]any{"type": "error", "code": code, "message": "MESSAGE-SENTINEL"}
}

var doneEvent = map[string]any{"type": "done"}

// goodRun is what an ordinary run says; it costs 4200.
func goodRun(summary string) []map[string]any {
	return []map[string]any{provisional(90_000), toolStart("get_service_logs"), usage(4200),
		resultEvent(agentresult.Result{Summary: summary, Evidence: []agentresult.Evidence{viewItem("desired 2, running 0")}}), doneEvent}
}

// fakeAgent speaks the agent service's wire.
type fakeAgent struct {
	srv *httptest.Server

	mu              sync.Mutex
	calls           []agentclient.RunRequest
	script          func(c *call)
	roles           []string
	models          []string
	catalogueStatus int
	runTimeout      time.Duration
	maxRunTokens    int64
	outputPrice     int64
	active, most    int
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{roles: []string{"chat", "investigator", "test"}, models: []string{testModel}, catalogueStatus: http.StatusOK, runTimeout: time.Minute, maxRunTokens: 200_000, outputPrice: 15_000_000}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/catalogue", func(w http.ResponseWriter, _ *http.Request) {
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
			"providers": []map[string]any{{"provider": testProvider, "models": models}}, "roles": a.roles,
			"limits": map[string]any{"max_run_tokens": a.maxRunTokens, "run_timeout_seconds": int(a.runTimeout.Seconds())},
		})
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var body agentclient.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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

// fixture is a real store with one service the identity may View and read the Logs of, a fake agent and a runner.
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
	runner   *Runner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, st: storetest.Open(t), agent: newFakeAgent(t), logs: &logBuffer{},
		clock: &clock{at: time.Now().UTC().Truncate(time.Millisecond)}}
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
	f.grant(authz.ActionSet(authz.View | authz.Logs))
	f.services(checkoutKey, ledgerKey)
	f.settings(func(*store.AgentSettings) {})
	f.agent.set(func(c *call) { c.send(goodRun("The service stopped after its last deploy.")...) })
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
		Store: f.st, Agent: client, Auth: auth.NewService(f.st, config.Config{AgentURL: agent.srv.URL, AgentRunTokenTTL: 15 * time.Minute}),
		Concurrent: 2, PerHour: 20, MaxWait: 15 * time.Minute, Interval: 20 * time.Millisecond, Now: f.clock.now,
		KickNotify: func() { f.kicks.Add(1) }, Log: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func (f *fixture) grant(set authz.ActionSet) {
	f.t.Helper()
	perms := map[authz.TagID]authz.ActionSet{}
	if set != 0 {
		perms[f.dev] = set
	}
	if err := f.st.SetAgentIdentityPermissions(context.Background(), perms, f.admin.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) services(keys ...model.ServiceKey) {
	f.t.Helper()
	var rows []model.Service
	for _, k := range keys {
		rows = append(rows, model.Service{Key: k, Status: "ACTIVE", Raw: []byte(`{}`), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(f.st).UpsertServices(context.Background(), rows); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) settings(change func(*store.AgentSettings)) {
	f.t.Helper()
	in := store.AgentSettings{MonthlyBudgetMicroUSD: 100_000_000,
		RoleModels: map[string]store.AgentModelRef{"investigator": {Provider: testProvider, Model: testModel}}}
	change(&in)
	if err := storetest.PutAgentSettings(context.Background(), f.st, in); err != nil {
		f.t.Fatal(err)
	}
}

// request records an investigation of the service, requested now.
func (f *fixture) request(service string) int64 {
	f.t.Helper()
	k := model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: service}
	var id int64
	if err := f.st.Pool.QueryRow(context.Background(), `insert into agent_investigations
		(subject_kind, subject_key, account_id, region, cluster_name, service_name, alert_kind, alert_title, alert_text, alert_opened_at, requested_at)
		values ('service', $1, $2, $3, $4, $5, 'service_failing', 'TITLE-SENTINEL is failing', 'ALERT-SENTINEL tasks stop', $6, $6) returning id`,
		fmt.Sprintf("service/%s/%s/%s/%s", k.AccountID, k.Region, k.Cluster, k.Service), k.AccountID, k.Region, k.Cluster, k.Service, f.clock.now()).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Error(err)
	}
}

func (f *fixture) inv(id int64) store.Investigation {
	f.t.Helper()
	v, err := f.st.GetInvestigation(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
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

// runTo runs the fixture's runner until the investigation is in state, then stops it and waits for what it started.
func (f *fixture) runTo(id int64, state string) store.Investigation {
	f.t.Helper()
	return f.runnerTo(f.runner, id, state)
}

func (f *fixture) runnerTo(r *Runner, id int64, state string) store.Investigation {
	f.t.Helper()
	stop := f.start(r)
	f.wait(fmt.Sprintf("investigation %d to be %s", id, state), func() bool { return f.inv(id).State == state && !f.anyRunning() })
	stop()
	return f.inv(id)
}

// runToRelease runs the runner until the investigation was claimed and given back.
func (f *fixture) runToRelease(id int64) store.Investigation {
	f.t.Helper()
	before := f.inv(id).ClaimSeq
	stop := f.start(f.runner)
	f.wait(fmt.Sprintf("investigation %d to be given back", id), func() bool {
		v := f.inv(id)
		return v.State == store.InvestigationRequested && v.ClaimSeq > before && !f.anyRunning()
	})
	stop()
	return f.inv(id)
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

func (f *fixture) want(v store.Investigation, state, reason string) {
	f.t.Helper()
	if v.State != state || v.Reason != reason {
		f.t.Errorf("investigation = %s %q, want %s %q", v.State, v.Reason, state, reason)
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
