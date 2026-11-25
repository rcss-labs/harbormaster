package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	resolveGrace           = 2 * time.Minute
	recentlyResolvedWindow = time.Hour
	leaseDuration          = 2 * time.Minute
	claimLimit             = 50
	routeCycleLimit        = 5 // rows one route may send per cycle: with the pacing, a cycle's work for a route stays short
	maxAttempts            = 3
	routePacing            = time.Second
	writeTimeout           = 5 * time.Second
	defaultInterval        = 10 * time.Second

	noteResolvedBeforeSent = "Resolved before it was sent."
	noteRouteOff           = "The route is off."
	noteRouteDeleted       = "The route was deleted."
	noteCovered            = "Already covered by the message before it."
	noteResolvedQuiet      = "Resolved during quiet hours; not sent."
	noteEnterAgain         = "Enter the destination again."
)

// Sender sends one message. *Senders is the real one; tests substitute a recorder.
type Sender interface {
	Send(ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result
}

// Dispatcher turns the alerts table into notifications. It is level-based: each cycle it compares the alerts with
// the persistent incidents, plans one delivery per route x incident x event under a unique key, and sends what is
// due. It keeps no state in memory, so a restart or a failover to another task continues where the last cycle
// stopped. Run it only on the poller leader; a second instance is safe but wasteful (the database arbitrates).
type Dispatcher struct {
	Store   *store.Store
	Senders Sender
	Box     *secretbox.Box // nil when HM_SESSION_KEY is empty
	BaseURL string
	// Tags resolves tag selectors; nil uses Store.
	Tags     TagMatcher
	Interval time.Duration
	// Settle is how long service_failing and service_degraded must stay open before they notify or count (0 = no wait).
	Settle    time.Duration
	Now       func() time.Time
	Log       *slog.Logger
	Telemetry *telemetry.Registry
	// Lease is how long a claimed row is held (leaseDuration by default); sends are cut off before it ends.
	Lease time.Duration
	// Sleep paces sends to one route; injectable for tests.
	Sleep func(context.Context, time.Duration) error
	// Jitter returns [0, 1); it adds up to 10% to a retry delay. Injectable for tests.
	Jitter func() float64
	// Guard is checked right before each send: a holder that lost the coordinator lease while a send was pending must
	// not send. Nil means always allowed. The coordinator sets it while it holds the lease.
	Guard lease.Guard

	// Investigate turns on the end-of-cycle step that records investigation requests and sends diagnosis messages.
	// InvestigationWindow is the dedup window of a request; InvestigateTimeout (default 5 s) bounds the recording, the
	// planning and claiming together, and the lookups of a diagnosis or review send. OnInvestigationRequested is called
	// after a cycle created at least one investigation. It must not block: it runs on its own goroutine, no cycle waits
	// for it, and while one call has not returned no other is started.
	Investigate              bool
	InvestigationWindow      time.Duration
	InvestigateTimeout       time.Duration
	OnInvestigationRequested func()

	// Reviews turns on the planning and sending of review messages in the same end-of-cycle step, after the diagnosis
	// messages. It does not depend on Investigate: either may be off.
	Reviews bool
	// CustomReviews is the operator switch for custom reviews (HM_AGENT_CUSTOM_REVIEWS). While it is off the result of a
	// custom run is neither planned nor sent, a manual one included.
	CustomReviews bool

	kickOnce sync.Once
	kickCh   chan struct{}

	// busy holds the routes whose previous send is still running; workers is the wait group of those sends.
	busyMu  sync.Mutex
	busy    map[int64]bool
	workers sync.WaitGroup

	// followUpBusy is busy for diagnosis and review sends only (guarded by busyMu); the alert path never reads it.
	followUpBusy        map[int64]bool
	followUpRunning     int // claimed diagnosis and review rows being worked on (guarded by busyMu)
	hookRunning         atomic.Bool
	investigationErrors atomic.Int64
}

// Wait blocks until the send workers of earlier cycles are done.
func (d *Dispatcher) Wait() { d.workers.Wait() }

func (d *Dispatcher) lease() time.Duration {
	if d.Lease > 0 {
		return d.Lease
	}
	return leaseDuration
}

// sendBudget is how long one route's sends may run: the lease less a margin for the write that records the last
// attempt, so no row is still being worked on after its lease expired and another task may have taken it.
func (d *Dispatcher) sendBudget() time.Duration {
	lease := d.lease()
	return lease - min(lease/4, 20*time.Second)
}

// budgetFor is how long the send may still run for a claimed batch: until the earliest lease, less the margin.
// It counts from the lease the database granted, not from when the worker started, so time spent between the
// claim and the first send is not added to it.
func (d *Dispatcher) budgetFor(batch []store.NotificationDelivery) time.Duration {
	margin := d.lease() - d.sendBudget()
	now := d.now()
	budget := d.sendBudget()
	for _, row := range batch {
		if row.ClaimedUntil != nil {
			budget = min(budget, row.ClaimedUntil.Sub(now)-margin)
		}
	}
	return max(budget, 0)
}

// busyRoutes lists the routes whose send is still running.
func (d *Dispatcher) busyRoutes() []int64 {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	out := make([]int64, 0, len(d.busy))
	for id := range d.busy {
		out = append(out, id)
	}
	return out
}

func (d *Dispatcher) setBusy(id int64, busy bool) {
	d.busyMu.Lock()
	defer d.busyMu.Unlock()
	if d.busy == nil {
		d.busy = map[int64]bool{}
	}
	if busy {
		d.busy[id] = true
	} else {
		delete(d.busy, id)
	}
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *Dispatcher) tags() TagMatcher {
	if d.Tags != nil {
		return d.Tags
	}
	return d.Store
}

func (d *Dispatcher) registry() *telemetry.Registry {
	if d.Telemetry != nil {
		return d.Telemetry
	}
	return telemetry.Default
}

func (d *Dispatcher) sleep(ctx context.Context, wait time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher) jitter() float64 {
	if d.Jitter != nil {
		return d.Jitter()
	}
	return rand.Float64()
}

func (d *Dispatcher) kicks() chan struct{} {
	d.kickOnce.Do(func() { d.kickCh = make(chan struct{}, 1) })
	return d.kickCh
}

// Kick wakes the loop so a change does not wait for the next interval. It never blocks and reaches only this
// process: Run runs only on the poller leader, so on another task the change waits at most Interval.
func (d *Dispatcher) Kick() {
	select {
	case d.kicks() <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run runs a cycle at once and then every Interval or Kick, until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Cycle(ctx); err != nil && ctx.Err() == nil {
			d.log().Error("notify: cycle", "err", err)
		}
		select {
		case <-ctx.Done():
			d.Wait()
			return
		case <-ticker.C:
		case <-d.kicks():
		}
	}
}

// Cycle runs one pass: sync incidents, plan deliveries, release held ones, send what is due. A failing step does
// not stop the independent ones after it; the errors are returned together.
func (d *Dispatcher) Cycle(ctx context.Context) error {
	now := d.now()
	var errs []error

	names := map[string]string{}
	if accounts, err := d.Store.ListAccounts(ctx); err != nil {
		errs = append(errs, fmt.Errorf("list accounts: %w", err))
	} else {
		for _, a := range accounts {
			names[a.AccountID] = a.Name
		}
	}

	open, resolved, err := d.Store.SyncNotificationIncidents(ctx, now, resolveGrace, recentlyResolvedWindow)
	if err != nil {
		errs = append(errs, err) // nothing to plan from; held and due rows are still handled below
	} else if routes, err := d.Store.ListNotificationRoutes(ctx); err != nil {
		errs = append(errs, err)
	} else if err := d.plan(ctx, now, routes, open, resolved, names); err != nil {
		errs = append(errs, err)
	}

	if err := d.Store.ReleaseHeldNotificationDeliveries(ctx, now, noteResolvedQuiet); err != nil {
		errs = append(errs, err)
	}
	if _, err := d.Store.ExpireStaleNotificationTests(ctx, now); err != nil {
		errs = append(errs, err)
	}
	if err := d.sendDue(ctx, now, names); err != nil {
		errs = append(errs, err)
	}
	if d.Investigate || d.Reviews {
		d.afterSend(ctx, now, names) // never adds to errs: the agent must not change what the cycle reports
	}
	return errors.Join(errs...)
}

func subjectOf(i store.NotificationIncident, names map[string]string) (Subject, bool) {
	severity, ok := SeverityName(i.Severity)
	if !ok {
		return Subject{}, false
	}
	name := names[i.Key.AccountID]
	if name == "" {
		name = i.Key.AccountID
	}
	return Subject{Key: i.Key, Kind: i.Kind, Severity: severity, Title: i.Title, Text: i.Text, At: i.OpenedAt, AccountName: name}, true
}

// plan makes the delivery rows that should exist. It builds the shared plan (BuildPlan, which the preview uses
// too), syncs the group incidents it wants, and plans opened, update, repeat and resolved rows for alert incidents
// and group incidents alike. If any route's scope fails to resolve it plans nothing: a partial picture could send
// an alert to a catch-all that a specific route covers.
func (d *Dispatcher) plan(ctx context.Context, now time.Time, routes []store.NotificationRoute, open, resolved []store.NotificationIncident, names map[string]string) error {
	scopes := map[int64]Scope{}
	needTotals := false
	routeByID := map[int64]store.NotificationRoute{}
	for _, r := range routes {
		routeByID[r.ID] = r
		if !r.Enabled {
			continue
		}
		scope, err := ResolveScope(ctx, d.tags(), r.Scope)
		if err != nil {
			return fmt.Errorf("notification route scope: %w", err)
		}
		scopes[r.ID] = scope
		needTotals = needTotals || len(scope.Groups) > 0
	}
	var totals Totals
	if needTotals {
		targets, err := d.Store.NotificationTargets(ctx)
		if err != nil {
			return err
		}
		totals = TotalsFrom(targets)
	}

	built := BuildPlan(routes, scopes, open, totals, names, now, d.Settle)
	openGroups, resolvedGroups, err := d.Store.SyncNotificationGroups(ctx, now, resolveGrace, recentlyResolvedWindow, built.Groups)
	if err != nil {
		return err
	}

	var ids []int64
	for _, list := range [][]store.NotificationIncident{open, resolved, openGroups, resolvedGroups} {
		for _, i := range list {
			ids = append(ids, i.ID)
		}
	}
	states, err := d.Store.NotificationPlanStates(ctx, ids)
	if err != nil {
		return err
	}

	var errs []error
	for _, incident := range open {
		subject, ok := subjectOf(incident, names)
		if !ok {
			continue
		}
		taking := map[int64]bool{}
		for _, id := range built.PerAlert[incident.ID] {
			taking[id] = true
		}
		// A catch-all that already opened this incident keeps repeating it, even after a specific route started
		// covering it (it never gets a second opened).
		for _, r := range routes {
			if r.Enabled && r.Fallback && states[[2]int64{r.ID, incident.ID}].OpenedStatus != "" && Matches(r, subject, scopes[r.ID]) {
				taking[r.ID] = true
			}
		}
		for _, r := range routes {
			if !taking[r.ID] {
				continue
			}
			state := states[[2]int64{r.ID, incident.ID}]
			switch {
			case state.OpenedStatus == "":
				errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventOpened, 0))
			case state.OpenedStatus == "cancelled":
				errs = append(errs, d.reviveOpened(ctx, now, r, incident, subject))
			case repeatDue(r, state, now):
				errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventRepeat, state.MaxRepeatSeq+1))
			}
		}
	}

	for _, incident := range openGroups {
		r, ok := routeByID[incident.GroupRouteID]
		subject, subjectOK := subjectOf(incident, names)
		if !ok || !r.Enabled || !subjectOK {
			continue
		}
		subject = groupSubject(subject, incident)
		state := states[[2]int64{r.ID, incident.ID}]
		switch {
		case state.OpenedStatus == "":
			errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventOpened, 0))
		case state.OpenedStatus == "cancelled":
			errs = append(errs, d.reviveOpened(ctx, now, r, incident, subject))
		case incident.Generation > state.MaxUpdateSeq:
			errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventUpdate, incident.Generation))
		case repeatDue(r, state, now):
			errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventRepeat, state.MaxRepeatSeq+1))
		}
	}

	var resolvedIDs []int64
	for _, list := range [][]store.NotificationIncident{resolved, resolvedGroups} {
		for _, incident := range list {
			resolvedIDs = append(resolvedIDs, incident.ID)
		}
	}
	if len(resolvedIDs) > 0 {
		if err := d.Store.CancelUnsentNotificationDeliveries(ctx, resolvedIDs, noteResolvedBeforeSent, noteResolvedQuiet, now); err != nil {
			errs = append(errs, err)
		}
	}
	for _, incident := range resolved {
		subject, ok := subjectOf(incident, names)
		if !ok {
			continue
		}
		for _, r := range routes {
			state := states[[2]int64{r.ID, incident.ID}]
			// A disabled route is silent: the admin turned it off, and the send step would refuse it anyway.
			if r.Enabled && r.NotifyResolved && state.OpenedStatus == "delivered" && !state.HasResolved {
				errs = append(errs, d.planDelivery(ctx, now, r, incident, subject, EventResolved, 0))
			}
		}
	}
	for _, incident := range resolvedGroups {
		r, ok := routeByID[incident.GroupRouteID]
		subject, subjectOK := subjectOf(incident, names)
		if !ok || !subjectOK || !hasEntry(r, incident) {
			continue // the route or the entry is gone: it resolves without a message
		}
		state := states[[2]int64{r.ID, incident.ID}]
		if r.Enabled && r.NotifyResolved && state.OpenedStatus == "delivered" && !state.HasResolved {
			errs = append(errs, d.planDelivery(ctx, now, r, incident, groupSubject(subject, incident), EventResolved, 0))
		}
	}
	return errors.Join(errs...)
}

