package store_test

import (
	"context"
	"errors"
	"fmt"
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
	got, _, err := s.ClaimInvestigation(ctx, claimAt, time.Minute)
	if err != nil || got.ID != ids[1] {
		t.Fatalf("claim past a locked row = %+v, %v; want %d", got, err, ids[1])
	}
	tx.Rollback(ctx) //nolint:errcheck

	first, _, err := s.ClaimInvestigation(ctx, claimAt, time.Minute)
	if err != nil || first.ID != ids[0] || first.State != "running" || first.Attempts != 1 || first.ClaimedUntil == nil || !first.ClaimedUntil.Equal(claimAt.Add(time.Minute)) {
		t.Fatalf("oldest claim = %+v, %v", first, err)
	}
	if _, _, err := s.ClaimInvestigation(ctx, claimAt, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim with nothing due = %v, want ErrNotFound", err)
	}
}

func TestClaim_ReclaimsAnExpiredRunOnce(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	first, _, err := s.ClaimInvestigation(ctx, invNow, time.Minute)
	if err != nil || first.Attempts != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if _, _, err := s.ClaimInvestigation(ctx, invNow.Add(30*time.Second), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a live lease was claimed: %v", err)
	}
	second, _, err := s.ClaimInvestigation(ctx, invNow.Add(2*time.Minute), time.Minute)
	if err != nil || second.ID != first.ID || second.Attempts != 2 || second.State != "running" {
		t.Fatalf("reclaim = %+v, %v", second, err)
	}
}

