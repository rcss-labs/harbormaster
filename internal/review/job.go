package review

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/reviewsched"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	roleName = "reviewer"

	retryLater  = 5 * time.Minute
	retryBusy   = time.Minute
	maxAttempts = 2
	// maxTaskServices is the largest count of services the agent takes in a task.
	maxTaskServices = 100_000

	// storeTry is how long each write that stores a result may take; three of them fit in the time the end of a run
	// gives the saving of an answer (10 s).
	storeTry = 3 * time.Second

	// Fixed reasons: what is stored of a review run that ended without a result.
	reasonBudget        = "budget"
	reasonNotVisible    = "not_visible"
	reasonNotConfigured = "not_configured"
	reasonRateLimited   = "rate_limited"
	reasonMissed        = "missed"
	reasonDisabled      = "disabled"
	reasonUnavailable   = "agent_unavailable"
	reasonLimitReached  = "limit_reached"
	reasonNoResult      = "no_result"
	reasonInterrupted   = "interrupted"
	reasonError         = "error"
)

// knownType is the closed set of review types this code runs; a type of a later release is not one of them.
func knownType(reviewType string) bool {
	switch reviewType {
	case "cost", "security", "reliability", "capacity":
		return true
	}
	return false
}

// endKind is what becomes of the claimed review run.
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

// job is one claimed review run. One goroutine owns it; however it ends, finish runs once. It never logs or audits the
// review's name, its scope, the agent's message or anything of the result. The paid run itself is the driver's.
type job struct {
	r     *Runner
	run   store.ReviewRun
	claim store.ReviewClaim
	drv   *agentjob.Driver

	identity store.User
	prepared agentjob.Prepared
	start    store.ReviewStart // what is recorded with the start, but for the run
	task     agentclient.Task
	leave    int64
	startErr error               // why the start could not be recorded
	outcome  *agentjob.Outcome   // set once the driver has run
	result   *agentresult.Result // the first one the agent sent

	end  ending
	once sync.Once
}

// execute runs the job to its end. A panic before the end makes it a failed one; none ends the process.
func (j *job) execute(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			j.logError("review: panic", agentrun.NewPanicError(rec))
		}
		j.finish(ctx) // does nothing when the job was ended already
	}()
	j.end = failed(reasonError)
	j.end = j.work(ctx)
	j.finish(ctx)
}

func (j *job) work(ctx context.Context) ending {
	if stop := j.prepare(ctx); stop != nil {
		return *stop
	}
	if ctx.Err() != nil {
		return j.notStarted()
	}
	out := j.drv.Run(ctx, j.prepared, agentjob.Job{
		Role: roleName, Task: j.task, Details: j.details(), LeaveMicroUSD: j.leave,
		Started: func(ctx context.Context, runID uuid.UUID, provider, model string) error {
			start := j.start
			start.RunID, start.Provider, start.Model = runID, provider, model
			j.startErr = j.r.Store.StartReviewRun(ctx, j.claim, start, j.r.now())
			return j.startErr
		},
		// The claim was taken before the run timeout was known; from here it covers the whole run.
		Renew: func(ctx context.Context, timeout time.Duration) error {
			return j.r.Store.RenewReviewRun(ctx, j.claim, j.r.now(), runLease(timeout))
		},
		Conclude: j.conclude,
	})
	j.outcome = &out
	if !out.Finished { // nothing was reserved: the claim is ended here
		j.end = j.endOf(out)
	}
	return j.end
}

func (j *job) logError(what string, err error) {
	j.r.logError(what, err, "review_run_id", j.run.ID)
}

// notStarted gives the claim back as if it had not been made: the job is being stopped or the lease is not certain,
// and the agent was not called.
func (j *job) notStarted() ending {
	return ending{kind: endReleased, word: reasonInterrupted, retryIn: j.r.interval()}
}

// giveBack gives the claim back without counting the attempt: the agent is away, busy or cannot make this review. The
// wait is bounded by the late limit: a row that no run was started for is skipped as missed when it is claimed next,
// and one that had a run ends here.
func (j *job) giveBack(word string, in time.Duration) ending {
	if j.run.StartedAt != nil && j.r.now().Add(in).Sub(j.run.ScheduledFor) > j.r.MaxLate {
		return failed(reasonUnavailable)
	}
	return ending{kind: endReleased, word: word, retryIn: in}
}

