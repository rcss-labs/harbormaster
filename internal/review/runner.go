// Package review is the coordinator job that runs the scheduled AI reviews: it records the occurrences that are due,
// claims one run at a time, has the agent make it as Harbormaster's own agent identity, checks what it answered and
// stores it. A period leads to at most one run row and a row to at most two paid runs. Each is reserved once and
// settled once: by the job that started it, by the next holder of the row, or by the repair of abandoned runs. A
// result is stored once, fenced by the claim. The daily cap is checked and then started, so two holders that overlap
// can exceed it by one.
package review

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/reviewsched"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInterval = 30 * time.Second
	// A claim is held for the run timeout, at least minRunLease, and leaseMargin more, which covers the slack a run is
	// given and the end of it.
	minRunLease = 5 * time.Minute
	leaseMargin = time.Minute
	// claimLimit bounds the claim, which waits for the holders of running rows without a limit of its own.
	claimLimit = 5 * time.Second
)

// Runner records, claims and runs scheduled reviews. Run it under the coordinator lease only.
type Runner struct {
	Store *store.Store
	// Driver is copied for each run: Runs, Guard, Now and Log are those of the run and of the lease term, whatever is
	// set here.
	Driver *agentjob.Driver
	Scopes notify.TagMatcher
	// PerDay is how many runs may start in a day. 0 turns reviews off: nothing is recorded, claimed or run.
	PerDay int
	// CustomReviews lets custom reviews be recorded and run. While it is off none is, and one that waits ends as disabled.
	CustomReviews bool
	// MaxLate is how long after its occurrence a run may first be started.
	MaxLate time.Duration
	// InvestigationMaxWait is how long a waiting investigation holds reviews up; 0 means for as long as it waits.
	InvestigationMaxWait time.Duration
	Interval             time.Duration // between looks at the schedules; 0 means 30 s
	// Guard is checked before anything is recorded or claimed and again before and after the reservation, so a
	// coordinator that lost its lease starts nothing. Nil means always allowed.
	Guard lease.Guard
	// KickNotify, when set, is called when a result was stored, so its messages do not wait for an interval.
	KickNotify func()
	Now        func() time.Time
	Log        *slog.Logger

	initOnce sync.Once
	kickCh   chan struct{}
}

func (r *Runner) init() {
	r.initOnce.Do(func() { r.kickCh = make(chan struct{}, 1) })
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) interval() time.Duration {
	if r.Interval <= 0 {
		return defaultInterval
	}
	return r.Interval
}

// off reports whether the operator has switched reviews of the type off.
func (r *Runner) off(reviewType string) bool {
	return reviewType == store.ReviewTypeCustom && !r.CustomReviews
}

// runLease is how long a claim is held for a run of the given timeout.
func runLease(timeout time.Duration) time.Duration { return max(timeout, minRunLease) + leaseMargin }

// Kick wakes the loop so a requested run does not wait for the next interval. It never blocks, and wakes only the loop
// in this process.
func (r *Runner) Kick() {
	r.init()
	select {
	case r.kickCh <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run looks at the schedules at once and then every Interval, on a Kick, and when a run has ended, until ctx ends. It
// goes on recording what is due while a run is in progress, and returns when that run has been ended.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	slot, freed := make(chan struct{}, 1), make(chan struct{}, 1)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for {
		r.tick(ctx, slot, freed, &jobs)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.kickCh:
		case <-freed:
		}
	}
}

// tick records what is due and starts a run when none of this process's is in progress.
func (r *Runner) tick(ctx context.Context, slot, freed chan struct{}, jobs *sync.WaitGroup) {
	if r.PerDay <= 0 {
		return
	}
	if err := agentjob.Confirm(ctx, r.Guard); err != nil {
		if ctx.Err() == nil {
			r.log().Warn("review: coordinator lease not confirmed, starting nothing", "err", err)
		}
		return
	}
	r.recordDue(ctx)
	select {
	case slot <- struct{}{}:
	default:
		return
	}
	j, ok := r.claim(ctx)
	if !ok {
		<-slot
		return
	}
	jobs.Go(func() {
		defer func() {
			<-slot
			select {
			case freed <- struct{}{}:
			default:
			}
		}()
		j.execute(ctx)
	})
}

// recordDue records the latest occurrence of each enabled review whose type is not switched off, once however often it
// is seen. Occurrences before the latest one leave nothing, so a long outage does not end in a burst of runs.
func (r *Runner) recordDue(ctx context.Context) {
	reviews, err := agentjob.Within(ctx, r.Store.ListReviews)
	if err != nil {
		if ctx.Err() == nil {
			r.logError("review: list the reviews", err)
		}
		return
	}
	now := r.now()
	var due []store.DueReview
	for _, rv := range reviews {
		if !rv.Enabled || r.off(rv.Type) {
			continue
		}
		schedule := reviewsched.Schedule{Cadence: rv.Cadence, AtMinute: rv.AtMinute, Weekday: rv.Weekday, MonthDay: rv.MonthDay, Timezone: rv.Timezone}
		at, err := reviewsched.Last(schedule, now)
		if err != nil {
			r.log().Warn("review: the schedule is not valid, not running it", "review_id", rv.ID, "err", err)
			continue
		}
		if !at.After(rv.ScheduleChangedAt) {
			continue
		}
		due = append(due, store.DueReview{
			ReviewID: rv.ID, Version: rv.Version, PeriodKey: reviewsched.PeriodKey(schedule, at), PeriodLabel: reviewsched.PeriodLabel(schedule, at),
			ScheduledFor: at, Missed: now.Sub(at) > r.MaxLate,
		})
	}
	if _, err := agentjob.Within(ctx, func(ctx context.Context) (int, error) {
		return r.Store.RecordDueReviewRuns(ctx, due, now)
	}); err != nil && ctx.Err() == nil {
		r.logError("review: record what is due", err)
	}
}

// claim takes the oldest due run, unless another review is running or an investigation waits.
func (r *Runner) claim(ctx context.Context) (*job, bool) {
	now := r.now()
	var waitingSince time.Time
	if r.InvestigationMaxWait > 0 {
		waitingSince = now.Add(-r.InvestigationMaxWait)
	}
	run, err := agentjob.Within(ctx, func(ctx context.Context) (store.ReviewRun, error) {
		ctx, cancel := context.WithTimeout(ctx, claimLimit)
		defer cancel()
		run, _, err := r.Store.ClaimReviewRun(ctx, now, runLease(0), waitingSince)
		return run, err
	})
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			r.logError("review: claim", err)
		}
		return nil, false
	}
	log := r.log().With("review_run_id", run.ID)
	drv := *r.Driver
	drv.Runs = &agentrun.Runner{Store: r.Store, Now: r.now, Log: log, Draining: ctx.Done()}
	drv.Guard, drv.Now, drv.Log = r.Guard, r.now, log
	return &job{r: r, run: run, claim: run.Claim(), drv: &drv}, true
}

// logError logs err, of a panic only its type and stack.
func (r *Runner) logError(what string, err error, args ...any) {
	var p *agentrun.PanicError
	if errors.As(err, &p) {
		r.log().Error(what, append(args, "panic_type", p.Type, "stack", p.Stack)...)
		return
	}
	r.log().Warn(what, append(args, "err", err)...)
}
