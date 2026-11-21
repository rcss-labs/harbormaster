package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func clusterGroupState(routeID int64, members ...store.IncidentMember) store.GroupState {
	return store.GroupState{
		RouteID: routeID, ScopeKind: "cluster",
		Key:  model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}},
		Kind: "cluster_failing", Severity: model.AlertWarning, Title: "core: failing", Text: "text",
		Threshold: 2, Failing: len(members), Total: 5, Members: members,
	}
}

func member(service string, critical bool) store.IncidentMember {
	return store.IncidentMember{Cluster: "core", Service: service, Kind: "service_failing", Title: service + " is failing", Critical: critical}
}

func syncGroups(t *testing.T, s *store.Store, now time.Time, desired ...store.GroupState) (open, resolved []store.NotificationIncident) {
	t.Helper()
	open, resolved, err := s.SyncNotificationGroups(context.Background(), now, 2*time.Minute, time.Hour, desired)
	if err != nil {
		t.Fatal(err)
	}
	return open, resolved
}

func TestSyncNotificationIncidents_LeavesGroupIncidentsAlone(t *testing.T) {
	s := storetest.Open(t)
	syncGroups(t, s, notifyT0, clusterGroupState(1, member("a", false), member("b", false)))
	xmin := func() string {
		var v string
		if err := s.Pool.QueryRow(context.Background(), `select xmin::text from notification_incidents where group_route_id <> 0`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := xmin()
	setAlerts(t, s, notifyAlert("api", "service_failing", model.AlertDanger))
	for i := 0; i < 3; i++ {
		open, _ := syncIncidents(t, s, notifyT0.Add(time.Duration(i+1)*time.Minute))
		if len(open) != 1 || open[0].GroupRouteID != 0 {
			t.Fatalf("alert sync returned %+v, want only the alert incident", open)
		}
	}
	if after := xmin(); after != before {
		t.Fatalf("the group incident row changed (xmin %s -> %s) under an alert sync", before, after)
	}
	var resolvedAt *time.Time
	if err := s.Pool.QueryRow(context.Background(), `select resolved_at from notification_incidents where group_route_id <> 0`).Scan(&resolvedAt); err != nil || resolvedAt != nil {
		t.Fatalf("group incident resolved_at = %v err=%v, want open", resolvedAt, err)
	}
}

func TestSyncNotificationGroups_OpensUpdatesAndResolves(t *testing.T) {
	s := storetest.Open(t)
	group := clusterGroupState(7, member("a", false), member("b", false))
	open, _ := syncGroups(t, s, notifyT0, group)
	if len(open) != 1 || open[0].GroupRouteID != 7 || open[0].ScopeKind != "cluster" || open[0].Failing != 2 || open[0].Total != 5 ||
		open[0].Threshold != 2 || len(open[0].Members) != 2 || open[0].Generation != 0 || open[0].Kind != "cluster_failing" {
		t.Fatalf("opened = %+v", open)
	}
	id := open[0].ID

	xmin := func() string {
		var v string
		s.Pool.QueryRow(context.Background(), `select xmin::text from notification_incidents where id = $1`, id).Scan(&v)
		return v
	}
	before := xmin()
	syncGroups(t, s, notifyT0.Add(10*time.Second), group)
	if xmin() != before {
		t.Fatal("an unchanged group was written")
	}

	absent, _ := syncGroups(t, s, notifyT0.Add(20*time.Second))
	if len(absent) != 1 || absent[0].MissingSince == nil {
		t.Fatalf("absent = %+v, want still open with missing_since", absent)
	}
	back, _ := syncGroups(t, s, notifyT0.Add(60*time.Second), group)
	if len(back) != 1 || back[0].ID != id || back[0].MissingSince != nil {
		t.Fatalf("inside the grace the same incident must continue: %+v", back)
	}

	syncGroups(t, s, notifyT0.Add(70*time.Second))
	open, resolved := syncGroups(t, s, notifyT0.Add(70*time.Second+121*time.Second))
	if len(open) != 0 || len(resolved) != 1 || resolved[0].ID != id || resolved[0].ResolvedAt == nil {
		t.Fatalf("after the grace: open=%+v resolved=%+v", open, resolved)
	}
	reopened, _ := syncGroups(t, s, notifyT0.Add(10*time.Minute), group)
	if len(reopened) != 1 || reopened[0].ID == id {
		t.Fatalf("a group that returns after resolving is a new incident: %+v", reopened)
	}
}

func TestSyncNotificationGroups_GenerationRisesOnlyWhenItGetsWorse(t *testing.T) {
	s := storetest.Open(t)
	a, b, c := member("a", false), member("b", false), member("c", false)
	step := func(minute int, g store.GroupState) store.NotificationIncident {
		open, _ := syncGroups(t, s, notifyT0.Add(time.Duration(minute)*time.Minute), g)
		if len(open) != 1 {
			t.Fatalf("open = %+v", open)
		}
		return open[0]
	}
	if got := step(0, clusterGroupState(1, a, b)); got.Generation != 0 {
		t.Fatalf("opened generation = %d", got.Generation)
	}
	if got := step(1, clusterGroupState(1, a, b, c)); got.Generation != 1 {
		t.Fatalf("a service joined: generation = %d, want 1", got.Generation)
	}
	if got := step(2, clusterGroupState(1, a, c)); got.Generation != 1 || len(got.Members) != 2 {
		t.Fatalf("a service left: generation = %d members %d, want 1 and 2 (it never falls)", got.Generation, len(got.Members))
	}
	worse := clusterGroupState(1, a, c)
	worse.Severity = model.AlertDanger
	if got := step(3, worse); got.Generation != 2 {
		t.Fatalf("severity rose: generation = %d, want 2", got.Generation)
	}
	retitled := worse
	retitled.Members = []store.IncidentMember{{Cluster: "core", Service: "a", Kind: "service_failing", Title: "a: new text", Critical: false}, c}
	retitled.Text = "new text"
	got := step(4, retitled)
	if got.Generation != 2 || got.Text != "new text" || got.Members[0].Title != "a: new text" {
		t.Fatalf("a changed title: %+v, want no rise but the text updated", got)
	}
	better := retitled
	better.Severity = model.AlertWarning
	if got := step(5, better); got.Generation != 2 {
		t.Fatalf("severity fell: generation = %d, want it kept", got.Generation)
	}
}

func TestSyncNotificationGroups_SameClusterTwoRoutesAreTwoIncidents(t *testing.T) {
	s := storetest.Open(t)
	open, _ := syncGroups(t, s, notifyT0, clusterGroupState(1, member("a", false)), clusterGroupState(2, member("a", false)))
	if len(open) != 2 || open[0].ID == open[1].ID || open[0].GroupRouteID == open[1].GroupRouteID {
		t.Fatalf("open = %+v, want one group per route", open)
	}
	// Dropping one route's group resolves only that one.
	syncGroups(t, s, notifyT0.Add(time.Second), clusterGroupState(1, member("a", false)))
	_, resolved := syncGroups(t, s, notifyT0.Add(5*time.Minute), clusterGroupState(1, member("a", false)))
	if len(resolved) != 1 || resolved[0].GroupRouteID != 2 {
		t.Fatalf("resolved = %+v, want route 2's group", resolved)
	}
}

func TestSyncNotificationGroups_AccountAllRegionsAndOneRegionAreDistinct(t *testing.T) {
	s := storetest.Open(t)
	account := func(region string) store.GroupState {
		return store.GroupState{
			RouteID: 1, ScopeKind: "account", Key: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: region}},
			Kind: "account_failing", Severity: model.AlertWarning, Title: "acct", Text: "t", Threshold: 1, Failing: 1, Total: 3,
			Members: []store.IncidentMember{member("a", false)},
		}
	}
	open, _ := syncGroups(t, s, notifyT0, account(""), account("eu-west-1"))
	if len(open) != 2 {
		t.Fatalf("open = %+v, want the all-regions and the one-region entry as two incidents", open)
	}
	regions := map[string]bool{open[0].Key.Region: true, open[1].Key.Region: true}
	if !regions[""] || !regions["eu-west-1"] {
		t.Fatalf("regions = %v", regions)
	}
}

