package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	resolveGrace           = 2 * time.Minute
	recentlyResolvedWindow = time.Hour
	leaseDuration          = 2 * time.Minute
	claimLimit             = 50
	maxInFlight            = 4
	maxAttempts            = 3
	routePacing            = time.Second
	writeTimeout           = 5 * time.Second
	defaultInterval        = 10 * time.Second

	noteResolvedBeforeSent = "Resolved before it was sent."
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
	// Sleep paces sends to one route; injectable for tests.
	Sleep func(context.Context, time.Duration) error
	// Jitter returns [0, 1); it adds up to 10% to a retry delay. Injectable for tests.
	Jitter func() float64

	kickOnce sync.Once
	kickCh   chan struct{}
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
	if err := d.sendDue(ctx, now, names); err != nil {
		errs = append(errs, err)
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
			Total: subject.Total, Members: subject.Members}
	}
	if event != EventResolved {
		if until, held := HeldUntil(r, subject.Severity, now); held {
			row.Status, row.NextAttemptAt, row.HeldUntil = "held", nil, &until
		}
	}
	_, err := d.Store.PlanNotificationDelivery(ctx, row)
	return err
}

// sendDue claims due deliveries and sends them, at most maxInFlight routes at a time, one route's sends in
// order and at least routePacing apart.
func (d *Dispatcher) sendDue(ctx context.Context, now time.Time, names map[string]string) error {
	rows, err := d.Store.ClaimDueNotificationDeliveries(ctx, now, leaseDuration, claimLimit)
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
	var batches [][]store.NotificationDelivery
	for _, id := range order {
		batches = append(batches, groups[id])
	}
	for _, row := range routeless {
		batches = append(batches, []store.NotificationDelivery{row})
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	slots := make(chan struct{}, maxInFlight)
	for _, batch := range batches {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			for i, row := range batch {
				if i > 0 {
					if err := d.sleep(ctx, routePacing); err != nil {
						return // shutting down; the leases expire and the rows are claimed again
					}
				}
				if err := d.deliver(ctx, row, names); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// deliver sends one claimed row and records the attempt. The write is detached from ctx so a shutdown in the
// middle of a send still records what happened.
func (d *Dispatcher) deliver(ctx context.Context, row store.NotificationDelivery, names map[string]string) error {
	attempts := row.Attempts + 1
	finish := func(o store.DeliveryOutcome) error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		return d.Store.FinishNotificationDelivery(writeCtx, row.ID, attempts, o, d.now())
	}
	fail := func(note string) error {
		d.registry().CountNotification(row.Destination, "failed")
		return finish(store.DeliveryOutcome{Note: note})
	}

	if row.RouteID == nil {
		return fail("The route was deleted.")
	}
	route, err := d.Store.GetNotificationRoute(ctx, *row.RouteID)
	if errors.Is(err, store.ErrNotFound) {
		return fail("The route was deleted.")
	}
	if err != nil {
		return err // the lease expires and the row is claimed again
	}
	if !route.Enabled && row.Event != string(EventTest) {
		return fail("The route is off.")
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
	message := Message{
		Event: Event(row.Event), DeliveryID: row.ID, BaseURL: d.BaseURL, RouteName: row.RouteName,
		Subject: Subject{Key: row.Key, Kind: row.Kind, Severity: row.Severity, Title: row.AlertTitle, Text: row.AlertText, At: row.CreatedAt, AccountName: name,
			Scope: row.Details.Scope, RouteID: *row.RouteID, Threshold: row.Details.Threshold, Failing: row.Details.Failing,
			Total: row.Details.Total, Members: row.Details.Members},
	}
	result := d.Senders.Send(ctx, route, secret, message)

	outcome := store.DeliveryOutcome{StatusCode: result.StatusCode, DurationMS: int(result.Duration.Milliseconds()), Note: result.Note}
	switch {
	case result.Delivered:
		outcome.Delivered = true
		if row.Retries > 0 {
			outcome.Note = fmt.Sprintf("Delivered on retry in %d ms", result.Duration.Milliseconds())
		}
		d.registry().CountNotification(row.Destination, "delivered")
	case result.Retryable && attempts < maxAttempts:
		outcome.Retryable = true
		outcome.RetryAt = d.now().Add(max(d.backoff(attempts), result.RetryAfter))
		d.registry().CountNotification(row.Destination, "retried")
	default:
		if result.Retryable {
			outcome.Note = fmt.Sprintf("%s after %d attempts", result.Note, attempts)
		}
		d.registry().CountNotification(row.Destination, "failed")
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

// TargetLabel is what the delivery log and the route list show as a route's destination. It never includes a URL
// path, a routing key or a token.
func TargetLabel(r store.NotificationRoute) string {
	c := r.Config
	switch r.Destination {
	case DestSlack:
		if c.Label != "" {
			return c.Label
		}
		if c.URLHost != "" {
			return c.URLHost
		}
		return "Slack"
	case DestPagerDuty:
		if c.Label != "" {
			return c.Label
		}
		return "PagerDuty"
	case DestWebhook:
		return c.URLHost
	case DestTelegram:
		if c.Label != "" {
			return c.Label
		}
		return c.ChatID
	case DestEmail:
		return strings.Join(c.Recipients, ", ")
	}
	return ""
}