func TestClaim_SecondExpiryFailsAsInterrupted(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	if _, _, err := s.ClaimInvestigation(ctx, invNow, time.Minute); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.ClaimInvestigation(ctx, invNow.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	late := invNow.Add(10 * time.Minute)
	if got, _, err := s.ClaimInvestigation(ctx, late, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("third claim = %+v, %v; want ErrNotFound", got, err)
	}
	row, err := s.GetInvestigation(ctx, second.ID)
	if err != nil || row.State != "failed" || row.Reason != "interrupted" || row.Attempts != 2 || row.FinishedAt == nil || !row.FinishedAt.Equal(late) || row.ClaimedUntil != nil {
		t.Fatalf("row = %+v, %v", row, err)
	}
}

func claimed(t *testing.T, s *store.Store, at time.Time) store.Investigation {
	t.Helper()
	inv, c, err := s.ClaimInvestigation(context.Background(), at, time.Minute)
	if err == nil && c != inv.Claim() {
		t.Fatalf("returned claim %+v differs from the row's %+v", c, inv.Claim())
	}
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
	if err := s.FinishInvestigation(ctx, first.Claim(), "done", "", done, invNow.Add(3*time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale attempt = %v, want ErrConflict", err)
	}
	row, _ := s.GetInvestigation(ctx, first.ID)
	if row.State != "running" || row.Summary != "" || invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatalf("the stale finish wrote: %+v", row)
	}
	good := &store.InvestigationResult{Summary: "fresh", SummaryReplaced: true, EvidenceDropped: 2, Evidence: []store.InvestigationEvidence{viewEvidence("one"), viewEvidence("two")}}
	if err := s.FinishInvestigation(ctx, second.Claim(), "done", "", good, invNow.Add(4*time.Minute)); err != nil {
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
	if err := s.FinishInvestigation(ctx, second.Claim(), "failed", "error", nil, invNow.Add(5*time.Minute)); !errors.Is(err, store.ErrConflict) {
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
		if err := s.FinishInvestigation(ctx, inv.Claim(), "done", "", result, invNow); err == nil || errors.Is(err, store.ErrConflict) {
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
		err := s.FinishInvestigation(ctx, inv.Claim(), "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence(excerpt)}}, invNow)
		if err == nil || errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	if invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatal("an oversized excerpt was written")
	}
	// The cap is on bytes and 1500 exactly fits.
	if err := s.FinishInvestigation(ctx, inv.Claim(), "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence(strings.Repeat("x", 1500))}}, invNow); err != nil {
		t.Fatalf("1500 bytes refused: %v", err)
	}
}

func TestRelease_GivesTheAttemptBackWhenAsked(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow)
	retry := invNow.Add(5 * time.Minute)
	if err := s.ReleaseInvestigation(ctx, inv.Claim(), retry, false); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, inv.ID)
	if row.State != "requested" || row.Attempts != 0 || row.ClaimedUntil == nil || !row.ClaimedUntil.Equal(retry) {
		t.Fatalf("released = %+v", row)
	}
	if _, _, err := s.ClaimInvestigation(ctx, invNow.Add(time.Minute), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claimed before the retry time: %v", err)
	}
	again := claimed(t, s, retry)
	if again.Attempts != 1 {
		t.Fatalf("attempts after a free release = %d, want 1", again.Attempts)
	}
	if err := s.ReleaseInvestigation(ctx, again.Claim(), retry.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	row, _ = s.GetInvestigation(ctx, inv.ID)
	if row.State != "requested" || row.Attempts != 1 {
		t.Fatalf("counted release = %+v, want attempts 1", row)
	}
	if err := s.ReleaseInvestigation(ctx, again.Claim(), retry, true); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("release of a row that is not running = %v, want ErrConflict", err)
	}
}

func TestInvestigationLifecycle_StartSkipAndCounts(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	inv := claimed(t, s, invNow.Add(2*time.Minute))
	c := inv.Claim()
	runID := uuid.New()
	if err := s.StartInvestigationRun(ctx, c, runID, "p", "m", invNow.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, inv.ID)
	if row.RunID == nil || *row.RunID != runID || row.Provider != "p" || row.Model != "m" || row.StartedAt == nil || !row.StartedAt.Equal(invNow.Add(3*time.Minute)) {
		t.Fatalf("started = %+v", row)
	}
	if n, err := s.InvestigationsStartedSince(ctx, invNow); err != nil || n != 1 {
		t.Fatalf("started since = %d, %v; want 1", n, err)
	}
	if n, _ := s.InvestigationsStartedSince(ctx, invNow.Add(time.Hour)); n != 0 {
		t.Fatalf("started in the future = %d", n)
	}
	if err := s.SkipInvestigation(ctx, c, "made_up", invNow); err == nil || errors.Is(err, store.ErrConflict) {
		t.Fatalf("skip with an unknown reason = %v, want a refusal", err)
	}
	if err := s.SkipInvestigation(ctx, c, "budget", invNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	skipped, _ := s.GetInvestigation(ctx, inv.ID)
	if skipped.State != "skipped" || skipped.Reason != "budget" || skipped.FinishedAt == nil || skipped.ClaimedUntil != nil {
		t.Fatalf("skipped = %+v", skipped)
	}
	if err := s.SkipInvestigation(ctx, c, "budget", invNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("skip of a skipped row = %v, want ErrConflict", err)
	}
	if err := s.SkipInvestigation(ctx, store.Claim{InvestigationID: 999999, Attempt: 1, Seq: 1}, "budget", invNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("skip of a missing row = %v, want ErrConflict", err)
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
	if err := s.FinishInvestigation(ctx, inv.Claim(), "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}, invNow); err != nil {
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

func invExec(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func invExists(t *testing.T, s *store.Store, service string) bool {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `select count(*) from agent_investigations where service_name = $1`, service).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestRetention_DeletesOldInvestigationsWithEvidence(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	now := time.Now()
	invSeed(t, s, []string{"old", "young", "stuckrequested", "stuckrunning"}, invNow, invNow, invNow, invNow)
	invExec(t, s, `update agent_investigations set state = 'done', summary = 's', finished_at = $1 where service_name = 'old'`, now.Add(-100*time.Hour))
	invExec(t, s, `update agent_investigations set state = 'failed', reason = 'error', finished_at = $1 where service_name = 'young'`, now.Add(-60*time.Hour))
	invExec(t, s, `update agent_investigations set requested_at = $1 where service_name = 'stuckrequested'`, now.Add(-1000*time.Hour))
	invExec(t, s, `update agent_investigations set state = 'running', attempts = 1, claimed_until = $1, requested_at = $1 where service_name = 'stuckrunning'`, now.Add(-1000*time.Hour))
	invExec(t, s, `insert into agent_investigation_evidence (investigation_id, ordinal, tool, action, account_id, region, cluster_name, service_name)
		select id, 1, 't', 'view', 'a', 'r', 'c', 's' from agent_investigations where service_name = 'old'`)
	res, err := s.RunRetention(ctx, now, store.Retention{AgentRuns: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_investigations"] != 1 || invExists(t, s, "old") || !invExists(t, s, "young") || !invExists(t, s, "stuckrequested") || !invExists(t, s, "stuckrunning") {
		t.Fatalf("result = %v; want only the settled investigation older than 72h gone", res)
	}
	if invCount(t, s, "agent_investigation_evidence") != 0 || invCount(t, s, "agent_investigation_incidents") != 3 {
		t.Fatal("evidence should cascade and the other links stay")
	}
}

// Whatever the setting, an investigation settled less than 48 hours ago is never deleted.
func TestRetention_InvestigationsNeverYoungerThanTheFloor(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now()
	invSeed(t, s, []string{"recent", "past"}, invNow, invNow)
	invExec(t, s, `update agent_investigations set state = 'done', summary = 's', finished_at = $1 where service_name = 'recent'`, now.Add(-30*time.Hour))
	invExec(t, s, `update agent_investigations set state = 'done', summary = 's', finished_at = $1 where service_name = 'past'`, now.Add(-49*time.Hour))
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !invExists(t, s, "recent") || invExists(t, s, "past") {
		t.Fatal("the floor should keep the 30h-old row and release the 49h-old one")
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
	// The month start is moved back an hour: a run in the last hour of the previous month stays too.
	boundaryKept := insertRun(t, s, time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC), "done", true)
	justOutside := insertRun(t, s, time.Date(2026, 10, 31, 22, 59, 0, 0, time.UTC), "done", true)
	lastMonth := insertRun(t, s, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), "done", true)
	res, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, thisMonth) || !runExists(t, s, boundaryKept) || runExists(t, s, justOutside) || runExists(t, s, lastMonth) || res["agent_runs"] != 2 {
		t.Fatalf("this month %v, boundary %v, just outside %v, last month %v, result = %v",
			runExists(t, s, thisMonth), runExists(t, s, boundaryKept), runExists(t, s, justOutside), runExists(t, s, lastMonth), res)
	}
}

// A finished, audited run from an earlier month stays until it is older than the configured age.
func TestRetention_KeepsRunsYoungerThanTheCutoff(t *testing.T) {
	s := storetest.Open(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	young := insertRun(t, s, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "done", true) // last month, 16 days old
	old := insertRun(t, s, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "done", true)
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: 30 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !runExists(t, s, young) || runExists(t, s, old) {
		t.Fatalf("young kept = %v, old kept = %v", runExists(t, s, young), runExists(t, s, old))
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

// --- fence ---

// holderOps are the five writes a holder makes with its claim.
func holderOps(s *store.Store, c store.Claim, now time.Time) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"start":   func() error { return s.StartInvestigationRun(ctx, c, uuid.New(), "p", "m", now) },
		"renew":   func() error { return s.RenewInvestigation(ctx, c, now, time.Hour) },
		"release": func() error { return s.ReleaseInvestigation(ctx, c, now, true) },
		"skip":    func() error { return s.SkipInvestigation(ctx, c, "budget", now) },
		"finish": func() error {
			return s.FinishInvestigation(ctx, c, "done", "", &store.InvestigationResult{Summary: "stale", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}, now)
		},
	}
}

// requireUntouched fails when an investigation differs from its snapshot or has evidence.
func requireUntouched(t *testing.T, s *store.Store, want store.Investigation) {
	t.Helper()
	got, err := s.GetInvestigation(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != want.State || got.Attempts != want.Attempts || got.ClaimSeq != want.ClaimSeq || got.Summary != want.Summary || got.Reason != want.Reason ||
		got.RunID != nil && want.RunID == nil || !sameTime(got.ClaimedUntil, want.ClaimedUntil) || !sameTime(got.StartedAt, want.StartedAt) || !sameTime(got.FinishedAt, want.FinishedAt) {
		t.Fatalf("row changed:\n got %+v\nwant %+v", got, want)
	}
	if invCount(t, s, "agent_investigation_evidence") != 0 {
		t.Fatal("evidence was written")
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// A free release gives the attempt back, so the next claim reuses the attempt number. Only claim_seq tells the
// paused first holder from the second.
func TestFence_StaleClaimIsRefusedAfterTheAttemptNumberIsReused(t *testing.T) {
	for _, op := range []string{"start", "renew", "release", "skip", "finish"} {
		t.Run(op, func(t *testing.T) {
			s, ctx := storetest.Open(t), context.Background()
			invSeed(t, s, []string{"a"}, invNow)
			first := claimed(t, s, invNow)
			if err := s.ReleaseInvestigation(ctx, first.Claim(), time.Time{}, false); err != nil {
				t.Fatal(err)
			}
			second := claimed(t, s, invNow.Add(time.Minute))
			if second.Attempts != first.Attempts || second.ClaimSeq == first.ClaimSeq {
				t.Fatalf("setup: attempts %d/%d, seq %d/%d", first.Attempts, second.Attempts, first.ClaimSeq, second.ClaimSeq)
			}
			if err := holderOps(s, first.Claim(), invNow.Add(2*time.Minute))[op](); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("%s with the first holder's claim = %v, want ErrConflict", op, err)
			}
			requireUntouched(t, s, second)
		})
	}
}

// A claim with the right seq but another attempt number is refused too.
func TestFence_WrongAttemptIsRefused(t *testing.T) {
	for _, op := range []string{"start", "renew", "release", "skip", "finish"} {
		t.Run(op, func(t *testing.T) {
			s := storetest.Open(t)
			invSeed(t, s, []string{"a"}, invNow)
			held := claimed(t, s, invNow)
			bad := held.Claim()
			bad.Attempt++
			if err := holderOps(s, bad, invNow)[op](); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("%s with attempt %d = %v, want ErrConflict", op, bad.Attempt, err)
			}
			requireUntouched(t, s, held)
		})
	}
}

// After the row is settled the same claim, attempt and seq no longer match a running row.
func TestFence_SettledRowRefusesItsOldClaim(t *testing.T) {
	for _, op := range []string{"start", "renew", "release", "skip", "finish"} {
		t.Run(op, func(t *testing.T) {
			s, ctx := storetest.Open(t), context.Background()
			invSeed(t, s, []string{"a"}, invNow)
			held := claimed(t, s, invNow)
			if err := s.FinishInvestigation(ctx, held.Claim(), "failed", "error", nil, invNow); err != nil {
				t.Fatal(err)
			}
			settled, _ := s.GetInvestigation(ctx, held.ID)
			if err := holderOps(s, held.Claim(), invNow)[op](); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("%s on a settled row = %v, want ErrConflict", op, err)
			}
			requireUntouched(t, s, settled)
		})
	}
}

func TestRenew_ExtendsOnlyTheHoldersLease(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	held := claimed(t, s, invNow)
	if err := s.RenewInvestigation(ctx, held.Claim(), invNow.Add(30*time.Second), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, held.ID)
	if row.ClaimedUntil == nil || !row.ClaimedUntil.Equal(invNow.Add(30*time.Second+5*time.Minute)) {
		t.Fatalf("claimed_until = %v", row.ClaimedUntil)
	}
	if _, _, err := s.ClaimInvestigation(ctx, invNow.Add(3*time.Minute), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a renewed lease was claimed: %v", err)
	}
}

// --- release and started_at ---

func TestRelease_FreeReleaseToZeroClearsStartedAtAndKeepsTheRun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	held := claimed(t, s, invNow)
	runID := uuid.New()
	if err := s.StartInvestigationRun(ctx, held.Claim(), runID, "p", "m", invNow); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.InvestigationsStartedSince(ctx, invNow.Add(-time.Hour)); n != 1 {
		t.Fatalf("started before the release = %d, want 1", n)
	}
	if err := s.ReleaseInvestigation(ctx, held.Claim(), time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, held.ID)
	if row.Attempts != 0 || row.StartedAt != nil || row.RunID == nil || *row.RunID != runID {
		t.Fatalf("row = %+v; want attempts 0, started_at null, run_id kept", row)
	}
	if n, _ := s.InvestigationsStartedSince(ctx, invNow.Add(-time.Hour)); n != 0 {
		t.Fatalf("started after a free release = %d, want 0", n)
	}
}

func TestRelease_CountedReleaseAndFreeReleaseOfASecondAttemptKeepStartedAt(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	first := claimed(t, s, invNow)
	if err := s.StartInvestigationRun(ctx, first.Claim(), uuid.New(), "p", "m", invNow); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseInvestigation(ctx, first.Claim(), time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if row, _ := s.GetInvestigation(ctx, first.ID); row.StartedAt == nil {
		t.Fatal("a counted release cleared started_at")
	}
	second := claimed(t, s, invNow.Add(time.Minute))
	if err := s.ReleaseInvestigation(ctx, second.Claim(), time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetInvestigation(ctx, first.ID)
	if row.Attempts != 1 || row.StartedAt == nil {
		t.Fatalf("row = %+v; the first attempt did start, so started_at stays", row)
	}
}

// --- state, reason and text refusals ---

func TestFinish_RefusesAnInconsistentStateOrReason(t *testing.T) {
	good := &store.InvestigationResult{Summary: "s"}
	cases := []struct {
		name, state, reason string
		result              *store.InvestigationResult
	}{
		{"done with a reason", "done", "error", good},
		{"done with an unknown reason", "done", "bogus", good},
		{"failed with no reason", "failed", "", nil},
		{"failed with an unknown reason", "failed", "bogus", nil},
		{"skipped through finish", "skipped", "budget", nil},
		{"requested through finish", "requested", "", nil},
		{"empty state", "", "", good},
		{"done without a result", "done", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			invSeed(t, s, []string{"a"}, invNow)
			held := claimed(t, s, invNow)
			err := s.FinishInvestigation(context.Background(), held.Claim(), tc.state, tc.reason, tc.result, invNow)
			if err == nil || errors.Is(err, store.ErrConflict) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			requireUntouched(t, s, held)
		})
	}
}

func TestSkip_RefusesAnUnknownOrEmptyReason(t *testing.T) {
	s := storetest.Open(t)
	invSeed(t, s, []string{"a"}, invNow)
	held := claimed(t, s, invNow)
	for _, reason := range []string{"", "bogus"} {
		if err := s.SkipInvestigation(context.Background(), held.Claim(), reason, invNow); err == nil || errors.Is(err, store.ErrConflict) {
			t.Fatalf("reason %q: err = %v, want a refusal", reason, err)
		}
	}
	requireUntouched(t, s, held)
}

func TestInvestigationText_RefusedBeforeAnySQL(t *testing.T) {
	long := strings.Repeat("x", 201)
	finish := func(mut func(*store.InvestigationResult)) func(*store.Store, store.Claim) error {
		return func(s *store.Store, c store.Claim) error {
			r := &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}
			mut(r)
			return s.FinishInvestigation(context.Background(), c, "done", "", r, invNow)
		}
	}
	start := func(provider, model string) func(*store.Store, store.Claim) error {
		return func(s *store.Store, c store.Claim) error {
			return s.StartInvestigationRun(context.Background(), c, uuid.New(), provider, model, invNow)
		}
	}
	cases := map[string]func(*store.Store, store.Claim) error{
		"provider invalid utf-8":  start("p\xff", "m"),
		"model has NUL":           start("p", "m\x00"),
		"provider too long":       start(long, "m"),
		"summary invalid utf-8":   finish(func(r *store.InvestigationResult) { r.Summary = "a\xc3" }),
		"summary NUL":             finish(func(r *store.InvestigationResult) { r.Summary = "a\x00b" }),
		"excerpt invalid utf-8":   finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = "ok\xff\xfe" }),
		"excerpt NUL":             finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = "a\x00" }),
		"excerpt too long":        finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = strings.Repeat("x", 1501) }),
		"note invalid utf-8":      finish(func(r *store.InvestigationResult) { r.Evidence[0].Note = "\xff" }),
		"note too long":           finish(func(r *store.InvestigationResult) { r.Evidence[0].Note = strings.Repeat("n", 301) }),
		"tool invalid utf-8":      finish(func(r *store.InvestigationResult) { r.Evidence[0].Tool = "t\xff" }),
		"service name invalid":    finish(func(r *store.InvestigationResult) { r.Evidence[0].ServiceName = "s\xff" }),
		"summary over 1200 runes": finish(func(r *store.InvestigationResult) { r.Summary = strings.Repeat("é", 1201) }),
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			invSeed(t, s, []string{"a"}, invNow)
			held := claimed(t, s, invNow)
			if err := run(s, held.Claim()); !errors.Is(err, store.ErrInvestigationText) {
				t.Fatalf("err = %v, want ErrInvestigationText", err)
			}
			requireUntouched(t, s, held)
		})
	}
}

