// Package investigate is the coordinator job that runs the AI investigations of alerts: it claims a request, runs the
// agent as Harbormaster's own agent identity, checks what it answered and stores it. A request leads to at most two
// paid runs. Each is reserved once and settled once: by the job that started it, by the next holder of the request, or
// by the repair of abandoned runs. A result is stored once, fenced by the claim.
package investigate

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInterval = 15 * time.Second
	// A claim is held for the run timeout, at least minRunLease, and leaseMargin more, which covers the slack a run is
	// given and the end of it.
	minRunLease = 5 * time.Minute
	leaseMargin = time.Minute
	// defaultSlack is how long past the agent's own run timeout a run is waited for.
	defaultSlack = 30 * time.Second
)

// Runner claims and runs investigations. Run it under the coordinator lease only.
type Runner struct {
	Store *store.Store
	Auth  *auth.Service
	Agent *agentclient.Client
	// Concurrent is how many investigations run at once (at least one); PerHour is how many may start in an hour, 0
	// meaning that none does: what is still requested is then ended without a run.
	Concurrent, PerHour int
	// MaxWait is how old a request may be when it is first started; 0 means any age.
	MaxWait  time.Duration
	Interval time.Duration // between looks for due requests; 0 means 15 s
	// Guard is checked before a request is claimed and again before and after the reservation, so a coordinator that
	// lost its lease starts nothing. Nil means always allowed.
	Guard lease.Guard
	// KickNotify, when set, is called when a result was stored, so its message does not wait for an interval.
	KickNotify func()
	Now        func() time.Time
	Log        *slog.Logger

	initOnce sync.Once
	kickCh   chan struct{}
	setUpCh  chan struct{}

	// Set by tests only.
	onEvent func(agentclient.Event)
	slack   time.Duration
}

func (r *Runner) init() {
	r.initOnce.Do(func() {
		r.kickCh = make(chan struct{}, 1)
		r.setUpCh = make(chan struct{}, 1)
	})
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

// runLease is how long a claim is held for a run of the given timeout.
func runLease(timeout time.Duration) time.Duration { return max(timeout, minRunLease) + leaseMargin }

// Kick wakes the loop so a request does not wait for the next interval. It never blocks, and wakes only the loop in
// this process.
func (r *Runner) Kick() {
	r.init()
	select {
	case r.kickCh <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run starts due investigations at once and then every Interval, on a Kick, and when one that ran has ended, until
// ctx ends. It returns when every investigation it started has been ended.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	slots := make(chan struct{}, max(r.Concurrent, 1))
	freed := make(chan struct{}, 1)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for {
		r.fill(ctx, slots, freed, &jobs)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.kickCh:
		case <-freed:
		}
	}
}

// fill starts due investigations while there is a free slot.
func (r *Runner) fill(ctx context.Context, slots, freed chan struct{}, jobs *sync.WaitGroup) {
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		default:
			return
		}
		j, ok := r.claim(ctx)
		if !ok {
			<-slots
			return
		}
		jobs.Go(func() {
			defer func() {
				<-slots
				select {
				case freed <- struct{}{}:
				default:
				}
			}()
			j.execute(ctx)
		})
	}
}

// claim takes the oldest due request, when the lease is still held.
func (r *Runner) claim(ctx context.Context) (*job, bool) {
	if err := r.confirm(ctx); err != nil {
		if ctx.Err() == nil {
			r.log().Warn("investigate: coordinator lease not confirmed, starting nothing", "err", err)
		}
		return nil, false
	}
	v, err := within(ctx, func(ctx context.Context) (store.Investigation, error) {
		v, _, err := r.Store.ClaimInvestigation(ctx, r.now(), runLease(0))
		return v, err
	})
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			r.logError("investigate: claim", err)
		}
		return nil, false
	}
	return &job{
		r: r, inv: v, claim: v.Claim(),
		runs: &agentrun.Runner{Store: r.Store, Now: r.now, Log: r.log(), Draining: ctx.Done()},
	}, true
}

// confirm reports why nothing may be started: the job is being stopped, or the lease is not certain.
func (r *Runner) confirm(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Guard == nil {
		return nil
	}
	return r.Guard(ctx)
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

// within makes a store step that is not cut off by the end of ctx, which would leave unknown whether it was made:
// it is bounded by its own limit, a short one once ctx has ended.
func within[T any](ctx context.Context, fn func(ctx context.Context) (T, error)) (T, error) {
	return agentrun.Within(context.WithoutCancel(ctx), agentrun.SetUpLimit, ctx.Done(), fn)
}

// step is within for a step that returns nothing.
func step(ctx context.Context, fn func(ctx context.Context) error) error {
	_, err := within(ctx, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return err
}
