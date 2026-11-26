package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// reviewDelivery is a review row as the dispatcher would hand it over: a route, no incident, no next attempt.
func reviewDelivery(route store.NotificationRoute) store.NotificationDelivery {
	rid := route.ID
	return store.NotificationDelivery{
		RouteID: &rid, RouteName: route.Name, Destination: route.Destination, Target: "hooks.example",
		Event: "review", Severity: "info", AlertTitle: "Cost review", AlertText: "Review finished.",
		Status: "pending", CreatedAt: notifyT0,
	}
}

func mustInsertReview(t *testing.T, s *store.Store, route store.NotificationRoute, runID int64, due time.Time) int64 {
	t.Helper()
	ok, err := s.InsertReviewDelivery(context.Background(), reviewDelivery(route), runID, due, nil)
	if err != nil || !ok {
		t.Fatalf("insert review = %v, %v, want true", ok, err)
	}
	return reviewIDOf(t, s, route.ID, runID)
}

func reviewIDOf(t *testing.T, s *store.Store, routeID, runID int64) int64 {
	t.Helper()
	var id int64
	if err := s.Pool.QueryRow(context.Background(), `select id from notification_deliveries
		where route_id = $1 and review_run_id = $2 and event = 'review'`, routeID, runID).Scan(&id); err != nil {
		t.Fatalf("find review row: %v", err)
	}
	return id
}

func newReviewRoute(t *testing.T, s *store.Store, name string) store.NotificationRoute {
	t.Helper()
	route, err := s.CreateNotificationRoute(context.Background(), newRoute(name), 0)
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func TestInsertReview_OncePerRouteAndRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, b := newReviewRoute(t, s, "a"), newReviewRoute(t, s, "b")
	due := notifyT0.Add(time.Minute)
	mustInsertReview(t, s, a, 5, due)
	if ok, err := s.InsertReviewDelivery(ctx, reviewDelivery(a), 5, due.Add(time.Hour), nil); err != nil || ok {
		t.Fatalf("second insert for the same route and run = %v, %v, want false, nil", ok, err)
	}
	if ok, err := s.InsertReviewDelivery(ctx, reviewDelivery(a), 6, due, nil); err != nil || !ok {
		t.Fatalf("insert for another run = %v, %v, want true", ok, err)
	}
	if ok, err := s.InsertReviewDelivery(ctx, reviewDelivery(b), 5, due, nil); err != nil || !ok {
		t.Fatalf("insert for another route = %v, %v, want true", ok, err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where event = 'review'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("review rows = %d, %v, want 3", n, err)
	}
	var stored time.Time
	if err := s.Pool.QueryRow(ctx, `select diagnosis_due_at from notification_deliveries where id = $1`, reviewIDOf(t, s, a.ID, 5)).Scan(&stored); err != nil || !stored.Equal(due) {
		t.Fatalf("due = %v, %v, want the first insert's %v", stored, err, due)
	}
	d, err := s.GetNotificationDelivery(ctx, reviewIDOf(t, s, a.ID, 5))
	if err != nil || d.Details.ReviewRunID != 5 {
		t.Fatalf("row = %+v, %v, want the run id in its details", d, err)
	}
}

func TestInsertReview_NeverSetsNextAttemptAtOrAnIncident(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	d := delivery(route, incident, "opened", 3) // an alert-shaped row: incident, next attempt, seq, severity, wrong event
	held := notifyT0.Add(time.Hour)
	if ok, err := s.InsertReviewDelivery(ctx, d, 9, notifyT0, &held); err != nil || !ok {
		t.Fatalf("insert = %v, %v", ok, err)
	}
	id := reviewIDOf(t, s, route.ID, 9)
	got, err := s.GetNotificationDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Event != "review" || got.Seq != 0 || got.Status != "pending" || got.Severity != "info" || got.IncidentID != nil || got.NextAttemptAt != nil {
		t.Fatalf("row = %+v, want review, seq 0, pending, info, no incident, no next_attempt_at", got)
	}
	st := readDiagnosis(t, s, id)
	if st.Due == nil || !st.Due.Equal(notifyT0) || st.HeldUntil == nil || !st.HeldUntil.Equal(held) {
		t.Fatalf("row = %+v, want due stored and held_until for display only", st)
	}
	noRoute := reviewDelivery(route)
	noRoute.RouteID = nil
	if ok, err := s.InsertReviewDelivery(ctx, noRoute, 10, notifyT0, nil); err == nil || ok {
		t.Fatalf("insert without a route = %v, %v, want an error", ok, err)
	}
}

func TestInsertReview_IgnoresLeaseResultAndAttemptsFromTheCaller(t *testing.T) {
	s := storetest.Open(t)
	route := newReviewRoute(t, s, "a")
	d := reviewDelivery(route)
	lease, finished, code, ms := notifyT0.Add(time.Hour), notifyT0, 200, 12
	d.ClaimedUntil, d.FinishedAt, d.StatusCode, d.DurationMS, d.Attempts = &lease, &finished, &code, &ms, 4
	d.Status = "delivered"
	if ok, err := s.InsertReviewDelivery(context.Background(), d, 1, notifyT0, nil); err != nil || !ok {
		t.Fatalf("insert = %v, %v", ok, err)
	}
	got, err := s.GetNotificationDelivery(context.Background(), reviewIDOf(t, s, route.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimedUntil != nil || got.FinishedAt != nil || got.StatusCode != nil || got.DurationMS != nil || got.Attempts != 0 || got.Status != "pending" {
		t.Fatalf("row = %+v, want pending with no lease, result or attempts", got)
	}
}

func TestOldClaimNeverReturnsAReviewRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustInsertReview(t, s, route, 1, notifyT0.Add(-time.Hour))
	rows, err := s.ClaimNotificationDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("old claim = %+v, %v, want nothing", rows, err)
	}
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	rows, err = s.ClaimNotificationDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Event != "opened" {
		t.Fatalf("old claim = %+v, %v, want the opened row only", rows, err)
	}
}

func TestDiagnosisClaimNeverReturnsAReviewRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustInsertReview(t, s, route, 1, notifyT0.Add(-time.Hour))
	rows, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("diagnosis claim = %+v, %v, want nothing", rows, err)
	}
	mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	rows, err = s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Event != "diagnosis" {
		t.Fatalf("diagnosis claim = %+v, %v, want the diagnosis row only", rows, err)
	}
}

