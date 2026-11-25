package investigate

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
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

	// Codes of the agent's error event that mean something here.
	codeNoResult    = "no_result"
	codeUnavailable = "unavailable" // the model could not be reached

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
// alert text, the agent's message or anything of the result.
type job struct {
	r     *Runner
	inv   store.Investigation
	claim store.Claim
	runs  *agentrun.Runner

	identity store.User
	run      store.AgentRun
	reserved bool // run is in the store and must be ended
	refused  bool // run was refused for the budget
	meter    *agentrun.Meter
	turn     agentrun.Turn
	result   *agentresult.Result // the first one the agent sent
	agentErr error               // why a call to the agent ended without an end event
	cancel   context.CancelFunc

	end  ending
	once sync.Once
}

// plan is what a run needs to start.
type plan struct {
	task      agentclient.Task
	timeout   time.Duration
	redactEnv bool
	token     string
}

// execute runs the job to its end. A panic anywhere ends it as failed, and never the process.
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
	p, stop := j.setUp(ctx)
	if stop != nil {
		return *stop
	}
	return j.stream(ctx, p)
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

// setUp does everything up to the call of the agent. Set-ups go one at a time, so that two of them cannot both take
// the last start the hourly cap allows. Until the reservation nothing is spent; a run that was reserved is ended by
// finish whatever follows.
func (j *job) setUp(ctx context.Context) (p plan, stop *ending) {
	select {
	case j.r.setUpCh <- struct{}{}:
		defer func() { <-j.r.setUpCh }()
	case <-ctx.Done():
		end := j.notStarted()
		return p, &end
	}
	if p, stop = j.prepare(ctx); stop != nil {
		return p, stop
	}
	return j.begin(ctx, p)
}

// prepare decides, without spending anything, whether there is something to run.
func (j *job) prepare(ctx context.Context) (p plan, stop *ending) {
	skip := func(reason string) (plan, *ending) {
		end := skipped(reason)
		return plan{}, &end
	}
	var err error
	if j.identity, err = within(ctx, j.r.Store.EnsureAgentIdentity); err != nil {
		return p, j.unready(ctx, "load the agent identity", err)
	}
	j.endPreviousRun(ctx)

	now := j.r.now()
	if j.r.PerHour <= 0 {
		return skip(reasonRateLimited)
	}
	if j.claim.Attempt == 1 {
		if j.r.MaxWait > 0 && now.Sub(j.inv.RequestedAt) > j.r.MaxWait {
			return skip(reasonExpired)
		}
		started, err := within(ctx, func(ctx context.Context) (int, error) {
			return j.r.Store.InvestigationsStartedSince(ctx, now.Add(-time.Hour))
		})
		if err != nil {
			return p, j.unready(ctx, "count the last hour's investigations", err)
		}
		if started >= j.r.PerHour {
			return skip(reasonRateLimited)
		}
	}

	subject, err := within(ctx, j.scope)
	if err != nil {
		return p, j.unready(ctx, "work out what the identity can see", err)
	}
	if len(subject.ServiceIDs) == 0 {
		return skip(reasonNotVisible)
	}

	settings, err := within(ctx, j.r.Store.GetAgentSettings)
	if err != nil {
		return p, j.unready(ctx, "load the agent settings", err)
	}
	ref, chosen := settings.RoleModels[roleName]
	if !chosen {
		return skip(reasonNotConfigured)
	}
	cat, err := j.r.Agent.Catalogue(ctx)
	if err != nil {
		if ctx.Err() != nil {
			end := j.notStarted()
			return p, &end
		}
		j.logError("investigate: agent catalogue unavailable", err)
		end := j.retryOrFail(reasonUnavailable, retryUnavailable)
		return p, &end
	}
	chosenModel, offered := cat.Find(ref.Provider, ref.Model)
	reservation := chosenModel.MaxRunCostMicroUSD(cat.MaxRunTokens)
	// A run whose worst case is not known must not start.
	if !offered || !cat.Supports(roleName) || cat.MaxRunTokens <= 0 || reservation < 0 {
		return skip(reasonNotConfigured)
	}

	j.run = store.AgentRun{ID: uuid.New(), Role: roleName, UserID: &j.identity.ID, Provider: ref.Provider, Model: ref.Model, ReservedMicroUSD: reservation}
	return plan{
		// Scrubbed and cut to what the agent takes; it frames the text as data.
		task: agentclient.Task{Kind: "investigate", Subject: subject, Alert: agentclient.TaskAlert{
			Kind: agentText(j.inv.AlertKind), Title: agentText(j.inv.AlertTitle), Text: agentText(j.inv.AlertText), OpenedAt: j.inv.AlertOpenedAt,
		}},
		timeout:   agentrun.RunTimeout(cat.RunTimeout),
		redactEnv: !slices.Contains(settings.UnredactedProviders, ref.Provider),
	}, nil
}

