package offhours

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInterval = time.Minute
	// blockedRetry is how long a service blocked by access_denied is left alone before the next attempt.
	blockedRetry = time.Hour
	// lastErrorLimit caps last_error so a long AWS message cannot bloat the row.
	lastErrorLimit = 500
	// defaultServiceTimeout bounds one service's AWS calls and writes.
	defaultServiceTimeout = 30 * time.Second
	// recordTimeout bounds recording a failure; it has its own budget so an expired service deadline cannot
	// stop the row from being kept.
	recordTimeout = 5 * time.Second
)

// Reconciler keeps AWS scheduled actions and capacity in line with the off-hours schedules. It runs only in the
// poller leader. Each service is handled in its own transaction that holds the enrolment lock for the whole call,
// so an API wake or sleep for the same service waits instead of racing it.
//
// Note on tests and the demo: the fake never runs cron or at() actions, so the enforcement step (D3) is what
// makes a fake service fall asleep and wake.
type Reconciler struct {
	Applier  *Applier
	Store    *store.Store
	Interval time.Duration
	Now      func() time.Time
	Log      *slog.Logger
	// ServiceTimeout bounds the work on one service; zero means 30 s.
	ServiceTimeout time.Duration

	kickOnce sync.Once
	kickCh   chan struct{}

	mu sync.Mutex
	// attention holds the services whose "needs attention" reason is already in the operation log, so it is
	// written when the condition first appears and not every cycle.
	attention map[model.ServiceKey]bool
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Reconciler) kicks() chan struct{} {
	r.kickOnce.Do(func() { r.kickCh = make(chan struct{}, 1) })
	return r.kickCh
}

// Kick wakes the loop so a change does not wait for the next interval. It never blocks. It wakes only the loop in
// this process: Run runs only in the poller leader, so on another task the change waits at most Interval.
func (r *Reconciler) Kick() {
	select {
	case r.kicks() <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run runs a cycle at once and then every Interval or Kick, until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Cycle(ctx); err != nil && ctx.Err() == nil {
			r.log().Error("offhours: cycle", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.kicks():
		}
	}
}

// candidate is a service that an enabled schedule selects.
type candidate struct {
	schedule store.OffHoursSchedule
	match    store.OffHoursMatch
}

// Cycle reconciles every enrolled and every selected service once. A failure on one service is logged and does not
// stop the others; only a failure to read the shared state returns an error.
func (r *Reconciler) Cycle(ctx context.Context) error {
	accounts, err := r.Store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	enabledAccounts := map[string]bool{}
	for _, account := range accounts {
		if account.Enabled {
			enabledAccounts[account.AccountID] = true
		}
	}
	schedules, err := r.Store.ListOffHoursSchedules(ctx)
	if err != nil {
		return err
	}
	scheduleByID := map[int64]store.OffHoursSchedule{}
	for _, sc := range schedules {
		scheduleByID[sc.ID] = sc
	}
	enrolments, err := r.Store.ListOffHoursEnrolments(ctx)
	if err != nil {
		return err
	}
	enrolled := map[model.ServiceKey]store.OffHoursEnrolment{}
	for _, e := range enrolments {
		enrolled[e.Key] = e
	}
	members, err := r.members(ctx, schedules, enrolled, enabledAccounts)
	if err != nil {
		return err // without the full membership nothing may be released
	}

	attentionNow := map[model.ServiceKey]bool{}
	for _, e := range enrolments {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !enabledAccounts[e.Key.AccountID] {
			continue
		}
		var member *candidate
		if c, ok := members[e.Key]; ok {
			member = &c
		}
		r.handle(ctx, r.reconcileEnrolled(ctx, e.Key, member, scheduleByID), e.Key, attentionNow)
	}
	for _, key := range sortedKeys(members) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, ok := enrolled[key]; ok {
			continue
		}
		r.handle(ctx, r.enrolNew(ctx, members[key]), key, attentionNow)
	}
	r.pruneAttention(attentionNow)
	return nil
}

// members resolves which schedule each selected service belongs to. A service that matches several enabled
// schedules stays on the one it is enrolled in, else goes to the lowest id (D2).
func (r *Reconciler) members(ctx context.Context, schedules []store.OffHoursSchedule, enrolled map[model.ServiceKey]store.OffHoursEnrolment, enabledAccounts map[string]bool) (map[model.ServiceKey]candidate, error) {
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ID < schedules[j].ID })
	out := map[model.ServiceKey]candidate{}
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		matches, err := r.Store.OffHoursMatches(ctx, sc.TagKey, sc.TagValue)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if !enabledAccounts[m.Key.AccountID] {
				continue
			}
			if _, taken := out[m.Key]; taken {
				e, isEnrolled := enrolled[m.Key]
				if !isEnrolled || e.ScheduleID == nil || *e.ScheduleID != sc.ID {
					continue // the lower id (or the schedule it is enrolled in) already holds it
				}
			}
			out[m.Key] = candidate{schedule: sc, match: m}
		}
	}
	return out, nil
}

