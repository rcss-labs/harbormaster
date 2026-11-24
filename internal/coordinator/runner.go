// Package coordinator runs the jobs that must have exactly one active instance: notifications, off-hours, image scans,
// the ARM64 platform resolver, the deployment outcome log, the daily rollups and retention. They run only while this
// process holds the coordinator lease, and each is told to check that lease right before an external side effect.
package coordinator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// DefaultRetentionEvery is how often rollups and retention run when RetentionEvery is zero.
const DefaultRetentionEvery = time.Hour

// Runner implements lease.Holder for the coordinator lease. A nil job is left off.
type Runner struct {
	Store  *store.Store
	Timing lease.Timing // GuardMinRemaining is used; zero means the default

	Scan           *scanjob.Runner
	OffHours       *offhours.Reconciler
	ImagePlatforms *platforms.Resolver
	Notify         *notify.Dispatcher
	Rollups        *rollup.Runner
	Retention      store.Retention
	RetentionEvery time.Duration
	Outcomes       *deploy.OutcomeLogger
	// Idle reports whether polling is idle; the image resolver stays quiet while it is.
	Idle func() bool
	// Bus carries kicks from web tasks. While this runner holds the lease it listens and wakes the named job; nil
	// means it listens to nothing and the jobs run at their intervals.
	Bus signal.Bus
	// OnKick, when set, is called with the job's name each time a bus message woke a job.
	OnKick func(job string)

	Telemetry *telemetry.Registry
	Log       *slog.Logger
}

// RetentionFrom maps the configured retention periods to the store's.
func RetentionFrom(c config.Config) store.Retention {
	return store.Retention{
		StoppedTasks:       c.Retention.StoppedTasks,
		ServiceEvents:      c.Retention.ServiceEvents,
		CountsHistory:      c.Retention.CountsHistory,
		OperationLog:       c.Retention.OperationLog,
		DeploymentHistory:  c.Retention.DeploymentHistory,
		AuditLog:           c.Retention.AuditLog,
		OrphanTags:         c.Retention.OrphanTags,
		AvailabilityDaily:  c.Retention.AvailabilityDaily,
		CapacityDaily:      c.Retention.CapacityDaily,
		UtilDaily:          c.Retention.UtilDaily,
		ScanImages:         c.Retention.ScanImages,
		ScanRuns:           c.Retention.ScanRuns,
		NotificationLog:    c.Retention.NotificationLog,
		AgentConversations: c.Retention.AgentConversations,
	}
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Hold runs every job while the coordinator lease l is held: the scan runner, the off-hours reconciler, the image
// resolver, the dispatcher and the outcome logger on their own goroutines, and the rollups then retention at once and
// every RetentionEvery (rollups first, so retention never deletes rows a rollup still needs). It sets each job's guard
// to a check of l, and returns when ctx ends and every job has stopped, clearing the guards.
func (r *Runner) Hold(ctx context.Context, l store.Lease) {
	ctx = awsx.WithSource(ctx, awsx.SourceJob)
	minRemaining := r.Timing.GuardMinRemaining
	if minRemaining == 0 {
		minRemaining = lease.DefaultTiming().GuardMinRemaining
	}
	guard := lease.GuardFor(r.Store, l, minRemaining)
	if r.Scan != nil {
		r.Scan.Guard = guard
		defer func() { r.Scan.Guard = nil }()
	}
	if r.OffHours != nil {
		r.OffHours.Guard = guard
		defer func() { r.OffHours.Guard = nil }()
	}
	if r.Notify != nil {
		r.Notify.Guard = guard
		defer func() { r.Notify.Guard = nil }()
	}

	var wg sync.WaitGroup
	defer wg.Wait() // runs before the guards are cleared: defers run last-in first-out
	start := func(run func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(ctx)
		}()
	}
	if r.Scan != nil {
		start(r.Scan.Run)
	}
	if r.OffHours != nil {
		start(r.OffHours.Run)
	}
	if r.ImagePlatforms != nil {
		if r.ImagePlatforms.Idle == nil {
			r.ImagePlatforms.Idle = r.Idle
		}
		start(r.ImagePlatforms.Run)
	}
	if r.Notify != nil {
		start(r.Notify.Run)
	}
	if r.Outcomes != nil {
		start(r.Outcomes.Run)
	}
	if r.Bus != nil {
		start(r.listenForKicks)
	}

	every := r.RetentionEvery
	if every <= 0 {
		every = DefaultRetentionEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	r.runRollups(ctx)
	r.runRetention(ctx, guard)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runRollups(ctx)
			r.runRetention(ctx, guard)
		}
	}
}

// runRollups computes the daily analytics rollups that are due. It must run before runRetention in the same
// tick so the raw rows it reads are still there. Errors are logged, never fatal: retention goes ahead regardless.
func (r *Runner) runRollups(ctx context.Context) {
	if r.Rollups == nil {
		return
	}
	result, err := r.Rollups.RunDue(ctx)
	if err != nil && ctx.Err() == nil {
		r.log().Error("coordinator: rollups", "err", err)
	}
	r.log().Debug("coordinator: rollups done", "rows", result)
}

// runRetention marks orphaned tags, then deletes expired rows. Errors are logged, never fatal. It deletes, so a holder
// that can no longer confirm the coordinator lease does not run it.
func (r *Runner) runRetention(ctx context.Context, guard lease.Guard) {
	if err := guard(ctx); err != nil {
		if ctx.Err() == nil {
			r.log().Warn("coordinator: lease not confirmed, skipping retention", "err", err)
		}
		return
	}
	now := time.Now()
	if err := r.Store.MarkOrphanedTags(ctx, now); err != nil && ctx.Err() == nil {
		r.log().Error("coordinator: mark orphaned tags", "err", err)
	}
	result, err := r.Store.RunRetention(ctx, now, r.Retention)
	if err != nil {
		if ctx.Err() == nil {
			r.log().Error("coordinator: retention", "err", err)
		}
		return
	}
	r.log().Debug("coordinator: retention done", "deleted", result)
}