// The state update and the evidence rows commit together. A constraint only the database knows about (added here)
// rejects the evidence insert after the update has run; the row must still be running.
func TestFinish_EvidenceAndStateAreOneTransaction(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	held := claimed(t, s, invNow)
	invExec(t, s, `alter table agent_investigation_evidence add constraint test_refuse_boom check (tool <> 'boom')`)
	ev := viewEvidence("x")
	ev.Tool = "boom"
	err := s.FinishInvestigation(ctx, held.Claim(), "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("first"), ev}}, invNow)
	if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvestigationText) {
		t.Fatalf("err = %v, want the database's refusal", err)
	}
	requireUntouched(t, s, held)
}

// --- candidates, members, keys, filters ---

func TestRecord_IgnoresResolvedIncidents(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k", model.AlertDanger))
	invOpened(t, s, route, incs[0])
	invExec(t, s, `update notification_incidents set resolved_at = $1`, invNow)
	if created, _ := invRecord(t, s, invNow); created != 0 {
		t.Fatalf("created = %d for a resolved incident", created)
	}
}

func TestRecord_OldestOpenedIncidentFirst(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("a", "k", model.AlertDanger), notifyAlert("b", "k", model.AlertDanger))
	for _, inc := range incs {
		invOpened(t, s, route, inc)
	}
	// The incident with the higher id opened earlier, so id order and opened_at order disagree.
	invExec(t, s, `update notification_incidents set opened_at = $1 where id = (select max(id) from notification_incidents)`, notifyT0.Add(-time.Hour))
	var wantService string
	if err := s.Pool.QueryRow(ctx, `select service_name from notification_incidents order by opened_at limit 1`).Scan(&wantService); err != nil {
		t.Fatal(err)
	}
	if created, _, err := s.RecordInvestigationRequests(ctx, invNow, invWindow, 1); err != nil || created != 1 {
		t.Fatalf("created = %d, %v", created, err)
	}
	if !invExists(t, s, wantService) {
		t.Fatalf("the first investigation is not for %s, the oldest opened incident", wantService)
	}
	if created, _ := invRecord(t, s, invNow); created != 1 || invCount(t, s, "agent_investigations") != 2 {
		t.Fatal("the next call should pick up the other incident")
	}
}

