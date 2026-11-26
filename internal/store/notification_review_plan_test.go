package store_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var reviewPlanNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type reviewPlanFixture struct {
	t *testing.T
	s *store.Store
}

func newReviewPlanFixture(t *testing.T) *reviewPlanFixture {
	t.Helper()
	return &reviewPlanFixture{t: t, s: storetest.Open(t)}
}

func (f *reviewPlanFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.s.Pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

// route is an enabled webhook route.
func (f *reviewPlanFixture) route(name string) store.NotificationRoute {
	f.t.Helper()
	r := newRoute(name)
	r.Enabled = true
	created, err := f.s.CreateNotificationRoute(context.Background(), r, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return created
}

func (f *reviewPlanFixture) review(name string, routes ...store.NotificationRoute) int64 {
	f.t.Helper()
	r := store.Review{Name: name, Type: "cost", Enabled: true, Cadence: "daily", AtMinute: 480, Timezone: "Europe/Berlin"}
	for _, route := range routes {
		r.RouteIDs = append(r.RouteIDs, route.ID)
	}
	created, err := f.s.CreateReview(context.Background(), r, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return created.ID
}

// run inserts a run row of the review (0 = a deleted one) in the given state.
func (f *reviewPlanFixture) run(reviewID int64, name, state, summary string, finished time.Time) int64 {
	f.t.Helper()
	var id int64
	if err := f.s.Pool.QueryRow(context.Background(), `insert into agent_review_runs
		(review_id, review_name, review_type, period_key, period_label, scheduled_for, state, summary, requested_at, finished_at)
		values (nullif($1, 0::bigint), $2, 'cost', 'x:' || gen_random_uuid(), '25 November 2026', $5, $3, $4, $5,
		        case when $3 in ('done', 'failed', 'skipped') then $5::timestamptz end) returning id`,
		reviewID, name, state, summary, finished).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *reviewPlanFixture) done(reviewID int64, name string) int64 {
	f.t.Helper()
	return f.run(reviewID, name, "done", "Spend is flat.", reviewPlanNow.Add(-time.Minute))
}

func (f *reviewPlanFixture) plan() int {
	f.t.Helper()
	n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *reviewPlanFixture) rows() int {
	f.t.Helper()
	return invCount(f.t, f.s, `notification_deliveries where event = 'review'`)
}

func (f *reviewPlanFixture) hasRow(runID int64, route store.NotificationRoute) bool {
	f.t.Helper()
	return invCount(f.t, f.s, fmt.Sprintf(`notification_deliveries where event = 'review' and review_run_id = %d and route_id = %d`, runID, route.ID)) == 1
}

func (f *reviewPlanFixture) plannedAt(runID int64) *time.Time {
	f.t.Helper()
	run, err := f.s.GetReviewRun(context.Background(), runID)
	if err != nil {
		f.t.Fatal(err)
	}
	return run.DeliveryPlannedAt
}

func TestPlanReview_OneRowPerRouteOfTheReview(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, b, other := f.route("a"), f.route("b"), f.route("other")
	first := f.done(f.review("Monthly spend", a, b), "Monthly spend")
	manual := f.done(f.review("By hand", b), "By hand")
	f.exec(`update agent_review_runs set manual = true where id = $1`, manual)

	if n := f.plan(); n != 3 {
		t.Fatalf("planned %d, want 3: two routes of one review and one of the manual run", n)
	}
	if !f.hasRow(first, a) || !f.hasRow(first, b) || !f.hasRow(manual, b) || f.hasRow(manual, a) || f.hasRow(first, other) || f.rows() != 3 {
		t.Fatalf("rows = %d, want exactly one for each route of each review", f.rows())
	}
	for _, run := range []int64{first, manual} {
		if at := f.plannedAt(run); at == nil || !at.Equal(reviewPlanNow) {
			t.Errorf("run %d delivery_planned_at = %v, want the plan time", run, at)
		}
	}
	id := reviewIDOf(t, f.s, a.ID, first)
	got, err := f.s.GetNotificationDelivery(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Event != "review" || got.Severity != "info" || got.Status != "pending" || got.IncidentID != nil || got.NextAttemptAt != nil || got.HeldUntil != nil ||
		got.RouteName != "a" || got.Destination != a.Destination || got.Target != a.TargetLabel() || got.Details.ReviewRunID != first ||
		got.AlertTitle != "Monthly spend" || got.AlertText != "" || got.Kind != "review" || got.Key != (model.ServiceKey{}) || !got.CreatedAt.Equal(reviewPlanNow) {
		t.Fatalf("row = %+v, want the run's review row with no alert key and no text", got)
	}
	if st := readDiagnosis(t, f.s, id); st.Due == nil || !st.Due.Equal(reviewPlanNow) {
		t.Fatalf("due = %v, want the plan time", st.Due)
	}
}

func TestPlanReview_ExactlyOnceAcrossCyclesAndHolders(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, b := f.route("a"), f.route("b")
	run := f.done(f.review("Monthly spend", a, b), "Monthly spend")
	var planned atomic.Int64
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 100)
			if err != nil {
				t.Error(err)
			}
			planned.Add(int64(n))
		})
	}
	wg.Wait()
	if planned.Load() != 2 || f.rows() != 2 {
		t.Fatalf("six concurrent planners planned %d rows (table has %d), want 2 in all", planned.Load(), f.rows())
	}
	if n := f.plan(); n != 0 {
		t.Fatalf("a later cycle planned %d, want 0", n)
	}
	f.exec(`delete from notification_deliveries where review_run_id = $1`, run) // the log's retention
	if n := f.plan(); n != 0 || f.rows() != 0 {
		t.Fatalf("planned %d after retention deleted the rows, want 0", n)
	}
}

