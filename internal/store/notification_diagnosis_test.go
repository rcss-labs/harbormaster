package store_test

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// diagnosisDelivery is a diagnosis row as the dispatcher would hand it over: no next attempt, nothing held.
func diagnosisDelivery(route store.NotificationRoute, incident store.NotificationIncident) store.NotificationDelivery {
	d := delivery(route, incident, "diagnosis", 0)
	d.NextAttemptAt = nil
	d.Details = store.DeliveryDetails{InvestigationID: 7}
	return d
}

func mustInsertDiagnosis(t *testing.T, s *store.Store, d store.NotificationDelivery, due time.Time) int64 {
	t.Helper()
	ok, err := s.InsertDiagnosisDelivery(context.Background(), d, due, nil)
	if err != nil || !ok {
		t.Fatalf("insert diagnosis = %v, %v, want true", ok, err)
	}
	return diagnosisIDOf(t, s, *d.RouteID, *d.IncidentID)
}

func diagnosisIDOf(t *testing.T, s *store.Store, routeID, incidentID int64) int64 {
	t.Helper()
	var id int64
	if err := s.Pool.QueryRow(context.Background(), `select id from notification_deliveries
		where route_id = $1 and incident_id = $2 and event = 'diagnosis'`, routeID, incidentID).Scan(&id); err != nil {
		t.Fatalf("find diagnosis row: %v", err)
	}
	return id
}

type diagnosisState struct {
	Status                        string
	Next, Due, HeldUntil, Claimed *time.Time
	Attempts                      int
	Seq                           int
}

