package notify

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInvestigateTimeout  = 5 * time.Second
	defaultInvestigationWindow = 15 * time.Minute
	requestLimit               = 200 // incidents one cycle may link to investigations
	diagnosisPlanLimit         = 100 // diagnosis rows one cycle may plan
	// diagnosisWorkers is how many diagnosis rows this process works on at once, over every cycle. A worker can hold a
	// database connection while its lookup waits, and the coordinator's pool is small.
	diagnosisWorkers = 2

	noteInvestigateOff     = "Investigate with AI is off."
	notePagerDutyNoMessage = "PagerDuty has no diagnosis message."
	noteInvestigationGone  = "The investigation is no longer available."
	noteDiagnosisPanicked  = "The message could not be prepared."
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

// InvestigationErrors counts the errors and panics of the end-of-cycle investigation step since the process started.
func (d *Dispatcher) InvestigationErrors() int64 { return d.investigationErrors.Load() }

func (d *Dispatcher) warnInvestigation(what string, err any) {
	count := d.investigationErrors.Add(1)
	d.registry().CountInvestigationStepError()
	d.log().Warn("notify: investigation step", "step", what, "err", err, "count", count)
}

// afterSend is the last step of a cycle, after sendDue started the alert sends. It records investigation requests,
// plans diagnosis rows and starts their sends. Nothing in it is allowed to touch the alert path: every statement runs
// under a timeout, a panic is recovered, every error is logged and counted and none is returned, and nothing in it
// waits for a hook or a send. The worst it can do to the next alert message is start the next cycle two timeouts
// later. A step that fails does not stop the ones after it: rows planned by an earlier cycle or another task may be
// due. A cancelled ctx is a shutdown, not an error.
func (d *Dispatcher) afterSend(ctx context.Context, now time.Time, names map[string]string) {
	defer func() {
		if r := recover(); r != nil {
			count := d.investigationErrors.Add(1)
			d.registry().CountInvestigationStepError()
			d.log().Error("notify: investigation step panicked", "panic", r, "count", count)
		}
	}()

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

	// Planning and claiming share the second timeout, half each, so a slow plan leaves the claim its own time.
	planCtx, cancelPlan := context.WithTimeout(ctx, d.investigateTimeout()/2)
	heldUntil := func(r store.NotificationRoute, severity string) (time.Time, bool) { return HeldUntil(r, severity, now) }
	_, err = d.Store.PlanDiagnosisDeliveries(planCtx, now, d.investigationWindow(), heldUntil, diagnosisPlanLimit)
	cancelPlan()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		d.warnInvestigation("plan diagnosis", err)
	}

	free := diagnosisWorkers - d.runningDiagnoses()
	if free <= 0 {
		return
	}
	claimCtx, cancelClaim := context.WithTimeout(ctx, d.investigateTimeout()/2)
	rows, err := d.Store.ClaimDiagnosisDeliveries(claimCtx, now, d.lease(), store.ClaimFilter{
		Limit: free, PerRoute: 1, Exclude: append(d.busyDiagnosisRoutes(), d.busyRoutes()...)})
	cancelClaim()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		d.warnInvestigation("claim diagnosis", err)
		return
	}
	d.sendDiagnoses(ctx, rows, names)
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

// runningDiagnoses is how many claimed diagnosis rows are being worked on, rows without a route included.
func (d *Dispatcher) runningDiagnoses() int {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	return d.diagnosisRunning
}

func (d *Dispatcher) addRunningDiagnosis(n int) {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	d.diagnosisRunning += n
}

// busyDiagnosisRoutes lists the routes whose previous diagnosis send is still running.
func (d *Dispatcher) busyDiagnosisRoutes() []int64 {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	out := make([]int64, 0, len(d.diagnosisBusy))
	for id := range d.diagnosisBusy {
		out = append(out, id)
	}
	return out
}

func (d *Dispatcher) setDiagnosisBusy(id int64, busy bool) {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	if d.diagnosisBusy == nil {
		d.diagnosisBusy = map[int64]bool{}
	}
	if busy {
		d.diagnosisBusy[id] = true
	} else {
		delete(d.diagnosisBusy, id)
	}
}

// sendDiagnoses starts one worker per claimed row. A route's diagnosis worker has its own busy set, apart from the
// alert path's, so a destination that hangs on a diagnosis never holds back an alert row of the same route. A panic in
// a worker is contained and fails its row, so a row that cannot be rendered is not sent again and again.
func (d *Dispatcher) sendDiagnoses(ctx context.Context, rows []store.NotificationDelivery, names map[string]string) {
	for _, row := range rows {
		routeID := int64(0)
		if row.RouteID != nil {
			routeID = *row.RouteID
			d.setDiagnosisBusy(routeID, true)
		}
		d.addRunningDiagnosis(1)
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			defer d.addRunningDiagnosis(-1)
			if routeID != 0 {
				defer d.setDiagnosisBusy(routeID, false)
			}
			defer func() {
				if r := recover(); r != nil {
					count := d.investigationErrors.Add(1)
					d.registry().CountInvestigationStepError()
					d.log().Error("notify: diagnosis worker panicked", "delivery", row.ID, "panic", r, "count", count)
					d.failPanickedDiagnosis(ctx, row)
				}
			}()
			d.work(ctx, []store.NotificationDelivery{row}, names)
		}()
	}
}

// failPanickedDiagnosis finishes a row whose send panicked. The write is detached from ctx so it also happens during a
// shutdown; if it fails, the lease expires and the row is claimed again.
func (d *Dispatcher) failPanickedDiagnosis(ctx context.Context, row store.NotificationDelivery) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := d.Store.FinishNotificationDelivery(writeCtx, row.ID, row.ClaimedUntil, row.Attempts+1,
		store.DeliveryOutcome{Note: noteDiagnosisPanicked}, d.now()); err != nil {
		d.warnInvestigation("fail panicked diagnosis", err)
	}
}