// A run another planner is working on is left to it: the step runs on the dispatcher's cycle and must not wait.
func TestPlanReview_ARunHeldByAnotherPlannerIsLeftToIt(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	held, free := f.done(f.review("held", a), "held"), f.done(f.review("free", a), "free")
	tx, err := f.s.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) }) //nolint:errcheck
	if _, err := tx.Exec(context.Background(), `select id from agent_review_runs where id = $1 for update`, held); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if n, err := f.s.PlanReviewDeliveries(ctx, reviewPlanNow, never, true, 100); err != nil || n != 1 || !f.hasRow(free, a) || f.hasRow(held, a) {
		t.Fatalf("planned %d, %v, want only the run nobody holds", n, err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.plan(); n != 1 || !f.hasRow(held, a) {
		t.Fatalf("planned %d once the run was free, want 1", n)
	}
}

func TestPlanReview_SkipsDisabledAndPagerDutyRoutes(t *testing.T) {
	f := newReviewPlanFixture(t)
	on, off, pager := f.route("on"), f.route("off"), f.route("pager")
	run := f.done(f.review("Monthly spend", on, off, pager), "Monthly spend")
	f.exec(`update notification_routes set enabled = false where id = $1`, off.ID)
	f.exec(`update notification_routes set destination = 'pagerduty' where id = $1`, pager.ID)
	if n := f.plan(); n != 1 || !f.hasRow(run, on) || f.rows() != 1 {
		t.Fatalf("planned %d (rows %d), want only the enabled route that is not PagerDuty", n, f.rows())
	}
}

func TestPlanReview_QuietHoursSetTheDueTime(t *testing.T) {
	f := newReviewPlanFixture(t)
	night, day := f.route("night"), f.route("day")
	run := f.done(f.review("Monthly spend", night, day), "Monthly spend")
	until := reviewPlanNow.Add(8 * time.Hour)
	asked := map[string]string{}
	held := func(r store.NotificationRoute, severity string) (time.Time, bool) {
		asked[r.Name] = severity
		return until, r.Name == "night"
	}
	n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, held, true, 100)
	if err != nil || n != 2 || !reflect.DeepEqual(asked, map[string]string{"night": "info", "day": "info"}) {
		t.Fatalf("planned %d, %v, asked about %v, want 2 rows and severity info for both routes", n, err, asked)
	}
	nightRow, dayRow := readDiagnosis(t, f.s, reviewIDOf(t, f.s, night.ID, run)), readDiagnosis(t, f.s, reviewIDOf(t, f.s, day.ID, run))
	if nightRow.Due == nil || !nightRow.Due.Equal(until) || nightRow.HeldUntil == nil || !nightRow.HeldUntil.Equal(until) {
		t.Errorf("night = %+v, want it due and held until the end of quiet hours", nightRow)
	}
	if dayRow.Due == nil || !dayRow.Due.Equal(reviewPlanNow) || dayRow.HeldUntil != nil {
		t.Errorf("day = %+v, want it due now", dayRow)
	}
}

func TestPlanReview_NotForFailedSkippedOrRunningRuns(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	review := f.review("Monthly spend", a)
	var unplanned []int64
	for _, state := range []string{"requested", "running", "failed", "skipped"} {
		unplanned = append(unplanned, f.run(review, state, state, "text", reviewPlanNow.Add(-time.Minute)))
	}
	unplanned = append(unplanned, f.run(review, "no summary", "done", "", reviewPlanNow.Add(-time.Minute)))
	done := f.done(review, "done")
	if n := f.plan(); n != 1 || !f.hasRow(done, a) || f.rows() != 1 {
		t.Fatalf("planned %d (rows %d), want only the done run with a summary", n, f.rows())
	}
	for _, id := range unplanned {
		if at := f.plannedAt(id); at != nil {
			t.Errorf("run %d was marked planned at %v", id, at)
		}
	}
}

