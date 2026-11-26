package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const reconcileStale = 20 * time.Minute

// agentAuditRows returns the agent_run audit rows, newest first.
func agentAuditRows(t *testing.T, st *store.Store) []store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(t.Context(), 200, 0)
	mustNoErr(t, err)
	var out []store.AuditEntry
	for _, e := range entries {
		if e.Action == "agent_run" {
			out = append(out, e)
		}
	}
	return out
}

func reserveAt(t *testing.T, st *store.Store, userID *int64, at time.Time) store.AgentRun {
	t.Helper()
	run := agentRun(100)
	run.UserID = userID
	mustNoErr(t, st.ReserveAgentRun(t.Context(), run, 1000*time.Hour, at))
	return run
}

func tokenFor(t *testing.T, st *store.Store, userID int64, runID uuid.UUID) {
	t.Helper()
	_, err := st.CreateRunToken(t.Context(), newRunToken(userID, "hma_"+runID.String()[:8], runID))
	mustNoErr(t, err)
}

func tokenCount(t *testing.T, st *store.Store, runID uuid.UUID) (n int) {
	t.Helper()
	mustNoErr(t, st.Pool.QueryRow(t.Context(), `select count(*) from api_tokens where kind = 'run' and run_id = $1`, runID).Scan(&n))
	return n
}

func reconcile(t *testing.T, st *store.Store, now time.Time) store.AgentReconcileResult {
	t.Helper()
	res, err := st.ReconcileAgentRuns(t.Context(), reconcileStale, now)
	mustNoErr(t, err)
	return res
}

func TestReconcileAgentRuns_RepairsAnAbandonedRunInOnePass(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	u, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	mustNoErr(t, err)
	run := reserveAt(t, st, &u.ID, agentNow)
	tokenFor(t, st, u.ID, run.ID)
	mustNoErr(t, st.RecordAgentRunUsage(t.Context(), run.ID, 11, 7, 93, agentNow.Add(time.Minute)))

	// One second before it is stale (measured from its last usage) a live run and its token are left alone.
	if res := reconcile(t, st, agentNow.Add(time.Minute+reconcileStale)); res != (store.AgentReconcileResult{}) {
		t.Fatalf("a live run was touched: %+v", res)
	}
	if state, _, _, _, _, _ := runRow(t, st, run.ID); state != "running" || tokenCount(t, st, run.ID) != 1 {
		t.Fatalf("a live run was touched: state %s", state)
	}

	now := agentNow.Add(time.Minute + reconcileStale + time.Second)
	if res := reconcile(t, st, now); res != (store.AgentReconcileResult{Settled: 1, TokensDeleted: 1, Audited: 1}) {
		t.Errorf("result = %+v", res)
	}
	if state, cost, _, _, _, _ := runRow(t, st, run.ID); state != "failed" || cost != 93 {
		t.Errorf("run = %s cost %d, want failed 93", state, cost)
	}
	var finished time.Time
	mustNoErr(t, st.Pool.QueryRow(t.Context(), `select finished_at from agent_runs where id = $1`, run.ID).Scan(&finished))
	if !finished.Equal(now) {
		t.Errorf("finished_at = %v, want %v", finished, now)
	}
	if n := tokenCount(t, st, run.ID); n != 0 {
		t.Errorf("%d tokens left", n)
	}
	rows := agentAuditRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	e, d := rows[0], rows[0].Details
	if e.ActorUserID == nil || *e.ActorUserID != u.ID || e.ActorUsername != "alice" || e.Target != run.ID.String() ||
		d["run_id"] != run.ID.String() || d["outcome"] != "failed" || d["cost_micro_usd"] != float64(93) || d["input_tokens"] != float64(11) ||
		d["output_tokens"] != float64(7) || d["role"] != "chat" || d["provider"] != "fake" || d["model"] != "m" || fmt.Sprint(d["tools"]) != "[]" {
		t.Errorf("audit = %+v", e)
	}
	if res := reconcile(t, st, now.Add(time.Hour)); res != (store.AgentReconcileResult{}) {
		t.Errorf("the second pass repaired something: %+v", res)
	}
	if n := len(agentAuditRows(t, st)); n != 1 {
		t.Errorf("%d audit rows after a second pass", n)
	}
}

