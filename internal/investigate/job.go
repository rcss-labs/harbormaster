package investigate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	roleName = "investigator"

	retryUnavailable = time.Minute
	retryBusy        = 30 * time.Second
	retryVendor      = 5 * time.Minute
	maxAttempts      = 2

	// storeTry is how long each write that stores a result may take; three of them fit in the time the end of a run
	// gives the saving of an answer (10 s).
	storeTry = 3 * time.Second

	// Fixed reasons: what is stored of an investigation that ended without a result.
	reasonBudget        = "budget"
	reasonNotVisible    = "not_visible"
	reasonNotConfigured = "not_configured"
	reasonRateLimited   = "rate_limited"
	reasonExpired       = "expired"
	reasonUnavailable   = "agent_unavailable"
	reasonLimitReached  = "limit_reached"
	reasonNoResult      = "no_result"
	reasonInterrupted   = "interrupted"
	reasonError         = "error"
)

// endKind is what becomes of the claimed investigation.
type endKind int

const (
	endFailed   endKind = iota // failed with a reason; the zero value, so nothing unforeseen looks like success
	endDone                    // the result is stored
	endSkipped                 // ended without a run, with a reason
	endReleased                // given back, to be claimed again
	endLost                    // another holder has it: it is left alone
)

// ending is how a job ends. word is its run's outcome in the audit log, always a fixed one.
type ending struct {
	kind    endKind
	reason  string
	word    string
	retryIn time.Duration
	counted bool // a released attempt that counts
}

func failed(reason string) ending  { return ending{kind: endFailed, reason: reason, word: reason} }
func skipped(reason string) ending { return ending{kind: endSkipped, reason: reason, word: reason} }

// job is one claimed investigation. One goroutine owns it; however it ends, finish runs once. It never logs or audits
// alert text, the agent's message or anything of the result. The paid run itself is the driver's.
type job struct {
	r     *Runner
	inv   store.Investigation
	claim store.Claim
	drv   *agentjob.Driver

	identity store.User
	prepared agentjob.Prepared
	task     agentclient.Task
	outcome  *agentjob.Outcome   // set once the driver has run
	result   *agentresult.Result // the first one the agent sent

	end  ending
	once sync.Once
}

// execute runs the job to its end. A panic before the end makes it a failed one; none ends the process.
func (j *job) execute(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			j.logError("investigate: panic", agentrun.NewPanicError(rec))
		}
		j.finish(ctx) // does nothing when the job was ended already
	}()
	j.end = failed(reasonError)
	j.end = j.work(ctx)
	j.finish(ctx)
}

func (j *job) work(ctx context.Context) ending {
	// Set-ups go one at a time, so that two of them cannot both take the last start the hourly cap allows. Until the
	// reservation nothing is spent; the driver ends a run that was reserved whatever follows.
	select {
	case j.r.setUpCh <- struct{}{}:
	case <-ctx.Done():
		return j.notStarted()
	}
	setUpOver := sync.OnceFunc(func() { <-j.r.setUpCh })
	defer setUpOver()
	if stop := j.prepare(ctx); stop != nil {
		return *stop
	}
	out := j.drv.Run(ctx, j.prepared, agentjob.Job{
		Role: roleName, Task: j.task, Details: j.details(),
		Started: func(ctx context.Context, runID uuid.UUID, provider, model string) error {
			return j.r.Store.StartInvestigationRun(ctx, j.claim, runID, provider, model, j.r.now())
		},
		// The claim was taken before the run timeout was known; from here it covers the whole run.
		Renew: func(ctx context.Context, timeout time.Duration) error {
			return j.r.Store.RenewInvestigation(ctx, j.claim, j.r.now(), runLease(timeout))
		},
		SetUpOver: setUpOver,
		Conclude:  j.conclude,
	})
	j.outcome = &out
	if !out.Finished { // nothing was reserved: the claim is ended here
		j.end = j.endOf(out)
	}
	return j.end
}

func (j *job) logError(what string, err error) {
	j.r.logError(what, err, "investigation_id", j.inv.ID)
}

// notStarted gives the claim back as if it had not been made: the job is being stopped or the lease is not certain,
// and the agent was not called.
func (j *job) notStarted() ending {
	return ending{kind: endReleased, word: reasonInterrupted, retryIn: j.r.interval()}
}

// retryOrFail gives a first attempt back to be tried once more; a second one fails.
func (j *job) retryOrFail(reason string, in time.Duration) ending {
	if j.claim.Attempt >= maxAttempts {
		return failed(reason)
	}
	return ending{kind: endReleased, word: reason, retryIn: in, counted: true}
}

