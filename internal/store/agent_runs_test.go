package store_test

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func runRow(t *testing.T, st *store.Store, id uuid.UUID) (state string, cost, in, out int64, tools []string, last *time.Time) {
	t.Helper()
	err := st.Pool.QueryRow(t.Context(), `select state, cost_micro_usd, input_tokens, output_tokens, tools, last_usage_at from agent_runs where id = $1`, id).
		Scan(&state, &cost, &in, &out, &tools, &last)
	mustNoErr(t, err)
	return
}

var novStart = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func TestRecordAgentRunUsage_LateUsageOnSweptRunRaisesButNeverLowers(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	r := agentRun(900)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow.Add(-2*time.Hour)))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 10, 10, 200, agentNow.Add(-2*time.Hour)))
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(1), time.Hour, agentNow)) // sweeps r
	late := agentNow.Add(time.Minute)
	if err := st.RecordAgentRunUsage(ctx, r.ID, 30, 40, 700, late); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatalf("late usage = %v, want ErrAgentRunNotRunning", err)
	}
	state, cost, in, out, _, last := runRow(t, st, r.ID)
	if state != "failed" || cost != 700 || in != 30 || out != 40 || last == nil || !last.Equal(late) {
		t.Fatalf("after late usage: %s cost=%d in=%d out=%d last=%v", state, cost, in, out, last)
	}
	// A lower figure never lowers a swept run.
	if err := st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 5, agentNow.Add(-time.Hour)); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatalf("lower late usage = %v", err)
	}
	state, cost, in, out, _, last = runRow(t, st, r.ID)
	if state != "failed" || cost != 700 || in != 30 || out != 40 || !last.Equal(late) {
		t.Fatalf("lowered: %s cost=%d in=%d out=%d last=%v", state, cost, in, out, last)
	}
	if err := st.RecordAgentRunUsage(ctx, uuid.New(), 1, 1, 1, agentNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
}

func TestRecordAgentRunUsage_DoneRunIsUnchanged(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	r := agentRun(100)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 2, 2, 50, agentNow))
	mustNoErr(t, st.SettleAgentRun(ctx, r.ID, "done", nil, agentNow))
	if err := st.RecordAgentRunUsage(ctx, r.ID, 9, 9, 99, agentNow.Add(time.Second)); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatalf("usage on done = %v, want ErrAgentRunNotRunning", err)
	}
	state, cost, in, out, _, _ := runRow(t, st, r.ID)
	if state != "done" || cost != 50 || in != 2 || out != 2 {
		t.Fatalf("done run changed: %s %d %d %d", state, cost, in, out)
	}
}

func TestSettleAgentRun_LateSettleDoesNotResurrectSweptRun(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	r := agentRun(900)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow.Add(-2*time.Hour)))
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(1), time.Hour, agentNow)) // sweeps r
	err := st.SettleAgentRun(ctx, r.ID, "done", []string{"list_services"}, agentNow)
	if !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatalf("late settle = %v, want ErrAgentRunNotRunning", err)
	}
	state, _, _, _, tools, _ := runRow(t, st, r.ID)
	if state != "failed" || len(tools) != 1 || tools[0] != "list_services" {
		t.Fatalf("after late settle: state %s tools %v; want failed with tools recorded", state, tools)
	}
	if err := st.SettleAgentRun(ctx, uuid.New(), "done", nil, agentNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
}

func TestReserveAgentRun_ZeroBudgetRefusesZeroReservationButSweeps(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	old := agentRun(0)
	_, err := st.Pool.Exec(ctx, `insert into agent_runs (id, role, provider, model, state, started_at) values ($1, 'chat', 'fake', 'm', 'running', $2)`,
		old.ID, agentNow.Add(-5*time.Hour))
	mustNoErr(t, err)
	if err := st.ReserveAgentRun(ctx, agentRun(0), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("zero reservation with budget 0 = %v, want ErrBudgetExhausted", err)
	}
	if state, _, _, _, _, _ := runRow(t, st, old.ID); state != "failed" {
		t.Fatalf("sweep not committed on refusal: %s", state)
	}
}