// A finished run is given a minute to write its own audit row; its outcome is the row's, and a user deleted since
// leaves the actor empty.
func TestReconcileAgentRuns_AuditsFinishedRunsAfterAMinute(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	u, err := st.CreateUser(t.Context(), "gone", "h", false, false)
	mustNoErr(t, err)
	done := reserveAt(t, st, &u.ID, agentNow)
	mustNoErr(t, st.SettleAgentRun(t.Context(), done.ID, "done", nil, []string{"get_service"}, agentNow))
	mustNoErr(t, storetest.DeleteUser(t.Context(), st, u.ID))

	if res := reconcile(t, st, agentNow.Add(59*time.Second)); res.Audited != 0 {
		t.Fatalf("audited within the minute: %+v", res)
	}
	if res := reconcile(t, st, agentNow.Add(61*time.Second)); res.Audited != 1 {
		t.Fatalf("result = %+v, want one audited", res)
	}
	rows := agentAuditRows(t, st)
	if len(rows) != 1 || rows[0].Details["outcome"] != "done" || fmt.Sprint(rows[0].Details["tools"]) != "[get_service]" ||
		rows[0].ActorUserID != nil || rows[0].ActorUsername != "" {
		t.Errorf("audit rows = %+v", rows)
	}
}

func TestReconcileAgentRuns_DeletesOnlyTokensWhoseRunIsOver(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	u, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	mustNoErr(t, err)
	live, settled := reserveAt(t, st, &u.ID, agentNow), reserveAt(t, st, &u.ID, agentNow)
	mustNoErr(t, st.SettleAgentRun(t.Context(), settled.ID, "done", nil, nil, agentNow))
	unknown := uuid.New()
	for _, id := range []uuid.UUID{live.ID, settled.ID, unknown} {
		tokenFor(t, st, u.ID, id)
	}
	if _, err := st.CreateAPIToken(t.Context(), store.APIToken{PublicID: "hmp_keep", UserID: u.ID, Kind: "pat", Name: "keep",
		SecretHash: "x", Scope: "read", ExpiresAt: time.Now().Add(time.Hour)}, 10); err != nil {
		t.Fatal(err)
	}
	if res := reconcile(t, st, agentNow.Add(time.Second)); res.TokensDeleted != 2 {
		t.Errorf("result = %+v, want two tokens deleted", res)
	}
	if tokenCount(t, st, live.ID) != 1 || tokenCount(t, st, settled.ID) != 0 || tokenCount(t, st, unknown) != 0 {
		t.Error("wrong tokens deleted")
	}
	var n int
	mustNoErr(t, st.Pool.QueryRow(t.Context(), `select count(*) from api_tokens where kind = 'pat'`).Scan(&n))
	if n != 1 {
		t.Error("a personal token was deleted")
	}
}