// repeatDue reports whether the route's repeat interval has passed since its last message about the incident.
func repeatDue(r store.NotificationRoute, state store.DeliveryPlanState, now time.Time) bool {
	return r.RepeatSeconds > 0 && (state.OpenedStatus == "delivered" || state.OpenedStatus == "failed") && !state.HasHeld &&
		state.LastSentAt != nil && now.Sub(*state.LastSentAt) >= time.Duration(r.RepeatSeconds)*time.Second
}

// groupSubject adds a group incident's facts to its subject.
func groupSubject(subject Subject, incident store.NotificationIncident) Subject {
	subject.Scope, subject.RouteID = incident.ScopeKind, incident.GroupRouteID
	subject.Threshold, subject.Failing, subject.Total, subject.Members = incident.Threshold, incident.Failing, incident.Total, incident.Members
	return subject
}

// hasEntry reports whether the route still has the cluster or account entry a group incident was opened for.
func hasEntry(r store.NotificationRoute, incident store.NotificationIncident) bool {
	for _, sel := range r.Scope {
		if sel.Kind != incident.ScopeKind || sel.AccountID != incident.Key.AccountID || sel.Region != incident.Key.Region {
			continue
		}
		if sel.Kind != store.NotificationSelectorCluster || sel.Cluster == incident.Key.Cluster {
			return true
		}
	}
	return false
}

