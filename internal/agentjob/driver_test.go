package agentjob

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	testRole     = "investigator"
	testProvider = "anthropic"
	testModel    = "claude-sonnet-4-5-20250929"
	// 200000 tokens at 15 dollars a million.
	testReservation = 3_000_000
)

var (
	checkoutKey = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "payments-prod"}, Service: "checkout-api"}
	ledgerKey   = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "ledger-writer"}
	fakeSecret  = "AKIA" + "IOSFODNN7EXAMPLE"
)

// ---- a fake agent over the wire ------------------------------------------------------------------------------------

type call struct {
	w http.ResponseWriter
	r *http.Request
}

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

func (c *call) hold() {
	select {
	case <-c.r.Context().Done():
	case <-time.After(15 * time.Second):
	}
}

func usage(cost int64) map[string]any {
	return map[string]any{"type": "usage", "input_tokens": cost / 10, "output_tokens": cost / 100, "cost_micro_usd": cost}
}

func agentError(code string) map[string]any {
	return map[string]any{"type": "error", "code": code, "message": "MESSAGE-SENTINEL"}
}

var doneEvent = map[string]any{"type": "done"}

func resultEvent(summary string) map[string]any {
	return map[string]any{"type": "result", "summary": summary, "evidence": []agentresult.Evidence{}}
}

type fakeAgent struct {
	srv *httptest.Server

	mu              sync.Mutex
	script          func(c *call)
	catalogueStatus int
	roles           []string
	models          []string
	runs            int
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{catalogueStatus: http.StatusOK, roles: []string{"chat", testRole}, models: []string{testModel}}
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
			models = append(models, map[string]any{"id": id, "input_per_mtok_micro_usd": 3_000_000, "output_per_mtok_micro_usd": 15_000_000, "context_tokens": 200_000})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"providers": []map[string]any{{"provider": testProvider, "models": models}}, "roles": a.roles,
			"limits": map[string]any{"max_run_tokens": 200_000, "run_timeout_seconds": 60},
		})
	})
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.runs++
		script := a.script
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		script(&call{w: w, r: r})
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

func (a *fakeAgent) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs
}

// ---- a driver on a real store --------------------------------------------------------------------------------------

type fixture struct {
	t        *testing.T
	st       *store.Store
	agent    *fakeAgent
	identity store.User
	driver   *Driver
	logs     *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, st: storetest.Open(t), agent: newFakeAgent(t), logs: &bytes.Buffer{}}
	var err error
	if f.identity, err = f.st.EnsureAgentIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.settings(func(*store.AgentSettings) {})
	f.agent.set(func(c *call) { c.send(usage(4200), resultEvent("Fine."), doneEvent) })
	client, err := agentclient.New(f.agent.srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(f.logs, nil))
	f.driver = &Driver{
		Store: f.st, Auth: auth.NewService(f.st, config.Config{AgentURL: f.agent.srv.URL, AgentRunTokenTTL: 15 * time.Minute}), Agent: client,
		Runs: &agentrun.Runner{Store: f.st, Log: log}, Log: log,
	}
	return f
}

func (f *fixture) settings(change func(*store.AgentSettings)) {
	f.t.Helper()
	in := store.AgentSettings{MonthlyBudgetMicroUSD: 100_000_000,
		RoleModels: map[string]store.AgentModelRef{testRole: {Provider: testProvider, Model: testModel}}}
	change(&in)
	if err := storetest.PutAgentSettings(context.Background(), f.st, in); err != nil {
		f.t.Fatal(err)
	}
}

type runRow struct {
	state          string
	cost           int64
	tokens, audits int
}