// A row somebody else holds must not stop the pass: the rest is repaired and the held row is left to the next pass.
func TestReconcileAgentRuns_SkipsLockedRows(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	u, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	mustNoErr(t, err)
	stale, staleHeld := reserveAt(t, st, &u.ID, agentNow), reserveAt(t, st, &u.ID, agentNow)
	doneHeld, done := reserveAt(t, st, &u.ID, agentNow), reserveAt(t, st, &u.ID, agentNow)
	for _, r := range []store.AgentRun{doneHeld, done} {
		mustNoErr(t, st.SettleAgentRun(t.Context(), r.ID, "done", nil, nil, agentNow))
	}
	tokenFor(t, st, u.ID, done.ID)
	tokenFor(t, st, u.ID, doneHeld.ID)
	tx, err := st.Pool.Begin(t.Context())
	mustNoErr(t, err)
	defer tx.Rollback(context.Background()) //nolint:errcheck
	_, err = tx.Exec(t.Context(), `select id from agent_runs where id = any($1) for update`, []uuid.UUID{staleHeld.ID, doneHeld.ID})
	mustNoErr(t, err)
	_, err = tx.Exec(t.Context(), `select id from api_tokens where run_id = $1 for update`, doneHeld.ID)
	mustNoErr(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	now := agentNow.Add(reconcileStale + time.Minute)
	res, err := st.ReconcileAgentRuns(ctx, reconcileStale, now)
	if err != nil || res != (store.AgentReconcileResult{Settled: 1, TokensDeleted: 1, Audited: 2}) {
		t.Fatalf("with rows held: %+v, %v", res, err)
	}
	if state, _, _, _, _, _ := runRow(t, st, stale.ID); state != "failed" {
		t.Errorf("stale run = %s", state)
	}
	mustNoErr(t, tx.Rollback(t.Context()))
	if res := reconcile(t, st, now); res != (store.AgentReconcileResult{Settled: 1, TokensDeleted: 1, Audited: 2}) {
		t.Errorf("after the locks are gone: %+v", res)
	}
	if n := len(agentAuditRows(t, st)); n != 4 {
		t.Errorf("%d audit rows, want one per run", n)
	}
}

// A row that was held for a moment only does not have to wait for the next pass.
func TestReconcileAgentRuns_LooksAgainAtARowHeldForAMoment(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	run := reserveAt(t, st, nil, agentNow)
	tx, err := st.Pool.Begin(t.Context())
	mustNoErr(t, err)
	_, err = tx.Exec(t.Context(), `select id from agent_runs for update`)
	mustNoErr(t, err)
	time.AfterFunc(100*time.Millisecond, func() { _ = tx.Rollback(context.Background()) })
	if res := reconcile(t, st, agentNow.Add(time.Hour)); res.Settled != 1 || res.Audited != 1 {
		t.Errorf("result = %+v, want the run %s settled and audited", res, run.ID)
	}
}

// One part failing must not keep the others from running.
func TestReconcileAgentRuns_EachPartRunsWhenAnotherFails(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	u, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	mustNoErr(t, err)
	stale, done := reserveAt(t, st, &u.ID, agentNow), reserveAt(t, st, &u.ID, agentNow)
	mustNoErr(t, st.SettleAgentRun(t.Context(), done.ID, "done", nil, nil, agentNow))
	tokenFor(t, st, u.ID, done.ID)
	for _, q := range []string{
		`create function refuse() returns trigger language plpgsql as $$ begin raise exception 'refused'; end $$`,
		`create trigger refuse_settle before update of state on agent_runs for each row execute function refuse()`,
		`create trigger refuse_delete before delete on api_tokens for each row execute function refuse()`,
	} {
		_, err := st.Pool.Exec(t.Context(), q)
		mustNoErr(t, err)
	}
	res, err := st.ReconcileAgentRuns(t.Context(), reconcileStale, agentNow.Add(time.Hour))
	if err == nil || res.Audited != 1 {
		t.Errorf("result = %+v, %v; want an error and the finished run audited", res, err)
	}
	if state, _, _, _, _, _ := runRow(t, st, stale.ID); state != "running" {
		t.Errorf("stale run = %s", state)
	}
}

// A run whose audit row cannot be written must not keep the runs after it from being audited.
func TestReconcileAgentRuns_OneUnwritableAuditRowDoesNotBlockTheRest(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	bad, good := reserveAt(t, st, nil, agentNow), reserveAt(t, st, nil, agentNow)
	mustNoErr(t, st.SettleAgentRun(t.Context(), bad.ID, "done", nil, nil, agentNow))
	mustNoErr(t, st.SettleAgentRun(t.Context(), good.ID, "done", nil, nil, agentNow.Add(time.Second)))
	for _, q := range []string{
		fmt.Sprintf(`create function refuse() returns trigger language plpgsql as $$ begin if new.target = '%s' then raise exception 'refused'; end if; return new; end $$`, bad.ID),
		`create trigger refuse before insert on audit_log for each row execute function refuse()`,
	} {
		_, err := st.Pool.Exec(t.Context(), q)
		mustNoErr(t, err)
	}
	res, err := st.ReconcileAgentRuns(t.Context(), reconcileStale, agentNow.Add(time.Hour))
	if err == nil || res.Audited != 1 {
		t.Errorf("result = %+v, %v", res, err)
	}
	var audited bool
	mustNoErr(t, st.Pool.QueryRow(t.Context(), `select audited_at is not null from agent_runs where id = $1`, bad.ID).Scan(&audited))
	if audited {
		t.Error("a run was marked audited without its row")
	}
}

func TestReconcileAgentRuns_ConcurrentPassesAndALiveTurnAuditOnce(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	var runs []store.AgentRun
	for range 10 {
		r := reserveAt(t, st, nil, agentNow)
		mustNoErr(t, st.SettleAgentRun(t.Context(), r.ID, "done", nil, nil, agentNow))
		runs = append(runs, r)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.ReconcileAgentRuns(context.Background(), reconcileStale, agentNow.Add(time.Hour)); err != nil {
				t.Error(err)
			}
		}()
	}
	for _, r := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.AuditAgentRun(context.Background(), r.ID, store.AgentRunAudit(r, "alice", "done")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows := agentAuditRows(t, st)
	seen := map[string]int{}
	for _, e := range rows {
		seen[e.Target]++
	}
	if len(rows) != 10 || len(seen) != 10 {
		t.Errorf("%d audit rows over %d runs, want exactly one for each of 10", len(rows), len(seen))
	}
}

func TestAuditAgentRun_OnceAndOnlyForASettledRun(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	run := reserveAt(t, st, nil, agentNow)
	entry := store.AgentRunAudit(run, "alice", "done")
	if err := st.AuditAgentRun(t.Context(), run.ID, entry); !errors.Is(err, store.ErrAgentRunRunning) {
		t.Errorf("a running run: %v, want ErrAgentRunRunning", err)
	}
	if err := st.AuditAgentRun(t.Context(), uuid.New(), entry); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown run: %v, want ErrNotFound", err)
	}
	if n := len(agentAuditRows(t, st)); n != 0 {
		t.Fatalf("%d audit rows written", n)
	}
	mustNoErr(t, st.SettleAgentRun(t.Context(), run.ID, "done", nil, nil, agentNow))
	for range 2 {
		mustNoErr(t, st.AuditAgentRun(t.Context(), run.ID, entry))
	}
	if n := len(agentAuditRows(t, st)); n != 1 {
		t.Errorf("%d audit rows, want 1", n)
	}
}

