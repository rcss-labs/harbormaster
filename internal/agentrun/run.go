package agentrun

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// MinStaleAfter is the least time a running run is given before a new reservation may sweep it.
	MinStaleAfter = 10 * time.Minute
	// A reservation that may have been made is looked for this many times, this far apart.
	lostReservationLooks     = 4
	lostReservationLookAgain = 250 * time.Millisecond
)

// SetUpLimit bounds each database step before a run starts, the reservation included.
var SetUpLimit = Limit{Normal: 10 * time.Second, Draining: 2 * time.Second}

// StaleAfter is how long a running run that records no usage is left before a new reservation sweeps it.
func StaleAfter(runTimeout time.Duration) time.Duration { return max(2*runTimeout, MinStaleAfter) }

var (
	// ErrAllowanceUsedUp is a reservation refused because this month's allowance has no room for it.
	ErrAllowanceUsedUp = errors.New("agent allowance used up")
	// ErrAllowanceTooSmall is a reservation refused because the allowance could never hold it, spent or not.
	ErrAllowanceTooSmall = errors.New("agent allowance smaller than one run")
)

// Actor is who a run is for: a user, or later the agent's own identity. Name goes to the audit row.
type Actor struct {
	Name string
}

// DefaultRunTimeout stands in when the agent's catalogue states no run timeout.
const DefaultRunTimeout = 5 * time.Minute

// RunTimeout is the timeout a catalogue states, or DefaultRunTimeout when it states none.
func RunTimeout(catalogued time.Duration) time.Duration {
	if catalogued <= 0 {
		return DefaultRunTimeout
	}
	return catalogued
}

// Ending is what the end of a run means outside it, apart from the words for whoever asked.
type Ending struct {
	Success bool
	// Outcome is the audit label: done, error or interrupted.
	Outcome string
}

// RunnerStore is what reserving and ending a run need of the store.
type RunnerStore interface {
	RunStore
	ReserveAgentRunLeaving(ctx context.Context, run store.AgentRun, leaveMicroUSD int64, staleAfter time.Duration, now time.Time) error
	GetAgentSettings(ctx context.Context) (store.AgentSettings, error)
	AgentSpend(ctx context.Context, monthStart time.Time) (settledMicroUSD, reservedMicroUSD int64, err error)
}

// Runner reserves and ends runs of any role and for any caller. It knows nothing of HTTP. Now defaults to
// time.Now and Log to slog.Default; a nil Draining never fires.
type Runner struct {
	Store    RunnerStore
	Now      func() time.Time
	Log      *slog.Logger
	Draining <-chan struct{}
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

// Reserve reserves a run. The reservation commits on a context of its own: a caller that leaves mid-call must not
// leave a half-known state behind, and a run that was reserved is settled by its owner, or here when the reservation
// failed without saying whether it was made (or at the latest by the reconcile pass). A refusal for the budget is
// ErrAllowanceUsedUp or ErrAllowanceTooSmall.
func (r *Runner) Reserve(ctx context.Context, run store.AgentRun, runTimeout time.Duration) error {
	return r.ReserveLeaving(ctx, run, runTimeout, 0)
}

// ReserveLeaving is Reserve for a run that must leave room for others: the store inserts it only when its
// reservation plus leaveMicroUSD fits, and the leave is not held. A refusal says "too small" when the allowance
// could never hold the reservation and the leave together.
func (r *Runner) ReserveLeaving(ctx context.Context, run store.AgentRun, runTimeout time.Duration, leaveMicroUSD int64) error {
	ctx = context.WithoutCancel(ctx)
	_, err := Within(ctx, SetUpLimit, r.Draining, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.Store.ReserveAgentRunLeaving(ctx, run, leaveMicroUSD, StaleAfter(runTimeout), r.now())
	})
	switch {
	case err == nil || errors.Is(err, store.ErrNotFound):
		return err
	case errors.Is(err, store.ErrBudgetExhausted):
		return r.budgetRefusal(ctx, run, leaveMicroUSD)
	}
	r.settleLostReservation(ctx, run.ID)
	return err
}

