package agentrun

import (
	"context"
	"errors"
	"go/build"
	"io"
	"log/slog"
	"math"
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
}

func (f *fakeRunnerStore) ReserveAgentRun(context.Context, store.AgentRun, time.Duration, time.Time) error {
	return f.reserveErr
}

func (f *fakeRunnerStore) hang(ctx context.Context) {
	if f.hangReads {
		select {
		case <-ctx.Done():
		case <-f.release:
		}
	}
}

func (f *fakeRunnerStore) GetAgentSettings(ctx context.Context) (store.AgentSettings, error) {
	f.hang(ctx)
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

func TestBudgetCovers(t *testing.T) {
	for name, tc := range map[string]struct {
		budget, settled, reserved, request int64
		want                               bool
	}{
		"plenty":                      {1000, 100, 100, 500, true},
		"exactly the room left":       {1000, 400, 100, 500, true},
		"one over the room left":      {1000, 400, 100, 501, false},
		"allowance 0 covers nothing":  {0, 0, 0, 0, false},
		"spent more than the budget":  {1000, 1001, 0, 0, false},
		"reserved more than the room": {1000, 600, 401, 0, false},
		"at the int64 cap, nothing":   {math.MaxInt64, math.MaxInt64, math.MaxInt64, 1, false},
		"at the int64 cap, reserved":  {math.MaxInt64, 1, math.MaxInt64, 0, false},
		"at the int64 cap, room":      {math.MaxInt64, 0, 0, math.MaxInt64, true},
		"free run, room":              {1, 0, 0, 0, true},
	} {
		if got := BudgetCovers(tc.budget, tc.settled, tc.reserved, tc.request); got != tc.want {
			t.Errorf("%s: BudgetCovers%v = %v, want %v", name, []int64{tc.budget, tc.settled, tc.reserved, tc.request}, got, tc.want)
		}
	}
}

// The package must stay usable by a background job: nothing in it may reach the web layer.
func TestAgentrunImportsNothingFromTheWebLayer(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range append(pkg.Imports, pkg.TestImports...) {
		if imp == "net/http" || strings.HasPrefix(imp, "net/http/") && imp != "net/http/httptest" ||
			strings.HasSuffix(imp, "/internal/api") || strings.Contains(imp, "/internal/api/") {
			t.Errorf("internal/agentrun imports %s", imp)
		}
	}
}