// reviveOpened brings back the cancelled opened row of an incident that is still open and that an enabled route
// takes: the route was switched off while the row waited, and the unique key blocks planning another.
func (d *Dispatcher) reviveOpened(ctx context.Context, now time.Time, r store.NotificationRoute, incident store.NotificationIncident, subject Subject) error {
	status, next, heldUntil := "pending", &now, (*time.Time)(nil)
	if until, held := HeldUntil(r, subject.Severity, now); held {
		status, next, heldUntil = "held", nil, &until
	}
	return d.Store.ReviveCancelledOpenedDelivery(ctx, r.ID, incident.ID, status, next, heldUntil)
}

// planDelivery inserts the row; a row that already exists (another replica, a repeated cycle) is a no-op. A
// group's row carries the snapshot its message is rendered from.
func (d *Dispatcher) planDelivery(ctx context.Context, now time.Time, r store.NotificationRoute, incident store.NotificationIncident, subject Subject, event Event, seq int) error {
	routeID, incidentID := r.ID, incident.ID
	row := store.NotificationDelivery{
		RouteID: &routeID, RouteName: r.Name, Destination: r.Destination, Target: TargetLabel(r),
		IncidentID: &incidentID, Event: string(event), Seq: seq, Severity: subject.Severity,
		AlertTitle: incident.Title, AlertText: incident.Text, Key: incident.Key, Kind: incident.Kind,
		Status: "pending", NextAttemptAt: &now, CreatedAt: now,
	}
	if subject.Scope != "" {
		row.Details = store.DeliveryDetails{Scope: subject.Scope, Threshold: subject.Threshold, Failing: subject.Failing,
			Total: subject.Total, Members: subject.Members, Generation: incident.Generation}
	}
	if event != EventResolved {
		if until, held := HeldUntil(r, subject.Severity, now); held {
			row.Status, row.NextAttemptAt, row.HeldUntil = "held", nil, &until
		}
	}
	_, err := d.Store.PlanNotificationDelivery(ctx, row)
	return err
}

