package agentrun

import (
	"bytes"
	"context"
	"errors"
	"go/build"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// fakeRunnerStore refuses every reservation for the budget; the reads that choose the words may be made to hang.
type fakeRunnerStore struct {
	fakeRunStore
	settings   store.AgentSettings
	hangReads  bool
	release    chan struct{}
	reserveErr error
	readErr    error
	readPanics bool
	reserved   chan struct{} // when set, ReserveAgentRun waits for its context
}

func (f *fakeRunnerStore) ReserveAgentRun(ctx context.Context, _ store.AgentRun, _ time.Duration, _ time.Time) error {
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