func TestNotificationPlanStates_CountsUpdates(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "opened", 0))
	update := delivery(route, incident, "update", 2)
	update.CreatedAt = notifyT0.Add(10 * time.Minute)
	mustPlan(t, s, update)
	held := delivery(route, incident, "update", 3)
	releaseAt := notifyT0.Add(8 * time.Hour)
	held.Status, held.NextAttemptAt, held.HeldUntil = "held", nil, &releaseAt
	mustPlan(t, s, held)
	// Settle the opened and the first update so they count as sent.
	s.ClaimDueNotificationDeliveries(ctx, notifyT0.Add(time.Hour), 2*time.Minute, 10)
	for _, d := range listNotifDeliveries(t, s) {
		if d.Status == "pending" {
			finishLeased(s, ctx, d.ID, 1, store.DeliveryOutcome{Delivered: true}, notifyT0.Add(time.Hour))
		}
	}
	states, err := s.NotificationPlanStates(ctx, []int64{incident.ID})
	if err != nil {
		t.Fatal(err)
	}
	st := states[[2]int64{route.ID, incident.ID}]
	if st.MaxUpdateSeq != 3 || !st.HasHeld || st.LastSentAt == nil || !st.LastSentAt.Equal(update.CreatedAt) {
		t.Fatalf("state = %+v, want MaxUpdateSeq 3, a held update, and LastSentAt at the newest update", st)
	}
}