func agentText(text string) string { return agentresult.CutExcerpt(secretshape.ScrubText(text)) }

// endPreviousRun ends the run of an earlier holder that did not live to do it: it keeps the figures last recorded.
// One that was ended already stays as it is.
func (j *job) endPreviousRun(ctx context.Context) {
	if j.inv.RunID == nil {
		return
	}
	run := store.AgentRun{ID: *j.inv.RunID, Role: roleName, UserID: &j.identity.ID, Provider: j.inv.Provider, Model: j.inv.Model}
	j.runs.End(ctx, run, j.actor(), "", agentrun.Ending{Outcome: reasonInterrupted}, j.details())
}

func (j *job) actor() agentrun.Actor { return agentrun.Actor{Name: j.identity.Username} }

// details are what the audit row of a run says besides the run: ids and fixed words only.
func (j *job) details() map[string]any {
	return map[string]any{"investigation_id": j.inv.ID, "subject_kind": j.inv.SubjectKind}
}

// begin reserves the run and gets it ready to start, with the lease and the claim confirmed on both sides of the
// reservation, which is made even when ctx ends meanwhile.
func (j *job) begin(ctx context.Context, p plan) (plan, *ending) {
	if err := j.r.confirm(ctx); err != nil {
		return p, j.unconfirmed(ctx, err)
	}
	if err := j.runs.Reserve(ctx, j.run, p.timeout); err != nil {
		if errors.Is(err, agentrun.ErrAllowanceUsedUp) || errors.Is(err, agentrun.ErrAllowanceTooSmall) {
			j.refused = true
			end := skipped(reasonBudget)
			return p, &end
		}
		return p, j.unready(ctx, "reserve the run", err)
	}
	j.reserved = true
	j.meter = &agentrun.Meter{Store: j.r.Store, RunID: j.run.ID, Now: j.r.now, Draining: ctx.Done()}
	if err := j.r.confirm(ctx); err != nil {
		return p, j.unconfirmed(ctx, err)
	}
	if err := step(ctx, func(ctx context.Context) error {
		return j.r.Store.StartInvestigationRun(ctx, j.claim, j.run.ID, j.run.Provider, j.run.Model, j.r.now())
	}); err != nil {
		return p, j.unready(ctx, "start the run", err)
	}
	var err error
	if p.token, err = within(ctx, func(ctx context.Context) (string, error) {
		token, _, err := j.r.Auth.MintAgentRunToken(ctx, j.run.ID, p.redactEnv)
		return token, err
	}); err != nil {
		return p, j.unready(ctx, "mint the run token", err)
	}
	// The claim was taken before the run timeout was known; from here it covers the whole run.
	if err := step(ctx, func(ctx context.Context) error {
		return j.r.Store.RenewInvestigation(ctx, j.claim, j.r.now(), runLease(p.timeout))
	}); err != nil {
		return p, j.unready(ctx, "renew the claim", err)
	}
	if ctx.Err() != nil {
		end := j.notStarted()
		return p, &end
	}
	return p, nil
}

func (j *job) unconfirmed(ctx context.Context, err error) *ending {
	if ctx.Err() == nil {
		j.logError("investigate: coordinator lease not confirmed, not starting", err)
	}
	end := j.notStarted()
	return &end
}

// stream runs the agent until its stream has ended and says what that means.
func (j *job) stream(ctx context.Context, p plan) ending {
	slack := j.r.slack
	if slack == 0 {
		slack = defaultSlack
	}
	runCtx, cancel := context.WithTimeout(ctx, p.timeout+slack)
	defer cancel()
	j.cancel = cancel
	err := j.r.Agent.Run(runCtx, p.token, agentclient.RunRequest{
		RunID: j.run.ID, Role: j.run.Role, Provider: j.run.Provider, Model: j.run.Model,
		MaxCostMicroUSD: j.run.ReservedMicroUSD, Task: &p.task,
	}, func(ev agentclient.Event) { j.apply(ctx, ev) })
	if !j.turn.Ended() {
		switch {
		case ctx.Err() != nil:
			j.turn.End(agentrun.Draining)
		case runCtx.Err() != nil:
			j.turn.End(agentrun.TimedOut)
		default:
			j.agentErr = err
			j.turn.Fail("", "")
		}
	}
	if err != nil {
		j.runs.LogRunError(j.run.ID, "investigate: agent run failed", err) // never any model text
	}
	return j.outcome()
}

// apply takes one event. Once the turn has ended only what the run costs still counts. A panic ends the turn.
func (j *job) apply(ctx context.Context, ev agentclient.Event) {
	defer func() {
		if rec := recover(); rec != nil {
			j.logError("investigate: panic", agentrun.NewPanicError(rec))
			j.perform(ctx, j.turn.End(agentrun.Panicked))
		}
	}()
	if j.r.onEvent != nil && !j.turn.Ended() {
		j.r.onEvent(ev)
	}
	if ev.Type == "result" && !j.turn.Ended() {
		if j.result == nil {
			j.result = ev.Result
		} else {
			j.r.log().Warn("investigate: the agent sent more than one result; the first one stands", "investigation_id", j.inv.ID)
		}
	}
	j.perform(ctx, j.turn.Apply(ev))
}