func TestReserveAgentRun_RejectsNegativeAndSurvivesOverflow(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, math.MaxInt64)
	if err := st.ReserveAgentRun(ctx, agentRun(-1), time.Hour, agentNow); err == nil || errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("negative reservation = %v, want a plain error", err)
	}
	big := agentRun(math.MaxInt64)
	mustNoErr(t, st.ReserveAgentRun(ctx, big, time.Hour, agentNow))
	if err := st.ReserveAgentRun(ctx, agentRun(1), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("reserve past MaxInt64 = %v, want ErrBudgetExhausted", err)
	}
	// Two runs at MaxInt64 cost must not make the sum error out.
	other := agentRun(0)
	_, err := st.Pool.Exec(ctx, `insert into agent_runs (id, role, provider, model, state, cost_micro_usd, started_at) values ($1, 'chat', 'fake', 'm', 'done', $2, $3)`,
		other.ID, int64(math.MaxInt64), agentNow)
	mustNoErr(t, err)
	if s, r, err := st.AgentSpend(ctx, novStart); err != nil || s != math.MaxInt64 || r != math.MaxInt64 {
		t.Fatalf("spend = %d, %d, %v; want saturated MaxInt64 both", s, r, err)
	}
	// The next month is a fresh budget: reservations still work.
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(math.MaxInt64), time.Hour, agentNow.AddDate(0, 1, 0)))
}

func TestReserveAgentRun_CostAboveReservationCounts(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	r := agentRun(100)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 800, agentNow))
	if err := st.ReserveAgentRun(ctx, agentRun(300), time.Hour, agentNow); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("reserve with overrun cost = %v, want ErrBudgetExhausted", err)
	}
	if _, reserved, _ := st.AgentSpend(ctx, novStart); reserved != 800 {
		t.Fatalf("reserved = %d, want 800 (cost above reservation)", reserved)
	}
}

func TestReserveAgentRun_SweepRespectsFreshLastUsage(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	r := agentRun(100)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow.Add(-5*time.Hour)))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 10, agentNow.Add(-time.Minute)))
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(1), time.Hour, agentNow))
	if state, _, _, _, _, _ := runRow(t, st, r.ID); state != "running" {
		t.Fatalf("run with fresh usage was swept: %s", state)
	}
}

func TestReserveAgentRun_IgnoresLastMonthsSpend(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	oct := time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	r := agentRun(1000)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, 24*time.Hour, oct))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 1000, oct))
	mustNoErr(t, st.SettleAgentRun(ctx, r.ID, "done", nil, oct))
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(1000), 24*time.Hour, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))
}

// 23:30 at UTC-5 on 30 Nov is already 1 Dec in UTC.
func TestReserveAgentRun_NonUTCNowUsesUTCMonth(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 1000)
	west := time.FixedZone("west", -5*3600)
	r := agentRun(1000)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, 24*time.Hour, time.Date(2026, 11, 30, 23, 30, 0, 0, west)))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 1000, time.Date(2026, 11, 30, 23, 30, 0, 0, west)))
	mustNoErr(t, st.SettleAgentRun(ctx, r.ID, "done", nil, time.Date(2026, 11, 30, 23, 30, 0, 0, west)))
	if s, _, _ := st.AgentSpend(ctx, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); s != 1000 {
		t.Fatalf("december spend = %d, want 1000", s)
	}
	if s, _, _ := st.AgentSpend(ctx, novStart); s != 0 {
		t.Fatalf("november spend = %d, want 0", s)
	}
	// December's budget is spent: a later reservation at the same non-UTC time must be refused.
	if err := st.ReserveAgentRun(ctx, agentRun(1), 24*time.Hour, time.Date(2026, 11, 30, 23, 30, 0, 0, west)); !errors.Is(err, store.ErrBudgetExhausted) {
		t.Fatalf("reserve in december at non-UTC now = %v, want ErrBudgetExhausted", err)
	}
}

