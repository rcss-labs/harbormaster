package agentrun

import (
	"context"
	"log/slog"
	"time"

	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// reconcileStaleAfter is when a running run that records no usage is given up: twice the longest run timeout the
	// agent can be configured with (30 minutes). It is fixed rather than asked of the agent: a coordinator may have no
	// way to reach it, and a limit that was lowered since would end runs that still have time. A new reservation
	// sweeps by the agent's real limit, so the allowance a dead run holds is usually free again long before.
	reconcileStaleAfter = time.Hour
	// DefaultReconcileInterval is how often a pass runs when Interval is zero.
	DefaultReconcileInterval = time.Minute
	reconcilePassTimeout     = 30 * time.Second
)

// ReconcileStore is the one store call a pass makes.
type ReconcileStore interface {
	ReconcileAgentRuns(ctx context.Context, staleAfter time.Duration, now time.Time) (store.AgentReconcileResult, error)
}

// Reconciler repairs what runs left behind when their own end could not do it: a process that was killed, a database
// that did not answer in time. It is what makes the deadlines of a turn's end a matter of speed, not correctness.
type Reconciler struct {
	Store    ReconcileStore
	Interval time.Duration
	Now      func() time.Time
	Log      *slog.Logger
	// Guard is checked before each pass, so a coordinator that lost its lease repairs nothing. Nil means always
	// allowed. The coordinator sets it while it holds the lease.
	Guard lease.Guard
}

// Run makes a pass at once and then every Interval until ctx ends.
func (r *Reconciler) Run(ctx context.Context) {
	every := r.Interval
	if every <= 0 {
		every = DefaultReconcileInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		r.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pass makes one repair pass. Errors and panics are logged, never fatal: the next pass tries again.
func (r *Reconciler) pass(ctx context.Context) {
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	defer func() {
		if rec := recover(); rec != nil {
			p := NewPanicError(rec)
			log.Error("agent reconcile: panic", "panic_type", p.Type, "stack", p.Stack)
		}
	}()
	if r.Guard != nil {
		if err := r.Guard(ctx); err != nil {
			if ctx.Err() == nil {
				log.Warn("agent reconcile: coordinator lease not confirmed, skipping", "err", err)
			}
			return
		}
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	passCtx, cancel := context.WithTimeout(ctx, reconcilePassTimeout)
	defer cancel()
	res, err := r.Store.ReconcileAgentRuns(passCtx, reconcileStaleAfter, now())
	if err != nil && ctx.Err() == nil {
		log.Error("agent reconcile", "err", err)
	}
	if res != (store.AgentReconcileResult{}) {
		log.Warn("agent reconcile: repaired runs whose turn could not finish", "settled", res.Settled,
			"tokens_deleted", res.TokensDeleted, "audited", res.Audited)
	}
}
