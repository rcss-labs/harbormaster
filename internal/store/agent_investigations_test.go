package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const invWindow = 15 * time.Minute

var invNow = notifyT0.Add(5 * time.Minute)

func invRoute(t *testing.T, s *store.Store, name string, investigate, enabled bool) store.NotificationRoute {
	t.Helper()
	r := newRoute(name)
	r.Investigate, r.Enabled = investigate, enabled
	created, err := s.CreateNotificationRoute(context.Background(), r, 0)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// invIncidents opens one alert incident per (service, kind) pair.
func invIncidents(t *testing.T, s *store.Store, alerts ...model.Alert) []store.NotificationIncident {
	t.Helper()
	setAlerts(t, s, alerts...)
	open, _ := syncIncidents(t, s, notifyT0)
	if len(open) != len(alerts) {
		t.Fatalf("opened %d incidents, want %d", len(open), len(alerts))
	}
	return open
}

func invDelivery(t *testing.T, s *store.Store, route store.NotificationRoute, inc store.NotificationIncident, event, status string, createdAt time.Time) {
	t.Helper()
	d := delivery(route, inc, event, 0)
	d.Status, d.CreatedAt = status, createdAt
	if event == "repeat" || event == "update" {
		d.Seq = 1
	}
	if ok, err := s.PlanNotificationDelivery(context.Background(), d); err != nil || !ok {
		t.Fatalf("plan %s = %v, %v", event, ok, err)
	}
}

func invOpened(t *testing.T, s *store.Store, route store.NotificationRoute, inc store.NotificationIncident) {
	t.Helper()
	invDelivery(t, s, route, inc, "opened", "pending", notifyT0)
}

func invRecord(t *testing.T, s *store.Store, now time.Time) (created, linked int) {
	t.Helper()
	created, linked, err := s.RecordInvestigationRequests(context.Background(), now, invWindow, 100)
	if err != nil {
		t.Fatal(err)
	}
	return created, linked
}

func invCount(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecord_OneInvestigationPerSubjectPerWindow(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertDanger), notifyAlert("api", "k3", model.AlertDanger))
	for _, inc := range incs {
		invOpened(t, s, route, inc)
	}
	created, linked := invRecord(t, s, invNow)
	if created != 1 || linked != 2 {
		t.Fatalf("created, linked = %d, %d; want 1, 2", created, linked)
	}
	if n := invCount(t, s, "agent_investigations"); n != 1 {
		t.Fatalf("investigations = %d, want 1", n)
	}
	if n := invCount(t, s, "agent_investigation_incidents"); n != 3 {
		t.Fatalf("links = %d, want 3", n)
	}
	var kind, key, state string
	var requested time.Time
	if err := s.Pool.QueryRow(context.Background(), `select subject_kind, subject_key, state, requested_at from agent_investigations`).Scan(&kind, &key, &state, &requested); err != nil {
		t.Fatal(err)
	}
	if kind != "service" || key != "service/111111111111/us-east-1/core/api" || state != "requested" || !requested.Equal(invNow) {
		t.Fatalf("row = %s %s %s %v", kind, key, state, requested)
	}
}

func TestRecord_NewInvestigationAfterTheWindow(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertDanger))
	invOpened(t, s, route, incs[0])
	first := notifyT0.Add(time.Minute)
	if created, _ := invRecord(t, s, first); created != 1 {
		t.Fatalf("first created = %d", created)
	}
	invDelivery(t, s, route, incs[1], "opened", "pending", notifyT0.Add(18*time.Minute))
	// Exactly one window after the first request the old investigation no longer absorbs the incident.
	if created, _ := invRecord(t, s, first.Add(invWindow)); created != 1 {
		t.Fatalf("second created = %d, want a new investigation after the window", created)
	}
	if n := invCount(t, s, "agent_investigations"); n != 2 {
		t.Fatalf("investigations = %d, want 2", n)
	}
}