func readDiagnosis(t *testing.T, s *store.Store, id int64) diagnosisState {
	t.Helper()
	var st diagnosisState
	err := s.Pool.QueryRow(context.Background(), `select status, next_attempt_at, diagnosis_due_at, held_until, claimed_until, attempts, seq
		from notification_deliveries where id = $1`, id).Scan(&st.Status, &st.Next, &st.Due, &st.HeldUntil, &st.Claimed, &st.Attempts, &st.Seq)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInsertDiagnosis_OncePerRouteAndIncident(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	d := diagnosisDelivery(route, incident)
	d.Seq = 5 // the store forces seq 0, so a caller cannot slip a second row past the unique key
	if ok, err := s.InsertDiagnosisDelivery(ctx, d, notifyT0, nil); err != nil || !ok {
		t.Fatalf("first insert = %v, %v", ok, err)
	}
	d.Seq = 9
	if ok, err := s.InsertDiagnosisDelivery(ctx, d, notifyT0.Add(time.Hour), nil); err != nil || ok {
		t.Fatalf("second insert = %v, %v, want false with no error", ok, err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where event = 'diagnosis'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("diagnosis rows = %d, %v, want 1", n, err)
	}
	st := readDiagnosis(t, s, diagnosisIDOf(t, s, route.ID, incident.ID))
	if st.Seq != 0 || st.Due == nil || !st.Due.Equal(notifyT0) {
		t.Fatalf("row = %+v, want seq 0 and the first due time", st)
	}
	// The same incident on another route is its own row.
	other, err := s.CreateNotificationRoute(ctx, newRoute("other"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.InsertDiagnosisDelivery(ctx, diagnosisDelivery(other, incident), notifyT0, nil); err != nil || !ok {
		t.Fatalf("other route insert = %v, %v", ok, err)
	}
}

func TestInsertDiagnosis_NeverSetsNextAttemptAt(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	d := diagnosisDelivery(route, incident)
	soon := notifyT0.Add(time.Minute)
	d.NextAttemptAt, d.Status = &soon, "held" // a caller that sets both is overridden, not trusted
	held := notifyT0.Add(8 * time.Hour)
	due := notifyT0.Add(8 * time.Hour)
	if ok, err := s.InsertDiagnosisDelivery(context.Background(), d, due, &held); err != nil || !ok {
		t.Fatalf("insert = %v, %v", ok, err)
	}
	st := readDiagnosis(t, s, diagnosisIDOf(t, s, route.ID, incident.ID))
	if st.Next != nil || st.Status != "pending" {
		t.Fatalf("row = %+v, want pending with no next_attempt_at", st)
	}
	if st.Due == nil || !st.Due.Equal(due) || st.HeldUntil == nil || !st.HeldUntil.Equal(held) {
		t.Fatalf("row = %+v, want due and held_until stored (held_until for display only)", st)
	}
}

func TestOldClaimNeverReturnsADiagnosisRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0.Add(-time.Hour))
	rows, err := s.ClaimNotificationDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("old claim = %+v, %v, want nothing", rows, err)
	}
	// An opened row beside it is still claimed exactly as before.
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	rows, err = s.ClaimNotificationDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Event != "opened" {
		t.Fatalf("old claim = %+v, %v, want the opened row only", rows, err)
	}
}

func TestOldHeldReleaseNeverTouchesADiagnosisRow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	past := notifyT0.Add(-time.Hour)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0.Add(time.Hour))
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set held_until = $2 where id = $1`, id, past); err != nil {
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
	// The incident resolving does not make the older release cancel it either.
	if _, err := s.Pool.Exec(ctx, `update notification_incidents set resolved_at = $2 where id = $1`, incident.ID, notifyT0); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHeldNotificationDeliveries(ctx, notifyT0, "resolved"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetNotificationDelivery(ctx, id); !reflect.DeepEqual(before, again) {
		t.Fatalf("after release on a resolved incident = %+v, want exactly the row it was", again)
	}
}

func TestClaimDiagnosis_DueOnly(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0.Add(time.Minute))
	mustPlan(t, s, delivery(route, incident, "opened", 0)) // due now, but not a diagnosis row
	rows, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("claim before due = %+v, %v, want nothing", rows, err)
	}
	rows, err = s.ClaimDiagnosisDeliveries(ctx, notifyT0.Add(time.Minute), time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Event != "diagnosis" || rows[0].Details.InvestigationID != 7 {
		t.Fatalf("claim at due = %+v, %v, want the diagnosis row with its investigation id", rows, err)
	}
	if rows[0].ClaimedUntil == nil || !rows[0].ClaimedUntil.Equal(notifyT0.Add(2*time.Minute)) {
		t.Fatalf("lease = %v, want now + lease", rows[0].ClaimedUntil)
	}
	again, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0.Add(time.Minute), time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, %v, want nothing while leased", again, err)
	}
	expired, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0.Add(3*time.Minute), time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(expired) != 1 {
		t.Fatalf("claim after lease = %+v, %v, want the row again", expired, err)
	}
}

func TestClaimDiagnosis_RespectsBusyRoutesAndPerRouteCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	routeA, err := s.CreateNotificationRoute(ctx, newRoute("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	routeB, err := s.CreateNotificationRoute(ctx, newRoute("b"), 0)
	if err != nil {
		t.Fatal(err)
	}
	setAlerts(t, s, notifyAlert("s1", "service_failing", model.AlertDanger), notifyAlert("s2", "service_failing", model.AlertDanger),
		notifyAlert("s3", "service_failing", model.AlertDanger))
	incidents, _ := syncIncidents(t, s, notifyT0)
	if len(incidents) != 3 {
		t.Fatalf("incidents = %d, want 3", len(incidents))
	}
	for _, in := range incidents {
		mustInsertDiagnosis(t, s, diagnosisDelivery(routeA, in), notifyT0)
	}
	mustInsertDiagnosis(t, s, diagnosisDelivery(routeB, incidents[0]), notifyT0)
	rows, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, PerRoute: 1})
	if err != nil || len(rows) != 2 {
		t.Fatalf("capped claim = %d rows, %v, want one per route (2)", len(rows), err)
	}
	perRoute := map[int64]int{}
	for _, r := range rows {
		perRoute[*r.RouteID]++
	}
	if perRoute[routeA.ID] != 1 || perRoute[routeB.ID] != 1 {
		t.Fatalf("rows per route = %v, want 1 and 1", perRoute)
	}
	rows, err = s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, Exclude: []int64{routeA.ID}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("claim with A busy and B leased = %+v, %v, want nothing", rows, err)
	}
	rows, err = s.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 10, Exclude: []int64{routeB.ID}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("claim with B busy = %d rows, %v, want A's remaining 2", len(rows), err)
	}
}

// TestClaimDiagnosis_TwoClaimersTakeARowOnce makes the overlap certain. The slow claimer's comparisons of
// timestamps sleep (a test-only operator that shadows the built-in one, found first through its search path), so
// its ranking snapshot is taken before the fast claimer takes the row and commits. The slow claimer then reaches the
// row after the lease is set; only the re-check on the locked row keeps it from taking the row a second time.
func TestClaimDiagnosis_TwoClaimersTakeARowOnce(t *testing.T) {
	fast := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, fast)
	mustInsertDiagnosis(t, fast, diagnosisDelivery(route, incident), notifyT0)
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
		rows, err := slow.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 5})
		slowDone <- result{rows, err}
	}()
	time.Sleep(150 * time.Millisecond) // the slow claimer is now inside its first sleeping comparison
	fastRows, err := fast.ClaimDiagnosisDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 5})
	if err != nil || len(fastRows) != 1 {
		t.Fatalf("fast claim = %+v, %v, want the row", fastRows, err)
	}
	got := <-slowDone
	if got.err != nil || len(got.rows) != 0 {
		t.Fatalf("slow claim = %+v, %v, want nothing: the row was already taken", got.rows, got.err)
	}
}

func TestClaimDiagnosis_LimitTakesTheEarliestDue(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	route, err := s.CreateNotificationRoute(ctx, newRoute("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	setAlerts(t, s, notifyAlert("s1", "service_failing", model.AlertDanger), notifyAlert("s2", "service_failing", model.AlertDanger),
		notifyAlert("s3", "service_failing", model.AlertDanger))
	incidents, _ := syncIncidents(t, s, notifyT0)
	dueByIncident := map[int64]time.Time{}
	// Inserted latest first, so id order and due order disagree.
	for i, in := range incidents {
		due := notifyT0.Add(time.Duration(len(incidents)-i) * time.Minute)
		mustInsertDiagnosis(t, s, diagnosisDelivery(route, in), due)
		dueByIncident[in.ID] = due
	}
	rows, err := s.ClaimDiagnosisDeliveries(ctx, notifyT0.Add(time.Hour), time.Minute, store.ClaimFilter{Limit: 2})
	if err != nil || len(rows) != 2 {
		t.Fatalf("claim = %d rows, %v, want 2", len(rows), err)
	}
	for _, r := range rows {
		if due := dueByIncident[*r.IncidentID]; due.After(notifyT0.Add(2 * time.Minute)) {
			t.Errorf("claimed a row due at +%v, want only the two earliest (due +1m and +2m)", due.Sub(notifyT0))
		}
	}
}

// claimOneDiagnosis inserts a diagnosis row and leases it directly in SQL, so the tests that use it do not depend on
// ClaimDiagnosisDeliveries; it returns the row id and the lease.
func claimOneDiagnosis(t *testing.T, s *store.Store) (int64, *time.Time) {
	t.Helper()
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	var lease time.Time
	if err := s.Pool.QueryRow(context.Background(), `update notification_deliveries set claimed_until = $2 where id = $1
		returning claimed_until`, id, notifyT0.Add(time.Minute)).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	return id, &lease
}

func TestFinish_DiagnosisRetryUsesItsOwnColumn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id, lease := claimOneDiagnosis(t, s)
	retryAt := notifyT0.Add(5 * time.Minute)
	if err := s.FinishNotificationDelivery(ctx, id, lease, 1, store.DeliveryOutcome{Retryable: true, RetryAt: retryAt, Note: "try later"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	st := readDiagnosis(t, s, id)
	if st.Status != "pending" || st.Next != nil || st.Due == nil || !st.Due.Equal(retryAt) || st.Claimed != nil || st.Attempts != 1 {
		t.Fatalf("after retryable finish = %+v, want pending, next_attempt_at null, diagnosis_due_at = retry time", st)
	}
	// Final outcomes leave next_attempt_at null too.
	rows, err := s.ClaimDiagnosisDeliveries(ctx, retryAt, time.Minute, store.ClaimFilter{Limit: 5})
	if err != nil || len(rows) != 1 {
		t.Fatalf("reclaim = %+v, %v", rows, err)
	}
	if err := s.FinishNotificationDelivery(ctx, id, rows[0].ClaimedUntil, 2, store.DeliveryOutcome{Delivered: true, DurationMS: 5}, retryAt); err != nil {
		t.Fatal(err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "delivered" || st.Next != nil {
		t.Fatalf("after delivery = %+v, want delivered with next_attempt_at null", st)
	}
}

func TestFinish_OtherEventsStillUseNextAttemptAt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	rows, _ := s.ClaimNotificationDeliveries(ctx, notifyT0, time.Minute, store.ClaimFilter{Limit: 5})
	if len(rows) != 1 {
		t.Fatalf("claim = %d rows", len(rows))
	}
	retryAt := notifyT0.Add(5 * time.Minute)
	if err := s.FinishNotificationDelivery(ctx, rows[0].ID, rows[0].ClaimedUntil, 1, store.DeliveryOutcome{Retryable: true, RetryAt: retryAt}, notifyT0); err != nil {
		t.Fatal(err)
	}
	st := readDiagnosis(t, s, rows[0].ID)
	if st.Next == nil || !st.Next.Equal(retryAt) || st.Due != nil {
		t.Fatalf("opened row after retryable finish = %+v, want next_attempt_at = retry time and no diagnosis_due_at", st)
	}
}

func TestRetry_FailedDiagnosisUsesItsOwnColumn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id, lease := claimOneDiagnosis(t, s)
	if err := s.FinishNotificationDelivery(ctx, id, lease, 3, store.DeliveryOutcome{Note: "refused"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "failed" {
		t.Fatalf("status = %s, want failed", st.Status)
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
	if rows, err := s.ClaimDiagnosisDeliveries(ctx, retryNow, time.Minute, store.ClaimFilter{Limit: 5}); err != nil || len(rows) != 1 {
		t.Fatalf("diagnosis claim after retry = %+v, %v, want the row", rows, err)
	}
}

func TestCancelUnsent_CancelsPendingDiagnosis(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0.Add(time.Hour))
	if err := s.CancelUnsentNotificationDeliveries(context.Background(), []int64{incident.ID}, "Resolved before it was sent.", "Held note", notifyT0); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetNotificationDelivery(context.Background(), id)
	if err != nil || d.Status != "cancelled" || d.Note != "Resolved before it was sent." || d.FinishedAt == nil {
		t.Fatalf("diagnosis row = %+v, %v, want cancelled with the pending note", d, err)
	}
	if rows, _ := s.ClaimDiagnosisDeliveries(context.Background(), notifyT0.Add(2*time.Hour), time.Minute, store.ClaimFilter{Limit: 5}); len(rows) != 0 {
		t.Fatalf("a cancelled row was claimed: %+v", rows)
	}
}

func TestCancelUnsent_LeavesAClaimedDiagnosisToFinish(t *testing.T) {
	s := storetest.Open(t)
	id, _ := claimOneDiagnosis(t, s)
	var incidentID int64
	if err := s.Pool.QueryRow(context.Background(), `select incident_id from notification_deliveries where id = $1`, id).Scan(&incidentID); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelUnsentNotificationDeliveries(context.Background(), []int64{incidentID}, "p", "h", notifyT0); err != nil {
		t.Fatal(err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "pending" || st.Claimed == nil {
		t.Fatalf("claimed row = %+v, want left pending under its lease", st)
	}
}

func TestDiagnosisConstraint_RefusesNextAttemptAtAndHeld(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	for name, stmt := range map[string]string{
		"next_attempt_at on a diagnosis row": `update notification_deliveries set next_attempt_at = now() where id = $1`,
		"held diagnosis row":                 `update notification_deliveries set status = 'held' where id = $1`,
		"due column on another event":        `update notification_deliveries set event = 'opened' where id = $1`,
	} {
		if _, err := s.Pool.Exec(ctx, stmt, id); err == nil {
			t.Errorf("%s was accepted, want the check constraint to refuse it", name)
		}
	}
	if st := readDiagnosis(t, s, id); st.Status != "pending" || st.Next != nil {
		t.Fatalf("row changed: %+v", st)
	}
}

func TestPlanStates_LastSentIgnoresDiagnosis(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	opened := delivery(route, incident, "opened", 0)
	opened.Status, opened.CreatedAt = "delivered", notifyT0
	mustPlan(t, s, opened)
	diag := diagnosisDelivery(route, incident)
	diag.CreatedAt = notifyT0.Add(time.Hour)
	mustInsertDiagnosis(t, s, diag, notifyT0)
	states, err := s.NotificationPlanStates(ctx, []int64{incident.ID})
	if err != nil {
		t.Fatal(err)
	}
	st := states[[2]int64{route.ID, incident.ID}]
	if st.LastSentAt == nil || !st.LastSentAt.Equal(notifyT0) || st.OpenedStatus != "delivered" || st.HasHeld || st.HasResolved || st.MaxRepeatSeq != 0 || st.MaxUpdateSeq != 0 {
		t.Fatalf("plan state = %+v, want the opened row's time and nothing from the diagnosis row", st)
	}
}

func TestMigration0220_KeepsExistingRowsAndOldEventsInsert(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	// Step back to the schema an older task sees: no diagnosis event, no due column.
	for _, stmt := range []string{
		`alter table notification_deliveries drop column diagnosis_due_at`,
		`alter table notification_deliveries drop constraint notification_deliveries_event_check`,
		`alter table notification_deliveries add constraint notification_deliveries_event_check
			check (event in ('opened', 'repeat', 'update', 'resolved', 'test'))`,
	} {
		if _, err := s.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i, ev := range []string{"opened", "repeat", "update", "resolved"} {
		mustPlan(t, s, delivery(route, incident, ev, i))
	}
	body, err := os.ReadFile("migrations/0220_notification_diagnosis.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply 0220 over existing rows: %v", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where diagnosis_due_at is null`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("existing rows with a null due column = %d, %v, want 4", n, err)
	}
	// An older task still writes only the old values, with the old column list.
	for i, ev := range []string{"opened", "repeat", "update", "resolved", "test"} {
		if _, err := s.Pool.Exec(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
			severity, alert_title, alert_text, status, next_attempt_at) values ($1, 'r', 'webhook', 't', $2, $3, $4, 'critical', 't', 'x', 'pending', now())`,
			route.ID, incident.ID, ev, 100+i); err != nil {
			t.Errorf("old-style insert of %q refused: %v", ev, err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `insert into notification_deliveries (route_id, route_name, destination, target, incident_id, event, seq,
		severity, alert_title, alert_text, status) values ($1, 'r', 'webhook', 't', $2, 'bogus', 200, 'critical', 't', 'x', 'pending')`, route.ID, incident.ID); err == nil {
		t.Error("an unknown event was accepted")
	}
	if ok, err := s.InsertDiagnosisDelivery(ctx, diagnosisDelivery(route, incident), notifyT0, nil); err != nil || !ok {
		t.Fatalf("diagnosis insert after the migration = %v, %v", ok, err)
	}
}

func TestInsertDiagnosis_RefusesAMissingRouteOrIncident(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	noRoute, noIncident := diagnosisDelivery(route, incident), diagnosisDelivery(route, incident)
	noRoute.RouteID, noIncident.IncidentID = nil, nil
	for name, d := range map[string]store.NotificationDelivery{"no route": noRoute, "no incident": noIncident} {
		if ok, err := s.InsertDiagnosisDelivery(context.Background(), d, notifyT0, nil); err == nil || ok {
			t.Errorf("%s: insert = %v, %v, want an error (a row without both ids would escape the unique key)", name, ok, err)
		}
	}
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from notification_deliveries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d, %v, want none", n, err)
	}
}

func TestInsertDiagnosis_IgnoresLeaseResultAndAttemptsFromTheCaller(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	d := diagnosisDelivery(route, incident)
	lease, finished, code, ms := notifyT0.Add(time.Hour), notifyT0, 200, 12
	d.ClaimedUntil, d.FinishedAt, d.StatusCode, d.DurationMS, d.Attempts = &lease, &finished, &code, &ms, 4
	id := mustInsertDiagnosis(t, s, d, notifyT0)
	got, err := s.GetNotificationDelivery(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimedUntil != nil || got.FinishedAt != nil || got.StatusCode != nil || got.DurationMS != nil || got.Attempts != 0 {
		t.Fatalf("row = %+v, want no lease, result or attempts (a lease would hide the row from its first claim)", got)
	}
}

func TestSummary_FailedDiagnosisNeverMakesARouteFailing(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	for seq := 1; seq <= 2; seq++ {
		d := delivery(route, incident, "repeat", seq)
		d.Status, d.NextAttemptAt = "failed", nil
		d.CreatedAt = notifyT0.Add(time.Duration(seq) * time.Minute)
		mustPlan(t, s, d)
	}
	diag := diagnosisDelivery(route, incident)
	diag.CreatedAt = notifyT0.Add(10 * time.Minute)
	id := mustInsertDiagnosis(t, s, diag, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'failed' where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	sum, err := s.NotificationSummary(ctx, notifyT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FailingRouteIDs) != 0 {
		t.Fatalf("failing routes = %v, want none: two failed alerts and one failed diagnosis are not three failed alerts", sum.FailingRouteIDs)
	}
	if sum.Failed24h != 2 {
		t.Fatalf("Failed24h = %d, want 2 (the failed diagnosis is not counted)", sum.Failed24h)
	}
}

func TestSummary_DeliveredDiagnosisDoesNotHideAFailingRoute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	for seq := 1; seq <= 3; seq++ {
		d := delivery(route, incident, "repeat", seq)
		d.Status, d.NextAttemptAt = "failed", nil
		d.CreatedAt = notifyT0.Add(time.Duration(seq) * time.Minute)
		mustPlan(t, s, d)
	}
	diag := diagnosisDelivery(route, incident)
	diag.CreatedAt = notifyT0.Add(10 * time.Minute) // newer than the three failures
	id := mustInsertDiagnosis(t, s, diag, notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'delivered', finished_at = $2 where id = $1`, id, notifyT0); err != nil {
		t.Fatal(err)
	}
	sum, err := s.NotificationSummary(ctx, notifyT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.FailingRouteIDs) != 1 || sum.FailingRouteIDs[0] != route.ID || sum.Failed24h != 3 {
		t.Fatalf("summary = %+v, want the route failing and 3 failed alerts: a delivered diagnosis is not an alert delivery", sum)
	}
}

func TestExpireStaleTests_LeavesAClaimedDiagnosisAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id, _ := claimOneDiagnosis(t, s) // pending, no next_attempt_at, lease set: the shape of an abandoned test row
	n, err := s.ExpireStaleNotificationTests(ctx, notifyT0.Add(time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("expired = %d, %v, want 0", n, err)
	}
	if st := readDiagnosis(t, s, id); st.Status != "pending" {
		t.Fatalf("status = %s, want pending: the lease expiring means reclaim, not failure", st.Status)
	}
}

func TestMaxRenderedGeneration_IgnoresADeliveredDiagnosis(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	id := mustInsertDiagnosis(t, s, diagnosisDelivery(route, incident), notifyT0)
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'delivered', details = '{"generation": 9}' where id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if g, err := s.MaxRenderedGeneration(ctx, route.ID, incident.ID); err != nil || g != 0 {
		t.Fatalf("generation = %d, %v, want 0: a diagnosis message shows no group generation", g, err)
	}
}