// unready ends a set-up that err stopped.
func (j *job) unready(ctx context.Context, what string, err error) *ending {
	var end ending
	switch {
	case errors.Is(err, store.ErrConflict):
		end = ending{kind: endLost, word: reasonInterrupted}
	case ctx.Err() != nil:
		end = j.notStarted()
	default:
		j.logError("investigate: "+what, err)
		end = j.retryOrFail(reasonError, retryUnavailable)
	}
	return &end
}

// unreadyByDriver ends a set-up that the driver could not finish and has logged.
func (j *job) unreadyByDriver(ctx context.Context) *ending {
	end := j.notStarted()
	if ctx.Err() == nil {
		end = j.retryOrFail(reasonError, retryUnavailable)
	}
	return &end
}

// prepare decides, without spending anything, whether there is something to run.
func (j *job) prepare(ctx context.Context) *ending {
	skip := func(reason string) *ending {
		end := skipped(reason)
		return &end
	}
	identity, kind := j.drv.Identify(ctx)
	if kind != agentjob.Ready {
		return j.unreadyByDriver(ctx)
	}
	j.identity = identity
	if err := j.endPreviousRun(ctx); err != nil {
		// It may still hold its reservation, or cost more: no second run beside it.
		j.logError("investigate: the previous run is not settled", err)
		end := ending{kind: endReleased, word: reasonInterrupted, retryIn: retryUnavailable}
		return &end
	}

	now := j.r.now()
	if j.r.PerHour <= 0 {
		return skip(reasonRateLimited)
	}
	if j.inv.StartedAt == nil { // no run was ever started for it, whatever became of its attempts
		if j.r.MaxWait > 0 && now.Sub(j.inv.RequestedAt) > j.r.MaxWait {
			return skip(reasonExpired)
		}
		started, err := agentjob.Within(ctx, func(ctx context.Context) (int, error) {
			return j.r.Store.InvestigationsStartedSince(ctx, now.Add(-time.Hour))
		})
		if err != nil {
			return j.unready(ctx, "count the last hour's investigations", err)
		}
		if started >= j.r.PerHour {
			return skip(reasonRateLimited)
		}
	}

	seen, err := agentjob.Within(ctx, j.scope)
	if err != nil {
		return j.unready(ctx, "work out what the identity can see", err)
	}
	if len(seen.visible) == 0 {
		return skip(reasonNotVisible)
	}

	prepared, kind := j.drv.PrepareWith(ctx, identity, roleName)
	switch kind {
	case agentjob.Ready:
	case agentjob.NotConfigured, agentjob.RoleNotOffered, agentjob.ModelUnavailable:
		return skip(reasonNotConfigured)
	case agentjob.AgentUnavailable:
		end := j.notStarted()
		if ctx.Err() == nil {
			end = j.retryOrFail(reasonUnavailable, retryUnavailable)
		}
		return &end
	default:
		return j.unreadyByDriver(ctx)
	}
	j.prepared = prepared
	j.task = agentclient.Task{Kind: "investigate", Subject: seen.subject, Alert: taskAlert(j.inv, seen.visible)}
	return nil
}

func agentText(text string) string { return agentresult.CutExcerpt(secretshape.ScrubText(text)) }

// endPreviousRun ends the run of an earlier holder that did not live to do it. An error means that it may still be
// running.
func (j *job) endPreviousRun(ctx context.Context) error {
	if j.inv.RunID == nil {
		return nil
	}
	run := store.AgentRun{ID: *j.inv.RunID, Role: roleName, UserID: &j.identity.ID, Provider: j.inv.Provider, Model: j.inv.Model}
	return j.drv.EndEarlier(ctx, j.identity, run, j.details())
}

// details are what the audit row of a run says besides the run: ids and fixed words only.
func (j *job) details() map[string]any {
	return map[string]any{"investigation_id": j.inv.ID, "subject_kind": j.inv.SubjectKind}
}

// conclude is what the driver asks, once, of a run that was reserved: what becomes of the investigation, and the
// write that does it.
func (j *job) conclude(out agentjob.Outcome) agentjob.Conclusion {
	j.result = out.Result
	j.end = j.safeEndOf(out)
	return agentjob.Conclusion{Word: j.end.word, Save: j.settle}
}

// safeEndOf is endOf, except that a panic in it ends the investigation as failed: the run is paid for and is being
// settled, so the claim must not be left running for a second one.
func (j *job) safeEndOf(out agentjob.Outcome) (end ending) {
	defer func() {
		if rec := recover(); rec != nil {
			j.logError("investigate: panic", agentrun.NewPanicError(rec))
			end = failed(reasonError)
		}
	}()
	return j.endOf(out)
}

