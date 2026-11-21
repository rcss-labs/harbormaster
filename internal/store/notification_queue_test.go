package store_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var (
	notifyTarget = awsx.Target{AccountID: "111111111111", Region: "us-east-1"}
	notifyT0     = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
)

func notifyKey(service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: notifyTarget.AccountID, Region: notifyTarget.Region, Cluster: "core"}, Service: service}
}

func notifyAlert(service, kind string, severity model.AlertSeverity) model.Alert {
	return model.Alert{Key: notifyKey(service), Kind: kind, Severity: severity, Title: service + " " + kind, Text: "text of " + service, At: notifyT0, Tab: "overview"}
}

func setAlerts(t *testing.T, s *store.Store, alerts ...model.Alert) {
	t.Helper()
	if err := s.ReplaceAlerts(context.Background(), notifyTarget, alerts); err != nil {
		t.Fatal(err)
	}
}

func syncIncidents(t *testing.T, s *store.Store, now time.Time) (open, resolved []store.NotificationIncident) {
	t.Helper()
	open, resolved, err := s.SyncNotificationIncidents(context.Background(), now, 2*time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return open, resolved
}

func TestSyncNotificationIncidents_OpensOncePerIdentity(t *testing.T) {
	s := storetest.Open(t)
	setAlerts(t, s,
		notifyAlert("api", "service_failing", model.AlertDanger),
		notifyAlert("web", "rollout_in_progress", model.AlertInfo))
	open, _ := syncIncidents(t, s, notifyT0)
	if len(open) != 1 || open[0].Kind != "service_failing" || open[0].Key != notifyKey("api") || open[0].Severity != model.AlertDanger {
		t.Fatalf("open = %+v, want one danger incident for api (the info alert is not notified)", open)
	}
	again, _ := syncIncidents(t, s, notifyT0.Add(10*time.Second))
	if len(again) != 1 || again[0].ID != open[0].ID {
		t.Fatalf("second sync = %+v, want the same incident", again)
	}

	// Two writers syncing at once must not error or duplicate.
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.SyncNotificationIncidents(context.Background(), notifyT0.Add(20*time.Second), 2*time.Minute, time.Hour)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sync: %v", err)
		}
	}
	final, _ := syncIncidents(t, s, notifyT0.Add(30*time.Second))
	if len(final) != 1 {
		t.Fatalf("incidents after concurrent syncs = %d, want 1", len(final))
	}
}

func TestSyncNotificationIncidents_GraceMergesAFlap(t *testing.T) {
	s := storetest.Open(t)
	alert := notifyAlert("api", "service_failing", model.AlertDanger)
	setAlerts(t, s, alert)
	first, _ := syncIncidents(t, s, notifyT0)

	setAlerts(t, s)
	during, resolved := syncIncidents(t, s, notifyT0.Add(30*time.Second))
	if len(during) != 1 || during[0].MissingSince == nil || len(resolved) != 0 {
		t.Fatalf("while absent: open=%+v resolved=%+v, want the incident still open with missing_since set", during, resolved)
	}

	setAlerts(t, s, alert)
	back, _ := syncIncidents(t, s, notifyT0.Add(60*time.Second))
	if len(back) != 1 || back[0].ID != first[0].ID || back[0].MissingSince != nil {
		t.Fatalf("after the flap: %+v, want the same incident with missing_since cleared", back)
	}
}

func TestSyncNotificationIncidents_ResolvesAfterGrace(t *testing.T) {
	s := storetest.Open(t)
	alert := notifyAlert("api", "service_failing", model.AlertDanger)
	setAlerts(t, s, alert)
	first, _ := syncIncidents(t, s, notifyT0)

	setAlerts(t, s)
	syncIncidents(t, s, notifyT0.Add(10*time.Second))
	open, resolved := syncIncidents(t, s, notifyT0.Add(10*time.Second+121*time.Second))
	if len(open) != 0 || len(resolved) != 1 || resolved[0].ID != first[0].ID || resolved[0].ResolvedAt == nil {
		t.Fatalf("after grace: open=%+v resolved=%+v, want the incident resolved", open, resolved)
	}

	setAlerts(t, s, alert)
	reopened, _ := syncIncidents(t, s, notifyT0.Add(10*time.Minute))
	if len(reopened) != 1 || reopened[0].ID == first[0].ID {
		t.Fatalf("reappeared alert = %+v, want a new incident id", reopened)
	}
}