func TestRecord_TwoRoutesOneInvestigation(t *testing.T) {
	s := storetest.Open(t)
	r1, r2 := invRoute(t, s, "r1", true, true), invRoute(t, s, "r2", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertDanger))
	invOpened(t, s, r1, incs[0])
	invOpened(t, s, r2, incs[0]) // the same incident announced on both routes
	invOpened(t, s, r2, incs[1])
	if created, _ := invRecord(t, s, invNow); created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if invCount(t, s, "agent_investigations") != 1 || invCount(t, s, "agent_investigation_incidents") != 2 {
		t.Fatal("want one investigation and two links")
	}
}

func TestRecord_GroupIncidentIsItsOwnSubject(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	svc := invIncidents(t, s, notifyAlert("a", "service_failing", model.AlertDanger))[0]
	groups, _ := syncGroups(t, s, notifyT0, clusterGroupState(route.ID, member("a", false), member("b", false)))
	invOpened(t, s, route, svc)
	invOpened(t, s, route, groups[0])
	if created, _ := invRecord(t, s, invNow); created != 2 {
		t.Fatalf("created = %d, want the group and its member as two", created)
	}
	var key string
	var members string
	if err := s.Pool.QueryRow(context.Background(), `select subject_key, members::text from agent_investigations where subject_kind = 'cluster'`).Scan(&key, &members); err != nil {
		t.Fatal(err)
	}
	if key != "cluster/111111111111/us-east-1/core" || !strings.Contains(members, `"service": "a"`) || !strings.Contains(members, `"service": "b"`) {
		t.Fatalf("group row key=%s members=%s", key, members)
	}
}

func TestRecord_TwoRoutesSameClusterEntryShareOne(t *testing.T) {
	s := storetest.Open(t)
	r1, r2 := invRoute(t, s, "r1", true, true), invRoute(t, s, "r2", true, true)
	g1, _ := syncGroups(t, s, notifyT0, clusterGroupState(r1.ID, member("a", false), member("b", false)), clusterGroupState(r2.ID, member("a", false), member("b", false)))
	if len(g1) != 2 {
		t.Fatalf("groups = %d", len(g1))
	}
	invOpened(t, s, r1, g1[0])
	invOpened(t, s, r2, g1[1])
	if created, _ := invRecord(t, s, invNow); created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if invCount(t, s, "agent_investigation_incidents") != 2 {
		t.Fatal("both group incidents should link")
	}
}

func TestRecord_IgnoresRepeatUpdateResolvedAndTestRows(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("web", "k1", model.AlertDanger))
	for _, event := range []string{"repeat", "update", "resolved"} {
		invDelivery(t, s, route, incs[0], event, "pending", notifyT0)
	}
	invDelivery(t, s, route, incs[1], "opened", "cancelled", notifyT0)
	if _, err := s.InsertNotificationTest(context.Background(), store.NotificationDelivery{
		RouteName: "r", Destination: "webhook", Target: "t", Event: "test", Severity: "critical", Status: "delivered", CreatedAt: notifyT0,
	}); err != nil {
		t.Fatal(err)
	}
	if created, linked := invRecord(t, s, invNow); created != 0 || linked != 0 {
		t.Fatalf("created, linked = %d, %d; want none", created, linked)
	}
}