// endOf is what the way a run came out means for the investigation.
func (j *job) endOf(out agentjob.Outcome) ending {
	switch out.Kind {
	case agentjob.Done:
		return ending{kind: endDone, word: "done"}
	case agentjob.Budget:
		return skipped(reasonBudget)
	case agentjob.NotConfigured, agentjob.RoleNotOffered, agentjob.ModelUnavailable:
		return skipped(reasonNotConfigured)
	case agentjob.LimitReached:
		return failed(reasonLimitReached)
	case agentjob.NoResult:
		return failed(reasonNoResult)
	case agentjob.AgentBusy:
		return j.busy()
	case agentjob.AgentUnavailable:
		return j.retryOrFail(reasonUnavailable, retryUnavailable)
	case agentjob.Cancelled:
		if !out.Called {
			return j.notStarted()
		}
		return ending{kind: endReleased, word: reasonInterrupted, counted: true}
	case agentjob.Interrupted:
		return failed(reasonInterrupted)
	case agentjob.Conflict:
		return ending{kind: endLost, word: reasonInterrupted}
	}
	switch {
	case out.ModelUnreachable:
		return j.retryOrFail(reasonError, retryVendor)
	case out.Called || out.Panicked:
		return failed(reasonError)
	default: // a set-up step failed before the agent was called
		return j.retryOrFail(reasonError, retryUnavailable)
	}
}

// busy gives the attempt back: the agent refused before it did anything. A first attempt that waited too long is
// skipped when it is claimed next. A second one keeps the start it recorded, so its age is not looked at there any
// more: it ends here.
func (j *job) busy() ending {
	waited := j.r.now().Add(retryBusy).Sub(j.inv.RequestedAt)
	if j.claim.Attempt > 1 && j.r.MaxWait > 0 && waited > j.r.MaxWait {
		return failed(reasonUnavailable)
	}
	return ending{kind: endReleased, word: "agent_busy", retryIn: retryBusy}
}

// finish ends the job once; later calls do nothing. A run that was reserved has been settled by the driver, with the
// write below as its save; otherwise the claim is ended here. Every step runs on a context that the end of ctx does
// not cut off.
func (j *job) finish(ctx context.Context) {
	j.once.Do(func() {
		var err error
		if j.outcome != nil && j.outcome.Finished {
			err = j.outcome.SaveErr
		} else if err = agentjob.Step(ctx, j.settle); err != nil {
			j.logError("investigate: could not end the investigation", err)
		}
		if err != nil {
			return
		}
		if j.outcome != nil && j.outcome.Kind == agentjob.Budget {
			j.drv.AuditSkippedBudget(ctx, j.prepared, *j.outcome, roleName, j.details())
		}
		if j.end.kind == endDone && j.r.KickNotify != nil {
			j.r.KickNotify()
		}
	})
}

// settle writes what becomes of the investigation, fenced by the claim.
func (j *job) settle(ctx context.Context) error {
	st, now := j.r.Store, j.r.now()
	switch j.end.kind {
	case endDone:
		return j.storeResult(ctx)
	case endSkipped:
		return st.SkipInvestigation(ctx, j.claim, j.end.reason, now)
	case endReleased:
		var retryAt time.Time
		if j.end.retryIn > 0 {
			retryAt = now.Add(j.end.retryIn)
		}
		return st.ReleaseInvestigation(ctx, j.claim, retryAt, j.end.counted)
	case endLost:
		return nil
	default:
		return st.FinishInvestigation(ctx, j.claim, store.InvestigationFailed, j.end.reason, nil, now)
	}
}

// storeResult checks the agent's result against what the identity may read now and stores it. The result is paid for,
// so one bad write is tried again. One that cannot be stored fails the investigation rather than leaving it running.
// Each of the three has its own time, so that one that hangs leaves some for the next.
func (j *job) storeResult(ctx context.Context) error {
	var err error
	for range 2 {
		err = j.inTime(ctx, j.storeOnce)
		if err == nil || errors.Is(err, store.ErrConflict) {
			return err // a conflict: another holder has it, and the result is discarded
		}
	}
	return errors.Join(err, j.inTime(ctx, func(ctx context.Context) error {
		return j.r.Store.FinishInvestigation(ctx, j.claim, store.InvestigationFailed, reasonError, nil, j.r.now())
	}))
}

func (j *job) inTime(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, storeTry)
	defer cancel()
	return fn(ctx)
}

func (j *job) storeOnce(ctx context.Context) error {
	see, err := agentjob.SightOf(ctx, j.r.Store, j.identity.ID)
	if err != nil {
		return err
	}
	accepted := Accept(*j.result, see.Can, agentjob.TaskIDs(j.task)...)
	if err := see.Err(); err != nil {
		return err
	}
	return j.r.Store.FinishInvestigation(ctx, j.claim, store.InvestigationDone, "", &accepted, j.r.now())
}