func sortedKeys(members map[model.ServiceKey]candidate) []model.ServiceKey {
	keys := make([]model.ServiceKey, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		if a.Cluster != b.Cluster {
			return a.Cluster < b.Cluster
		}
		return a.Service < b.Service
	})
	return keys
}

// handle logs the outcome of one service. Refusals that are expected are not errors.
func (r *Reconciler) handle(ctx context.Context, err error, key model.ServiceKey, attentionNow map[model.ServiceKey]bool) {
	switch {
	case err == nil:
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		r.log().Debug("offhours: stopped while reconciling", "service", key.Service, "cluster", key.Cluster)
	case errors.Is(err, ErrNeedsAttention):
		attentionNow[key] = true
		r.noteAttention(context.Background(), key)
	case errors.Is(err, ErrZeroCapacity), errors.Is(err, ErrMinZero), errors.Is(err, ErrServiceGone):
		r.log().Debug("offhours: service not managed", "service", key.Service, "cluster", key.Cluster, "reason", err)
	case errors.Is(err, context.DeadlineExceeded):
		r.log().Warn("offhours: service timed out, retrying next cycle", "service", key.Service, "cluster", key.Cluster, "err", err)
	case errors.Is(err, ErrAccessDenied):
		r.log().Warn("offhours: access denied", "service", key.Service, "cluster", key.Cluster, "err", err)
	case awsx.IsThrottle(err):
		r.log().Info("offhours: throttled, skipping until the next cycle", "service", key.Service, "cluster", key.Cluster)
	default:
		r.log().Error("offhours: reconcile service", "service", key.Service, "cluster", key.Cluster, "err", err)
	}
}

// noteAttention writes the operation-log entry for a service that needs attention, once per appearance.
func (r *Reconciler) noteAttention(ctx context.Context, key model.ServiceKey) {
	r.mu.Lock()
	if r.attention == nil {
		r.attention = map[model.ServiceKey]bool{}
	}
	seen := r.attention[key]
	r.attention[key] = true
	r.mu.Unlock()
	if seen {
		return
	}
	err := r.Store.AppendOperation(ctx, store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
		Source: "harbormaster", Text: ReasonNeedsAttention,
		Details: map[string]any{"op": "offhours", "action": "needs_attention", "schedule_id": nil},
	})
	if err != nil {
		// Forget it so the next cycle tries to write the entry again.
		r.mu.Lock()
		delete(r.attention, key)
		r.mu.Unlock()
		r.log().Error("offhours: log needs attention", "service", key.Service, "err", err)
	}
}

// pruneAttention forgets services whose condition went away, so it is logged again if it returns.
func (r *Reconciler) pruneAttention(current map[model.ServiceKey]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.attention {
		if !current[key] {
			delete(r.attention, key)
		}
	}
}

// inService runs fn in one transaction. If fn fails after the enrolment row exists the transaction is committed
// with last_error (or the blocked state) recorded: the Applier stamps the row before it writes to AWS, so a row
// means AWS may be partly changed and a rollback would lose track of it. The transaction is rolled back only when
// there is no row to keep (a refusal before any write, or a failure before the row was stored).
//
// The work runs on a context detached from ctx with its own deadline: losing leadership or shutting down in the
// middle of a service must not roll back after AWS was already changed. ctx is checked between services only.
func (r *Reconciler) inService(ctx context.Context, key model.ServiceKey, fn func(ctx context.Context, tx store.Tx) error) error {
	base := context.WithoutCancel(ctx)
	timeout := r.ServiceTimeout
	if timeout <= 0 {
		timeout = defaultServiceTimeout
	}
	var failure error
	// The transaction itself (begin, commit) has no deadline: only the work and the failure record do.
	err := r.Store.InTx(base, func(tx store.Tx) error {
		workCtx, cancel := context.WithTimeout(base, timeout)
		fnErr := fn(workCtx, tx)
		cancel()
		if fnErr == nil {
			return nil
		}
		if isRefusal(fnErr) {
			return fnErr
		}
		recordCtx, cancelRecord := context.WithTimeout(base, recordTimeout)
		defer cancelRecord()
		if recordErr := r.recordFailure(recordCtx, tx, key, fnErr); recordErr != nil {
			return fnErr
		}
		failure = fnErr
		return nil
	})
	if err != nil {
		return err
	}
	return failure
}