func TestSyncNotificationIncidents_RecentlyResolvedWindow(t *testing.T) {
	s := storetest.Open(t)
	setAlerts(t, s, notifyAlert("api", "service_failing", model.AlertDanger))
	syncIncidents(t, s, notifyT0)
	setAlerts(t, s)
	syncIncidents(t, s, notifyT0.Add(time.Second))
	syncIncidents(t, s, notifyT0.Add(200*time.Second)) // resolves
	_, resolved, err := s.SyncNotificationIncidents(context.Background(), notifyT0.Add(3*time.Hour), 2*time.Minute, time.Hour)
	if err != nil || len(resolved) != 0 {
		t.Fatalf("resolved outside the window = %+v err=%v, want none", resolved, err)
	}
}

func TestSyncNotificationIncidents_UnchangedWritesNothing(t *testing.T) {
	s := storetest.Open(t)
	setAlerts(t, s, notifyAlert("api", "service_failing", model.AlertDanger))
	syncIncidents(t, s, notifyT0)
	xmin := func() string {
		var v string
		if err := s.Pool.QueryRow(context.Background(), `select xmin::text from notification_incidents`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := xmin()
	syncIncidents(t, s, notifyT0.Add(10*time.Second))
	syncIncidents(t, s, notifyT0.Add(20*time.Second))
	if after := xmin(); after != before {
		t.Fatalf("xmin changed from %s to %s: a no-op sync wrote the row", before, after)
	}
}

func TestSyncNotificationIncidents_UpdatesTitleAndText(t *testing.T) {
	s := storetest.Open(t)
	alert := notifyAlert("api", "service_failing", model.AlertDanger)
	setAlerts(t, s, alert)
	syncIncidents(t, s, notifyT0)
	alert.Title, alert.Text = "api is failing harder", "new text"
	setAlerts(t, s, alert)
	open, _ := syncIncidents(t, s, notifyT0.Add(10*time.Second))
	if len(open) != 1 || open[0].Title != "api is failing harder" || open[0].Text != "new text" {
		t.Fatalf("open = %+v, want the new title and text", open)
	}
}

// --- deliveries ---

func seedIncidentAndRoute(t *testing.T, s *store.Store) (store.NotificationIncident, store.NotificationRoute) {
	t.Helper()
	setAlerts(t, s, notifyAlert("api", "service_failing", model.AlertDanger))
	open, _ := syncIncidents(t, s, notifyT0)
	route, err := s.CreateNotificationRoute(context.Background(), newRoute("route"), 0)
	if err != nil {
		t.Fatal(err)
	}
	return open[0], route
}

func delivery(route store.NotificationRoute, incident store.NotificationIncident, event string, seq int) store.NotificationDelivery {
	id, rid := incident.ID, route.ID
	due := notifyT0
	return store.NotificationDelivery{
		RouteID: &rid, RouteName: route.Name, Destination: route.Destination, Target: "hooks.example",
		IncidentID: &id, Event: event, Seq: seq, Severity: "critical", AlertTitle: incident.Title, AlertText: incident.Text,
		Key: incident.Key, Kind: incident.Kind, Status: "pending", NextAttemptAt: &due, CreatedAt: notifyT0,
	}
}

func mustPlan(t *testing.T, s *store.Store, d store.NotificationDelivery) store.NotificationDelivery {
	t.Helper()
	ok, err := s.PlanNotificationDelivery(context.Background(), d)
	if err != nil || !ok {
		t.Fatalf("plan = %v, %v", ok, err)
	}
	rows, _, err := s.ListNotificationDeliveries(context.Background(), store.DeliveryFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.RouteID != nil && d.RouteID != nil && *row.RouteID == *d.RouteID && row.Event == d.Event && row.Seq == d.Seq && *row.IncidentID == *d.IncidentID {
			return row
		}
	}
	t.Fatal("planned row not found")
	return store.NotificationDelivery{}
}

func TestPlanNotificationDelivery_SecondInsertIsNoOp(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	d := delivery(route, incident, "opened", 0)
	if ok, err := s.PlanNotificationDelivery(context.Background(), d); err != nil || !ok {
		t.Fatalf("first plan = %v, %v", ok, err)
	}
	if ok, err := s.PlanNotificationDelivery(context.Background(), d); err != nil || ok {
		t.Fatalf("second plan = %v, %v, want false with no error", ok, err)
	}
	if ok, _ := s.PlanNotificationDelivery(context.Background(), delivery(route, incident, "repeat", 1)); !ok {
		t.Fatal("a different event or seq must plan")
	}
}

func TestClaimDueNotificationDeliveries_TwoClaimersGetDisjointRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	route, err := s.CreateNotificationRoute(ctx, newRoute("route"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var alerts []model.Alert
	for i := range 10 {
		alerts = append(alerts, notifyAlert("svc"+string(rune('a'+i)), "service_failing", model.AlertDanger))
	}
	setAlerts(t, s, alerts...)
	open, _ := syncIncidents(t, s, notifyT0)
	for _, incident := range open {
		mustPlan(t, s, delivery(route, incident, "opened", 0))
	}
	var wg sync.WaitGroup
	results := make([][]store.NotificationDelivery, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10)
			if err != nil {
				t.Error(err)
			}
			results[i] = rows
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, rows := range results {
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatalf("delivery %d claimed twice", row.ID)
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != 10 {
		t.Fatalf("claimed %d distinct rows, want 10", len(seen))
	}
}

func TestClaim_SkipsFutureHeldAndLeasedRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setAlerts(t, s,
		notifyAlert("a", "service_failing", model.AlertDanger), notifyAlert("b", "service_failing", model.AlertDanger),
		notifyAlert("c", "service_failing", model.AlertDanger), notifyAlert("d", "service_failing", model.AlertDanger))
	open, _ := syncIncidents(t, s, notifyT0)
	route, _ := s.CreateNotificationRoute(ctx, newRoute("route"), 0)
	sort.Slice(open, func(i, j int) bool { return open[i].Key.Service < open[j].Key.Service })

	future := delivery(route, open[0], "opened", 0)
	later := notifyT0.Add(time.Hour)
	future.NextAttemptAt = &later
	held := delivery(route, open[1], "opened", 0)
	held.Status, held.NextAttemptAt = "held", nil
	heldUntil := notifyT0.Add(time.Hour)
	held.HeldUntil = &heldUntil
	due := delivery(route, open[2], "opened", 0)
	for _, d := range []store.NotificationDelivery{future, held, due} {
		mustPlan(t, s, d)
	}
	got, err := s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10)
	if err != nil || len(got) != 1 || got[0].IncidentID == nil || *got[0].IncidentID != open[2].ID {
		t.Fatalf("claimed %+v err=%v, want only the due row", got, err)
	}
	again, _ := s.ClaimDueNotificationDeliveries(ctx, notifyT0.Add(time.Minute), 2*time.Minute, 10)
	if len(again) != 0 {
		t.Fatalf("a row under a live lease was claimed again: %+v", again)
	}
}

func TestClaim_ExpiredLeaseIsReclaimed(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	if got, _ := s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10); len(got) != 1 {
		t.Fatalf("first claim = %+v", got)
	}
	got, err := s.ClaimDueNotificationDeliveries(ctx, notifyT0.Add(3*time.Minute), 2*time.Minute, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim after the lease expired = %+v err=%v, want the row again", got, err)
	}
}

func TestFinishNotificationDelivery_RetryThenFail(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	row := mustPlan(t, s, delivery(route, incident, "opened", 0))
	s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10)

	retryAt := notifyT0.Add(30 * time.Second)
	if err := s.FinishNotificationDelivery(ctx, row.ID, 1, store.DeliveryOutcome{Retryable: true, RetryAt: retryAt, StatusCode: 503, Note: "HTTP 503"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	got := listNotifDeliveries(t, s)[0]
	if got.Status != "pending" || got.Attempts != 1 || got.NextAttemptAt == nil || !got.NextAttemptAt.Equal(retryAt) || got.ClaimedUntil != nil {
		t.Fatalf("after a retryable outcome: %+v", got)
	}

	if err := s.FinishNotificationDelivery(ctx, row.ID, 2, store.DeliveryOutcome{StatusCode: 400, Note: "HTTP 400"}, retryAt); err != nil {
		t.Fatal(err)
	}
	got = listNotifDeliveries(t, s)[0]
	if got.Status != "failed" || got.Attempts != 2 || got.FinishedAt == nil || got.Note != "HTTP 400" {
		t.Fatalf("after a non-retryable outcome: %+v", got)
	}

	// A late writer cannot undo a settled row.
	if err := s.FinishNotificationDelivery(ctx, row.ID, 3, store.DeliveryOutcome{Delivered: true, Note: "late"}, retryAt); err != nil {
		t.Fatal(err)
	}
	if got = listNotifDeliveries(t, s)[0]; got.Status != "failed" || got.Note != "HTTP 400" {
		t.Fatalf("a finish on a failed row changed it: %+v", got)
	}
}

func TestFinishNotificationDelivery_DeliveredRowIsFinal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	row := mustPlan(t, s, delivery(route, incident, "opened", 0))
	s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10)
	if err := s.FinishNotificationDelivery(ctx, row.ID, 1, store.DeliveryOutcome{Delivered: true, StatusCode: 200, DurationMS: 42, Note: "Delivered in 42 ms"}, notifyT0); err != nil {
		t.Fatal(err)
	}
	s.FinishNotificationDelivery(ctx, row.ID, 2, store.DeliveryOutcome{StatusCode: 500, Note: "HTTP 500"}, notifyT0)
	got := listNotifDeliveries(t, s)[0]
	if got.Status != "delivered" || got.Note != "Delivered in 42 ms" || got.StatusCode == nil || *got.StatusCode != 200 || got.DurationMS == nil || *got.DurationMS != 42 {
		t.Fatalf("delivered row = %+v", got)
	}
}