func TestPlanReview_ARouteAddedLaterGetsNoOldResult(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, late := f.route("a"), f.route("late")
	review := f.review("Monthly spend", a)
	run := f.done(review, "Monthly spend")
	if n := f.plan(); n != 1 {
		t.Fatalf("planned %d, want 1", n)
	}
	f.exec(`insert into agent_review_routes (review_id, route_id) values ($1, $2)`, review, late.ID)
	if n := f.plan(); n != 0 || f.hasRow(run, late) {
		t.Fatalf("planned %d for a route added after the result, want 0", n)
	}
}

func TestPlanReview_StaleOrOrphanedRunIsMarkedWithoutRows(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	review := f.review("Monthly spend", a)
	gone := f.review("Deleted", a)
	orphan := f.done(gone, "Deleted")
	if err := f.s.DeleteReview(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	stale := f.run(review, "stale", "done", "Old.", reviewPlanNow.Add(-24*time.Hour-time.Second))
	edge := f.run(review, "edge", "done", "Just in time.", reviewPlanNow.Add(-24*time.Hour))
	undated := f.done(review, "undated")
	f.exec(`update agent_review_runs set finished_at = null where id = $1`, undated)
	if n := f.plan(); n != 1 || !f.hasRow(edge, a) || f.rows() != 1 {
		t.Fatalf("planned %d (rows %d), want only the run that finished 24 hours ago", n, f.rows())
	}
	for name, id := range map[string]int64{"orphan": orphan, "stale": stale, "undated": undated, "edge": edge} {
		if at := f.plannedAt(id); at == nil || !at.Equal(reviewPlanNow) {
			t.Errorf("%s run: delivery_planned_at = %v, want it marked", name, at)
		}
	}
}

func TestPlanReview_AReviewThatIsOffSendsOnlyManualRuns(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	off := f.review("Off", a)
	scheduled, manual := f.done(off, "Off"), f.done(off, "Off")
	f.exec(`update agent_review_runs set manual = true where id = $1`, manual)
	f.exec(`update agent_reviews set enabled = false where id = $1`, off)
	on := f.done(f.review("On", a), "On")
	if n := f.plan(); n != 2 || f.hasRow(scheduled, a) || !f.hasRow(manual, a) || !f.hasRow(on, a) {
		t.Fatalf("planned %d (rows %d), want the manual run and the run of the review that is on", n, f.rows())
	}
	if f.plannedAt(scheduled) == nil {
		t.Fatal("the scheduled run was not marked: it would be sent when the review is turned on again")
	}
	f.exec(`update agent_reviews set enabled = true`)
	if n := f.plan(); n != 0 || f.hasRow(scheduled, a) {
		t.Fatalf("planned %d once the review was on again, want 0", n)
	}
}

// With the operator switch for custom reviews off, a finished custom run is marked and gets no rows, a manual one too;
// a built-in run is planned. Turning the switch on again does not plan the marked run (as for a review turned on again).
func TestPlan_CustomRunsWhileTheSwitchIsOffGetNoRows(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	review := f.review("Custom", a)
	scheduled, manual, builtIn := f.done(review, "Custom"), f.done(review, "Custom"), f.done(review, "Cost")
	f.exec(`update agent_review_runs set review_type = 'custom' where id in ($1, $2)`, scheduled, manual)
	f.exec(`update agent_review_runs set manual = true where id = $1`, manual)
	planWith := func(custom bool) int {
		t.Helper()
		n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, custom, 100)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := planWith(false); n != 1 || f.hasRow(scheduled, a) || f.hasRow(manual, a) || !f.hasRow(builtIn, a) {
		t.Fatalf("planned %d (rows %d), want only the built-in run", n, f.rows())
	}
	if f.plannedAt(scheduled) == nil || f.plannedAt(manual) == nil {
		t.Fatal("a custom run was not marked: it would be sent when the switch is turned on again")
	}
	if n := planWith(true); n != 0 || f.rows() != 1 {
		t.Fatalf("planned %d (rows %d) once the switch was on again, want 0", n, f.rows())
	}
}

func TestPlan_CustomRunsAreSentWhileTheSwitchIsOn(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	run := f.done(f.review("Custom", a), "Custom")
	f.exec(`update agent_review_runs set review_type = 'custom' where id = $1`, run)
	if n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 100); err != nil || n != 1 || !f.hasRow(run, a) {
		t.Fatalf("planned %d, %v (rows %d), want the custom run planned", n, err, f.rows())
	}
}