func TestRecord_IgnoresRoutesWithTheSwitchOffOrDisabled(t *testing.T) {
	s := storetest.Open(t)
	off, disabled, gone := invRoute(t, s, "off", false, true), invRoute(t, s, "disabled", true, false), invRoute(t, s, "gone", true, true)
	incs := invIncidents(t, s, notifyAlert("a", "k", model.AlertDanger), notifyAlert("b", "k", model.AlertDanger), notifyAlert("c", "k", model.AlertDanger))
	invOpened(t, s, off, incs[0])
	invOpened(t, s, disabled, incs[1])
	invOpened(t, s, gone, incs[2])
	if err := s.DeleteNotificationRoute(context.Background(), gone.ID); err != nil {
		t.Fatal(err)
	}
	if created, _ := invRecord(t, s, invNow); created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	// Turning the switch on later makes the same still-recent row a candidate.
	on := off
	on.Investigate = true
	if _, err := s.UpdateNotificationRoute(context.Background(), on, 0); err != nil {
		t.Fatal(err)
	}
	if created, _ := invRecord(t, s, invNow); created != 1 {
		t.Fatalf("created after the switch = %d, want 1", created)
	}
}

func TestRecord_IgnoresOpenedRowsOlderThanTheWindow(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("a", "k", model.AlertDanger), notifyAlert("b", "k", model.AlertDanger))
	invDelivery(t, s, route, incs[0], "opened", "pending", invNow.Add(-invWindow)) // exactly one window old
	invDelivery(t, s, route, incs[1], "opened", "pending", invNow.Add(-invWindow+time.Second))
	if created, _ := invRecord(t, s, invNow); created != 1 {
		t.Fatalf("created = %d, want only the row inside the window", created)
	}
}

func TestRecord_IgnoresAccountLevelAlerts(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("", "account_unreachable", model.AlertDanger))
	invOpened(t, s, route, incs[0])
	if created, _ := invRecord(t, s, invNow); created != 0 {
		t.Fatalf("created = %d, want 0 for an alert with no service", created)
	}
}

func TestRecord_IdempotentAcrossCycles(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	for _, inc := range invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("web", "k1", model.AlertDanger)) {
		invOpened(t, s, route, inc)
	}
	if created, _ := invRecord(t, s, invNow); created != 2 {
		t.Fatalf("first cycle created = %d", created)
	}
	for i := 1; i <= 3; i++ {
		if created, linked := invRecord(t, s, invNow.Add(time.Duration(i)*time.Minute)); created != 0 || linked != 0 {
			t.Fatalf("cycle %d created, linked = %d, %d; want none", i, created, linked)
		}
	}
	if invCount(t, s, "agent_investigations") != 2 || invCount(t, s, "agent_investigation_incidents") != 2 {
		t.Fatal("repeat cycles changed the rows")
	}
}

func TestRecord_ConcurrentCallersCreateOne(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	for _, inc := range invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("api", "k2", model.AlertDanger), notifyAlert("api", "k3", model.AlertDanger)) {
		invOpened(t, s, route, inc)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, _, err := s.RecordInvestigationRequests(context.Background(), invNow, invWindow, 100)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total += created
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 || invCount(t, s, "agent_investigations") != 1 || invCount(t, s, "agent_investigation_incidents") != 3 {
		t.Fatalf("created %d, investigations %d, links %d; want 1, 1, 3", total, invCount(t, s, "agent_investigations"), invCount(t, s, "agent_investigation_incidents"))
	}
}

// invSeed records one investigation per service, requested at the given times, and returns their ids oldest first.
func invSeed(t *testing.T, s *store.Store, services []string, at ...time.Time) []int64 {
	t.Helper()
	route := invRoute(t, s, "r", true, true)
	alerts := make([]model.Alert, len(services))
	for i, svc := range services {
		alerts[i] = notifyAlert(svc, "k", model.AlertDanger)
	}
	incs := invIncidents(t, s, alerts...)
	for i, inc := range incs {
		invOpened(t, s, route, inc)
		invRecord(t, s, at[i])
	}
	rows, err := s.Pool.Query(context.Background(), `select id from agent_investigations order by requested_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != len(services) {
		t.Fatalf("seeded %d investigations, want %d", len(ids), len(services))
	}
	return ids
}

func TestClaim_OldestFirstAndSkipsLocked(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := invSeed(t, s, []string{"a", "b"}, invNow, invNow.Add(time.Minute))
	claimAt := invNow.Add(10 * time.Minute)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select 1 from agent_investigations where id = $1 for update`, ids[0]); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimInvestigation(ctx, claimAt, time.Minute)
	if err != nil || got.ID != ids[1] {
		t.Fatalf("claim past a locked row = %+v, %v; want %d", got, err, ids[1])
	}
	tx.Rollback(ctx) //nolint:errcheck

	first, err := s.ClaimInvestigation(ctx, claimAt, time.Minute)
	if err != nil || first.ID != ids[0] || first.State != "running" || first.Attempts != 1 || first.ClaimedUntil == nil || !first.ClaimedUntil.Equal(claimAt.Add(time.Minute)) {
		t.Fatalf("oldest claim = %+v, %v", first, err)
	}
	if _, err := s.ClaimInvestigation(ctx, claimAt, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim with nothing due = %v, want ErrNotFound", err)
	}
}

