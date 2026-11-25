package notify

import (
	"context"
	"errors"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInvestigateTimeout  = 5 * time.Second
	defaultInvestigationWindow = 15 * time.Minute
	requestLimit               = 200 // incidents one cycle may link to investigations
	diagnosisPlanLimit         = 100 // diagnosis rows one cycle may plan
	reviewPlanLimit            = 20  // review runs one cycle may plan; a run has at most 5 routes
	// followUpWorkers is how many diagnosis and review rows this process works on at once, over every cycle. A worker
	// can hold a database connection while its lookup waits, and the coordinator's pool is small.
	followUpWorkers = 2

	noteInvestigateOff     = "Investigate with AI is off."
	notePagerDutyNoMessage = "PagerDuty has no diagnosis message."
	noteInvestigationGone  = "The investigation is no longer available."
	notePagerDutyNoReview  = "PagerDuty has no review message."
	noteReviewGone         = "The review result is no longer available."
	noteRouteOffReview     = "This route is no longer on the review."
	noteFollowUpPanicked   = "The message could not be prepared."
)

func (d *Dispatcher) investigateTimeout() time.Duration {
	if d.InvestigateTimeout > 0 {
		return d.InvestigateTimeout
	}
	return defaultInvestigateTimeout
}

func (d *Dispatcher) investigationWindow() time.Duration {
	if d.InvestigationWindow > 0 {
		return d.InvestigationWindow
	}
	return defaultInvestigationWindow
}

// InvestigationErrors counts the errors and panics of the end-of-cycle investigation and review step since the process
// started.
func (d *Dispatcher) InvestigationErrors() int64 { return d.investigationErrors.Load() }

func (d *Dispatcher) warnInvestigation(what string, err any) {
	count := d.investigationErrors.Add(1)
	d.log().Warn("notify: investigation step", "step", what, "err", err, "count", count)
}

// afterSend is the last step of a cycle, after sendDue started the alert sends. It records investigation requests,
// plans diagnosis rows and starts their sends (Investigate), then does the same for review rows (Reviews). Nothing in
// it is allowed to touch the alert path: every statement runs under a timeout, a panic is recovered, every error is
// logged and counted and none is returned, and nothing in it waits for a hook or a send. The worst it can do to the
// next alert message is start the next cycle two timeouts later. A step that fails does not stop the ones after it:
// rows planned by an earlier cycle or another task may be due. A cancelled ctx is a shutdown, not an error.
func (d *Dispatcher) afterSend(ctx context.Context, now time.Time, names map[string]string) {
	defer func() {
		if r := recover(); r != nil {
			count := d.investigationErrors.Add(1)
			d.log().Error("notify: investigation step panicked", "panic", r, "count", count)
		}
	}()

	if d.Investigate {
		recordCtx, cancelRecord := context.WithTimeout(ctx, d.investigateTimeout())
		created, _, err := d.Store.RecordInvestigationRequests(recordCtx, now, d.investigationWindow(), requestLimit)
		cancelRecord()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			d.warnInvestigation("record requests", err)
		} else if created > 0 {
			d.kickJob()
		}
	}

	// Everything below shares the second timeout.
	deadline := time.Now().Add(d.investigateTimeout())
	var free int
	var exclude []int64
	if d.Investigate {
		free, exclude = d.diagnosisStep(ctx, now, names)
	} else {
		free, exclude = d.freeFollowUps()
	}
	if d.Reviews {
		d.reviewStep(ctx, deadline, now, names, free, exclude)
	}
}

// freeFollowUps is how many follow-up rows may be claimed now, and the routes none may be claimed for.
func (d *Dispatcher) freeFollowUps() (free int, exclude []int64) {
	return followUpWorkers - d.runningFollowUps(), append(d.busyFollowUpRoutes(), d.busyRoutes()...)
}

// diagnosisStep plans diagnosis rows and starts the due ones. Planning and claiming have half the timeout each, so a
// slow plan leaves the claim its own time. It returns what it leaves to the rows that come after a diagnosis: the
// workers and routes as they were before its claim, less what it took. A worker or route freed since then is not
// given away, because another diagnosis may be waiting for it, and nothing is left when the claim failed.
func (d *Dispatcher) diagnosisStep(ctx context.Context, now time.Time, names map[string]string) (free int, exclude []int64) {
	planCtx, cancelPlan := context.WithTimeout(ctx, d.investigateTimeout()/2)
	_, err := d.Store.PlanDiagnosisDeliveries(planCtx, now, heldAt(now), diagnosisPlanLimit)
	cancelPlan()
	if ctx.Err() != nil {
		return 0, nil
	}
	if err != nil {
		d.warnInvestigation("plan diagnosis", err)
	}

	free, exclude = d.freeFollowUps()
	if free <= 0 {
		return 0, nil
	}
	claimCtx, cancelClaim := context.WithTimeout(ctx, d.investigateTimeout()/2)
	rows, err := d.Store.ClaimDiagnosisDeliveries(claimCtx, now, d.lease(), store.ClaimFilter{Limit: free, PerRoute: 1, Exclude: exclude})
	cancelClaim()
	if ctx.Err() != nil {
		return 0, nil
	}
	if err != nil {
		d.warnInvestigation("claim diagnosis", err)
		return 0, nil
	}
	d.sendFollowUps(ctx, rows, names)
	for _, row := range rows {
		if row.RouteID != nil {
			exclude = append(exclude, *row.RouteID)
		}
	}
	return free - len(rows), exclude
}

