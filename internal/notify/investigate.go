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
	diagnosisClaimLimit        = 20  // diagnosis sends one cycle may start: one per route, so this bounds the workers

	noteInvestigateOff     = "Investigate with AI is off."
	notePagerDutyNoMessage = "PagerDuty has no diagnosis message."
	noteInvestigationGone  = "The investigation is no longer available."
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
	d.investigationErrors.Add(1)
	d.log().Warn("notify: investigation step", "step", what, "err", err)
}

// afterSend is the last step of a cycle, after sendDue started the alert sends. It records investigation requests,
// plans diagnosis rows and starts their sends. Nothing in it is allowed to touch the alert path: each statement group
// runs under its own timeout, a panic is recovered, every error is logged and counted and none is returned. The worst
// it can do to the next alert message is start the next cycle two timeouts later.
func (d *Dispatcher) afterSend(ctx context.Context, now time.Time, names map[string]string) {
	defer func() {
		if r := recover(); r != nil {
			d.investigationErrors.Add(1)
			d.log().Error("notify: investigation step panicked", "panic", r)
		}
	}()

	recordCtx, cancelRecord := context.WithTimeout(ctx, d.investigateTimeout())
	created, _, err := d.Store.RecordInvestigationRequests(recordCtx, now, d.investigationWindow(), requestLimit)
	cancelRecord()
	if err != nil {
		d.warnInvestigation("record requests", err)
	} else if created > 0 {
		d.kickJob(ctx)
	}

	planCtx, cancelPlan := context.WithTimeout(ctx, d.investigateTimeout())
	defer cancelPlan()
	heldUntil := func(r store.NotificationRoute, severity string) (time.Time, bool) { return HeldUntil(r, severity, now) }
	if _, err := d.Store.PlanDiagnosisDeliveries(planCtx, now, heldUntil, diagnosisPlanLimit); err != nil {
		d.warnInvestigation("plan diagnosis", err)
	}
	// Claim even when planning failed: rows planned by an earlier cycle or another task may be due.
	rows, err := d.Store.ClaimDiagnosisDeliveries(planCtx, now, d.lease(), store.ClaimFilter{
		Limit: diagnosisClaimLimit, PerRoute: 1, Exclude: d.busyDiagnosisRoutes()})
	if err != nil {
		d.warnInvestigation("claim diagnosis", err)
		return
	}
	d.sendDiagnoses(ctx, rows, names)
}

// kickJob wakes the investigator job. The hook is the job's concern, but a blocked or failing one must not hold the
// cycle: it runs on its own goroutine and the cycle waits for it at most InvestigateTimeout.
func (d *Dispatcher) kickJob(ctx context.Context) {
	if d.OnInvestigationRequested == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				d.warnInvestigation("job hook", r)
			}
		}()
		d.OnInvestigationRequested()
	}()
	timer := time.NewTimer(d.investigateTimeout())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		d.warnInvestigation("job hook", "did not return in time")
	case <-ctx.Done():
	}
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
// a worker is contained: the row's lease expires and the row is claimed again.
func (d *Dispatcher) sendDiagnoses(ctx context.Context, rows []store.NotificationDelivery, names map[string]string) {
	for _, row := range rows {
		routeID := int64(0)
		if row.RouteID != nil {
			routeID = *row.RouteID
			d.setDiagnosisBusy(routeID, true)
		}
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			if routeID != 0 {
				defer d.setDiagnosisBusy(routeID, false)
			}
			defer func() {
				if r := recover(); r != nil {
					d.investigationErrors.Add(1)
					d.log().Error("notify: diagnosis worker panicked", "delivery", row.ID, "panic", r)
				}
			}()
			d.work(ctx, []store.NotificationDelivery{row}, names)
		}()
	}
}