// One poisoned run leaves nothing behind (its rows and its mark are one transaction) and does not stop the others.
func TestPlanReview_OneFailingRunDoesNotStopTheRest(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, b := f.route("a"), f.route("poison")
	bad := f.done(f.review("bad", a, b), "bad")
	good := f.done(f.review("good", a), "good")
	f.exec(`create function refuse_poison() returns trigger language plpgsql as $$ begin raise exception 'refused'; end $$`)
	f.exec(`create trigger refuse_poison before insert on notification_deliveries for each row when (new.route_name = 'poison') execute function refuse_poison()`)
	n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 100)
	if err == nil || !strings.Contains(err.Error(), "refused") || n != 1 || !f.hasRow(good, a) {
		t.Fatalf("planned %d, err %v, want the good run planned and the bad run's error", n, err)
	}
	if f.rows() != 1 || f.plannedAt(bad) != nil {
		t.Fatalf("rows = %d, bad run planned at %v, want none of the bad run's rows and no mark", f.rows(), f.plannedAt(bad))
	}
	f.exec(`drop trigger refuse_poison on notification_deliveries`)
	if n := f.plan(); n != 2 || !f.hasRow(bad, a) || !f.hasRow(bad, b) {
		t.Fatalf("planned %d after the cause was gone, want both rows of the bad run", n)
	}
}

func TestPlanReview_LimitBoundsOneCall(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	review := f.review("Monthly spend", a)
	for range 3 {
		f.done(review, "Monthly spend")
	}
	for i, want := range []int{1, 1, 1, 0} {
		if n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 1); err != nil || n != want {
			t.Fatalf("call %d with limit 1 planned %d, %v, want %d", i+1, n, err, want)
		}
	}
	f.done(review, "Monthly spend")
	if n, err := f.s.PlanReviewDeliveries(context.Background(), reviewPlanNow, never, true, 0); err != nil || n != 0 {
		t.Fatalf("limit 0 planned %d, %v, want 0", n, err)
	}
}

// The coordinator's pool is small and the step may use one connection of it.
func TestPlanReview_UsesOneConnection(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, b := f.route("a"), f.route("b")
	f.done(f.review("Monthly spend", a, b), "Monthly spend")
	cfg := f.s.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	one := &store.Store{Pool: pool}
	if n, err := one.PlanReviewDeliveries(ctx, reviewPlanNow, never, true, 100); err != nil || n != 2 {
		t.Fatalf("planned %d, %v on a pool of one, want 2", n, err)
	}
}

// A transaction that is blocked in the middle ends with its context, leaves nothing behind and gives its connection back.
func TestPlanReview_ABlockedTransactionEndsWithItsContext(t *testing.T) {
	f := newReviewPlanFixture(t)
	a := f.route("a")
	review := f.review("Monthly spend", a)
	run := f.done(review, "Monthly spend")
	f.done(review, "Monthly spend")
	tx, err := f.s.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) }) //nolint:errcheck
	if _, err := tx.Exec(context.Background(), `lock table agent_review_routes in access exclusive mode`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := f.s.PlanReviewDeliveries(ctx, reviewPlanNow, never, true, 100)
		done <- result{n, err}
	}()
	select {
	case got := <-done:
		if got.err == nil || got.n != 0 || strings.Count(got.err.Error(), "of run") != 1 {
			t.Fatalf("planned %d, %v while blocked, want 0, the timeout and no second run tried after it", got.n, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the plan did not end with its context")
	}
	if f.plannedAt(run) != nil || f.rows() != 0 {
		t.Fatalf("planned at %v with %d rows, want nothing left behind", f.plannedAt(run), f.rows())
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.plan(); n != 2 {
		t.Fatalf("planned %d once unblocked, want 2", n)
	}
}

func TestReviewHasRoute(t *testing.T) {
	f := newReviewPlanFixture(t)
	a, b := f.route("a"), f.route("b")
	review := f.review("Monthly spend", a)
	check := func(reviewID, routeID int64, wantHas, wantEnabled bool) {
		t.Helper()
		has, enabled, err := f.s.ReviewHasRoute(context.Background(), reviewID, routeID)
		if err != nil || has != wantHas || enabled != wantEnabled {
			t.Errorf("review %d, route %d = %v, %v, %v, want %v, %v", reviewID, routeID, has, enabled, err, wantHas, wantEnabled)
		}
	}
	f.review("Another", b)
	check(review, a.ID, true, true)
	check(review, b.ID, false, true)
	f.exec(`update agent_reviews set enabled = false where id = $1`, review)
	check(review, a.ID, true, false)
	if err := f.s.DeleteReview(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	check(review, a.ID, false, false)
}