// sendDue claims due deliveries and starts one worker per route; it does not wait for them, so a destination that
// hangs delays only its own route. A route is claimed at most routeCycleLimit rows at a time and not at all while
// its previous worker is still running. Tests and shutdown use Wait.
func (d *Dispatcher) sendDue(ctx context.Context, now time.Time, names map[string]string) error {
	rows, err := d.Store.ClaimNotificationDeliveries(ctx, now, d.lease(), store.ClaimFilter{
		Limit: claimLimit, PerRoute: routeCycleLimit, Exclude: d.busyRoutes()})
	if err != nil || len(rows) == 0 {
		return err
	}
	groups := map[int64][]store.NotificationDelivery{}
	var order []int64
	var routeless []store.NotificationDelivery
	for _, row := range rows {
		if row.RouteID == nil {
			routeless = append(routeless, row)
			continue
		}
		if _, seen := groups[*row.RouteID]; !seen {
			order = append(order, *row.RouteID)
		}
		groups[*row.RouteID] = append(groups[*row.RouteID], row)
	}
	for _, id := range order {
		d.setBusy(id, true)
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			defer d.setBusy(id, false)
			d.work(ctx, groups[id], names)
		}()
	}
	for _, row := range routeless {
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			d.work(ctx, []store.NotificationDelivery{row}, names)
		}()
	}
	return nil
}