// budgetRefusal logs a refused reservation and picks the words for it. An allowance that is itself smaller than one
// run's reservation says so: nothing may have been spent, and "used up" would send an admin looking for spend.
func (r *Runner) budgetRefusal(ctx context.Context, run store.AgentRun, leaveMicroUSD int64) error {
	// The reads run inside the set-up limit like every other step, and fall back to the plain refusal.
	type spend struct {
		settings          store.AgentSettings
		settled, reserved int64
	}
	var userID int64
	if run.UserID != nil {
		userID = *run.UserID
	}
	read, err := Within(ctx, SetUpLimit, r.Draining, func(ctx context.Context) (spend, error) {
		settings, err := r.Store.GetAgentSettings(ctx)
		if err != nil {
			return spend{}, err
		}
		settled, reserved, err := r.Store.AgentSpend(ctx, r.now())
		return spend{settings, settled, reserved}, err
	})
	if err != nil {
		r.LogRunError(run.ID, "agent run: could not read the budget for a refusal", err)
		r.log().Warn("agent budget exhausted", "role", run.Role, "user_id", userID, "reserved_micro_usd", run.ReservedMicroUSD, "leave_micro_usd", leaveMicroUSD)
		return ErrAllowanceUsedUp
	}
	budget := read.settings.MonthlyBudgetMicroUSD
	r.log().Warn("agent budget exhausted", "role", run.Role, "user_id", userID, "reserved_micro_usd", run.ReservedMicroUSD, "leave_micro_usd", leaveMicroUSD,
		"left_micro_usd", BudgetLeft(budget, read.settled, read.reserved))
	// An allowance of 0 holds nothing, a free run included, so it is "too small" whatever the run reserves.
	if budget <= 0 || budget < run.ReservedMicroUSD || budget-run.ReservedMicroUSD < leaveMicroUSD {
		return ErrAllowanceTooSmall
	}
	return ErrAllowanceUsedUp
}

// BudgetLeft is what an allowance has left after settled and reserved spend, never below 0 and never wrapping:
// each step subtracts only a figure that the one before has shown to be no larger. Inputs are non-negative.
func BudgetLeft(budget, settled, reserved int64) int64 {
	if settled > budget {
		return 0
	}
	room := budget - settled
	if reserved > room {
		return 0
	}
	return room - reserved
}

// settleLostReservation settles a run whose reservation failed in a way that does not say whether it was made. The
// commit may still be under way, so a run that is not there is looked for a few more times. Its audit row is the
// reconcile pass's to write.
func (r *Runner) settleLostReservation(ctx context.Context, runID uuid.UUID) {
	_, err := Within(ctx, SetUpLimit, r.Draining, func(ctx context.Context) (struct{}, error) {
		for looks := 1; ; looks++ {
			err := r.Store.SettleAgentRun(ctx, runID, "failed", nil, nil, r.now())
			if !errors.Is(err, store.ErrNotFound) || looks == lostReservationLooks {
				return struct{}{}, err
			}
			select {
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			case <-time.After(lostReservationLookAgain):
			}
		}
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrAgentRunNotRunning) {
		r.LogRunError(runID, "agent run: could not settle a reservation that may have been made", err)
	}
}

// Ops is how a run of any role ends in the store. ip is the client address for the audit row, empty when there is none.
func (r *Runner) Ops(run store.AgentRun, actor Actor, ip string, meter *Meter, end Ending) *StoreOps {
	state := "failed"
	if end.Success {
		state = "done"
	}
	return &StoreOps{Store: r.Store, Run: run, Meter: meter, State: state, Outcome: end.Outcome,
		Tools: []string{}, Username: actor.Name, IP: ip, Now: r.now}
}

// Finish ends a run of any role; see Finisher for the steps. It works on a ctx that has ended.
func (r *Runner) Finish(ctx context.Context, ops *StoreOps) Result {
	f := Finisher{Ops: ops, Draining: r.Draining, Log: func(step string, err error) {
		r.LogRunError(ops.Run.ID, "agent run: could not "+step, err)
	}}
	ended := f.Finish(ctx)
	var u Usage
	if ops.Meter != nil {
		u, _ = ops.Meter.Last()
	}
	r.log().Info("agent run finished", "run_id", ops.Run.ID, "role", ops.Run.Role, "user_id", ops.Run.UserID, "outcome", ops.Outcome,
		"input_tokens", u.InputTokens, "output_tokens", u.OutputTokens, "cost_micro_usd", u.CostMicroUSD)
	return ended
}

// LogRunError logs err against a run, with the stack when it was a panic.
func (r *Runner) LogRunError(runID uuid.UUID, what string, err error) {
	var p *PanicError
	if errors.As(err, &p) {
		r.log().Error(what, "run_id", runID, "panic_type", p.Type, "stack", p.Stack)
		return
	}
	r.log().Warn(what, "run_id", runID, "error", err)
}

// End ends a run that has no meter known to this process: one left by an earlier holder, or an orphan. The run is
// settled with the figures its row has, its token is deleted, and its audit row (with details added) is written, in the
// steps of Finish.
func (r *Runner) End(ctx context.Context, run store.AgentRun, actor Actor, ip string, end Ending, details map[string]any) Result {
	ops := r.Ops(run, actor, ip, nil, end)
	ops.Details = details
	return r.Finish(ctx, ops)
}