// retryOrFail gives a first attempt back to be tried once more; a second one fails.
func (j *job) retryOrFail(reason string) ending {
	if j.claim.Attempt >= maxAttempts {
		return failed(reason)
	}
	return ending{kind: endReleased, word: reason, retryIn: retryLater, counted: true}
}

// unready ends a set-up that a failed step stopped before anything was spent.
func (j *job) unready(ctx context.Context) *ending {
	end := j.notStarted()
	if ctx.Err() == nil {
		end = j.retryOrFail(reasonError)
	}
	return &end
}

// prepare decides, without spending anything, whether there is something to run, and what.
func (j *job) prepare(ctx context.Context) *ending {
	skip := func(reason string) *ending {
		end := skipped(reason)
		return &end
	}
	failedStep := func(what string, err error) *ending {
		if ctx.Err() == nil {
			j.logError("review: "+what, err)
		}
		return j.unready(ctx)
	}
	identity, kind := j.drv.Identify(ctx)
	if kind != agentjob.Ready {
		return j.unready(ctx)
	}
	j.identity = identity
	if err := j.endPreviousRun(ctx); err != nil {
		// It may still hold its reservation, or cost more: no second run beside it.
		return failedStep("the previous run is not settled", err)
	}

	if !knownType(j.run.ReviewType) {
		return skip(reasonNotConfigured)
	}
	rv, err := j.review(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return skip(reasonDisabled)
	}
	if err != nil {
		return failedStep("load the review", err)
	}
	if !rv.Enabled && !j.run.Manual {
		return skip(reasonDisabled)
	}
	now := j.r.now()
	if j.run.StartedAt == nil { // no run was ever started for it, whatever became of its attempts
		if now.Sub(j.run.ScheduledFor) > j.r.MaxLate {
			return skip(reasonMissed)
		}
		started, err := agentjob.Within(ctx, func(ctx context.Context) (int, error) {
			return j.r.Store.ReviewRunsStartedSince(ctx, now.Add(-24*time.Hour))
		})
		if err != nil {
			return failedStep("count the last day's reviews", err)
		}
		if started >= j.r.PerDay {
			return skip(reasonRateLimited)
		}
	}

	prepared, kind := j.drv.PrepareWith(ctx, identity, roleName)
	switch kind {
	case agentjob.Ready:
	case agentjob.NotConfigured:
		return skip(reasonNotConfigured)
	case agentjob.RoleNotOffered, agentjob.ModelUnavailable, agentjob.AgentUnavailable:
		return j.waitForTheAgent()
	default:
		return j.unready(ctx)
	}
	// A row that had a run is claimed again within the lease of it. One that is claimed later than that would pay for
	// another period's data.
	latest := j.r.MaxLate + runLease(agentrun.RunTimeout(prepared.Catalogue.RunTimeout))
	if j.run.StartedAt != nil && now.Sub(j.run.ScheduledFor) > latest {
		end := failed(reasonInterrupted)
		return &end
	}
	if !prepared.Catalogue.SupportsReview(j.run.ReviewType) {
		return j.waitForTheAgent()
	}
	j.prepared = prepared

	covered, err := agentjob.Within(ctx, func(ctx context.Context) (Covered, error) {
		subject, err := agentjob.ReadOnlySubject(ctx, j.r.Store, identity.ID)
		if err != nil {
			return Covered{}, err
		}
		return Coverage(ctx, j.r.Store, j.r.Scopes, rv, subject)
	})
	if err != nil {
		return failedStep("work out what the identity can see", err)
	}
	if covered.Services == 0 {
		return skip(reasonNotVisible)
	}
	span := reviewsched.Range(cadence(j.run, rv))
	j.start = store.ReviewStart{ScopeLabel: covered.Label, Range: span, CoveredTags: covered.Tags, ServicesTotal: covered.Services}
	j.task = agentclient.Task{
		Kind:   "review",
		Review: &agentclient.TaskReview{Type: j.run.ReviewType, Range: span, Period: periodText(j.run)},
		Scope:  &agentclient.TaskScope{Entries: covered.Entries, ServicesTotal: min(covered.Services, maxTaskServices)},
	}
	j.leave = agentrun.LeaveFor(prepared.Catalogue, prepared.Settings, "chat", "investigator")
	return nil
}