// work sends one route's claimed rows in order, at least routePacing apart, within the send budget. Rows it did not
// get to, and a send the budget or a shutdown cut off, are given back without an attempt counted.
func (d *Dispatcher) work(ctx context.Context, batch []store.NotificationDelivery, names map[string]string) {
	budget, cancel := context.WithTimeout(ctx, d.budgetFor(batch))
	defer cancel()
	for i, row := range batch {
		if i > 0 {
			if d.sleep(budget, routePacing) != nil {
				d.release(ctx, batch[i:]...)
				return
			}
		}
		if budget.Err() != nil {
			d.release(ctx, batch[i:]...)
			return
		}
		if err := d.deliver(budget, ctx, row, names); err != nil {
			d.log().Error("notify: deliver", "err", err)
			d.release(ctx, row) // a no-op when the row was finished; otherwise it is claimable at once
		}
	}
}

// release gives claimed rows back. The write is detached from ctx so it also happens during a shutdown.
func (d *Dispatcher) release(ctx context.Context, rows ...store.NotificationDelivery) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	for _, row := range rows {
		if err := d.Store.ReleaseNotificationDelivery(writeCtx, row.ID, row.ClaimedUntil); err != nil {
			d.log().Error("notify: release", "err", err)
		}
	}
}

// deliver sends one claimed row and records the attempt. sendCtx bounds the send; the write is detached from
// parent so a shutdown in the middle of a send still records what happened. A send that sendCtx cut off (a
// shutdown, or the budget) is not an attempt and not a failure: the row is given back.
func (d *Dispatcher) deliver(sendCtx, parent context.Context, row store.NotificationDelivery, names map[string]string) error {
	attempts := row.Attempts + 1
	finish := func(o store.DeliveryOutcome) error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), writeTimeout)
		defer cancel()
		return d.Store.FinishNotificationDelivery(writeCtx, row.ID, row.ClaimedUntil, attempts, o, d.now())
	}
	isDiagnosis, isReview := row.Event == string(EventDiagnosis), row.Event == string(EventReview)
	count := func(result string) {
		if !isDiagnosis && !isReview { // the series is about alerts; an AI message that fails must not look like one
			d.registry().CountNotification(row.Destination, result)
		}
	}
	fail := func(note string) error {
		count("failed")
		return finish(store.DeliveryOutcome{Note: note})
	}
	cancelRow := func(note string) error { return finish(store.DeliveryOutcome{Cancelled: true, Note: note}) }

	if row.RouteID == nil {
		return cancelRow(noteRouteDeleted)
	}
	route, err := d.Store.GetNotificationRoute(sendCtx, *row.RouteID)
	if errors.Is(err, store.ErrNotFound) {
		return cancelRow(noteRouteDeleted)
	}
	if err != nil {
		return err // the lease expires and the row is claimed again
	}
	if !route.Enabled && row.Event != string(EventTest) {
		return cancelRow(noteRouteOff)
	}
	if isDiagnosis && !route.Investigate {
		return cancelRow(noteInvestigateOff)
	}
	if isDiagnosis && route.Destination == DestPagerDuty {
		return cancelRow(notePagerDutyNoMessage)
	}
	if isReview && route.Destination == DestPagerDuty {
		return cancelRow(notePagerDutyNoReview)
	}

	// An incident row is rendered from the incident as it is now: an alert that resolved while the row waited (a
	// held row, a manual retry) is not announced, and an aggregate shows its current services, not the plan-time
	// snapshot. An update that an earlier message already covers is not sent.
	subject := Subject{Key: row.Key, Kind: row.Kind, Severity: row.Severity, Title: row.AlertTitle, Text: row.AlertText, At: row.CreatedAt,
		Scope: row.Details.Scope, RouteID: *row.RouteID, Threshold: row.Details.Threshold, Failing: row.Details.Failing,
		Total: row.Details.Total, Members: row.Details.Members}
	rendered := 0
	if row.IncidentID != nil && row.Event != string(EventResolved) && row.Event != string(EventTest) {
		incident, err := d.Store.GetNotificationIncident(sendCtx, *row.IncidentID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			if incident.ResolvedAt != nil {
				return cancelRow(noteResolvedBeforeSent)
			}
			if row.Details.Scope != "" {
				if row.Event == string(EventUpdate) {
					covered, err := d.Store.MaxRenderedGeneration(sendCtx, *row.RouteID, *row.IncidentID)
					if err != nil {
						return err
					}
					if covered >= row.Seq {
						return cancelRow(noteCovered)
					}
				}
				if severity, ok := SeverityName(incident.Severity); ok {
					subject.Severity = severity
				}
				subject.Title, subject.Text = incident.Title, incident.Text
				subject.Threshold, subject.Failing, subject.Total, subject.Members = incident.Threshold, incident.Failing, incident.Total, incident.Members
				rendered = incident.Generation
			}
		}
	}

	var diagnosis *Diagnosis
	if isDiagnosis {
		lookupCtx, cancel := context.WithTimeout(sendCtx, d.investigateTimeout())
		investigation, err := d.Store.GetInvestigation(lookupCtx, row.Details.InvestigationID)
		cancel()
		if errors.Is(err, store.ErrNotFound) || err == nil && (investigation.State != "done" || investigation.Summary == "") {
			return cancelRow(noteInvestigationGone)
		}
		if err != nil {
			return err
		}
		diagnosis = &Diagnosis{InvestigationID: investigation.ID, Summary: investigation.Summary}
	}

	var review *Review
	if isReview {
		var note string
		if review, note, err = d.reviewOf(sendCtx, row.Details.ReviewRunID, route.ID); err != nil {
			return err
		}
		if review == nil {
			return cancelRow(note)
		}
	}

	var secret Secret
	if len(route.SecretEnc) > 0 {
		if d.Box == nil {
			return fail(noteEnterAgain)
		}
		plain, err := d.Box.Open(route.SecretEnc)
		if err != nil || json.Unmarshal(plain, &secret) != nil {
			return fail(noteEnterAgain)
		}
	}

	name := names[row.Key.AccountID]
	if name == "" {
		name = row.Key.AccountID
	}
	subject.AccountName = name
	message := Message{Event: Event(row.Event), DeliveryID: row.ID, BaseURL: d.BaseURL, RouteName: row.RouteName, Subject: subject, Diagnosis: diagnosis, Review: review}
	if d.Investigate && route.Investigate && row.Event == string(EventOpened) && row.IncidentID != nil {
		message.Investigate, message.IncidentID = true, *row.IncidentID
	}
	if d.Guard != nil {
		// The lease may have been lost while this row waited, rendered or slept between sends. The unique delivery key
		// and the claim keep a send from happening twice; this stops a paused holder from sending at all.
		if err := d.Guard(sendCtx); err != nil {
			d.log().Warn("notify: coordinator lease not confirmed, not sending", "delivery", row.ID, "err", err)
			d.release(parent, row)
			return nil
		}
	}
	result := d.Senders.Send(sendCtx, route, secret, message)

	if sendCtx.Err() != nil && !result.Delivered {
		d.release(parent, row)
		return nil
	}
	outcome := store.DeliveryOutcome{StatusCode: result.StatusCode, DurationMS: int(result.Duration.Milliseconds()), Note: result.Note}
	switch {
	case result.Delivered:
		outcome.Delivered = true
		outcome.Generation = rendered
		if row.Retries > 0 {
			outcome.Note = fmt.Sprintf("Delivered on retry in %d ms", result.Duration.Milliseconds())
		}
		count("delivered")
	case result.Retryable && attempts < maxAttempts:
		outcome.Retryable = true
		outcome.RetryAt = d.now().Add(max(d.backoff(attempts), result.RetryAfter))
		count("retried")
	default:
		if result.Retryable {
			outcome.Note = fmt.Sprintf("%s after %d attempts", result.Note, attempts)
		}
		count("failed")
	}
	return finish(outcome)
}

// backoff is the delay before the next attempt: 30 s after the first, 2 minutes after the second, plus up to 10%.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	base := 30 * time.Second
	if attempt >= 2 {
		base = 2 * time.Minute
	}
	return base + time.Duration(float64(base)*0.1*d.jitter())
}

// TargetLabel is what the delivery log and the route list show as a route's destination.
func TargetLabel(r store.NotificationRoute) string { return r.TargetLabel() }