// The audit row never contradicts the run row: a finer word for the same end is kept, another end is replaced.
func TestAuditAgentRun_OutcomeAgreesWithTheRow(t *testing.T) {
	for _, tc := range []struct{ state, given, want string }{
		{"done", "done", "done"}, {"done", "interrupted", "done"},
		{"failed", "done", "failed"}, {"failed", "interrupted", "interrupted"}, {"failed", "error", "error"},
	} {
		st := storetest.Open(t)
		setBudget(t, st, 1_000_000)
		run := reserveAt(t, st, nil, agentNow)
		mustNoErr(t, st.SettleAgentRun(t.Context(), run.ID, tc.state, nil, nil, agentNow))
		mustNoErr(t, st.AuditAgentRun(t.Context(), run.ID, store.AgentRunAudit(run, "", tc.given)))
		if rows := agentAuditRows(t, st); len(rows) != 1 || rows[0].Details["outcome"] != tc.want {
			t.Errorf("%s row, %s given: %+v, want outcome %s", tc.state, tc.given, rows, tc.want)
		}
	}
}

// The audit row says what the run row says, whatever figures its writer had.
func TestAuditAgentRun_FiguresComeFromTheRow(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	run := reserveAt(t, st, nil, agentNow)
	mustNoErr(t, st.SettleAgentRun(t.Context(), run.ID, "done", &store.AgentUsage{InputTokens: 1, OutputTokens: 2, CostMicroUSD: 70}, []string{"get_service"}, agentNow))
	run.InputTokens, run.OutputTokens, run.CostMicroUSD = 9, 9, 999
	mustNoErr(t, st.AuditAgentRun(t.Context(), run.ID, store.AgentRunAudit(run, "", "done")))
	d := agentAuditRows(t, st)[0].Details
	if d["input_tokens"] != float64(1) || d["output_tokens"] != float64(2) || d["cost_micro_usd"] != float64(70) || fmt.Sprint(d["tools"]) != "[get_service]" {
		t.Errorf("audit = %v", d)
	}
}