func TestReviewClaimNeverReturnsADiagnosisRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0.Add(-time.Hour))
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	rows, err := s.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("review claim = %+v, %v, want nothing", rows, err)
	}
	id := mustInsertReview(t, s, route, 1, notifyT0)
	rows, err = s.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Event != "review" {
		t.Fatalf("review claim = %+v, %v, want the review row only", rows, err)
	}
}

func TestOldHeldReleaseNeverTouchesAReviewRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	route := newReviewRoute(t, s, "a")
	id := mustInsertReview(t, s, route, 1, notifyT0.Add(time.Hour))
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set held_until = $2 where id = $1`, id, notifyT0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetNotificationDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHeldNotificationDeliveries(ctx, notifyT0, "resolved"); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetNotificationDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || after.Status != "pending" || after.NextAttemptAt != nil {
		t.Fatalf("after release = %+v, want exactly the row it was (%+v)", after, before)
	}
	if err := s.CancelUnsentNotificationDeliveries(ctx, []int64{1, 2, 3}, "p", "h", notifyT0); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetNotificationDelivery(ctx, id); !reflect.DeepEqual(before, again) {
		t.Fatalf("after an alert cancel = %+v, want exactly the row it was", again)
	}
}

func TestClaimReviewDeliveries_DueOnlyBusyRoutesAndPerRouteCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, b := newReviewRoute(t, s, "a"), newReviewRoute(t, s, "b")
	later := mustInsertReview(t, s, a, 3, notifyT0.Add(30*time.Second)) // due while the first leases (1m) are still live
	for run := int64(1); run <= 2; run++ {
		mustInsertReview(t, s, a, run, notifyT0)
	}
	mustInsertReview(t, s, b, 1, notifyT0)
	rows, err := s.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, PerRoute: 1})
	if err != nil || len(rows) != 2 {
		t.Fatalf("capped claim = %d rows, %v, want one per route (2)", len(rows), err)
	}
	perRoute := map[int64]int{}
	for _, r := range rows {
		perRoute[*r.RouteID]++
		if r.ID == later {
			t.Errorf("claimed a row that is not due yet")
		}
		if r.ClaimedUntil == nil || !r.ClaimedUntil.Equal(notifyT0.Add(time.Minute)) || r.Details.ReviewRunID == 0 {
			t.Errorf("row = %+v, want a lease of now + lease and its run id", r)
		}
	}
	if perRoute[a.ID] != 1 || perRoute[b.ID] != 1 {
		t.Fatalf("rows per route = %v, want 1 and 1", perRoute)
	}
	if rows, err = s.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, Exclude: []int64{a.ID}}); err != nil || len(rows) != 0 {
		t.Fatalf("claim with A busy and B leased = %+v, %v, want nothing", rows, err)
	}
	if rows, err = s.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, Exclude: []int64{b.ID}}); err != nil || len(rows) != 1 {
		t.Fatalf("claim with B busy = %d rows, %v, want A's remaining due row", len(rows), err)
	}
	if rows, err = s.ClaimReviewDeliveries(ctx, notifyT0.Add(30*time.Second), time.Minute, store.ClaimFilter{Limit: 10}); err != nil || len(rows) != 1 || rows[0].ID != later {
		t.Fatalf("claim at the later due time = %+v, %v, want the later row", rows, err)
	}
}

// The method is the one of TestClaimDiagnosis_TwoClaimersTakeARowOnce: a sleeping timestamp comparison holds the slow
// claimer's ranking snapshot back until the fast claimer has taken the row.
func TestClaimReviewDeliveries_TwoClaimersTakeARowOnce(t *testing.T) {
	fast := storetest.Open(t)
	ctx := context.Background()
	route := newReviewRoute(t, fast, "a")
	mustInsertReview(t, fast, route, 1, notifyT0)
	var schema string
	if err := fast.Pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`create function slow_le(a timestamptz, b timestamptz) returns bool language plpgsql as $$
			begin
				if current_setting('hm.slow', true) = 'on' then perform pg_sleep(0.6); end if;
				return not (a > b);
			end $$`,
		`create operator <= (leftarg = timestamptz, rightarg = timestamptz, function = slow_le)`,
	} {
		if _, err := fast.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	slow, err := store.OpenWithParams(ctx, os.Getenv("HM_TEST_DATABASE_URL"), map[string]string{"search_path": schema + ", pg_catalog", "hm.slow": "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()

	type result struct {
		rows []store.NotificationDelivery
		err  error
	}
	slowDone := make(chan result, 1)
	go func() {
		rows, err := slow.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 5})
		slowDone <- result{rows, err}
	}()
	waitForSleepingClaimer(t, fast)
	fastRows, err := fast.ClaimReviewDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 5})
	if err != nil || len(fastRows) != 1 {
		t.Fatalf("fast claim = %+v, %v, want the row", fastRows, err)
	}
	got := <-slowDone
	if got.err != nil || len(got.rows) != 0 {
		t.Fatalf("slow claim = %+v, %v, want nothing: the row was already taken", got.rows, got.err)
	}
}

// claimOneReview inserts a review row and leases it directly in SQL, so the tests that use it do not depend on the
// claim; it returns the row id and the lease.
func claimOneReview(t *testing.T, s *store.Store) (int64, store.NotificationRoute, *time.Time) {
	t.Helper()
	route := newReviewRoute(t, s, "a")
	id := mustInsertReview(t, s, route, 1, notifyT0)
	var lease time.Time
	if err := s.Pool.QueryRow(context.Background(), `update notification_deliveries set claimed_until = $2 where id = $1
		returning claimed_until`, id, notifyT0.Add(time.Minute)).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	return id, route, &lease
}

func TestFinish_ReviewRetryUsesTheFollowUpColumn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id, route, lease := claimOneReview(t, s)
	retryAt := notifyT0.Add(5 * time.Minute)
	if err := s.FinishNotificationDelivery(ctx, id, lease, 1, store.DeliveryOutcome{Retryable: true, RetryAt: retryAt, Note: "try later"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	st := readDiagnosis(t, s, id)
	if st.Status != "pending" || st.Next != nil || st.Due == nil || !st.Due.Equal(retryAt) || st.Claimed != nil || st.Attempts != 1 {
		t.Fatalf("after retryable finish = %+v, want pending, next_attempt_at null, diagnosis_due_at = retry time", st)
	}
	rows, err := s.ClaimReviewDeliveries(ctx, retryAt, time.Minute, store.ClaimFilter{Limit: 5})
	if err != nil || len(rows) != 1 || *rows[0].RouteID != route.ID {
		t.Fatalf("reclaim = %+v, %v", rows, err)
	}
	if err := s.FinishNotificationDelivery(ctx, id, rows[0].ClaimedUntil, 2, store.DeliveryOutcome{Delivered: true, DurationMS: 5}, retryAt); err != nil {
		t.Fatal(err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "delivered" || st.Next != nil {
		t.Fatalf("after delivery = %+v, want delivered with next_attempt_at null", st)
	}
}

func TestRetry_FailedReviewUsesTheFollowUpColumn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id, _, lease := claimOneReview(t, s)
	if err := s.FinishNotificationDelivery(ctx, id, lease, 3, store.DeliveryOutcome{Note: "refused"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	retryNow := notifyT0.Add(time.Hour)
	d, err := s.RetryNotificationDelivery(ctx, id, retryNow)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if d.NextAttemptAt != nil {
		t.Fatalf("retried row next_attempt_at = %v, want nil", d.NextAttemptAt)
	}
	st := readDiagnosis(t, s, id)
	if st.Status != "pending" || st.Next != nil || st.Due == nil || !st.Due.Equal(retryNow) || st.Attempts != 0 {
		t.Fatalf("after retry = %+v, want pending, due = now, next_attempt_at null", st)
	}
	if rows, err := s.ClaimNotificationDeliveries(ctx, retryNow, time.Minute, store.ClaimFilter{Limit: 5}); err != nil || len(rows) != 0 {
		t.Fatalf("old claim after retry = %+v, %v, want nothing", rows, err)
	}
	if rows, err := s.ClaimReviewDeliveries(ctx, retryNow, time.Minute, store.ClaimFilter{Limit: 5}); err != nil || len(rows) != 1 {
		t.Fatalf("review claim after retry = %+v, %v, want the row", rows, err)
	}
}

func TestReviewConstraint_RefusesNextAttemptAtHeldAndAnIncident(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertReview(t, s, route, 1, notifyT0)
	diag := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	opened := mustPlan(t, s, delivery(route, incident, "opened", 0)).ID
	for name, stmt := range map[string]string{
		"next_attempt_at on a review row": fmt.Sprintf(`update notification_deliveries set next_attempt_at = now() where id = %d`, id),
		"held review row":                 fmt.Sprintf(`update notification_deliveries set status = 'held' where id = %d`, id),
		"incident on a review row":        fmt.Sprintf(`update notification_deliveries set incident_id = %d where id = %d`, incident.ID, id),
		"review event without a run id":   fmt.Sprintf(`update notification_deliveries set review_run_id = null where id = %d`, id),
		"due column on another event":     fmt.Sprintf(`update notification_deliveries set event = 'opened' where id = %d`, id),
		"due column on an alert row":      fmt.Sprintf(`update notification_deliveries set diagnosis_due_at = now() where id = %d`, opened),
		"run id on a diagnosis row":       fmt.Sprintf(`update notification_deliveries set review_run_id = 4 where id = %d`, diag),
		"review event on a diagnosis row": fmt.Sprintf(`update notification_deliveries set event = 'review', review_run_id = 4 where id = %d`, diag),
		"next_attempt_at on diagnosis":    fmt.Sprintf(`update notification_deliveries set next_attempt_at = now() where id = %d`, diag),
	} {
		if _, err := s.Pool.Exec(ctx, stmt); err == nil {
			t.Errorf("%s was accepted, want a constraint to refuse it", name)
		}
	}
	if st := readDiagnosis(t, s, id); st.Status != "pending" || st.Next != nil {
		t.Fatalf("row changed: %+v", st)
	}
}

func TestCancelReviewDeliveries_LeavesAClaimedRowToFinish(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, b := newReviewRoute(t, s, "a"), newReviewRoute(t, s, "b")
	claimed := mustInsertReview(t, s, a, 1, notifyT0)
	pendingA := mustInsertReview(t, s, a, 2, notifyT0)
	pendingB := mustInsertReview(t, s, b, 1, notifyT0)
	otherRun := mustInsertReview(t, s, b, 2, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set claimed_until = $2 where id = $1`, claimed, notifyT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Run 1 is cancelled: its leased row stays, route b's pending row goes, run 2 is untouched.
	n, err := s.CancelReviewDeliveries(ctx, 1, nil, "Cancelled.", notifyT0)
	if err != nil || n != 1 {
		t.Fatalf("cancel run 1 = %d, %v, want 1 (route b's pending row; route a's is leased)", n, err)
	}
	if st := readDiagnosis(t, s, claimed); st.Status != "pending" || st.Claimed == nil {
		t.Fatalf("leased row = %+v, want left pending under its lease", st)
	}
	if d, _ := s.GetNotificationDelivery(ctx, pendingB); d.Status != "cancelled" || d.Note != "Cancelled." || d.FinishedAt == nil || d.ClaimedUntil != nil {
		t.Fatalf("route b run 1 = %+v, want cancelled with the note", d)
	}
	for _, id := range []int64{pendingA, otherRun} {
		if st := readDiagnosis(t, s, id); st.Status != "pending" {
			t.Fatalf("row %d = %+v, want pending (another run)", id, st)
		}
	}
	// One route only.
	n, err = s.CancelReviewDeliveries(ctx, 2, &a.ID, "One route.", notifyT0)
	if err != nil || n != 1 {
		t.Fatalf("cancel run 2 on route a = %d, %v, want 1", n, err)
	}
	if st := readDiagnosis(t, s, otherRun); st.Status != "pending" {
		t.Fatalf("route b run 2 = %+v, want pending", st)
	}
	if rows, _ := s.ClaimReviewDeliveries(ctx, notifyT0.Add(2*time.Hour), time.Minute, store.ClaimFilter{Limit: 10}); len(rows) != 2 {
		t.Fatalf("claim = %d rows, want the leased row (lease over) and route b run 2", len(rows))
	}
}