func TestCancelUnsentNotificationDeliveries_CancelsUpdates(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	incident, route := seedIncidentAndRoute(t, s)
	mustPlan(t, s, delivery(route, incident, "update", 1))
	if err := s.CancelUnsentNotificationDeliveries(ctx, []int64{incident.ID}, "gone", "quiet", notifyT0); err != nil {
		t.Fatal(err)
	}
	if rows := listNotifDeliveries(t, s); len(rows) != 1 || rows[0].Status != "cancelled" || rows[0].Note != "gone" {
		t.Fatalf("rows = %+v, want the update cancelled", rows)
	}
}

func TestPlanNotificationDelivery_KeepsDetails(t *testing.T) {
	s := storetest.Open(t)
	incident, route := seedIncidentAndRoute(t, s)
	d := delivery(route, incident, "opened", 0)
	d.Details = store.DeliveryDetails{Scope: "cluster", Threshold: 2, Failing: 3, Total: 5, Members: []store.IncidentMember{member("a", true), member("b", false)}}
	row := mustPlan(t, s, d)
	if row.Details.Scope != "cluster" || row.Details.Threshold != 2 || row.Details.Failing != 3 || row.Details.Total != 5 ||
		len(row.Details.Members) != 2 || !row.Details.Members[0].Critical || row.Details.Members[1].Service != "b" {
		t.Fatalf("details = %+v", row.Details)
	}
	if ok, _ := s.PlanNotificationDelivery(context.Background(), delivery(route, incident, "update", 2)); !ok {
		t.Fatal("an update at seq 2 must plan")
	}
	if ok, _ := s.PlanNotificationDelivery(context.Background(), delivery(route, incident, "update", 2)); ok {
		t.Fatal("a second update at seq 2 must be a no-op")
	}
	plain := listNotifDeliveries(t, s)
	for _, p := range plain {
		if p.Event == "update" && (p.Details.Scope != "" || len(p.Details.Members) != 0) {
			t.Errorf("a row without details reads back %+v", p.Details)
		}
	}
}

func TestListOpenNotificationIncidents_ExcludesGroupsAndResolved(t *testing.T) {
	s := storetest.Open(t)
	setAlerts(t, s, notifyAlert("a", "service_failing", model.AlertDanger), notifyAlert("b", "service_failing", model.AlertDanger))
	syncIncidents(t, s, notifyT0)
	syncGroups(t, s, notifyT0, clusterGroupState(1, member("a", false)))
	setAlerts(t, s, notifyAlert("a", "service_failing", model.AlertDanger))
	syncIncidents(t, s, notifyT0.Add(time.Second))
	syncIncidents(t, s, notifyT0.Add(5*time.Minute)) // b resolves
	open, err := s.ListOpenNotificationIncidents(context.Background())
	if err != nil || len(open) != 1 || open[0].Key.Service != "a" || open[0].GroupRouteID != 0 {
		t.Fatalf("open = %+v err=%v, want only alert a", open, err)
	}
	xmin := func() string {
		var v string
		s.Pool.QueryRow(context.Background(), `select string_agg(xmin::text, ',' order by id) from notification_incidents`).Scan(&v)
		return v
	}
	before := xmin()
	if _, err := s.ListOpenNotificationIncidents(context.Background()); err != nil || xmin() != before {
		t.Fatal("listing must not write")
	}
}