// isRefusal reports errors that mean nothing was written to AWS or the database.
func isRefusal(err error) bool {
	return errors.Is(err, ErrZeroCapacity) || errors.Is(err, ErrMinZero) || errors.Is(err, ErrNeedsAttention) || errors.Is(err, ErrServiceGone)
}

// recordFailure stores the outcome of a failed attempt on the existing row. It fails when there is no row.
func (r *Reconciler) recordFailure(ctx context.Context, tx store.Tx, key model.ServiceKey, cause error) error {
	e, err := r.Store.LockOffHoursEnrolment(ctx, tx, key)
	if err != nil {
		return err
	}
	now := r.now()
	e.LastCheckedAt = &now
	switch {
	case errors.Is(cause, ErrAccessDenied):
		e.State, e.BlockedReason, e.LastError = "blocked", "access_denied", ""
	case awsx.IsThrottle(cause):
		// A throttle is not a fault of the service; leave last_error alone and try again next cycle.
	default:
		e.LastError = trimError(cause)
	}
	return r.Store.UpsertOffHoursEnrolment(ctx, tx, e)
}

func trimError(err error) string {
	text := err.Error()
	if len(text) > lastErrorLimit {
		return text[:lastErrorLimit]
	}
	return text
}

// retryDue reports whether a blocked service is tried again: an hour after the last attempt, or once its schedule
// was saved after that attempt.
func (r *Reconciler) retryDue(e store.OffHoursEnrolment, sc *store.OffHoursSchedule) bool {
	if e.LastCheckedAt == nil {
		return true
	}
	if r.now().Sub(*e.LastCheckedAt) > blockedRetry {
		return true
	}
	return sc != nil && sc.UpdatedAt.After(*e.LastCheckedAt)
}