func TestSummary_FailedReviewNeverMakesARouteFailing(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	for seq := 1; seq <= 2; seq++ {
		d := delivery(route, incident, "repeat", seq)
		d.Status, d.NextAttemptAt = "failed", nil
		d.CreatedAt = notifyT0.Add(time.Duration(seq) * time.Minute)
		mustPlan(t, s, d)
	}
	id := mustInsertReview(t, s, route, 1, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'failed', created_at = $2 where id = $1`, id, notifyT0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sum, err := s.NotificationSummary(ctx, notifyT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FailingRouteIDs) != 0 || sum.Failed24h != 2 {
		t.Fatalf("summary = %+v, want no failing route and 2 failed (a failed review is not an alert delivery)", sum)
	}
}

func TestSummary_DeliveredReviewDoesNotHideAFailingRoute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	for seq := 1; seq <= 3; seq++ {
		d := delivery(route, incident, "repeat", seq)
		d.Status, d.NextAttemptAt = "failed", nil
		d.CreatedAt = notifyT0.Add(time.Duration(seq) * time.Minute)
		mustPlan(t, s, d)
	}
	id := mustInsertReview(t, s, route, 1, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'delivered', finished_at = $2, created_at = $3 where id = $1`,
		id, notifyT0, notifyT0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sum, err := s.NotificationSummary(ctx, notifyT0.Add(time.Hour))
	if err != nil || len(sum.FailingRouteIDs) != 1 || sum.FailingRouteIDs[0] != route.ID || sum.Failed24h != 3 {
		t.Fatalf("summary = %+v, %v, want the route failing and 3 failed alerts", sum, err)
	}
}

func TestPlanStates_LastSentIgnoresReviews(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	opened := delivery(route, incident, "opened", 0)
	opened.Status, opened.CreatedAt = "delivered", notifyT0
	mustPlan(t, s, opened)
	id := mustInsertReview(t, s, route, 1, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'delivered', created_at = $2 where id = $1`, id, notifyT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	states, err := s.NotificationPlanStates(ctx, []int64{incident.ID})
	if err != nil {
		t.Fatal(err)
	}
	st := states[[2]int64{route.ID, incident.ID}]
	if st.LastSentAt == nil || !st.LastSentAt.Equal(notifyT0) || st.OpenedStatus != "delivered" || st.HasHeld || st.HasResolved || st.MaxRepeatSeq != 0 || st.MaxUpdateSeq != 0 {
		t.Fatalf("plan state = %+v, want the opened row's time and nothing from the review row", st)
	}
	if len(states) != 1 {
		t.Fatalf("plan states = %v, want only the one incident", states)
	}
}

func TestExpireStaleTests_LeavesAReviewRowAlone(t *testing.T) {
	s := storetest.Open(t)
	id, _, _ := claimOneReview(t, s) // pending, no next_attempt_at, lease set: the shape of an abandoned test row
	n, err := s.ExpireStaleNotificationTests(context.Background(), notifyT0.Add(time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("expired = %d, %v, want 0", n, err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "pending" {
		t.Fatalf("status = %s, want pending", st.Status)
	}
}

func TestRetention_NotificationLogDeletesOldReviewRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	route := newReviewRoute(t, s, "a")
	oldID, freshID := mustInsertReview(t, s, route, 1, notifyT0), mustInsertReview(t, s, route, 2, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set created_at = $2 where id = $1`, oldID, notifyT0.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRetention(ctx, notifyT0.Add(30*time.Minute), store.Retention{NotificationLog: 30 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	var left []int64
	rows, err := s.Pool.Query(ctx, `select id from notification_deliveries where event = 'review'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if !reflect.DeepEqual(left, []int64{freshID}) {
		t.Fatalf("review rows left = %v, want only the fresh one (%d)", left, freshID)
	}
}

// stepBackTo0220 returns the schema an older task sees: no review event, no review_run_id column, the diagnosis-only
// due check of 0220. Rows in the table stay.
func stepBackTo0220(t *testing.T, s *store.Store) {
	t.Helper()
	for _, stmt := range []string{
		`alter table notification_deliveries drop column review_run_id`, // takes its check and its indexes along
		`alter table notification_deliveries drop constraint notification_deliveries_review_no_incident`,
		`drop index notification_deliveries_review_due_idx`,
		`alter table notification_deliveries drop constraint notification_deliveries_event_check`,
		`alter table notification_deliveries add constraint notification_deliveries_event_check
			check (event in ('opened', 'repeat', 'update', 'resolved', 'test', 'diagnosis'))`,
		`alter table notification_deliveries drop constraint notification_deliveries_diagnosis_due`,
		`alter table notification_deliveries add constraint notification_deliveries_diagnosis_due check (
			(event <> 'diagnosis' or (next_attempt_at is null and status <> 'held'))
			and (event = 'diagnosis' or diagnosis_due_at is null))`,
	} {
		if _, err := s.Pool.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestMigration0222_KeepsExistingRowsAndOldEventsInsert(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	stepBackTo0220(t, s)
	for i, ev := range []string{"opened", "repeat", "update", "resolved"} {
		mustPlan(t, s, delivery(route, incident, ev, i))
	}
	mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	body, err := os.ReadFile("migrations/0222_notification_review.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply 0222 over existing rows: %v", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where review_run_id is null`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("existing rows with a null run id = %d, %v, want 5 (four alerts and the diagnosis row)", n, err)
	}
	// An older task still writes only the old values, with the old column list.
	for i, ev := range []string{"opened", "repeat", "update", "resolved", "test"} {
		if _, err := s.Pool.Exec(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
			severity, alert_title, alert_text, status, next_attempt_at) values ($1, 'r', 'webhook', 't', $2, $3, $4, 'critical', 't', 'x', 'pending', now())`,
			route.ID, incident.ID, ev, 100+i); err != nil {
			t.Errorf("old-style insert of %q refused: %v", ev, err)
		}
	}
	// The phase 2 task's diagnosis insert (its own column list) still passes.
	if _, err := s.Pool.Exec(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
		severity, alert_title, alert_text, status, diagnosis_due_at) values ($1, 'r', 'webhook', 't', $2, 'diagnosis', 300, 'critical', 't', 'x', 'pending', now())`,
		route.ID, incident.ID); err != nil {
		t.Errorf("phase 2 diagnosis insert refused: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
		severity, alert_title, alert_text, status) values ($1, 'r', 'webhook', 't', $2, 'bogus', 200, 'critical', 't', 'x', 'pending')`, route.ID, incident.ID); err == nil {
		t.Error("an unknown event was accepted")
	}
	if ok, err := s.InsertReviewDelivery(ctx, reviewDelivery(route), 1, notifyT0, nil); err != nil || !ok {
		t.Fatalf("review insert after the migration = %v, %v", ok, err)
	}
}

// While an older task's reader holds the table, the migration's ALTER waits and every later statement queues behind
// it; the migration must give up before the dispatcher's 5 second result write does (see 0220).
func TestMigration0222_LockTimeoutLetsAnOldSenderFinish(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	stepBackTo0220(t, s)
	var id int64
	if err := s.Pool.QueryRow(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
		severity, alert_title, alert_text, status, next_attempt_at, claimed_until) values ($1, 'r', 'webhook', 't', $2, 'opened', 0,
		'critical', 't', 'x', 'pending', now(), now() + interval '1 minute') returning id`, route.ID, incident.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/0222_notification_review.up.sql")
	if err != nil {
		t.Fatal(err)
	}

	reader, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx) //nolint:errcheck // already rolled back on the happy path
	if _, err := reader.Exec(ctx, `select count(*) from notification_deliveries`); err != nil {
		t.Fatal(err)
	}
	migrated := make(chan error, 1)
	go func() {
		_, err := s.Pool.Exec(ctx, string(body))
		migrated <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for waiting := false; !waiting; {
		if time.Now().After(deadline) {
			select {
			case err := <-migrated:
				t.Fatalf("the migration ended without waiting: %v", err)
			default:
			}
			t.Fatal("the migration never started waiting for its lock")
		}
		if err := s.Pool.QueryRow(ctx, `select count(*) > 0 from pg_stat_activity where wait_event_type = 'Lock'
			and query like '-- Review messages%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.Pool.Exec(writeCtx, `update notification_deliveries set status = 'delivered', claimed_until = null, finished_at = now()
		where id = $1 and status = 'pending'`, id); err != nil {
		t.Fatalf("an older sender's result write failed while the migration waited: %v", err)
	}
	if err := <-migrated; err == nil {
		t.Fatal("the migration succeeded under an open reader, want a lock timeout")
	}
}

func TestClaimFollowUp_RefusesAnyEventButTheTwoConstants(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustInsertReview(t, s, route, 1, notifyT0)
	mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	for _, event := range []string{"opened", "", "'review'", "review' or 'a' = 'a", "diagnosis'; --"} {
		rows, err := s.ClaimFollowUpDeliveriesForTest(ctx, event, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
		if err == nil || len(rows) != 0 {
			t.Errorf("claim of event %q = %+v, %v, want an error and no rows", event, rows, err)
		}
	}
	var leased int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where claimed_until is not null`).Scan(&leased); err != nil || leased != 0 {
		t.Fatalf("leased rows = %d, %v, want none: a refused event must not lease anything", leased, err)
	}
}

func TestInsertReview_InATransactionRollsBackAndUsesOneConnection(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	route := newReviewRoute(t, s, "a")
	var schema string
	if err := s.Pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	one, err := store.OpenWithOptions(ctx, os.Getenv("HM_TEST_DATABASE_URL"), store.OpenOptions{
		RuntimeParams: map[string]string{"search_path": schema + ", pg_catalog"}, MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()

	errRollBack := errors.New("roll back")
	// A second connection would wait for the only one, which the transaction holds: the short deadline turns that
	// deadlock into a failure.
	txCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = one.InTx(txCtx, func(tx store.Tx) error {
		ok, err := tx.InsertReviewDelivery(txCtx, reviewDelivery(route), 7, notifyT0, nil)
		if err != nil || !ok {
			t.Errorf("insert in the transaction = %v, %v, want true", ok, err)
		}
		if again, err := tx.InsertReviewDelivery(txCtx, reviewDelivery(route), 7, notifyT0, nil); err != nil || again {
			t.Errorf("second insert in the transaction = %v, %v, want false, nil", again, err)
		}
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("InTx = %v, want the rollback error", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where event = 'review'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("review rows after rollback = %d, %v, want none", n, err)
	}
	againCtx, cancelAgain := context.WithTimeout(ctx, 5*time.Second)
	defer cancelAgain()
	if err := one.InTx(againCtx, func(tx store.Tx) error {
		ok, err := tx.InsertReviewDelivery(againCtx, reviewDelivery(route), 7, notifyT0, nil)
		if err != nil || !ok {
			t.Errorf("insert = %v, %v, want true", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where event = 'review' and review_run_id = 7`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("review rows after commit = %d, %v, want 1", n, err)
	}
}