func listNotifDeliveries(t *testing.T, s *store.Store) []store.NotificationDelivery {
	t.Helper()
	rows, _, err := s.ListNotificationDeliveries(context.Background(), store.DeliveryFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestReleaseHeld_SendsWhenOpenCancelsWhenResolved(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setAlerts(t, s, notifyAlert("a", "service_degraded", model.AlertWarning), notifyAlert("b", "service_degraded", model.AlertWarning))
	open, _ := syncIncidents(t, s, notifyT0)
	route, _ := s.CreateNotificationRoute(ctx, newRoute("route"), 0)
	sort.Slice(open, func(i, j int) bool { return open[i].Key.Service < open[j].Key.Service })
	release := notifyT0.Add(8 * time.Hour)
	for _, incident := range open {
		d := delivery(route, incident, "opened", 0)
		d.Status, d.NextAttemptAt, d.HeldUntil = "held", nil, &release
		mustPlan(t, s, d)
	}
	// Incident b resolves.
	setAlerts(t, s, notifyAlert("a", "service_degraded", model.AlertWarning))
	syncIncidents(t, s, notifyT0.Add(time.Minute))
	syncIncidents(t, s, notifyT0.Add(5*time.Minute))

	if err := s.ReleaseHeldNotificationDeliveries(ctx, notifyT0.Add(time.Hour), "Resolved during quiet hours; not sent."); err != nil {
		t.Fatal(err)
	}
	for _, d := range listNotifDeliveries(t, s) {
		if d.Status != "held" && *d.IncidentID == open[0].ID {
			t.Fatalf("a held row before its release time changed: %+v", d)
		}
	}
	if err := s.ReleaseHeldNotificationDeliveries(ctx, release, "Resolved during quiet hours; not sent."); err != nil {
		t.Fatal(err)
	}
	byService := map[string]store.NotificationDelivery{}
	for _, d := range listNotifDeliveries(t, s) {
		byService[d.Key.Service] = d
	}
	if a := byService["a"]; a.Status != "pending" || a.NextAttemptAt == nil || !a.NextAttemptAt.Equal(release) {
		t.Fatalf("open incident's held row = %+v, want pending and due at the release time", a)
	}
	if b := byService["b"]; b.Status != "cancelled" || b.Note != "Resolved during quiet hours; not sent." {
		t.Fatalf("resolved incident's held row = %+v, want cancelled with the note", b)
	}
}

func TestCancelUnsentNotificationDeliveries_SkipsLeasedAndSentRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	repeat := delivery(route, incident, "repeat", 1)
	mustPlan(t, s, repeat)
	held := delivery(route, incident, "repeat", 2)
	releaseAt := notifyT0.Add(8 * time.Hour)
	held.Status, held.NextAttemptAt, held.HeldUntil = "held", nil, &releaseAt
	mustPlan(t, s, held)
	// Lease only the opened row (it is the oldest by id).
	claimed, _ := s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 1)
	if len(claimed) != 1 {
		t.Fatal("expected one claimed row")
	}
	if err := s.CancelUnsentNotificationDeliveries(ctx, []int64{incident.ID}, "Resolved before it was sent.", "Resolved during quiet hours; not sent.", notifyT0); err != nil {
		t.Fatal(err)
	}
	for _, d := range listNotifDeliveries(t, s) {
		switch {
		case d.Event == "opened":
			if d.Status != "pending" {
				t.Errorf("leased row changed to %s", d.Status)
			}
		case d.Seq == 1:
			if d.Status != "cancelled" || d.Note != "Resolved before it was sent." {
				t.Errorf("pending repeat row = %+v, want cancelled with the pending note", d)
			}
		case d.Seq == 2:
			if d.Status != "cancelled" || d.Note != "Resolved during quiet hours; not sent." {
				t.Errorf("held repeat row = %+v, want cancelled with the quiet hours note", d)
			}
		}
	}
}