// reviewStep plans review rows and starts the due ones with the workers and routes the diagnosis rows left. It has no
// timeout of its own: it uses what is left until deadline, half of it to plan, so a slow plan leaves the claim its own
// time. Planning needs no worker, so a run is planned while the workers are busy.
func (d *Dispatcher) reviewStep(ctx context.Context, deadline, now time.Time, names map[string]string, free int, exclude []int64) {
	stepCtx, cancelStep := context.WithDeadline(ctx, deadline)
	defer cancelStep()
	planCtx, cancelPlan := context.WithTimeout(stepCtx, time.Until(deadline)/2)
	_, err := d.Store.PlanReviewDeliveries(planCtx, now, heldAt(now), reviewPlanLimit)
	cancelPlan()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		d.warnInvestigation("plan reviews", err)
	}
	if free <= 0 {
		return
	}
	rows, err := d.Store.ClaimReviewDeliveries(stepCtx, now, d.lease(), store.ClaimFilter{Limit: free, PerRoute: 1, Exclude: exclude})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		d.warnInvestigation("claim reviews", err)
		return
	}
	d.sendFollowUps(ctx, rows, names)
}

// reviewOf reads what a review message to the route reports, or a note saying why it is not sent: the run or its
// result is gone, or its review is gone or no longer sends to the route. Both lookups share one timeout.
func (d *Dispatcher) reviewOf(ctx context.Context, runID, routeID int64) (review *Review, note string, err error) {
	lookupCtx, cancel := context.WithTimeout(ctx, d.investigateTimeout())
	defer cancel()
	run, err := d.Store.GetReviewRun(lookupCtx, runID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (run.State != "done" || run.Summary == "") {
		return nil, noteReviewGone, nil
	}
	if err != nil {
		return nil, "", err
	}
	onReview := false
	if run.ReviewID != nil {
		if onReview, err = d.Store.ReviewHasRoute(lookupCtx, *run.ReviewID, routeID); err != nil {
			return nil, "", err
		}
	}
	if !onReview {
		return nil, noteRouteOffReview, nil
	}
	return &Review{RunID: run.ID, Name: run.ReviewName, Type: run.ReviewType, PeriodLabel: run.PeriodLabel, Summary: run.Summary}, "", nil
}

// heldAt is HeldUntil at a fixed time.
func heldAt(now time.Time) func(r store.NotificationRoute, severity string) (time.Time, bool) {
	return func(r store.NotificationRoute, severity string) (time.Time, bool) { return HeldUntil(r, severity, now) }
}

// kickJob wakes the investigator job and never waits for it: the hook runs on its own goroutine, one at a time. While
// a call has not returned, further ones are skipped, so a hook that is stuck costs one goroutine and no cycle time;
// the job finds the requests it was not told about on its next run.
func (d *Dispatcher) kickJob() {
	if d.OnInvestigationRequested == nil {
		return
	}
	if !d.hookRunning.CompareAndSwap(false, true) {
		d.warnInvestigation("job hook", "the previous call has not returned, skipped")
		return
	}
	go func() {
		defer d.hookRunning.Store(false)
		defer func() {
			if r := recover(); r != nil {
				d.warnInvestigation("job hook", r)
			}
		}()
		d.OnInvestigationRequested()
	}()
}

// runningFollowUps is how many claimed diagnosis and review rows are being worked on, rows without a route included.
func (d *Dispatcher) runningFollowUps() int {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	return d.followUpRunning
}

func (d *Dispatcher) addRunningFollowUp(n int) {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	d.followUpRunning += n
}

// busyFollowUpRoutes lists the routes whose previous diagnosis or review send is still running.
func (d *Dispatcher) busyFollowUpRoutes() []int64 {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	out := make([]int64, 0, len(d.followUpBusy))
	for id := range d.followUpBusy {
		out = append(out, id)
	}
	return out
}

func (d *Dispatcher) setFollowUpBusy(id int64, busy bool) {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	if d.followUpBusy == nil {
		d.followUpBusy = map[int64]bool{}
	}
	if busy {
		d.followUpBusy[id] = true
	} else {
		delete(d.followUpBusy, id)
	}
}

// sendFollowUps starts one worker per claimed diagnosis or review row. These workers share one busy set, apart from the
// alert path's, so a destination that hangs on one of them never holds back an alert row of the same route. A panic in
// a worker is contained and fails its row, so a row that cannot be rendered is not sent again and again.
func (d *Dispatcher) sendFollowUps(ctx context.Context, rows []store.NotificationDelivery, names map[string]string) {
	for _, row := range rows {
		routeID := int64(0)
		if row.RouteID != nil {
			routeID = *row.RouteID
			d.setFollowUpBusy(routeID, true)
		}
		d.addRunningFollowUp(1)
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			defer d.addRunningFollowUp(-1)
			if routeID != 0 {
				defer d.setFollowUpBusy(routeID, false)
			}
			defer func() {
				if r := recover(); r != nil {
					count := d.investigationErrors.Add(1)
					d.log().Error("notify: follow-up worker panicked", "event", row.Event, "delivery", row.ID, "panic", r, "count", count)
					d.failPanickedFollowUp(ctx, row)
				}
			}()
			d.work(ctx, []store.NotificationDelivery{row}, names)
		}()
	}
}

// failPanickedFollowUp finishes a row whose send panicked. The write is detached from ctx so it also happens during a
// shutdown; if it fails, the lease expires and the row is claimed again.
func (d *Dispatcher) failPanickedFollowUp(ctx context.Context, row store.NotificationDelivery) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := d.Store.FinishNotificationDelivery(writeCtx, row.ID, row.ClaimedUntil, row.Attempts+1,
		store.DeliveryOutcome{Note: noteFollowUpPanicked}, d.now()); err != nil {
		d.warnInvestigation("fail panicked message", err)
	}
}