// review loads the definition of the run. One that was deleted is store.ErrNotFound.
func (j *job) review(ctx context.Context) (store.Review, error) {
	if j.run.ReviewID == nil {
		return store.Review{}, store.ErrNotFound
	}
	return agentjob.Within(ctx, func(ctx context.Context) (store.Review, error) {
		return j.r.Store.GetReview(ctx, *j.run.ReviewID)
	})
}

// waitForTheAgent gives the row back while the agent cannot make this review: it is away, or older than the role, the
// type or the model.
func (j *job) waitForTheAgent() *ending {
	end := j.giveBack(reasonUnavailable, retryLater)
	return &end
}

// endPreviousRun ends the run of an earlier holder that did not live to do it. An error means that it may still be
// running.
func (j *job) endPreviousRun(ctx context.Context) error {
	if j.run.RunID == nil {
		return nil
	}
	run := store.AgentRun{ID: *j.run.RunID, Role: roleName, UserID: &j.identity.ID, Provider: j.run.Provider, Model: j.run.Model}
	return j.drv.EndEarlier(ctx, j.identity, run, j.details())
}

// details are what the audit row of a run says besides the run: ids and fixed words only.
func (j *job) details() map[string]any {
	var reviewID any
	if j.run.ReviewID != nil {
		reviewID = *j.run.ReviewID
	}
	return map[string]any{"review_id": reviewID, "review_run_id": j.run.ID, "review_type": j.run.ReviewType}
}

// conclude is what the driver asks, once, of a run that was reserved: what becomes of the review run, and the write
// that does it.
func (j *job) conclude(out agentjob.Outcome) agentjob.Conclusion {
	j.result = out.Result
	j.end = j.safeEndOf(out)
	return agentjob.Conclusion{Word: j.end.word, Save: j.settle}
}

// safeEndOf is endOf, except that a panic in it ends the review run as failed: the run is paid for and is being
// settled, so the claim must not be left running for a second one.
func (j *job) safeEndOf(out agentjob.Outcome) (end ending) {
	defer func() {
		if rec := recover(); rec != nil {
			j.logError("review: panic", agentrun.NewPanicError(rec))
			end = failed(reasonError)
		}
	}()
	return j.endOf(out)
}

