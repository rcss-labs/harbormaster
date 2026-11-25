package agentrun

import (
	"bytes"
	"context"
	"errors"
	"go/build"
	"io"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// fakeRunnerStore refuses every reservation for the budget; the reads that choose the words may be made to hang.
type fakeRunnerStore struct {
	fakeRunStore
	settings      store.AgentSettings
	hangReads     bool
	release       chan struct{}
	reserveErr    error
	readErr       error
	readPanics    bool
	gotLeave      int64
	calledReserve bool
	reserved      chan struct{} // when set, ReserveAgentRunLeaving waits for its context
}

func (f *fakeRunnerStore) ReserveAgentRunLeaving(ctx context.Context, _ store.AgentRun, leave int64, _ time.Duration, _ time.Time) error {
	f.gotLeave = leave
	f.calledReserve = true
	if f.reserved != nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.reserveErr
}

func (f *fakeRunnerStore) hang(ctx context.Context) {
	if f.readPanics {
		panic("secret-looking text")
	}
	if f.hangReads {
		select {
		case <-ctx.Done():
		case <-f.release:
		}
	}
}

func (f *fakeRunnerStore) GetAgentSettings(ctx context.Context) (store.AgentSettings, error) {
	f.hang(ctx)
	if f.readErr != nil {
		return store.AgentSettings{}, f.readErr
	}
	return f.settings, ctx.Err()
}

func (f *fakeRunnerStore) AgentSpend(ctx context.Context, _ time.Time) (int64, int64, error) {
	f.hang(ctx)
	return 0, 0, ctx.Err()
}

func newRunner(st *fakeRunnerStore) *Runner {
	st.release = make(chan struct{})
	return &Runner{Store: st, Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestRunner_ReserveSaysWhichRefusalItIs(t *testing.T) {
	run := store.AgentRun{ID: uuid.New(), Role: "chat", ReservedMicroUSD: 600}
	for name, tc := range map[string]struct {
		budget int64
		want   error
	}{
		"allowance below one run":   {budget: 500, want: ErrAllowanceTooSmall},
		"allowance spent elsewhere": {budget: 600, want: ErrAllowanceUsedUp},
		"a free run, allowance 0":   {budget: 0, want: ErrAllowanceTooSmall},
	} {
		t.Run(name, func(t *testing.T) {
			r := run
			if name == "a free run, allowance 0" {
				r.ReservedMicroUSD = 0
			}
			st := &fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, settings: store.AgentSettings{MonthlyBudgetMicroUSD: tc.budget}}
			if err := newRunner(st).Reserve(t.Context(), r, time.Minute); !errors.Is(err, tc.want) {
				t.Errorf("Reserve = %v, want %v", err, tc.want)
			}
		})
	}
}

// The two reads that choose the words run inside the set-up limit like every other step: a database that stops
// answering cannot hold a refusal back.
func TestRunner_ReserveRefusalReadsAreBounded(t *testing.T) {
	defer func(l Limit) { SetUpLimit = l }(SetUpLimit)
	SetUpLimit = Limit{Normal: 100 * time.Millisecond, Draining: 100 * time.Millisecond}
	st := &fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, hangReads: true}
	r := newRunner(st)
	t.Cleanup(func() { close(st.release) })
	got := make(chan error, 1)
	go func() {
		got <- r.Reserve(t.Context(), store.AgentRun{ID: uuid.New(), Role: "chat", ReservedMicroUSD: 1}, time.Minute)
	}()
	select {
	case err := <-got:
		if !errors.Is(err, ErrAllowanceUsedUp) {
			t.Errorf("Reserve = %v, want the used-up refusal when the reads fail", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reserve is still waiting for the budget reads")
	}
}

// The package must stay usable by a background job: nothing in it may reach the web layer.
func TestAgentrunImportsNothingFromTheWebLayer(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
		if imp == "net/http" || strings.HasPrefix(imp, "net/http/") && imp != "net/http/httptest" ||
			strings.HasSuffix(imp, "/internal/api") || strings.Contains(imp, "/internal/api/") {
			t.Errorf("internal/agentrun imports %s", imp)
		}
	}
}

// A refusal whose reads fail keeps its wording but says why in the log: an error, a timeout and a panic each.
func TestRunner_ReserveLogsWhyTheBudgetReadFailed(t *testing.T) {
	defer func(l Limit) { SetUpLimit = l }(SetUpLimit)
	SetUpLimit = Limit{Normal: 100 * time.Millisecond, Draining: 100 * time.Millisecond}
	for name, tc := range map[string]struct {
		st   *fakeRunnerStore
		want string
	}{
		"error":   {&fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, readErr: errors.New("connection refused")}, "connection refused"},
		"timeout": {&fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, hangReads: true}, "deadline exceeded"},
		"panic":   {&fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, readPanics: true}, "panic_type=string"},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			r := newRunner(tc.st)
			r.Log = slog.New(slog.NewTextHandler(&logs, nil))
			t.Cleanup(func() { close(tc.st.release) })
			if err := r.Reserve(t.Context(), store.AgentRun{ID: uuid.New(), Role: "chat", ReservedMicroUSD: 1}, time.Minute); !errors.Is(err, ErrAllowanceUsedUp) {
				t.Errorf("Reserve = %v, want the used-up refusal", err)
			}
			out := logs.String()
			if !strings.Contains(out, "could not read the budget for a refusal") || !strings.Contains(out, tc.want) {
				t.Errorf("log = %s, want the failed read and %q", out, tc.want)
			}
			if strings.Contains(out, "secret-looking text") {
				t.Errorf("a panic's value was logged: %s", out)
			}
		})
	}
}

