package agentjob

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Codes of the agent's error event that mean something here.
const (
	codeNoResult    = "no_result"
	codeUnavailable = "unavailable" // the model could not be reached
)

// Run makes one run: it confirms the lease, reserves, has the caller record the start, makes the run token, calls the
// agent, meters its usage as it arrives, and settles the run, deletes the token and audits it, once, on a context
// that the end of ctx does not cut off. A run that was reserved is always settled, whatever happens meanwhile. The
// first result after which the agent says done is the one that counts; the caller accepts it.
func (d *Driver) Run(ctx context.Context, p Prepared, job Job) (out Outcome) {
	reservation, known := worstCase(p.Model, p.Catalogue.MaxRunTokens)
	if !known {
		if job.SetUpOver != nil {
			job.SetUpOver()
		}
		return Outcome{Kind: ModelUnavailable}
	}
	r := &active{
		d: d, p: p, job: job,
		run: store.AgentRun{ID: uuid.New(), Role: job.Role, UserID: &p.Identity.ID, Provider: p.Provider, Model: p.Model.ID, ReservedMicroUSD: reservation},
	}
	r.out = Outcome{Kind: Failed, RunID: r.run.ID, Provider: r.run.Provider, Model: r.run.Model}
	// Once, and never a panic: a caller that fails to let the next set-up in must not keep this run from being settled.
	var setUpOver sync.Once
	r.setUpOver = func() {
		setUpOver.Do(func() {
			defer func() {
				if rec := recover(); rec != nil {
					d.logErr("agentjob: panic", agentrun.NewPanicError(rec))
				}
			}()
			if job.SetUpOver != nil {
				job.SetUpOver()
			}
		})
	}
	defer func() {
		if rec := recover(); rec != nil {
			d.logErr("agentjob: panic", agentrun.NewPanicError(rec))
			r.out.Kind, r.out.Panicked, r.out.Result = Failed, true, nil
		}
		r.setUpOver()
		r.finish(ctx)
		out = r.out
	}()
	if r.begin(ctx) {
		r.stream(ctx)
	}
	return r.out
}

// active is one run in progress. One goroutine owns it.
type active struct {
	d   *Driver
	p   Prepared
	job Job
	run store.AgentRun

	reserved bool // the run is in the store and must be settled
	meter    *agentrun.Meter
	turn     agentrun.Turn
	result   *agentresult.Result // the first one the agent sent
	agentErr error               // why a call to the agent ended without an end event
	cancel   context.CancelFunc
	token    string

	out         Outcome
	setUpOver   func()
	concludeErr error // the caller panicked in Conclude: nothing it would have saved was saved
	once        sync.Once
}

// stopped sets how a step that failed with err ended the run: the caller's claim is lost, the job is stopping, or
// the step failed and may be tried again.
func (r *active) stopped(ctx context.Context, what string, err error) bool {
	switch {
	case errors.Is(err, store.ErrConflict):
		r.out.Kind = Conflict
	case ctx.Err() != nil:
		r.out.Kind = Cancelled
	default:
		r.d.logErr("agentjob: "+what, err)
		r.out.Kind = Failed
	}
	return false
}

func (r *active) unconfirmed(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		r.d.logErr("agentjob: coordinator lease not confirmed, not starting", err)
	}
	r.out.Kind = Cancelled
	return false
}

// begin reserves the run and gets it ready to be called, with the lease confirmed on both sides of the reservation,
// which is made even when ctx ends meanwhile. It reports whether the agent is to be called.
func (r *active) begin(ctx context.Context) bool {
	d, job := r.d, r.job
	if err := Confirm(ctx, d.Guard); err != nil {
		return r.unconfirmed(ctx, err)
	}
	timeout := agentrun.RunTimeout(r.p.Catalogue.RunTimeout)
	if err := d.Runs.ReserveLeaving(ctx, r.run, timeout, job.LeaveMicroUSD); err != nil {
		if errors.Is(err, agentrun.ErrAllowanceUsedUp) || errors.Is(err, agentrun.ErrAllowanceTooSmall) {
			r.out.Kind = Budget
			return false
		}
		return r.stopped(ctx, "reserve the run", err)
	}
	r.reserved = true
	r.meter = &agentrun.Meter{Store: d.Store, RunID: r.run.ID, Now: d.now, Draining: ctx.Done()}
	if err := Confirm(ctx, d.Guard); err != nil {
		return r.unconfirmed(ctx, err)
	}
	if err := Step(ctx, func(ctx context.Context) error { return job.Started(ctx, r.run.ID, r.run.Provider, r.run.Model) }); err != nil {
		return r.stopped(ctx, "start the run", err)
	}
	token, err := Within(ctx, func(ctx context.Context) (string, error) {
		token, _, err := d.Auth.MintAgentRunToken(ctx, r.run.ID, r.p.RedactEnv)
		return token, err
	})
	if err != nil {
		return r.stopped(ctx, "mint the run token", err)
	}
	if job.Renew != nil {
		if err := Step(ctx, func(ctx context.Context) error { return job.Renew(ctx, timeout) }); err != nil {
			return r.stopped(ctx, "renew the claim", err)
		}
	}
	if ctx.Err() != nil {
		r.out.Kind = Cancelled
		return false
	}
	r.token = token
	return true
}