func TestClaim_ReclaimsAnExpiredRunOnce(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	first, err := s.ClaimInvestigation(ctx, invNow, time.Minute)
	if err != nil || first.Attempts != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if _, err := s.ClaimInvestigation(ctx, invNow.Add(30*time.Second), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a live lease was claimed: %v", err)
	}
	second, err := s.ClaimInvestigation(ctx, invNow.Add(2*time.Minute), time.Minute)
	if err != nil || second.ID != first.ID || second.Attempts != 2 || second.State != "running" {
		t.Fatalf("reclaim = %+v, %v", second, err)
	}
}

func TestClaim_SecondExpiryFailsAsInterrupted(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	if _, err := s.ClaimInvestigation(ctx, invNow, time.Minute); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimInvestigation(ctx, invNow.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	late := invNow.Add(10 * time.Minute)
	if got, err := s.ClaimInvestigation(ctx, late, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("third claim = %+v, %v; want ErrNotFound", got, err)
	}
	row, err := s.GetInvestigation(ctx, second.ID)
	if err != nil || row.State != "failed" || row.Reason != "interrupted" || row.Attempts != 2 || row.FinishedAt == nil || !row.FinishedAt.Equal(late) || row.ClaimedUntil != nil {
		t.Fatalf("row = %+v, %v", row, err)
	}
}

func claimed(t *testing.T, s *store.Store, at time.Time) store.Investigation {
	t.Helper()
	inv, err := s.ClaimInvestigation(context.Background(), at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func viewEvidence(excerpt string) store.InvestigationEvidence {
	return store.InvestigationEvidence{Tool: "get_service", Action: "view", AccountID: "111111111111", Region: "us-east-1", ClusterName: "core", ServiceName: "api", Note: "n", Excerpt: excerpt}
}

func TestFinish_ConflictForAStaleAttempt(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	first := claimed(t, s, invNow)
	second := claimed(t, s, invNow.Add(2*time.Minute))
	done := &store.InvestigationResult{Summary: "stale", Evidence: []store.InvestigationEvidence{viewEvidence("old")}}
	if err := s.FinishInvestigation(ctx, first.ID, first.Attempts, "done", "", done, invNow.Add(3*time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale attempt = %v, want ErrConflict", err)
	}
	row, _ := s.GetInvestigation(ctx, first.ID)
	if row.State != "running" || row.Summary != "" || invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatalf("the stale finish wrote: %+v", row)
	}
	good := &store.InvestigationResult{Summary: "fresh", SummaryReplaced: true, EvidenceDropped: 2, Evidence: []store.InvestigationEvidence{viewEvidence("one"), viewEvidence("two")}}
	if err := s.FinishInvestigation(ctx, second.ID, second.Attempts, "done", "", good, invNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, _ = s.GetInvestigation(ctx, second.ID)
	if row.State != "done" || row.Summary != "fresh" || !row.SummaryReplaced || row.EvidenceDropped != 2 || row.ClaimedUntil != nil || row.FinishedAt == nil || !row.FinishedAt.Equal(invNow.Add(4*time.Minute)) {
		t.Fatalf("finished row = %+v", row)
	}
	ev, err := s.ListInvestigationEvidence(ctx, second.ID)
	if err != nil || len(ev) != 2 || ev[0].Ordinal != 1 || ev[1].Ordinal != 2 || ev[0].Excerpt != "one" || ev[1].Excerpt != "two" {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
	// A second finish on the settled row is refused and the evidence stays.
	if err := s.FinishInvestigation(ctx, second.ID, second.Attempts, "failed", "error", nil, invNow.Add(5*time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("finish of a settled row = %v, want ErrConflict", err)
	}
}

func TestFinish_RefusesUnknownEvidenceAction(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	bad := func(mut func(*store.InvestigationEvidence)) *store.InvestigationResult {
		e := viewEvidence("x")
		mut(&e)
		return &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("ok"), e}}
	}
	thirteen := &store.InvestigationResult{Summary: "s"}
	for i := 0; i < 13; i++ {
		thirteen.Evidence = append(thirteen.Evidence, viewEvidence("x"))
	}
	cases := map[string]*store.InvestigationResult{
		"unknown action":  bad(func(e *store.InvestigationEvidence) { e.Action = "restart" }),
		"empty action":    bad(func(e *store.InvestigationEvidence) { e.Action = "" }),
		"empty account":   bad(func(e *store.InvestigationEvidence) { e.AccountID = "" }),
		"empty region":    bad(func(e *store.InvestigationEvidence) { e.Region = "" }),
		"empty cluster":   bad(func(e *store.InvestigationEvidence) { e.ClusterName = "" }),
		"empty service":   bad(func(e *store.InvestigationEvidence) { e.ServiceName = "" }),
		"empty summary":   {Summary: ""},
		"blank summary":   {Summary: "  \n\t"},
		"too many items":  thirteen,
		"oversized note":  bad(func(e *store.InvestigationEvidence) { e.Note = strings.Repeat("n", 301) }),
		"oversized words": {Summary: strings.Repeat("s", 1201)},
	}
	for name, result := range cases {
		if err := s.FinishInvestigation(ctx, inv.ID, inv.Attempts, "done", "", result, invNow); err == nil || errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	row, _ := s.GetInvestigation(ctx, inv.ID)
	if row.State != "running" || invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatalf("a refused finish wrote: %+v", row)
	}
}

func TestFinish_RefusesAnOversizedExcerpt(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	for name, excerpt := range map[string]string{
		"1501 bytes":           strings.Repeat("x", 1501),
		"multibyte over bytes": strings.Repeat("é", 751), // 751 characters, 1502 bytes
	} {
		err := s.FinishInvestigation(ctx, inv.ID, inv.Attempts, "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence(excerpt)}}, invNow)
		if err == nil || errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatal("an oversized excerpt was written")
	}
	// The cap is on bytes and 1500 exactly fits.
	if err := s.FinishInvestigation(ctx, inv.ID, inv.Attempts, "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence(strings.Repeat("x", 1500))}}, invNow); err != nil {
		t.Fatalf("1500 bytes refused: %v", err)
	}
}

func TestRelease_GivesTheAttemptBackWhenAsked(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	retry := invNow.Add(5 * time.Minute)
	if err := s.ReleaseInvestigation(ctx, inv.ID, inv.Attempts, retry, false); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, inv.ID)
	if row.State != "requested" || row.Attempts != 0 || row.ClaimedUntil == nil || !row.ClaimedUntil.Equal(retry) {
		t.Fatalf("released = %+v", row)
	}
	if _, err := s.ClaimInvestigation(ctx, invNow.Add(time.Minute), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claimed before the retry time: %v", err)
	}
	again := claimed(t, s, retry)
	if again.Attempts != 1 {
		t.Fatalf("attempts after a free release = %d, want 1", again.Attempts)
	}
	if err := s.ReleaseInvestigation(ctx, again.ID, again.Attempts, retry.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	row, _ = s.GetInvestigation(ctx, inv.ID)
	if row.State != "requested" || row.Attempts != 1 {
		t.Fatalf("counted release = %+v, want attempts 1", row)
	}
	if err := s.ReleaseInvestigation(ctx, again.ID, again.Attempts, retry, true); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("release of a row that is not running = %v, want ErrConflict", err)
	}
}

func TestInvestigationLifecycle_StartSkipAndCounts(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := invSeed(t, s, []string{"a", "b"}, invNow, invNow.Add(time.Minute))
	inv := claimed(t, s, invNow.Add(2*time.Minute))
	runID := uuid.New()
	if err := s.StartInvestigationRun(ctx, inv.ID, inv.Attempts+1, runID, "p", "m"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("start with a wrong attempt = %v, want ErrConflict", err)
	}
	if err := s.StartInvestigationRun(ctx, inv.ID, inv.Attempts, runID, "p", "m"); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, inv.ID)
	if row.RunID == nil || *row.RunID != runID || row.Provider != "p" || row.Model != "m" || row.StartedAt == nil {
		t.Fatalf("started = %+v", row)
	}
	if n, err := s.InvestigationsStartedSince(ctx, time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("started since = %d, %v; want 1", n, err)
	}
	if n, _ := s.InvestigationsStartedSince(ctx, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("started in the future = %d", n)
	}
	if err := s.SkipInvestigation(ctx, ids[1], "budget", invNow); err != nil {
		t.Fatal(err)
	}
	skipped, _ := s.GetInvestigation(ctx, ids[1])
	if skipped.State != "skipped" || skipped.Reason != "budget" || skipped.FinishedAt == nil {
		t.Fatalf("skipped = %+v", skipped)
	}
	if err := s.SkipInvestigation(ctx, ids[1], "budget", invNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("skip of a skipped row = %v, want ErrConflict", err)
	}
	if err := s.SkipInvestigation(ctx, 999999, "budget", invNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("skip of a missing row = %v, want ErrNotFound", err)
	}
	if err := s.SkipInvestigation(ctx, inv.ID, "made_up", invNow); err == nil || errors.Is(err, store.ErrConflict) {
		t.Fatalf("skip with an unknown reason = %v, want a refusal", err)
	}
}

func TestInvestigationReads_ServiceListCursorAndOpenIncidents(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k1", model.AlertDanger), notifyAlert("web", "k1", model.AlertDanger))
	for _, inc := range incs {
		invOpened(t, s, route, inc)
	}
	invRecord(t, s, invNow)
	api := notifyKey("api")
	got, err := s.ListServiceInvestigations(ctx, api, 10)
	if err != nil || len(got) != 1 || got[0].ServiceName != "api" {
		t.Fatalf("service list = %+v, %v", got, err)
	}
	page, err := s.ListInvestigations(ctx, store.InvestigationFilter{Limit: 1})
	if err != nil || len(page) != 1 {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := s.ListInvestigations(ctx, store.InvestigationFilter{Limit: 5, BeforeAt: &page[0].RequestedAt, BeforeID: page[0].ID})
	if err != nil || len(next) != 1 || next[0].ID == page[0].ID {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	open, err := s.InvestigationsForOpenIncidents(ctx, []model.ServiceKey{api, notifyKey("web"), notifyKey("none")})
	if err != nil || len(open) != 2 {
		t.Fatalf("open incident map = %+v, %v", open, err)
	}
	if inv, ok := open[store.InvestigationKey{ServiceKey: api, Kind: "k1"}]; !ok || inv.ServiceName != "api" {
		t.Fatalf("missing the api entry: %+v", open)
	}
}

func TestIncidentDeletionKeepsTheInvestigation(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	if err := s.FinishInvestigation(ctx, inv.ID, inv.Attempts, "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}, invNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `delete from notification_incidents`); err != nil {
		t.Fatal(err)
	}
	if invCount(t, s, "agent_investigation_incidents") != 0 {
		t.Fatal("the link survived its incident")
	}
	if _, err := s.GetInvestigation(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if invCount(t, s, "agent_investigation_evidence") != 1 {
		t.Fatal("the evidence went with the incident")
	}
}

func TestRetention_DeletesOldInvestigationsWithEvidence(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	now := time.Now()
	invSeed(t, s, []string{"old", "oldopen", "fresh"}, invNow, invNow.Add(time.Minute), invNow.Add(2*time.Minute))
	// "old" finished long ago, "oldopen" never finished but was requested long ago, "fresh" is recent.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`update agent_investigations set requested_at = $1, state = 'done', summary = 's', finished_at = $2 where service_name = 'old'`, now.Add(-1000*time.Hour), now.Add(-30*time.Hour))
	exec(`update agent_investigations set requested_at = $1 where service_name = 'oldopen'`, now.Add(-1000*time.Hour))
	exec(`update agent_investigations set requested_at = $1 where service_name = 'fresh'`, now.Add(-time.Hour))
	exec(`insert into agent_investigation_evidence (investigation_id, ordinal, tool, action, account_id, region, cluster_name, service_name)
		select id, 1, 't', 'view', 'a', 'r', 'c', 's' from agent_investigations where service_name = 'old'`)
	res, err := s.RunRetention(ctx, now, store.Retention{AgentRuns: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_investigations"] != 2 {
		t.Fatalf("result = %v, want 2 investigations deleted (finished_at and requested_at both count)", res)
	}
	if invCount(t, s, "agent_investigations") != 1 || invCount(t, s, "agent_investigation_evidence") != 0 || invCount(t, s, "agent_investigation_incidents") != 1 {
		t.Fatal("expected the fresh investigation and its link only")
	}
}

func insertRun(t *testing.T, s *store.Store, started time.Time, state string, audited bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var auditedAt *time.Time
	var finished *time.Time
	if audited {
		auditedAt = &started
	}
	if state != "running" {
		finished = &started
	}
	if _, err := s.Pool.Exec(context.Background(), `insert into agent_runs (id, role, provider, model, state, started_at, finished_at, audited_at)
		values ($1, 'chat', 'p', 'm', $2, $3, $4, $5)`, id, state, started, finished, auditedAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func runExists(t *testing.T, s *store.Store, id uuid.UUID) bool {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from agent_runs where id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestRetention_NeverDeletesThisMonthsRuns(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	thisMonth := insertRun(t, s, time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC), "done", true) // far older than one hour
	lastMonth := insertRun(t, s, time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC), "done", true)
	res, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, thisMonth) || runExists(t, s, lastMonth) || res["agent_runs"] != 1 {
		t.Fatalf("this month kept = %v, last month kept = %v, result = %v", runExists(t, s, thisMonth), runExists(t, s, lastMonth), res)
	}
}

func TestRetention_NeverDeletesRunningRuns(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	// Audited on purpose: the running state alone must protect it.
	running := insertRun(t, s, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "running", true)
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, running) {
		t.Fatal("a running run was deleted")
	}
}

// The phase 1 reconciler writes the audit row of a finished run later; the run must outlive it.
func TestRetention_NeverDeletesUnauditedRuns(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	unaudited := insertRun(t, s, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "failed", false)
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, unaudited) {
		t.Fatal("an unaudited run was deleted")
	}
}

func TestRetention_ZeroAgentRunsKeepsEverything(t *testing.T) {
	s := storetest.Open(t)
	old := insertRun(t, s, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "done", true)
	if _, err := s.RunRetention(context.Background(), time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), store.Retention{}); err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, old) {
		t.Fatal("a zero retention deleted a run")
	}
}