// endOf is what the way a run came out means for the review run.
func (j *job) endOf(out agentjob.Outcome) ending {
	switch out.Kind {
	case agentjob.Done:
		return ending{kind: endDone, word: "done"}
	case agentjob.Budget:
		return skipped(reasonBudget)
	case agentjob.NotConfigured:
		return skipped(reasonNotConfigured)
	case agentjob.RoleNotOffered, agentjob.ModelUnavailable:
		return *j.waitForTheAgent()
	case agentjob.LimitReached:
		return failed(reasonLimitReached)
	case agentjob.NoResult:
		return failed(reasonNoResult)
	case agentjob.AgentBusy:
		return j.giveBack("agent_busy", retryBusy)
	case agentjob.AgentUnavailable:
		if out.Refused { // the request is wrong, and would be again
			return failed(reasonError)
		}
		return j.retryOrFail(reasonUnavailable)
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
	if out.Called || out.Panicked || errors.Is(j.startErr, store.ErrInvestigationText) {
		return failed(reasonError)
	}
	return j.retryOrFail(reasonError) // a set-up step failed before the agent was called
}

// finish ends the job once; later calls do nothing. A run that was reserved has been settled by the driver, with the
// write below as its save; otherwise the claim is ended here. Every step runs on a context that the end of ctx does
// not cut off.
func (j *job) finish(ctx context.Context) {
	j.once.Do(func() {
		if j.outcome != nil && j.outcome.Finished {
			if j.outcome.SaveErr != nil {
				j.endUnsaved(ctx)
				return
			}
		} else if err := agentjob.Step(ctx, j.settle); err != nil {
			j.logError("review: could not end the review run", err)
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

// endUnsaved tries once more to end the row of a run that was settled without it, as failed: a row left running would
// be claimed again and paid for again. A row that another holder has, or that was ended already, is left alone.
func (j *job) endUnsaved(ctx context.Context) {
	if errors.Is(j.outcome.SaveErr, store.ErrConflict) {
		return
	}
	err := agentjob.Step(ctx, func(ctx context.Context) error {
		return j.r.Store.FinishReviewRun(ctx, j.claim, store.InvestigationFailed, reasonError, nil, nil, j.r.now())
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		j.logError("review: could not end the review run", err)
	}
}

// settle writes what becomes of the review run, fenced by the claim.
func (j *job) settle(ctx context.Context) error {
	st, now := j.r.Store, j.r.now()
	switch j.end.kind {
	case endDone:
		return j.storeResult(ctx)
	case endSkipped:
		return st.SkipReviewRun(ctx, j.claim, j.end.reason, now)
	case endReleased:
		var retryAt time.Time
		if j.end.retryIn > 0 {
			retryAt = now.Add(j.end.retryIn)
		}
		return st.ReleaseReviewRun(ctx, j.claim, retryAt, j.end.counted)
	case endLost:
		return nil
	default:
		return st.FinishReviewRun(ctx, j.claim, store.InvestigationFailed, j.end.reason, nil, nil, now)
	}
}

// storeResult checks the agent's result against what the identity may View now and stores it. The result is paid for,
// so one bad write is tried again. One that cannot be stored fails the review run rather than leaving it running. Each
// of the three has its own time, so that one that hangs leaves some for the next.
func (j *job) storeResult(ctx context.Context) error {
	var err error
	for range 2 {
		err = j.inTime(ctx, j.storeOnce)
		if err == nil || errors.Is(err, store.ErrConflict) {
			return err // a conflict: another holder has it, and the result is discarded
		}
		if errors.Is(err, store.ErrInvestigationText) {
			break // it would be refused again
		}
	}
	return errors.Join(err, j.inTime(ctx, func(ctx context.Context) error {
		return j.r.Store.FinishReviewRun(ctx, j.claim, store.InvestigationFailed, reasonError, nil, nil, j.r.now())
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
	accepted := agentjob.Accept(*j.result, see.Can, agentjob.AcceptOptions{Replacement: agentresult.ReplacementReviewSummary})
	if err := see.Err(); err != nil {
		return err
	}
	return j.r.Store.FinishReviewRun(ctx, j.claim, store.InvestigationDone, "", &accepted, see.ViewTags(), j.r.now())
}

var weekKey = regexp.MustCompile(`^w:(\d{4})-W(\d{2})$`)

// periodText is the period as the agent is told it: made of the period key, or of the time of a manual run, and never
// of text that a person typed. It holds letters, digits, spaces, commas and colons only.
func periodText(run store.ReviewRun) string {
	if day, err := time.Parse("d:2006-01-02", run.PeriodKey); err == nil {
		return day.Format("2 Jan 2006")
	}
	if month, err := time.Parse("m:2006-01", run.PeriodKey); err == nil {
		return month.Format("January 2006")
	}
	if m := weekKey.FindStringSubmatch(run.PeriodKey); m != nil {
		if week, err := strconv.Atoi(m[2]); err == nil && week >= 1 && week <= 53 {
			return fmt.Sprintf("Week %d, %s", week, m[1])
		}
	}
	at := run.ScheduledFor.UTC()
	if strings.HasPrefix(run.PeriodKey, "x:") {
		return at.Format("Run on 2 Jan 2006, 15:04 UTC")
	}
	return at.Format("2 Jan 2006")
}

// cadence is the cadence a run reviews: that of its period, or of the review for a manual run.
func cadence(run store.ReviewRun, rv store.Review) string {
	kind, _, _ := strings.Cut(run.PeriodKey, ":")
	switch kind {
	case "d":
		return reviewsched.Daily
	case "w":
		return reviewsched.Weekly
	case "m":
		return reviewsched.Monthly
	}
	return rv.Cadence
}
