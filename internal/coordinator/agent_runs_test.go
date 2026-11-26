package coordinator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// reconcileRecorder notes the passes of a reconciler and can make them fail or panic.
type reconcileRecorder struct {
	*store.Store
	mu     sync.Mutex
	passes []time.Duration // the stale age of each
	panics bool
}

func (r *reconcileRecorder) ReconcileAgentRuns(ctx context.Context, staleAfter time.Duration, now time.Time) (store.AgentReconcileResult, error) {
	r.mu.Lock()
	r.passes = append(r.passes, staleAfter)
	first := len(r.passes) == 1
	r.mu.Unlock()
	if r.panics && first {
		panic("boom")
	}
	return r.Store.ReconcileAgentRuns(ctx, staleAfter, now)
}

func (r *reconcileRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.passes)
}

// abandonedRun is a run that a dead process left running, its token with it, started that long ago.
func abandonedRun(t *testing.T, st *store.Store, ago time.Duration) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if err := storetest.PutAgentSettings(ctx, st, store.AgentSettings{MonthlyBudgetMicroUSD: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "alice"+uuid.NewString()[:8], "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := st.ReserveAgentRun(ctx, store.AgentRun{ID: id, Role: "chat", UserID: &u.ID, Provider: "fake", Model: "m", ReservedMicroUSD: 1},
		1000*time.Hour, time.Now().Add(-ago)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRunToken(ctx, store.APIToken{PublicID: "hma_" + id.String()[:8], UserID: u.ID, Kind: "run", Name: "agent run",
		SecretHash: "hash", Scope: "read", ExpiresAt: time.Now().Add(time.Hour), RunID: &id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func agentState(t *testing.T, st *store.Store, id uuid.UUID) (state string, tokens, audits int) {
	t.Helper()
	if err := st.Pool.QueryRow(context.Background(), `select state,
		(select count(*) from api_tokens where run_id = $1), (select count(*) from audit_log where target = $1::text)
		from agent_runs where id = $1`, id).Scan(&state, &tokens, &audits); err != nil {
		t.Fatal(err)
	}
	return state, tokens, audits
}

func TestAgentReconcile_RunsAtOnceAndAgainEveryIntervalUnderTheLease(t *testing.T) {
	st := storetest.Open(t)
	// Twice the longest run the agent can be configured for is an hour: only a run past that is stale.
	stale, live := abandonedRun(t, st, 61*time.Minute), abandonedRun(t, st, 59*time.Minute)
	rec := &reconcileRecorder{Store: st, panics: true}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour,
		AgentRuns: &agentrun.Reconciler{Store: rec, Interval: 100 * time.Millisecond, Log: quiet()}}
	_, stop := startManager(t, st, r)
	// The first pass panics; the next one still comes and repairs the run.
	waitFor(t, 10*time.Second, "a second pass", func() bool { return rec.count() >= 3 })
	if state, tokens, audits := agentState(t, st, stale); state != "failed" || tokens != 0 || audits != 1 {
		t.Errorf("stale run: %s, %d tokens, %d audit rows; want failed, none, one", state, tokens, audits)
	}
	if state, tokens, audits := agentState(t, st, live); state != "running" || tokens != 1 || audits != 0 {
		t.Errorf("live run: %s, %d tokens, %d audit rows; want it untouched", state, tokens, audits)
	}
	stop()
	if r.AgentRuns.Guard != nil {
		t.Error("the stopped holder left its guard set")
	}
	n := rec.count()
	time.Sleep(300 * time.Millisecond)
	if rec.count() != n {
		t.Error("passes went on after the lease was given back")
	}
}

func TestAgentReconcile_FirstPassDoesNotWaitForTheInterval(t *testing.T) {
	st := storetest.Open(t)
	rec := &reconcileRecorder{Store: st}
	startManager(t, st, &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour,
		AgentRuns: &agentrun.Reconciler{Store: rec, Interval: time.Hour, Log: quiet()}})
	waitFor(t, 10*time.Second, "the first pass", func() bool { return rec.count() == 1 })
}

// A holder that cannot confirm the coordinator lease repairs nothing.
func TestAgentReconcile_DoesNothingWithoutTheLease(t *testing.T) {
	st := storetest.Open(t)
	id := abandonedRun(t, st, 2*time.Hour)
	rec := &reconcileRecorder{Store: st}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour,
		AgentRuns: &agentrun.Reconciler{Store: rec, Interval: 50 * time.Millisecond, Log: quiet()}}
	if err := st.EnsureCoordinatorLease(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r.Hold(ctx, store.Lease{Key: store.LeaseKey{Kind: store.LeaseCoordinator}, Epoch: 1})
	if state, _, _ := agentState(t, st, id); state != "running" || rec.count() != 0 {
		t.Errorf("run %s after %d passes without the lease", state, rec.count())
	}
}