// enrolNew enrols a selected service and puts it to sleep at once if the grid says so (D5).
func (r *Reconciler) enrolNew(ctx context.Context, c candidate) error {
	return r.inService(ctx, c.match.Key, func(ctx context.Context, tx store.Tx) error {
		// Serialise new enrolments across leaders: a stale ex-leader and the new leader must not both enrol it.
		if err := r.Store.LockOffHoursService(ctx, tx, c.match.Key); err != nil {
			return err
		}
		if _, err := r.Store.LockOffHoursEnrolment(ctx, tx, c.match.Key); err == nil {
			return nil // enrolled by someone else since the cycle began
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		live, err := r.Applier.ReadLive(ctx, c.match.Key)
		if err != nil {
			return err
		}
		e, err := r.Applier.Enrol(ctx, tx, c.schedule, c.match, live)
		if err != nil {
			return err
		}
		week, loc, err := scheduleClock(c.schedule)
		if err != nil {
			return err
		}
		now := r.now()
		switch {
		case StateAt(week, loc, now, nil).Kind == Asleep:
			if err := r.Applier.Sleep(ctx, tx, &e, nil, c.schedule.Name); err != nil {
				return err
			}
		case live.Max == 0:
			// Recovered from a lost row while asleep and enrolled in awake hours: wake it now, the fresh stamp
			// would otherwise hold the next cycle in grace.
			if err := r.Applier.Wake(ctx, tx, &e, nil, c.schedule.Name); err != nil {
				return err
			}
		}
		return r.finish(ctx, tx, &e, now)
	})
}

// finish stores a successful pass: checked now, no error, not blocked.
func (r *Reconciler) finish(ctx context.Context, tx store.Tx, e *store.OffHoursEnrolment, now time.Time) error {
	e.LastCheckedAt = &now
	e.LastError = ""
	e.State, e.BlockedReason = "active", ""
	return r.Store.UpsertOffHoursEnrolment(ctx, tx, *e)
}

func (r *Reconciler) reconcileEnrolled(ctx context.Context, key model.ServiceKey, member *candidate, schedules map[int64]store.OffHoursSchedule) error {
	return r.inService(ctx, key, func(ctx context.Context, tx store.Tx) error {
		e, err := r.Store.LockOffHoursEnrolment(ctx, tx, key)
		if errors.Is(err, store.ErrNotFound) {
			return nil // released by someone else since the cycle began
		}
		if err != nil {
			return err
		}
		if member == nil {
			return r.release(ctx, tx, e, schedules)
		}
		return r.syncMember(ctx, tx, e, member.schedule)
	})
}

// release restores a service that left its schedule (disabled, deleted, tag removed) or is gone from AWS.
func (r *Reconciler) release(ctx context.Context, tx store.Tx, e store.OffHoursEnrolment, schedules map[int64]store.OffHoursSchedule) error {
	var current *store.OffHoursSchedule
	name := "deleted schedule"
	if e.ScheduleID != nil {
		if sc, ok := schedules[*e.ScheduleID]; ok {
			current, name = &sc, sc.Name
		}
	}
	if e.State == "blocked" && !r.retryDue(e, current) {
		return nil
	}
	live, err := r.Applier.ReadLive(ctx, e.Key)
	if errors.Is(err, ErrServiceGone) {
		return r.forget(ctx, tx, e)
	}
	if err != nil {
		return err
	}
	return r.Applier.Release(ctx, tx, e, live, name)
}

// forget drops the row of a service that no longer exists. Nothing is written to AWS.
func (r *Reconciler) forget(ctx context.Context, tx store.Tx, e store.OffHoursEnrolment) error {
	if err := r.Store.DeleteOffHoursEnrolment(ctx, tx, e.Key); err != nil {
		return err
	}
	return r.Applier.record(ctx, tx, e, nil, "release", "Left off-hours schedule: the service no longer exists.")
}

// syncMember brings one enrolled, still selected service in line with its schedule.
func (r *Reconciler) syncMember(ctx context.Context, tx store.Tx, e store.OffHoursEnrolment, sc store.OffHoursSchedule) error {
	if e.State == "blocked" && !r.retryDue(e, &sc) {
		return nil
	}
	week, loc, err := scheduleClock(sc)
	if err != nil {
		return err
	}
	live, err := r.Applier.ReadLive(ctx, e.Key)
	if errors.Is(err, ErrServiceGone) {
		return r.forget(ctx, tx, e)
	}
	if err != nil {
		return err
	}
	now := r.now()

	// Local changes first, stored before any AWS write so a failure does not lose them.
	changed := false
	if e.ScheduleID == nil || *e.ScheduleID != sc.ID {
		id := sc.ID // a move to another schedule
		e.ScheduleID = &id
		changed = true
	}
	if e.WokenUntil != nil && !now.Before(*e.WokenUntil) {
		e.WokenUntil, e.WokenBy = nil, nil // the window is over; SyncActions deletes its action
		changed = true
	}
	inGrace := InGrace(week, loc, now, e.LastAppliedAt)
	if !inGrace && live.HasTarget && live.Max > 0 && live.Min > 0 && (live.Min != e.SavedMin || live.Max != e.SavedMax) {
		e.SavedMin, e.SavedMax = live.Min, live.Max
		if err := r.Applier.record(ctx, tx, e, nil, "adopt",
			fmt.Sprintf("Saved capacity updated to min %d, max %d: changed outside Harbormaster.", live.Min, live.Max)); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if err := r.Store.UpsertOffHoursEnrolment(ctx, tx, e); err != nil {
			return err
		}
	}

	state := StateAt(week, loc, now, e.WokenUntil)
	expectAwake := state.Kind != Asleep
	if !live.HasTarget {
		// Someone removed the target, or Enrol stopped before it registered one. Scheduled actions need a target.
		e.RegisteredTarget = true // Harbormaster owns the target from now on
		min, max := 0, 0
		if expectAwake {
			min, max = e.SavedMin, e.SavedMax
		}
		if err := r.Applier.stamp(ctx, tx, &e); err != nil {
			return err
		}
		if err := r.Applier.registerRange(ctx, e.Key, min, max); err != nil {
			return err
		}
		if err := r.Applier.record(ctx, tx, e, nil, "reregister",
			fmt.Sprintf("Scalable target was missing; re-registered with min %d, max %d.", min, max)); err != nil {
			return err
		}
		live.HasTarget, live.Min, live.Max = true, min, max
	}

	if err := r.Applier.SyncActions(ctx, e, sc, live); err != nil {
		return err
	}

	// A woken window overrides the grace period when AWS is at 0/0: the at() put or the wake failed half way.
	switch {
	case state.Kind == Woken && live.Max == 0:
		err = r.Applier.Wake(ctx, tx, &e, nil, sc.Name)
		live.Min, live.Max = e.SavedMin, e.SavedMax
	case inGrace:
	case expectAwake && live.Max == 0:
		err = r.Applier.Wake(ctx, tx, &e, nil, sc.Name)
		live.Min, live.Max = e.SavedMin, e.SavedMax
	case !expectAwake && live.Max > 0:
		err = r.Applier.Sleep(ctx, tx, &e, nil, sc.Name)
		live.Min, live.Max = 0, 0
	}
	if err != nil {
		return err
	}
	if expectAwake && live.Desired > 0 {
		e.AwakeTasks = live.Desired
	}
	return r.finish(ctx, tx, &e, now)
}