func TestAgentSpend_UnchangedWhenUserAndConversationDeleted(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	u, err := st.CreateUser(ctx, "alice", "h", false, false)
	mustNoErr(t, err)
	c, err := st.CreateAgentConversation(ctx, u.ID, "t")
	mustNoErr(t, err)
	r := agentRun(500)
	r.UserID, r.ConversationID = &u.ID, &c.ID
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow))
	mustNoErr(t, st.RecordAgentRunUsage(ctx, r.ID, 1, 1, 400, agentNow))
	mustNoErr(t, st.SettleAgentRun(ctx, r.ID, "done", nil, agentNow))
	mustNoErr(t, st.DeleteAgentConversation(ctx, u.ID, c.ID))
	if _, err := st.Pool.Exec(ctx, `delete from users where id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, err := st.AgentSpend(ctx, novStart); err != nil || s != 400 {
		t.Fatalf("spend after deletes = %d, %v; want 400", s, err)
	}
	var uid, cid *int64
	mustNoErr(t, st.Pool.QueryRow(ctx, `select user_id, conversation_id from agent_runs where id = $1`, r.ID).Scan(&uid, &cid))
	if uid != nil || cid != nil {
		t.Fatalf("run still references user/conversation: %v %v", uid, cid)
	}
}

func TestReserveAgentRun_ConversationMustBeOwned(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	alice, _ := st.CreateUser(ctx, "alice", "h", false, false)
	bob, _ := st.CreateUser(ctx, "bob", "h", false, false)
	c, err := st.CreateAgentConversation(ctx, alice.ID, "t")
	mustNoErr(t, err)
	r := agentRun(10)
	r.UserID, r.ConversationID = &bob.ID, &c.ID
	if err := st.ReserveAgentRun(ctx, r, time.Hour, agentNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reserve on another user's conversation = %v, want ErrNotFound", err)
	}
	r.ConversationID, r.UserID = &c.ID, nil
	if err := st.ReserveAgentRun(ctx, r, time.Hour, agentNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reserve on a conversation with no user = %v, want ErrNotFound", err)
	}
	r.UserID = &alice.ID
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow))
}

func TestSettleAgentRun_SecondSettleOfSweptRunKeepsTools(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	setBudget(t, st, 10_000)
	r := agentRun(900)
	mustNoErr(t, st.ReserveAgentRun(ctx, r, time.Hour, agentNow.Add(-2*time.Hour)))
	mustNoErr(t, st.ReserveAgentRun(ctx, agentRun(1), time.Hour, agentNow)) // sweeps r
	if err := st.SettleAgentRun(ctx, r.ID, "failed", []string{"list_services"}, agentNow); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatal(err)
	}
	if err := st.SettleAgentRun(ctx, r.ID, "failed", nil, agentNow); !errors.Is(err, store.ErrAgentRunNotRunning) {
		t.Fatal(err)
	}
	if _, _, _, _, tools, _ := runRow(t, st, r.ID); len(tools) != 1 || tools[0] != "list_services" {
		t.Fatalf("tools after second settle = %v, want [list_services]", tools)
	}
}

func TestAgentUsageByDay_SaturatesInsteadOfOverflowing(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		_, err := st.Pool.Exec(ctx, `insert into agent_runs (id, role, provider, model, state, cost_micro_usd, input_tokens, output_tokens, started_at)
			values ($1, 'chat', 'fake', 'm', 'done', $2, $2, $2, $3)`, uuid.New(), int64(math.MaxInt64), agentNow)
		mustNoErr(t, err)
	}
	days, err := st.AgentUsageByDay(ctx, novStart)
	if err != nil || len(days) != 1 || days[0].Runs != 2 || days[0].CostMicroUSD != math.MaxInt64 || days[0].InputTokens != math.MaxInt64 {
		t.Fatalf("usage = %+v, %v", days, err)
	}
}

func TestRetryDeadlock(t *testing.T) {
	deadlock := &pgconn.PgError{Code: "40P01"}
	calls := 0
	if err := store.RetryDeadlock(func() error {
		calls++
		if calls == 1 {
			return deadlock
		}
		return nil
	}); err != nil || calls != 2 {
		t.Fatalf("one deadlock: err %v after %d calls, want nil after 2", err, calls)
	}
	calls = 0
	if err := store.RetryDeadlock(func() error { calls++; return deadlock }); err == nil || calls != 2 {
		t.Fatalf("persistent deadlock: err %v after %d calls, want error after exactly 2", err, calls)
	}
	calls = 0
	other := errors.New("boom")
	if err := store.RetryDeadlock(func() error { calls++; return other }); !errors.Is(err, other) || calls != 1 {
		t.Fatalf("other error: %v after %d calls, want it unretried", err, calls)
	}
}