func TestRetryNotificationDelivery_OnlyFailedWithLiveRoute(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	row := mustPlan(t, s, delivery(route, incident, "opened", 0))
	if _, err := s.RetryNotificationDelivery(ctx, row.ID, notifyT0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("retry of a pending row = %v, want ErrConflict", err)
	}
	if _, err := s.RetryNotificationDelivery(ctx, 999999, notifyT0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retry of an unknown id = %v, want ErrNotFound", err)
	}
	s.ClaimDueNotificationDeliveries(ctx, notifyT0, 2*time.Minute, 10)
	s.FinishNotificationDelivery(ctx, row.ID, 3, store.DeliveryOutcome{StatusCode: 503, Note: "HTTP 503 after 3 attempts"}, notifyT0)

	retried, err := s.RetryNotificationDelivery(ctx, row.ID, notifyT0.Add(time.Hour))
	if err != nil || retried.Status != "pending" || retried.Attempts != 0 || retried.Retries != 1 || retried.FinishedAt != nil {
		t.Fatalf("retry = %+v err=%v, want pending with attempts 0 and retries 1", retried, err)
	}

	s.ClaimDueNotificationDeliveries(ctx, notifyT0.Add(time.Hour), 2*time.Minute, 10)
	s.FinishNotificationDelivery(ctx, row.ID, 1, store.DeliveryOutcome{StatusCode: 400}, notifyT0.Add(time.Hour))
	if err := s.DeleteNotificationRoute(ctx, route.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryNotificationDelivery(ctx, row.ID, notifyT0.Add(2*time.Hour)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("retry after the route was deleted = %v, want ErrConflict", err)
	}
}

func TestDeleteNotificationRoute_KeepsItsLogRows(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	if err := s.DeleteNotificationRoute(context.Background(), route.ID); err != nil {
		t.Fatal(err)
	}
	rows := listNotifDeliveries(t, s)
	if len(rows) != 1 || rows[0].RouteID != nil || rows[0].RouteName != "route" {
		t.Fatalf("rows = %+v, want the row kept with a null route id and its name", rows)
	}
}

func TestListNotificationDeliveries_FilterAndCursor(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	route, _ := s.CreateNotificationRoute(ctx, newRoute("route"), 0)
	for i := range 5 {
		d := store.NotificationDelivery{
			RouteName: route.Name, Destination: "webhook", Target: "hooks.example", Event: "test", Severity: "warning",
			AlertTitle: "Test message", Status: "delivered", CreatedAt: notifyT0.Add(time.Duration(i) * time.Minute),
		}
		if i%2 == 1 {
			d.Status = "failed"
		}
		if _, err := s.InsertNotificationTest(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	failed, _, err := s.ListNotificationDeliveries(ctx, store.DeliveryFilter{Status: "failed", Limit: 10})
	if err != nil || len(failed) != 2 {
		t.Fatalf("failed filter = %d rows err=%v, want 2", len(failed), err)
	}
	var ids []int64
	var cursor *store.DeliveryCursor
	for range 5 {
		page, next, err := s.ListNotificationDeliveries(ctx, store.DeliveryFilter{Limit: 2, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page {
			ids = append(ids, d.ID)
		}
		if next == nil {
			break
		}
		cursor = next
	}
	if len(ids) != 5 {
		t.Fatalf("paged %d rows, want 5", len(ids))
	}
	seen := map[int64]bool{}
	for i, id := range ids {
		if seen[id] {
			t.Fatalf("row %d repeated across pages", id)
		}
		seen[id] = true
		if i > 0 && id >= ids[i-1] {
			t.Fatalf("rows are not newest first: %v", ids)
		}
	}
}

func TestNotificationSummary_CountsFailuresAndFailingRoutes(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	failing, _ := s.CreateNotificationRoute(ctx, newRoute("failing"), 0)
	mixed, _ := s.CreateNotificationRoute(ctx, newRoute("mixed"), 0)
	testsOnly, _ := s.CreateNotificationRoute(ctx, newRoute("tests"), 0)
	setAlerts(t, s,
		notifyAlert("a", "service_failing", model.AlertDanger), notifyAlert("b", "service_failing", model.AlertDanger),
		notifyAlert("c", "service_failing", model.AlertDanger))
	open, _ := syncIncidents(t, s, notifyT0)
	sort.Slice(open, func(i, j int) bool { return open[i].Key.Service < open[j].Key.Service })
	now := notifyT0.Add(time.Hour)

	record := func(route store.NotificationRoute, incident store.NotificationIncident, status string, at time.Time) {
		d := delivery(route, incident, "opened", 0)
		d.CreatedAt, d.Status = at, status
		mustPlan(t, s, d)
	}
	for i, incident := range open {
		at := notifyT0.Add(time.Duration(i) * time.Minute)
		record(failing, incident, "failed", at)
		status := "failed"
		if i == 1 {
			status = "delivered"
		}
		record(mixed, incident, status, at)
	}
	for i := range 3 {
		d := store.NotificationDelivery{RouteID: &testsOnly.ID, RouteName: "tests", Destination: "webhook", Target: "x", Event: "test",
			Severity: "warning", AlertTitle: "Test message", Status: "failed", CreatedAt: notifyT0.Add(time.Duration(i) * time.Minute)}
		if _, err := s.InsertNotificationTest(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// An old failure is outside the 24 hour count.
	old := store.NotificationDelivery{RouteName: "gone", Destination: "webhook", Target: "x", Event: "test", Severity: "warning",
		AlertTitle: "Test message", Status: "failed", CreatedAt: notifyT0.Add(-48 * time.Hour)}
	if _, err := s.InsertNotificationTest(ctx, old); err != nil {
		t.Fatal(err)
	}

	sum, err := s.NotificationSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// failing: 3, mixed: 2, tests: 3 failed tests; the 48 hour old one is excluded.
	if sum.Failed24h != 8 {
		t.Errorf("Failed24h = %d, want 8", sum.Failed24h)
	}
	if len(sum.FailingRouteIDs) != 1 || sum.FailingRouteIDs[0] != failing.ID {
		t.Errorf("FailingRouteIDs = %v, want only %d (a delivered row among the last three, and tests, do not count)", sum.FailingRouteIDs, failing.ID)
	}
}

func TestRunRetention_PrunesNotificationLog(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	old := delivery(route, incident, "opened", 0)
	old.CreatedAt = notifyT0.Add(-40 * 24 * time.Hour)
	mustPlan(t, s, old)
	fresh := delivery(route, incident, "repeat", 1)
	mustPlan(t, s, fresh)
	// A long-resolved incident and a recent one.
	setAlerts(t, s)
	syncIncidents(t, s, notifyT0.Add(time.Minute))
	syncIncidents(t, s, notifyT0.Add(10*time.Minute)) // resolves the first incident
	if _, err := s.Pool.Exec(ctx, `update notification_incidents set resolved_at = $1`, notifyT0.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	setAlerts(t, s, notifyAlert("web", "service_failing", model.AlertDanger))
	syncIncidents(t, s, notifyT0.Add(20*time.Minute))

	res, err := s.RunRetention(ctx, notifyT0.Add(30*time.Minute), store.Retention{NotificationLog: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res["notification_deliveries"] < 1 || res["notification_incidents"] != 1 {
		t.Fatalf("retention result = %v, want old deliveries and the old resolved incident pruned", res)
	}
	var incidents int
	s.Pool.QueryRow(ctx, `select count(*) from notification_incidents`).Scan(&incidents)
	if incidents != 1 {
		t.Fatalf("incidents left = %d, want the open one", incidents)
	}
}