func TestRecord_GroupMembersAreCutAtFifty(t *testing.T) {
	s := storetest.Open(t)
	route := invRoute(t, s, "r", true, true)
	var members []store.IncidentMember
	for i := 0; i < 60; i++ {
		members = append(members, member(fmt.Sprintf("svc%02d", i), false))
	}
	groups, _ := syncGroups(t, s, notifyT0, clusterGroupState(route.ID, members...))
	invOpened(t, s, route, groups[0])
	invRecord(t, s, invNow)
	var n int
	var first string
	if err := s.Pool.QueryRow(context.Background(), `select jsonb_array_length(members), members->0->>'service' from agent_investigations`).Scan(&n, &first); err != nil {
		t.Fatal(err)
	}
	if n != 50 || first != "svc00" {
		t.Fatalf("members = %d starting at %s, want 50 starting at svc00", n, first)
	}
}

func TestRecord_AccountEntryKey(t *testing.T) {
	for _, tc := range []struct{ region, want string }{
		{"", "account/111111111111/"},
		{"us-east-1", "account/111111111111/us-east-1"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			s := storetest.Open(t)
			route := invRoute(t, s, "r", true, true)
			g := clusterGroupState(route.ID, member("a", false), member("b", false))
			g.ScopeKind, g.Kind = "account", "account_failing"
			g.Key.Cluster, g.Key.Region = "", tc.region
			groups, _ := syncGroups(t, s, notifyT0, g)
			invOpened(t, s, route, groups[0])
			invRecord(t, s, invNow)
			var kind, key, cluster string
			if err := s.Pool.QueryRow(context.Background(), `select subject_kind, subject_key, cluster_name from agent_investigations`).Scan(&kind, &key, &cluster); err != nil {
				t.Fatal(err)
			}
			if kind != "account" || key != tc.want || cluster != "" {
				t.Fatalf("kind %s key %q cluster %q; want account %q", kind, key, cluster, tc.want)
			}
		})
	}
}