func (f *fixture) runs() []runRow {
	f.t.Helper()
	rows, err := f.st.Pool.Query(context.Background(), `select r.state, r.cost_micro_usd,
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
		if err := rows.Scan(&r.state, &r.cost, &r.tokens, &r.audits); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (f *fixture) auditRows() []store.AuditEntry {
	f.t.Helper()
	all, err := f.st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == "agent_run" {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) wait(what string, cond func() bool) {
	f.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) prepared() Prepared {
	f.t.Helper()
	p, kind := f.driver.Prepare(context.Background(), testRole)
	if kind != Ready {
		f.t.Fatalf("Prepare = %v, want Ready", kind)
	}
	return p
}

// ---- Prepare -------------------------------------------------------------------------------------------------------

func TestDriver_PrepareSpendsNothing(t *testing.T) {
	f := newFixture(t)
	p, kind := f.driver.Prepare(context.Background(), testRole)
	if kind != Ready {
		t.Fatalf("kind = %v, want Ready", kind)
	}
	if p.Identity.ID != f.identity.ID || p.Provider != testProvider || p.Model.ID != testModel || !p.RedactEnv ||
		!p.Subject.ReadOnly || p.Catalogue.MaxRunTokens != 200_000 {
		t.Errorf("prepared = %+v", p)
	}
	if runs := f.runs(); len(runs) != 0 || f.agent.calls() != 0 || len(f.auditRows()) != 0 {
		t.Errorf("%d runs, %d agent calls, %d audit rows; want none", len(runs), f.agent.calls(), len(f.auditRows()))
	}
	var tokens int
	if err := f.st.Pool.QueryRow(context.Background(), `select count(*) from api_tokens where run_id is not null`).Scan(&tokens); err != nil || tokens != 0 {
		t.Errorf("%d run tokens, %v", tokens, err)
	}

	f.settings(func(s *store.AgentSettings) { s.UnredactedProviders = []string{testProvider} })
	if p, _ := f.driver.Prepare(context.Background(), testRole); p.RedactEnv {
		t.Error("redaction was not lifted for the provider")
	}
}

func TestDriver_PrepareKinds(t *testing.T) {
	cases := map[string]struct {
		prepare func(f *fixture)
		role    string
		want    Kind
	}{
		"no model for the role":   {prepare: func(f *fixture) { f.settings(func(s *store.AgentSettings) { s.RoleModels = nil }) }, want: NotConfigured},
		"model not in catalogue":  {prepare: func(f *fixture) { f.agent.models = []string{"claude-opus-4-5-20251101"} }, want: NotConfigured},
		"role not offered":        {prepare: func(f *fixture) { f.agent.roles = []string{"chat"} }, want: NotConfigured},
		"no catalogue":            {prepare: func(f *fixture) { f.agent.catalogueStatus = http.StatusServiceUnavailable }, want: AgentUnavailable},
		"a model that has no use": {prepare: func(f *fixture) { f.agent.models = nil }, want: NotConfigured},
		"ready":                   {prepare: func(*fixture) {}, want: Ready},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c.prepare(f)
			if _, kind := f.driver.Prepare(context.Background(), testRole); kind != c.want {
				t.Errorf("kind = %v, want %v", kind, c.want)
			}
		})
	}
}

// ---- Run -----------------------------------------------------------------------------------------------------------

// Whatever the ending, a reserved run is settled once at its last figures, its token is gone, it has one audit row,
// and the caller was asked once what to make of it and its save ran once.
func TestDriver_OutcomeKindsCoverEveryEnding(t *testing.T) {
	cases := map[string]struct {
		script  func(f *fixture, c *call)
		prepare func(f *fixture, job *Job)
		stop    bool // cancel the run once the usage is stored
		kind    Kind
		state   string
		cost    int64
		check   func(t *testing.T, out Outcome)
	}{
		"done": {script: func(_ *fixture, c *call) {
			c.send(usage(4200), map[string]any{"type": "tool_start", "tool": "get_service_logs"}, resultEvent("Fine."), doneEvent)
		},
			kind: Done, state: "done", cost: 4200, check: func(t *testing.T, out Outcome) {
				if out.Result == nil || out.Result.Summary != "Fine." || !slices.Equal(out.Tools, []string{"get_service_logs"}) || !out.Called {
					t.Errorf("outcome = %+v", out)
				}
			}},
		"the first result wins": {script: func(_ *fixture, c *call) {
			c.send(usage(700), resultEvent("First."), resultEvent("Second."), doneEvent)
		},
			kind: Done, state: "done", cost: 700, check: func(t *testing.T, out Outcome) {
				if out.Result == nil || out.Result.Summary != "First." {
					t.Errorf("result = %+v", out.Result)
				}
			}},
		"agent error":       {script: func(_ *fixture, c *call) { c.send(usage(700), agentError("rate_limited")) }, kind: Failed, state: "failed", cost: 700},
		"result then error": {script: func(_ *fixture, c *call) { c.send(usage(700), resultEvent("Fine."), agentError("rate_limited")) }, kind: Failed, state: "failed", cost: 700},
		"vendor drop": {script: func(_ *fixture, c *call) { c.send(usage(700), agentError("unavailable")) }, kind: Failed, state: "failed", cost: 700,
			check: func(t *testing.T, out Outcome) {
				if !out.ModelUnreachable {
					t.Error("the model's being unreachable was not reported")
				}
			}},
		"no result":         {script: func(_ *fixture, c *call) { c.send(usage(800), agentError("no_result")) }, kind: NoResult, state: "failed", cost: 800},
		"done without":      {script: func(_ *fixture, c *call) { c.send(usage(900), doneEvent) }, kind: LimitReached, state: "failed", cost: 900},
		"agent refuses":     {script: func(_ *fixture, c *call) { c.w.WriteHeader(http.StatusTooManyRequests) }, kind: AgentBusy, state: "failed"},
		"agent unreachable": {script: func(_ *fixture, c *call) { c.w.WriteHeader(http.StatusBadGateway) }, kind: AgentUnavailable, state: "failed"},
		"unknown model":     {script: func(_ *fixture, c *call) { c.w.WriteHeader(http.StatusNotFound) }, kind: Failed, state: "failed"},
		"stream breaks":     {script: func(_ *fixture, c *call) { c.send(usage(600)) }, kind: AgentUnavailable, state: "failed", cost: 600},
		"cancel": {script: func(_ *fixture, c *call) { c.send(usage(600)); c.hold() }, stop: true, kind: Cancelled, state: "failed", cost: 600,
			check: func(t *testing.T, out Outcome) {
				if !out.Called {
					t.Error("a stopped run that was called is not reported as called")
				}
			}},
		"overdue": {script: func(_ *fixture, c *call) { c.send(usage(600)); c.hold() },
			prepare: func(f *fixture, _ *Job) { f.driver.Slack = -59 * time.Second }, kind: Failed, state: "failed", cost: 600},
		"panic in the event handler": {script: func(_ *fixture, c *call) {
			c.send(usage(500))
			c.send(map[string]any{"type": "tool_start", "tool": "get_service"}, resultEvent("Fine."), doneEvent)
		},
			prepare: func(f *fixture, _ *Job) {
				f.driver.OnEvent = func(ev agentclient.Event) {
					if ev.Type == "tool_start" {
						panic("PANIC-SENTINEL")
					}
				}
			}, kind: Failed, state: "failed", cost: 500},
		"swept run": {script: func(f *fixture, c *call) {
			c.send(usage(500))
			f.wait("the usage to be stored", func() bool { r := f.runs(); return len(r) == 1 && r[0].cost == 500 })
			if _, err := f.st.Pool.Exec(context.Background(), `update agent_runs set state = 'failed', finished_at = now()`); err != nil {
				t.Error(err)
			}
			c.send(usage(900), resultEvent("Fine."), doneEvent)
			c.hold()
		}, kind: Interrupted, state: "failed", cost: 900},
		"the claim is lost at the start": {prepare: func(_ *fixture, job *Job) {
			job.Started = func(context.Context, uuid.UUID, string, string) error { return store.ErrConflict }
		}, kind: Conflict, state: "failed"},
		"the start fails": {prepare: func(_ *fixture, job *Job) {
			job.Started = func(context.Context, uuid.UUID, string, string) error { return errors.New("down") }
		}, kind: Failed, state: "failed", check: func(t *testing.T, out Outcome) {
			if out.Called || out.Panicked {
				t.Errorf("outcome = %+v, want a failure before the call", out)
			}
		}},
		"the token cannot be made": {prepare: func(f *fixture, _ *Job) {
			if _, err := f.st.Pool.Exec(context.Background(), `alter table api_tokens add constraint no_token check (kind <> 'run')`); err != nil {
				t.Fatal(err)
			}
		}, kind: Failed, state: "failed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			spoke := make(chan struct{}, 8)
			if c.script != nil {
				f.agent.set(func(call *call) {
					if c.stop {
						spoke <- struct{}{}
					}
					c.script(f, call)
				})
			}
			var started, concluded, saved atomic.Int32
			job := Job{Role: testRole, Details: map[string]any{"investigation_id": 7},
				Started: func(context.Context, uuid.UUID, string, string) error { started.Add(1); return nil }}
			job.Conclude = func(out Outcome) Conclusion {
				concluded.Add(1)
				if out.Kind != c.kind {
					t.Errorf("Conclude saw %v, want %v", out.Kind, c.kind)
				}
				word := "w"
				if out.Kind == Done {
					word = "done" // the store replaces a word that contradicts the state
				}
				return Conclusion{Word: word, Save: func(context.Context) error { saved.Add(1); return nil }}
			}
			p := f.prepared()
			if c.prepare != nil {
				c.prepare(f, &job)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got := make(chan Outcome, 1)
			go func() { got <- f.driver.Run(ctx, p, job) }()
			if c.stop {
				<-spoke
				f.wait("the usage to be stored", func() bool { r := f.runs(); return len(r) == 1 && r[0].cost > 0 })
				cancel()
			}
			var out Outcome
			select {
			case out = <-got:
			case <-time.After(60 * time.Second):
				t.Fatal("Run did not return")
			}
			if out.Kind != c.kind {
				t.Fatalf("kind = %v, want %v (%+v)", out.Kind, c.kind, out)
			}
			runs := f.runs()
			if len(runs) != 1 || runs[0].state != c.state || runs[0].cost != c.cost || runs[0].tokens != 0 || runs[0].audits != 1 {
				t.Fatalf("runs = %+v, want one %s run of %d, no token, one audit row", runs, c.state, c.cost)
			}
			if !out.Finished || out.SaveErr != nil || out.RunID == uuid.Nil || out.Provider != testProvider || out.Model != testModel {
				t.Errorf("outcome = %+v", out)
			}
			if concluded.Load() != 1 || saved.Load() != 1 {
				t.Errorf("Conclude ran %d times and Save %d, want once each", concluded.Load(), saved.Load())
			}
			rows := f.auditRows()
			if len(rows) != 1 || rows[0].Details["outcome"] != map[bool]string{true: "done", false: "w"}[c.kind == Done] || rows[0].Details["saved"] != true || rows[0].Details["investigation_id"] != float64(7) {
				t.Errorf("audit = %+v", rows)
			}
			if c.check != nil {
				c.check(t, out)
			}
			if strings.Contains(f.logs.String(), "MESSAGE-SENTINEL") || strings.Contains(f.logs.String(), "PANIC-SENTINEL") {
				t.Errorf("the log holds what the agent said:\n%s", f.logs.String())
			}
		})
	}
}

// A refused reservation spends nothing and leaves no run; the caller audits it.
func TestDriver_ABudgetRefusalLeavesNoRun(t *testing.T) {
	f := newFixture(t)
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = testReservation - 1 })
	p := f.prepared()
	var started atomic.Int32
	job := Job{Role: testRole, Details: map[string]any{"investigation_id": 7},
		Started: func(context.Context, uuid.UUID, string, string) error { started.Add(1); return nil },
		Conclude: func(Outcome) Conclusion {
			t.Error("Conclude ran for a run that was never reserved")
			return Conclusion{}
		}}
	out := f.driver.Run(context.Background(), p, job)
	if out.Kind != Budget || out.Finished || started.Load() != 0 || f.agent.calls() != 0 || len(f.runs()) != 0 || len(f.auditRows()) != 0 {
		t.Fatalf("outcome %+v, %d starts, %d calls, %d runs, %d audit rows", out, started.Load(), f.agent.calls(), len(f.runs()), len(f.auditRows()))
	}
	f.driver.AuditSkippedBudget(context.Background(), p, out, testRole, job.Details)
	rows := f.auditRows()
	if len(rows) != 1 || rows[0].Details["outcome"] != "skipped_budget" || rows[0].ActorUsername != f.identity.Username || rows[0].Target != out.RunID.String() ||
		rows[0].Details["investigation_id"] != float64(7) || rows[0].Details["model"] != testModel || rows[0].Details["cost_micro_usd"] != float64(0) {
		t.Errorf("audit = %+v", rows)
	}
}

// A run that was reserved when the lease is lost is settled and the agent is never called.
func TestDriver_ALostLeaseStartsNothingMore(t *testing.T) {
	f := newFixture(t)
	p := f.prepared()
	var checks atomic.Int32
	f.driver.Guard = func(context.Context) error {
		if checks.Add(1) == 2 { // before the reservation is fine, after it is not
			return errors.New("lease lost")
		}
		return nil
	}
	var over atomic.Int32
	out := f.driver.Run(context.Background(), p, Job{Role: testRole, SetUpOver: func() { over.Add(1) },
		Started: func(context.Context, uuid.UUID, string, string) error { t.Error("started"); return nil }})
	runs := f.runs()
	if out.Kind != Cancelled || out.Called || !out.Finished || f.agent.calls() != 0 || over.Load() != 1 ||
		len(runs) != 1 || runs[0].state != "failed" || runs[0].tokens != 0 || runs[0].audits != 1 {
		t.Errorf("outcome %+v, %d calls, setup over %d, runs %+v", out, f.agent.calls(), over.Load(), runs)
	}
}

// EndEarlier settles a run that an earlier holder left, with the figures its row has.
func TestDriver_EndEarlierSettlesALeftRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	run := store.AgentRun{ID: uuid.New(), Role: testRole, UserID: &f.identity.ID, Provider: testProvider, Model: testModel, ReservedMicroUSD: testReservation}
	if err := f.st.ReserveAgentRun(ctx, run, 10*time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordAgentRunUsage(ctx, run.ID, 420, 42, 3300, time.Now()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.driver.EndEarlier(ctx, f.identity, run, map[string]any{"investigation_id": 7}); err != nil {
			t.Fatal(err)
		}
	}
	runs := f.runs()
	rows := f.auditRows()
	if len(runs) != 1 || runs[0].state != "failed" || runs[0].cost != 3300 || runs[0].audits != 1 ||
		len(rows) != 1 || rows[0].Details["outcome"] != "interrupted" || rows[0].Details["investigation_id"] != float64(7) {
		t.Errorf("runs %+v, audit %+v", runs, rows)
	}
	gone := store.AgentRun{ID: uuid.New(), Role: testRole, UserID: &f.identity.ID}
	if err := f.driver.EndEarlier(ctx, f.identity, gone, nil); err != nil {
		t.Errorf("a run that is gone: %v", err)
	}
}

// ---- Accept --------------------------------------------------------------------------------------------------------

func viewItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service", ServiceID: checkoutKey.ID(), Action: "view", Note: "The service has no running tasks.", Excerpt: excerpt}
}

func logsItem(excerpt string) agentresult.Evidence {
	return agentresult.Evidence{Tool: "get_service_logs", ServiceID: checkoutKey.ID(), Action: "logs", Note: "The start fails.", Excerpt: excerpt}
}

func resultWith(items ...agentresult.Evidence) agentresult.Result {
	return agentresult.Result{Summary: "The service stopped after its last deploy.", Evidence: items}
}

func readAll(model.ServiceKey, authz.Action) bool { return true }

// Phase 2's Accept, now here: the same decisions for the same inputs.
func TestAccept_MovedUnchanged(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	lying := logsItem("connection refused")
	lying.Action = "view"
	upper := viewItem("desired 2, running 0")
	upper.Action = "VIEW"
	unknownTool := viewItem("x")
	unknownTool.Tool = "delete_service"
	badID := viewItem("x")
	badID.ServiceID = encode("482109375512|us-east-1|payments-prod|check\x00out")
	other := viewItem("x")
	other.ServiceID = ledgerKey.ID()
	secret := logsItem("login with " + fakeSecret + " refused")
	secret.Note = "Key " + fakeSecret
	var many []agentresult.Evidence
	for range 15 {
		many = append(many, viewItem("desired 2, running 0"))
	}
	viewOnly := func(key model.ServiceKey, action authz.Action) bool {
		return key == checkoutKey && action == authz.View
	}
	huge := resultWith(viewItem("ok"), unknownTool)
	huge.EvidenceDropped = math.MaxInt

	cases := map[string]struct {
		res           agentresult.Result
		can           func(model.ServiceKey, authz.Action) bool
		kept, dropped int
		check         func(t *testing.T, got store.InvestigationResult)
	}{
		"the action is derived from the tool": {res: resultWith(lying, upper, logsItem("connection refused"), viewItem("desired 2, running 0")), can: readAll, kept: 2, dropped: 2,
			check: func(t *testing.T, got store.InvestigationResult) {
				if got.Evidence[0].Action != "logs" || got.Evidence[1].Action != "view" || got.Evidence[0].ServiceName != checkoutKey.Service {
					t.Errorf("evidence = %+v", got.Evidence)
				}
			}},
		"unknown tools":                 {res: resultWith(unknownTool, viewItem("ok")), can: readAll, kept: 1, dropped: 1},
		"unparsable service ids":        {res: resultWith(badID, viewItem("ok")), can: readAll, kept: 1, dropped: 1},
		"what the identity cannot read": {res: resultWith(logsItem("secret plan"), viewItem("desired 2"), other), can: viewOnly, kept: 1, dropped: 2},
		"no way to tell":                {res: resultWith(viewItem("ok")), can: nil, kept: 0, dropped: 1},
		"twelve at most":                {res: resultWith(many...), can: readAll, kept: 12, dropped: 3},
		"an empty excerpt":              {res: resultWith(viewItem(" \x00\n"), viewItem("ok")), can: readAll, kept: 1, dropped: 1},
		"a count that fits the column":  {res: huge, can: readAll, kept: 1, dropped: 1_000_000},
		"secrets are scrubbed": {res: resultWith(secret), can: readAll, kept: 1, dropped: 0, check: func(t *testing.T, got store.InvestigationResult) {
			if e := got.Evidence[0]; strings.Contains(e.Excerpt, fakeSecret) || e.Note != agentresult.ReplacementNote {
				t.Errorf("excerpt %q, note %q", e.Excerpt, e.Note)
			}
		}},
		"a summary that is a secret": {res: agentresult.Result{Summary: "It uses " + fakeSecret + " to sign."}, can: readAll, kept: 0, dropped: 0,
			check: func(t *testing.T, got store.InvestigationResult) {
				if got.Summary != agentresult.ReplacementSummary || !got.SummaryReplaced {
					t.Errorf("summary = %q, replaced %v", got.Summary, got.SummaryReplaced)
				}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := Accept(c.res, c.can)
			if len(got.Evidence) != c.kept || got.EvidenceDropped != c.dropped {
				t.Fatalf("kept %d, dropped %d; want %d and %d: %+v", len(got.Evidence), got.EvidenceDropped, c.kept, c.dropped, got.Evidence)
			}
			for i, e := range got.Evidence {
				if e.Ordinal != i+1 {
					t.Errorf("ordinal %d at %d", e.Ordinal, i)
				}
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

var _ = fmt.Sprint