// Reserve stops waiting soon after the server starts to shut down, not at the full limit.
func TestRunner_ReserveGivesUpWhenDraining(t *testing.T) {
	defer func(l Limit) { SetUpLimit = l }(SetUpLimit)
	SetUpLimit = Limit{Normal: time.Minute, Draining: 50 * time.Millisecond}
	st := &fakeRunnerStore{reserved: make(chan struct{})}
	r := newRunner(st)
	draining := make(chan struct{})
	close(draining)
	r.Draining = draining
	got := make(chan error, 1)
	go func() { got <- r.Reserve(t.Context(), store.AgentRun{ID: uuid.New(), Role: "chat"}, time.Minute) }()
	select {
	case err := <-got:
		if err == nil {
			t.Error("Reserve = nil, want the error of a reservation that was given up")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reserve kept waiting while the server was draining")
	}
}

// A conversation that is gone is no lost reservation: nothing is settled. Any other failure settles the run.
func TestRunner_ReserveSettlesOnlyWhenTheReservationIsUnknown(t *testing.T) {
	gone := &fakeRunnerStore{reserveErr: store.ErrNotFound}
	if err := newRunner(gone).Reserve(t.Context(), store.AgentRun{ID: uuid.New()}, time.Minute); !errors.Is(err, store.ErrNotFound) || len(gone.calls) != 0 {
		t.Errorf("not found: err %v, settle calls %v", err, gone.calls)
	}
	lost := &fakeRunnerStore{reserveErr: errors.New("connection reset")}
	if err := newRunner(lost).Reserve(t.Context(), store.AgentRun{ID: uuid.New()}, time.Minute); err == nil || len(lost.calls) != 1 || lost.calls[0] != "settle failed" {
		t.Errorf("unknown: err %v, settle calls %v", err, lost.calls)
	}
}

func TestBudgetLeft(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	for name, tc := range map[string]struct{ budget, settled, reserved, want int64 }{
		"plain":                    {1000, 100, 200, 700},
		"nothing left":             {1000, 800, 200, 0},
		"settled over the budget":  {1000, 1001, 0, 0},
		"reserved over the room":   {1000, 600, 401, 0},
		"both at the cap":          {1_000_000, maxInt64, maxInt64, 0},
		"whole cap free":           {maxInt64, 0, 0, maxInt64},
		"cap budget, cap reserved": {maxInt64, 0, maxInt64, 0},
	} {
		if got := BudgetLeft(tc.budget, tc.settled, tc.reserved); got != tc.want {
			t.Errorf("%s: BudgetLeft = %d, want %d", name, got, tc.want)
		}
	}
}

func TestTurn_EndingMapsEveryReason(t *testing.T) {
	for reason, want := range map[Reason]Ending{
		Done:        {Success: true, Outcome: "done"},
		AgentFailed: {Outcome: "error"},
		TimedOut:    {Outcome: "error"},
		BrowserGone: {Outcome: "interrupted"},
		Draining:    {Outcome: "interrupted"},
		RunLost:     {Outcome: "interrupted"},
		Panicked:    {Outcome: "error"},
		Streaming:   {Outcome: "error"},
	} {
		var turn Turn
		turn.End(reason)
		if got := turn.Ending(); got != want {
			t.Errorf("reason %d: Ending = %+v, want %+v", reason, got, want)
		}
	}
}

func TestRunTimeout(t *testing.T) {
	if got := RunTimeout(0); got != DefaultRunTimeout {
		t.Errorf("RunTimeout(0) = %v", got)
	}
	if got := RunTimeout(-time.Second); got != DefaultRunTimeout {
		t.Errorf("RunTimeout(-1s) = %v", got)
	}
	if got := RunTimeout(90 * time.Second); got != 90*time.Second {
		t.Errorf("RunTimeout(90s) = %v", got)
	}
}

// A coordinator job ends a run it has no meter for: the run settles, its token goes, and one audit row names the
// actor, the address and the extra details.
func TestRunner_EndSettlesDeletesTheTokenAndAudits(t *testing.T) {
	st := &fakeRunnerStore{}
	r := newRunner(st)
	run := store.AgentRun{ID: uuid.New(), Role: "investigator", Provider: "fake", Model: "m"}
	res := r.End(t.Context(), run, Actor{Name: "harbormaster-agent"}, "", Ending{Outcome: "interrupted"}, map[string]any{"investigation_id": 3})
	if res.SaveErr != nil || !slices.Equal(st.calls, []string{"settle failed"}) || st.usage != nil || !st.tokenDeleted.Load() {
		t.Errorf("result %+v, settle calls %v, figures %+v, token deleted %v", res, st.calls, st.usage, st.tokenDeleted.Load())
	}
	d := st.entry.Details
	if st.entry.ActorUsername != "harbormaster-agent" || d["outcome"] != "interrupted" || d["investigation_id"] != 3 || d["role"] != "investigator" {
		t.Errorf("entry = %+v", st.entry)
	}
}

// The one who ends a run learns whether it was settled, and a run that had ended before is not reported as ended again.
func TestRunner_EndReportsWhetherTheRunWasSettled(t *testing.T) {
	bad := errors.New("no")
	for name, c := range map[string]struct {
		settle error
		want   error
		logged bool
	}{
		"settled":        {nil, nil, true},
		"ended before":   {store.ErrAgentRunNotRunning, nil, false},
		"not there":      {store.ErrNotFound, store.ErrNotFound, false},
		"not settled":    {bad, bad, false},
		"a settle panic": {errPanic, nil, false},
	} {
		st := &fakeRunnerStore{}
		st.settleErr = c.settle
		var logs bytes.Buffer
		r := &Runner{Store: st, Log: slog.New(slog.NewTextHandler(&logs, nil))}
		if c.settle == errPanic {
			r.Store = panicSettle{st}
		}
		res := r.End(t.Context(), store.AgentRun{ID: uuid.New()}, Actor{Name: "a"}, "", Ending{Outcome: "interrupted"}, nil)
		if c.settle == errPanic {
			var p *PanicError
			if !errors.As(res.SettleErr, &p) {
				t.Errorf("%s: SettleErr = %v", name, res.SettleErr)
			}
		} else if !errors.Is(res.SettleErr, c.want) {
			t.Errorf("%s: SettleErr = %v, want %v", name, res.SettleErr, c.want)
		}
		if got := strings.Contains(logs.String(), "agent run finished"); got != c.logged {
			t.Errorf("%s: logged as finished: %v, want %v", name, got, c.logged)
		}
	}
}

var errPanic = errors.New("panic")

// panicSettle is a store whose settle panics.
type panicSettle struct{ *fakeRunnerStore }

func (panicSettle) SettleAgentRun(context.Context, uuid.UUID, string, *store.AgentUsage, []string, time.Time) error {
	panic("boom")
}

// Finish needs no meter, no clock and no logger: the defaults stand in.
func TestRunner_FinishWithNoMeterOrDefaults(t *testing.T) {
	st := &fakeRunnerStore{}
	r := &Runner{Store: st}
	ops := r.Ops(store.AgentRun{ID: uuid.New(), Role: "investigator"}, Actor{Name: "a"}, "", nil, Ending{Success: true, Outcome: "done"})
	r.Finish(t.Context(), ops)
	if !slices.Equal(st.calls, []string{"settle done"}) || st.entry.Details["outcome"] != "done" {
		t.Errorf("settle calls %v, entry %+v", st.calls, st.entry)
	}
}

func TestRunner_ReserveLeavingSaysWhichRefusalItIs(t *testing.T) {
	run := store.AgentRun{ID: uuid.New(), Role: "reviewer", ReservedMicroUSD: 40}
	for name, tc := range map[string]struct {
		leave int64
		want  error
	}{
		"request and leave exceed the allowance": {leave: 70, want: ErrAllowanceTooSmall},
		"request and leave fit the allowance":    {leave: 60, want: ErrAllowanceUsedUp},
		"leave at the int64 cap":                 {leave: math.MaxInt64, want: ErrAllowanceTooSmall},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, settings: store.AgentSettings{MonthlyBudgetMicroUSD: 100}}
			if err := newRunner(st).ReserveLeaving(t.Context(), run, time.Minute, tc.leave); !errors.Is(err, tc.want) {
				t.Errorf("ReserveLeaving = %v, want %v", err, tc.want)
			}
			if st.gotLeave != tc.leave {
				t.Errorf("the store was given a leave of %d, want %d", st.gotLeave, tc.leave)
			}
		})
	}
	// A negative leave is refused before the store is asked.
	neg := &fakeRunnerStore{}
	if err := newRunner(neg).ReserveLeaving(t.Context(), run, time.Minute, -1); err == nil || errors.Is(err, ErrAllowanceUsedUp) || neg.calledReserve {
		t.Errorf("negative leave = %v (store asked: %v), want an error before the store", err, neg.calledReserve)
	}
	// Reserve leaves nothing.
	st := &fakeRunnerStore{}
	if err := newRunner(st).Reserve(t.Context(), run, time.Minute); err != nil || st.gotLeave != 0 {
		t.Errorf("Reserve = %v with leave %d, want nil and 0", err, st.gotLeave)
	}
}