func TestListInvestigations_Filters(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a", "b", "c"}, invNow, invNow, invNow)
	invExec(t, s, `update agent_investigations set state = 'done', summary = 's', finished_at = now() where service_name = 'a'`)
	invExec(t, s, `update agent_investigations set subject_kind = 'cluster' where service_name = 'b'`)
	count := func(f store.InvestigationFilter) int {
		got, err := s.ListInvestigations(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := count(store.InvestigationFilter{}); n != 3 {
		t.Fatalf("unfiltered = %d", n)
	}
	if n := count(store.InvestigationFilter{State: "done"}); n != 1 {
		t.Fatalf("state done = %d", n)
	}
	if n := count(store.InvestigationFilter{State: "requested"}); n != 2 {
		t.Fatalf("state requested = %d", n)
	}
	if n := count(store.InvestigationFilter{SubjectKind: "cluster"}); n != 1 {
		t.Fatalf("kind cluster = %d", n)
	}
	if n := count(store.InvestigationFilter{State: "done", SubjectKind: "cluster"}); n != 0 {
		t.Fatalf("done and cluster = %d", n)
	}
}

func TestInvestigationsForOpenIncidents_SkipsResolvedAndGroupIncidents(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := invRoute(t, s, "r", true, true)
	incs := invIncidents(t, s, notifyAlert("api", "k", model.AlertDanger), notifyAlert("web", "k", model.AlertDanger))
	for _, inc := range incs {
		invOpened(t, s, route, inc)
	}
	invRecord(t, s, invNow)
	invExec(t, s, `update notification_incidents set resolved_at = $1 where service_name = 'web'`, invNow)
	got, err := s.InvestigationsForOpenIncidents(ctx, []model.ServiceKey{notifyKey("api"), notifyKey("web")})
	if err != nil || len(got) != 1 {
		t.Fatalf("map = %+v, %v; want only the open incident", got, err)
	}
}

// --- claim details ---

func TestClaim_ALiveRunningLeaseIsNotTouched(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	claimed(t, s, invNow)
	second := claimed(t, s, invNow.Add(2*time.Minute)) // attempt 2, lease until +3m
	if _, _, err := s.ClaimInvestigation(ctx, invNow.Add(2*time.Minute+30*time.Second), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %v, want ErrNotFound", err)
	}
	requireUntouched(t, s, second)
}

// A counted release on the last attempt leaves a requested row with no attempt; the next claim fails it.
func TestClaim_ARequestedRowWithNoAttemptLeftFailsAsInterrupted(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	invSeed(t, s, []string{"a"}, invNow)
	first := claimed(t, s, invNow)
	if err := s.ReleaseInvestigation(ctx, first.Claim(), time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	second := claimed(t, s, invNow.Add(time.Minute))
	if err := s.ReleaseInvestigation(ctx, second.Claim(), invNow.Add(2*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	late := invNow.Add(5 * time.Minute)
	if _, _, err := s.ClaimInvestigation(ctx, late, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %v, want ErrNotFound", err)
	}
	row, _ := s.GetInvestigation(ctx, first.ID)
	if row.State != "failed" || row.Reason != "interrupted" || row.Attempts != 2 || row.FinishedAt == nil || !row.FinishedAt.Equal(late) {
		t.Fatalf("row = %+v", row)
	}
}

// An exhausted row is failed and the claim carries on to the next due row in the same call.
func TestClaim_SkipsPastAnExhaustedRowToTheNextDueOne(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := invSeed(t, s, []string{"a", "b"}, invNow, invNow.Add(time.Second))
	first := claimed(t, s, invNow)
	claimed(t, s, invNow.Add(2*time.Minute)) // a: attempt 2 (oldest first, so a again)
	if first.ID != ids[0] {
		t.Fatalf("setup: first = %d, want %d", first.ID, ids[0])
	}
	got, _, err := s.ClaimInvestigation(ctx, invNow.Add(10*time.Minute), time.Minute)
	if err != nil || got.ID != ids[1] || got.Attempts != 1 {
		t.Fatalf("claim = %+v, %v; want the second investigation at attempt 1", got, err)
	}
	if a, _ := s.GetInvestigation(ctx, ids[0]); a.State != "failed" || a.Reason != "interrupted" {
		t.Fatalf("the exhausted row = %+v", a)
	}
}