// stream calls the agent until its stream has ended and says what that means.
func (r *active) stream(ctx context.Context) {
	d := r.d
	slack := d.Slack
	if slack == 0 {
		slack = DefaultSlack
	}
	runCtx, cancel := context.WithTimeout(ctx, agentrun.RunTimeout(r.p.Catalogue.RunTimeout)+slack)
	defer cancel()
	r.cancel = cancel
	r.setUpOver()
	r.out.Called = true
	err := d.Agent.Run(runCtx, r.token, agentclient.RunRequest{
		RunID: r.run.ID, Role: r.run.Role, Provider: r.run.Provider, Model: r.run.Model,
		MaxCostMicroUSD: r.run.ReservedMicroUSD, Task: &r.job.Task,
	}, func(ev agentclient.Event) { r.apply(ctx, ev) })
	if !r.turn.Ended() {
		switch {
		case ctx.Err() != nil:
			r.turn.End(agentrun.Draining)
		case runCtx.Err() != nil:
			r.turn.End(agentrun.TimedOut)
		default:
			r.agentErr = err
			r.turn.Fail("", "")
		}
	}
	if err != nil {
		d.logRunError(r.run.ID, "agentjob: agent run failed", err) // never any model text
	}
	r.out.Tools = slices.Clone(r.turn.Tools())
	r.conclude()
}

// apply takes one event. Once the turn has ended only what the run costs still counts. A panic ends the turn.
func (r *active) apply(ctx context.Context, ev agentclient.Event) {
	defer func() {
		if rec := recover(); rec != nil {
			r.d.logErr("agentjob: panic", agentrun.NewPanicError(rec))
			r.perform(ctx, r.turn.End(agentrun.Panicked))
		}
	}()
	if r.d.OnEvent != nil && !r.turn.Ended() {
		r.d.OnEvent(ev)
	}
	if ev.Type == "result" && !r.turn.Ended() {
		if r.result == nil {
			r.result = ev.Result
		} else {
			r.d.log().Warn("agentjob: the agent sent more than one result; the first one stands", "run_id", r.run.ID)
		}
	}
	r.perform(ctx, r.turn.Apply(ev))
}

func (r *active) perform(ctx context.Context, effects []agentrun.Effect) {
	for _, e := range effects {
		switch e.Kind {
		case agentrun.KeepUsage:
			r.meter.See(e.Usage)
		case agentrun.RecordUsage:
			r.meter.See(e.Usage)
			if err := r.meter.Record(ctx); err != nil { // it can no longer be billed, so it must stop
				r.d.logRunError(r.run.ID, "agentjob: usage not recorded, stopping the run", err)
				r.perform(ctx, r.turn.End(agentrun.RunLost))
			}
		case agentrun.Cancel:
			r.cancel()
		}
	}
}

// conclude says what the end of the agent's run means.
func (r *active) conclude() {
	out := &r.out
	switch r.turn.Reason() {
	case agentrun.Done:
		if r.result == nil {
			out.Kind = LimitReached
		} else {
			out.Kind, out.Result = Done, r.result
		}
	case agentrun.Draining:
		out.Kind = Cancelled
	case agentrun.RunLost:
		out.Kind = Interrupted
	case agentrun.AgentFailed:
		switch code, _ := r.turn.Failure(); {
		case errors.Is(r.agentErr, agentclient.ErrAgentBusy):
			out.Kind = AgentBusy
		case errors.Is(r.agentErr, agentclient.ErrAgentUnavailable):
			out.Kind, out.Refused = AgentUnavailable, errors.Is(r.agentErr, agentclient.ErrBadRequest)
		case code == codeNoResult:
			out.Kind = NoResult
		case code == codeUnavailable:
			out.Kind, out.ModelUnreachable = Failed, true
		default:
			out.Kind = Failed
		}
	default:
		out.Kind = Failed
	}
}

// finish ends a run that was reserved, once; later calls do nothing. Every step runs on a context that the end of ctx
// does not cut off, each under its own limit, and whatever became of the one before it.
func (r *active) finish(ctx context.Context) {
	r.once.Do(func() {
		if !r.reserved {
			return
		}
		conclusion := r.conclusion()
		ops := r.d.Runs.Ops(r.run, agentrun.Actor{Name: r.p.Identity.Username}, "", r.meter,
			agentrun.Ending{Success: r.out.Kind == Done, Outcome: conclusion.Word})
		ops.Tools = slices.Clone(r.turn.Tools())
		ops.Details = r.job.Details
		if conclusion.Save != nil {
			ops.SaveAnswer = func(ctx context.Context) (int64, error) { return 0, conclusion.Save(ctx) }
		}
		res := r.d.Runs.Finish(ctx, ops)
		r.out.Finished, r.out.SaveErr = true, res.SaveErr
		if r.concludeErr != nil {
			r.out.SaveErr = r.concludeErr
		}
	})
}

// conclusion asks the caller what to make of the outcome. A caller that panics leaves a failed run, so that the run
// is still settled.
func (r *active) conclusion() (c Conclusion) {
	c = Conclusion{Word: defaultWord(r.out.Kind)}
	if r.job.Conclude == nil {
		return c
	}
	defer func() {
		if rec := recover(); rec != nil {
			pe := agentrun.NewPanicError(rec)
			r.d.logErr("agentjob: panic", pe)
			r.out.Kind, r.out.Panicked, r.out.Result = Failed, true, nil
			r.concludeErr = pe
			c = Conclusion{Word: defaultWord(Failed)}
		}
	}()
	c = r.job.Conclude(r.out)
	if c.Word == "" {
		c.Word = defaultWord(r.out.Kind)
	}
	return c
}

// defaultWord is the audit word for a kind when the caller words none.
func defaultWord(k Kind) string {
	switch k {
	case Done:
		return "done"
	case NoResult:
		return "no_result"
	case LimitReached:
		return "limit_reached"
	case AgentBusy:
		return "agent_busy"
	case AgentUnavailable:
		return "agent_unavailable"
	case Cancelled, Interrupted, Conflict:
		return wordInterrupted
	default:
		return "error"
	}
}