func TestLeaveFor_SumsConfiguredRolesOnly(t *testing.T) {
	const modelA, modelB = "claude-sonnet-5-5", "claude-haiku-4-5"
	cat := agentclient.Catalogue{
		MaxRunTokens: 200000,
		Providers: []agentclient.ProviderModels{{Provider: "anthropic", Models: []agentclient.Model{
			{ID: modelA, InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000}, // 3,000,000 per run
			{ID: modelB, InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 5_000_000},  // 1,000,000 per run
		}}},
	}
	settings := store.AgentSettings{RoleModels: map[string]store.AgentModelRef{
		"chat":         {Provider: "anthropic", Model: modelA},
		"investigator": {Provider: "anthropic", Model: modelB},
		"lost":         {Provider: "anthropic", Model: "claude-retired-1-0"},
		"elsewhere":    {Provider: "other", Model: modelA},
	}}
	for name, tc := range map[string]struct {
		roles []string
		want  int64
	}{
		"chat and investigator":          {[]string{"chat", "investigator"}, 4_000_000},
		"one role":                       {[]string{"investigator"}, 1_000_000},
		"a role with no model":           {[]string{"chat", "reviewer"}, 3_000_000},
		"a model the catalogue lacks":    {[]string{"chat", "lost"}, 3_000_000},
		"a model under another provider": {[]string{"elsewhere"}, 0},
		"no roles":                       {nil, 0},
	} {
		if got := LeaveFor(cat, settings, tc.roles...); got != tc.want {
			t.Errorf("%s: LeaveFor = %d, want %d", name, got, tc.want)
		}
	}

	// A wrapped product or a negative price counts as nothing, never as a negative leave.
	wrapped := agentclient.Catalogue{MaxRunTokens: math.MaxInt64, Providers: []agentclient.ProviderModels{{Provider: "anthropic",
		Models: []agentclient.Model{{ID: modelA, InputMicroUSDPerMTok: 3}, {ID: modelB, InputMicroUSDPerMTok: -5_000_000, OutputMicroUSDPerMTok: -5_000_000}}}}}
	if got := LeaveFor(wrapped, settings, "chat", "investigator"); got < 0 {
		t.Errorf("negative price: LeaveFor = %d, want it never below 0", got)
	}
	wrapped.MaxRunTokens = 200000
	if got := LeaveFor(wrapped, settings, "investigator"); got != 0 {
		t.Errorf("negative price alone: LeaveFor = %d, want 0", got)
	}

	// Many costly roles add up to the cap and stop there.
	big := agentclient.Catalogue{MaxRunTokens: 1_000_000, Providers: []agentclient.ProviderModels{{Provider: "anthropic",
		Models: []agentclient.Model{{ID: modelA, InputMicroUSDPerMTok: 9_000_000_000_000}}}}}
	roles := make([]string, 1_100_000)
	for i := range roles {
		roles[i] = "chat"
	}
	if got := LeaveFor(big, settings, roles...); got != math.MaxInt64 {
		t.Errorf("saturating sum = %d, want %d", got, int64(math.MaxInt64))
	}
}

func TestRunner_ReserveLogsTheLeaveOnlyWhenThereIsOne(t *testing.T) {
	for leave, want := range map[int64]bool{0: false, 70: true} {
		var logs bytes.Buffer
		st := &fakeRunnerStore{reserveErr: store.ErrBudgetExhausted, settings: store.AgentSettings{MonthlyBudgetMicroUSD: 100}}
		r := newRunner(st)
		r.Log = slog.New(slog.NewTextHandler(&logs, nil))
		_ = r.ReserveLeaving(t.Context(), store.AgentRun{ID: uuid.New(), Role: "reviewer", ReservedMicroUSD: 40}, time.Minute, leave)
		if got := strings.Contains(logs.String(), "leave_micro_usd"); got != want {
			t.Errorf("leave %d: log mentions the leave = %v, want %v: %s", leave, got, want, logs.String())
		}
	}
}
