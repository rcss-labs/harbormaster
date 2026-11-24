package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var agentNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func agentRun(reserved int64) store.AgentRun {
	return store.AgentRun{ID: uuid.New(), Role: "chat", Provider: "fake", Model: "m", ReservedMicroUSD: reserved}
}

func setBudget(t *testing.T, st *store.Store, micro int64) {
	t.Helper()
	if err := st.PutAgentSettings(t.Context(), store.AgentSettings{MonthlyBudgetMicroUSD: micro}); err != nil {
		t.Fatal(err)
	}
}

func TestReserveAgentRun_RefusesWhenBudgetWouldBeExceeded(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	r := agentRun(600)
	if err := st.ReserveAgentRun(ctx, r, time.Hour, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 500, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleAgentRun(ctx, r.ID, "done", nil, agentNow); err != nil {
		t.Fatal(err)
	}
	// 500 settled so far: 500 more fits exactly, 501 does not.
	over := agentRun(501)
	if err := st.ReserveAgentRun(ctx, over, time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("reserve over budget = %v, want ErrBudgetExhausted", err)
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `select count(*) from agent_runs where id = $1`, over.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused run was written: %d, %v", n, err)
	}
	if err := st.ReserveAgentRun(ctx, agentRun(500), time.Hour, agentNow); err != nil {
		t.Fatalf("reserve exactly to budget: %v", err)
	}
}

func TestReserveAgentRun_ZeroBudgetRefuses(t *testing.T) {
	st := storetest.Open(t)
	if err := st.ReserveAgentRun(t.Context(), agentRun(1), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("reserve with budget 0 = %v, want ErrBudgetExhausted", err)
	}
}

func TestReserveAgentRun_CountsRunningRunsAtReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	if err := st.ReserveAgentRun(ctx, agentRun(700), time.Hour, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := st.ReserveAgentRun(ctx, agentRun(400), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("second reserve = %v, want ErrBudgetExhausted", err)
	}
	settled, reserved, err := st.AgentSpend(ctx, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || settled != 0 || reserved != 700 {
		t.Fatalf("spend = %d settled, %d reserved, %v; want 0, 700", settled, reserved, err)
	}
}

// Twenty callers race for a budget that fits three: the settings-row lock must admit exactly three.
func TestReserveAgentRun_ConcurrentReservationsNeverExceedBudget(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 300)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = st.ReserveAgentRun(ctx, agentRun(100), time.Hour, agentNow)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrBudgetExhausted):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 3 {
		t.Fatalf("%d reservations succeeded, want exactly 3", ok)
	}
	_, reserved, err := st.AgentSpend(ctx, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || reserved != 300 {
		t.Fatalf("reserved = %d, %v; want 300", reserved, err)
	}
}

func TestReserveAgentRun_SettlesStaleRunsFirst(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	stale := agentRun(900)
	if err := st.ReserveAgentRun(ctx, stale, time.Hour, agentNow.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentRunUsage(ctx, stale.ID, 10, 10, 200, agentNow.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The stale run keeps its recorded cost (200) and gives back the rest of its reservation.
	if err := st.ReserveAgentRun(ctx, agentRun(800), time.Hour, agentNow); err != nil {
		t.Fatalf("reserve after stale sweep: %v", err)
	}
	var state string
	var cost int64
	if err := st.Pool.QueryRow(ctx, `select state, cost_micro_usd from agent_runs where id = $1`, stale.ID).Scan(&state, &cost); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || cost != 200 {
		t.Fatalf("stale run = %s / %d, want failed / 200", state, cost)
	}

	// The sweep is committed even when the reservation that triggered it is refused.
	old := agentRun(0)
	if _, err := st.Pool.Exec(ctx, `insert into agent_runs (id, role, provider, model, state, started_at) values ($1, 'chat', 'fake', 'm', 'running', $2)`,
		old.ID, agentNow.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReserveAgentRun(ctx, agentRun(999999), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("refusal expected, got %v", err)
	}
	if err := st.Pool.QueryRow(ctx, `select state from agent_runs where id = $1`, old.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("stale run after refused reserve = %q, %v; want failed", state, err)
	}
}

func TestSettleAgentRun_FreesUnusedReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	r := agentRun(900)
	if err := st.ReserveAgentRun(ctx, r, time.Hour, agentNow); err != nil {
		t.Fatal(err)
	}
	// Usage overwrites (it may go down): provisional 300, then corrected to 120.
	if err := st.RecordAgentRunUsage(ctx, r.ID, 5, 5, 300, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentRunUsage(ctx, r.ID, 4, 4, 120, agentNow); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleAgentRun(ctx, r.ID, "done", []string{"list_services"}, agentNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	settled, reserved, err := st.AgentSpend(ctx, month)
	if err != nil || settled != 120 || reserved != 0 {
		t.Fatalf("spend = %d, %d, %v; want 120, 0", settled, reserved, err)
	}
	if err := st.ReserveAgentRun(ctx, agentRun(880), time.Hour, agentNow); err != nil {
		t.Fatalf("freed reservation not reusable: %v", err)
	}
}

func TestAgentSpend_UsesUTCMonth(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	lastSecond := time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	r := agentRun(10)
	if err := st.ReserveAgentRun(ctx, r, time.Hour, lastSecond); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 10, lastSecond); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleAgentRun(ctx, r.ID, "done", nil, lastSecond); err != nil {
		t.Fatal(err)
	}
	oct, nov := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if s, _, err := st.AgentSpend(ctx, oct); err != nil || s != 10 {
		t.Fatalf("october = %d, %v; want 10", s, err)
	}
	if s, _, err := st.AgentSpend(ctx, nov); err != nil || s != 0 {
		t.Fatalf("november = %d, %v; want 0", s, err)
	}
	days, err := st.AgentUsageByDay(ctx, oct)
	if err != nil || len(days) != 1 || days[0].Runs != 1 || days[0].CostMicroUSD != 10 {
		t.Fatalf("usage by day = %+v, %v", days, err)
	}
}