func (j *job) perform(ctx context.Context, effects []agentrun.Effect) {
	for _, e := range effects {
		switch e.Kind {
		case agentrun.KeepUsage:
			j.meter.See(e.Usage)
		case agentrun.RecordUsage:
			j.meter.See(e.Usage)
			if err := j.meter.Record(ctx); err != nil { // it can no longer be billed, so it must stop
				j.runs.LogRunError(j.run.ID, "investigate: usage not recorded, stopping the run", err)
				j.perform(ctx, j.turn.End(agentrun.RunLost))
			}
		case agentrun.Cancel:
			j.cancel()
		}
	}
}

// outcome is what the end of the agent run means for the investigation.
func (j *job) outcome() ending {
	switch j.turn.Reason() {
	case agentrun.Done:
		if j.result == nil {
			return failed(reasonLimitReached)
		}
		return ending{kind: endDone, word: "done"}
	case agentrun.Draining:
		return ending{kind: endReleased, word: reasonInterrupted, counted: true}
	case agentrun.RunLost:
		return failed(reasonInterrupted)
	case agentrun.AgentFailed:
		switch code, _ := j.turn.Failure(); {
		case errors.Is(j.agentErr, agentclient.ErrAgentBusy):
			return j.busy()
		case errors.Is(j.agentErr, agentclient.ErrAgentUnavailable):
			return j.retryOrFail(reasonUnavailable, retryUnavailable)
		case code == codeNoResult:
			return failed(reasonNoResult)
		case code == codeUnavailable:
			return j.retryOrFail(reasonError, retryVendor)
		}
	}
	return failed(reasonError)
}

// busy gives the attempt back: the agent refused before it did anything. A first attempt that waited too long is
// skipped when it is claimed next; a second one is not looked at for its age any more, so it ends here.
func (j *job) busy() ending {
	waited := j.r.now().Add(retryBusy).Sub(j.inv.RequestedAt)
	if j.claim.Attempt > 1 && j.r.MaxWait > 0 && waited > j.r.MaxWait {
		return failed(reasonUnavailable)
	}
	return ending{kind: endReleased, word: "agent_busy", retryIn: retryBusy}
}

// finish ends the job once; later calls do nothing. Every step runs on a context that the end of ctx does not cut
// off, each under its own limit, and whatever became of the one before it.
func (j *job) finish(ctx context.Context) {
	j.once.Do(func() {
		var err error
		if j.reserved {
			ops := j.runs.Ops(j.run, j.actor(), "", j.meter, agentrun.Ending{Success: j.end.kind == endDone, Outcome: j.end.word})
			ops.Tools = slices.Clone(j.turn.Tools())
			ops.Details = j.details()
			ops.SaveAnswer = func(ctx context.Context) (int64, error) { return 0, j.settle(ctx) }
			err = j.runs.Finish(ctx, ops).SaveErr
		} else if err = step(ctx, j.settle); err != nil {
			j.logError("investigate: could not end the investigation", err)
		}
		if err != nil {
			return
		}
		if j.refused {
			j.auditRefusal(ctx)
		}
		if j.end.kind == endDone && j.r.KickNotify != nil {
			j.r.KickNotify()
		}
	})
}

// auditRefusal records a run that the budget refused. It has no run row, so it is an ordinary audit entry.
func (j *job) auditRefusal(ctx context.Context) {
	entry := store.AgentRunAudit(j.run, j.identity.Username, "skipped_budget")
	for k, v := range j.details() {
		entry.Details[k] = v
	}
	if err := step(ctx, func(ctx context.Context) error { return j.r.Store.Audit(ctx, entry) }); err != nil {
		j.logError("investigate: could not audit the refused run", err)
	}
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

// storeResult checks the agent's result against what the identity may read now and stores it. One that cannot be
// stored fails the investigation rather than leaving it running.
func (j *job) storeResult(ctx context.Context) error {
	see, err := j.r.sightOf(ctx, j.identity.ID)
	if err == nil {
		accepted := Accept(*j.result, see.can)
		if err = see.err; err == nil {
			err = j.r.Store.FinishInvestigation(ctx, j.claim, store.InvestigationDone, "", &accepted, j.r.now())
		}
	}
	if err == nil || errors.Is(err, store.ErrConflict) {
		return err // a conflict: another holder has it, and the result is discarded
	}
	return errors.Join(err, j.r.Store.FinishInvestigation(ctx, j.claim, store.InvestigationFailed, reasonError, nil, j.r.now()))
}
