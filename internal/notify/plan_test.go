package notify

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var planNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

const (
	pAcct  = "111111111111"
	pAcct2 = "222222222222"
)

var planNames = map[string]string{pAcct: "prod", pAcct2: "staging"}

func awsTarget(account, region string) awsx.Target {
	return awsx.Target{AccountID: account, Region: region}
}

func pKey(account, region, cluster, service string) model.ServiceKey {
	return svc(account, region, cluster, service)
}

var (
	clusterA = model.ClusterKey{AccountID: pAcct, Region: "us-east-1", Cluster: "A"}
	svc1     = model.ServiceKey{ClusterKey: clusterA, Service: "svc1"}
	svc2     = model.ServiceKey{ClusterKey: clusterA, Service: "svc2"}
	svc3     = model.ServiceKey{ClusterKey: clusterA, Service: "svc3"}
)

var nextIncidentID int64

// inc builds an open alert incident that has been open for age.
func inc(key model.ServiceKey, kind string, severity model.AlertSeverity, age time.Duration) store.NotificationIncident {
	nextIncidentID++
	return store.NotificationIncident{
		ID: nextIncidentID, Key: key, Kind: kind, Severity: severity, ScopeKind: "alert",
		Title: key.Service + " " + kind, Text: "text of " + key.Service, OpenedAt: planNow.Add(-age),
	}
}

func route(id int64, mutate ...func(*store.NotificationRoute)) store.NotificationRoute {
	kinds := make([]string, len(Kinds))
	for i, k := range Kinds {
		kinds[i] = k.Kind
	}
	r := store.NotificationRoute{ID: id, Name: fmt.Sprintf("route %d", id), Enabled: true, MinSeverity: "warning", Kinds: kinds}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

func resolved(t *testing.T, selectors ...store.NotificationSelector) Scope {
	t.Helper()
	scope, err := ResolveScope(context.Background(), &fakeTagMatcher{}, selectors)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func clusterEntry(c model.ClusterKey, threshold int) store.NotificationSelector {
	return store.NotificationSelector{Kind: "cluster", AccountID: c.AccountID, Region: c.Region, Cluster: c.Cluster, Threshold: threshold}
}

func serviceEntry(k model.ServiceKey) store.NotificationSelector {
	return store.NotificationSelector{Kind: "service", AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster, Service: k.Service}
}

func accountEntry(account, region string, threshold int) store.NotificationSelector {
	return store.NotificationSelector{Kind: "account", AccountID: account, Region: region, Threshold: threshold}
}

func totalsOf(services ...model.ServiceKey) Totals {
	var targets store.NotificationTargets
	for _, k := range services {
		targets.Services = append(targets.Services, store.NotificationTargetService{Key: k})
	}
	return TotalsFrom(targets)
}

func threeServices() Totals { return totalsOf(svc1, svc2, svc3) }

func build(routes []store.NotificationRoute, scopes map[int64]Scope, incidents []store.NotificationIncident, totals Totals) Plan {
	return BuildPlan(routes, scopes, incidents, totals, planNames, planNow, 3*time.Minute)
}

func routeIDs(p Plan, incident store.NotificationIncident) []int64 { return p.PerAlert[incident.ID] }

func TestNotifiable_CapacityKindsWaitOthersDoNot(t *testing.T) {
	k := svc1
	for name, tc := range map[string]struct {
		incident store.NotificationIncident
		settle   time.Duration
		want     bool
	}{
		"failing at 2m59s":             {inc(k, "service_failing", model.AlertDanger, 2*time.Minute+59*time.Second), 3 * time.Minute, false},
		"failing at 3m":                {inc(k, "service_failing", model.AlertDanger, 3*time.Minute), 3 * time.Minute, true},
		"degraded at 2m59s":            {inc(k, "service_degraded", model.AlertWarning, 2*time.Minute+59*time.Second), 3 * time.Minute, false},
		"degraded at 3m":               {inc(k, "service_degraded", model.AlertWarning, 3*time.Minute), 3 * time.Minute, true},
		"restart loop at once":         {inc(k, "restart_loop", model.AlertWarning, 0), 3 * time.Minute, true},
		"deployment failed at once":    {inc(k, "deployment_failed", model.AlertDanger, 0), 3 * time.Minute, true},
		"cannot place at once":         {inc(k, "cannot_place", model.AlertDanger, 0), 3 * time.Minute, true},
		"unreachable at once":          {inc(k, "account_unreachable", model.AlertDanger, 0), 3 * time.Minute, true},
		"settle 0 makes failing count": {inc(k, "service_failing", model.AlertDanger, 0), 0, true},
	} {
		if got := Notifiable(tc.incident, planNow, tc.settle); got != tc.want {
			t.Errorf("%s: Notifiable = %v, want %v", name, got, tc.want)
		}
	}
	inGrace := inc(k, "restart_loop", model.AlertWarning, time.Hour)
	missing := planNow.Add(-30 * time.Second)
	inGrace.MissingSince = &missing
	if Notifiable(inGrace, planNow, 0) {
		t.Error("an incident inside its resolve grace must not be notifiable")
	}
}

func TestTotalsFrom_CountsActiveServicesPerLevel(t *testing.T) {
	other := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "eu-west-1", Cluster: "B"}, Service: "x"}
	totals := totalsOf(svc1, svc2, other)
	if totals.Clusters[clusterA] != 2 || totals.Accounts[pAcct] != 3 || totals.Targets[awsTarget(pAcct, "us-east-1")] != 2 || totals.Targets[awsTarget(pAcct, "eu-west-1")] != 1 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestBuildPlan_OwnerExample_ThresholdOneSendsTwo(t *testing.T) {
	alert := inc(svc2, "restart_loop", model.AlertWarning, 0)
	routes := []store.NotificationRoute{route(1), route(2)}
	scopes := map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 1)), 2: resolved(t, serviceEntry(svc2))}
	plan := build(routes, scopes, []store.NotificationIncident{alert}, threeServices())

	if got := routeIDs(plan, alert); len(got) != 1 || got[0] != 2 {
		t.Fatalf("per-alert routes = %v, want only the service route 2", got)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("groups = %+v, want route 1's cluster group", plan.Groups)
	}
	g := plan.Groups[0]
	if g.RouteID != 1 || g.ScopeKind != "cluster" || g.Failing != 1 || g.Total != 3 || g.Threshold != 1 || g.Kind != KindClusterFailing ||
		g.Title != "A: 1 of 3 services failing" || g.Key != (model.ServiceKey{ClusterKey: clusterA}) || len(g.Members) != 1 || g.Members[0].Service != "svc2" {
		t.Fatalf("group = %+v", g)
	}
	if !plan.Covered[alert.ID] {
		t.Error("the alert is covered")
	}
}

func TestBuildPlan_OwnerExample_ThresholdTwoSendsOne(t *testing.T) {
	alert := inc(svc2, "restart_loop", model.AlertWarning, 0)
	routes := []store.NotificationRoute{route(1), route(2)}
	scopes := map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 2)), 2: resolved(t, serviceEntry(svc2))}
	plan := build(routes, scopes, []store.NotificationIncident{alert}, threeServices())

	if got := routeIDs(plan, alert); len(got) != 1 || got[0] != 2 {
		t.Fatalf("per-alert routes = %v, want only the service route 2", got)
	}
	if len(plan.Groups) != 0 {
		t.Fatalf("groups = %+v, want none below the threshold", plan.Groups)
	}
	if len(plan.Below) != 1 || plan.Below[0].State.RouteID != 1 || plan.Below[0].State.Failing != 1 || plan.Below[0].State.Total != 3 || plan.Below[0].Open {
		t.Fatalf("below = %+v, want route 1's entry at 1 of 3", plan.Below)
	}

	// A second service fails: now route 1 sends too.
	second := inc(svc3, "restart_loop", model.AlertWarning, 0)
	plan = build(routes, scopes, []store.NotificationIncident{alert, second}, threeServices())
	if len(plan.Groups) != 1 || plan.Groups[0].Failing != 2 || len(plan.Below) != 0 {
		t.Fatalf("with two failing: groups %+v below %+v", plan.Groups, plan.Below)
	}
}

func TestBuildPlan_ClusterEntryNeverMatchesPerAlert(t *testing.T) {
	alerts := []store.NotificationIncident{inc(svc1, "restart_loop", model.AlertWarning, 0), inc(svc2, "cannot_place", model.AlertDanger, 0)}
	plan := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 1))}, alerts, threeServices())
	if len(plan.PerAlert) != 0 {
		t.Fatalf("per-alert = %v, a cluster entry never matches per alert", plan.PerAlert)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].Failing != 2 {
		t.Fatalf("groups = %+v", plan.Groups)
	}
}

func TestBuildPlan_CountsDistinctServicesPassingTheRouteFilters(t *testing.T) {
	twoOnOne := []store.NotificationIncident{
		inc(svc1, "restart_loop", model.AlertWarning, 0), inc(svc1, "cannot_place", model.AlertDanger, 0),
	}
	scopes := map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 2))}
	plan := build([]store.NotificationRoute{route(1)}, scopes, twoOnOne, threeServices())
	if len(plan.Groups) != 0 {
		t.Fatalf("two alerts on one service count once: groups = %+v", plan.Groups)
	}

	mixed := []store.NotificationIncident{inc(svc1, "restart_loop", model.AlertWarning, 0), inc(svc2, "cannot_place", model.AlertDanger, 0)}
	criticalOnly := route(1, func(r *store.NotificationRoute) { r.MinSeverity = "critical" })
	plan = build([]store.NotificationRoute{criticalOnly}, scopes, mixed, threeServices())
	if len(plan.Groups) != 0 || len(plan.Below) != 1 || plan.Below[0].State.Failing != 1 {
		t.Fatalf("a warning must not count on a critical-only route: groups %+v below %+v", plan.Groups, plan.Below)
	}

	noRestartKind := route(1, func(r *store.NotificationRoute) { r.Kinds = []string{"cannot_place"} })
	plan = build([]store.NotificationRoute{noRestartKind}, scopes, mixed, threeServices())
	if len(plan.Groups) != 0 || len(plan.Below) != 1 || plan.Below[0].State.Failing != 1 {
		t.Fatalf("an unchecked kind must not count: groups %+v below %+v", plan.Groups, plan.Below)
	}

	plan = build([]store.NotificationRoute{route(1)}, scopes, mixed, threeServices())
	if len(plan.Groups) != 1 || plan.Groups[0].Failing != 2 {
		t.Fatalf("both services count: %+v", plan.Groups)
	}
}

func TestBuildPlan_MixedEntriesEachInTheirOwnMode(t *testing.T) {
	other := model.ClusterKey{AccountID: pAcct, Region: "us-east-1", Cluster: "B"}
	b1 := model.ServiceKey{ClusterKey: other, Service: "b1"}
	alerts := []store.NotificationIncident{inc(svc1, "restart_loop", model.AlertWarning, 0), inc(b1, "restart_loop", model.AlertWarning, 0)}
	scopes := map[int64]Scope{1: resolved(t, serviceEntry(svc1), clusterEntry(other, 1))}
	plan := build([]store.NotificationRoute{route(1)}, scopes, alerts, totalsOf(svc1, b1))
	if got := routeIDs(plan, alerts[0]); len(got) != 1 {
		t.Fatalf("the service entry takes its alert per alert: %v", plan.PerAlert)
	}
	if got := routeIDs(plan, alerts[1]); len(got) != 0 {
		t.Fatalf("the cluster entry must not take b1 per alert: %v", got)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].Key.Cluster != "B" {
		t.Fatalf("groups = %+v, want the cluster B group", plan.Groups)
	}
}

func TestBuildPlan_AccountEntryCountsAcrossClustersAndHonoursRegion(t *testing.T) {
	east := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "us-east-1", Cluster: "A"}, Service: "a"}
	eastB := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "us-east-1", Cluster: "B"}, Service: "b"}
	west := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "eu-west-1", Cluster: "C"}, Service: "c"}
	elsewhere := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct2, Region: "us-east-1", Cluster: "A"}, Service: "a"}
	totals := totalsOf(east, eastB, west, elsewhere, model.ServiceKey{ClusterKey: east.ClusterKey, Service: "ok"})
	alerts := []store.NotificationIncident{
		inc(east, "restart_loop", model.AlertWarning, 0), inc(eastB, "restart_loop", model.AlertWarning, 0),
		inc(west, "restart_loop", model.AlertWarning, 0), inc(elsewhere, "restart_loop", model.AlertWarning, 0),
	}
	all := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, accountEntry(pAcct, "", 3))}, alerts, totals)
	if len(all.Groups) != 1 || all.Groups[0].Failing != 3 || all.Groups[0].Total != 4 || all.Groups[0].ScopeKind != "account" ||
		all.Groups[0].Kind != KindAccountFailing || !strings.HasPrefix(all.Groups[0].Title, "prod: 3 of 4") {
		t.Fatalf("all regions: %+v", all.Groups)
	}
	one := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, accountEntry(pAcct, "us-east-1", 2))}, alerts, totals)
	if len(one.Groups) != 1 || one.Groups[0].Failing != 2 || one.Groups[0].Total != 3 || !strings.HasPrefix(one.Groups[0].Title, "prod / us-east-1: 2 of 3") ||
		one.Groups[0].Key.Region != "us-east-1" {
		t.Fatalf("one region: %+v", one.Groups)
	}
	both := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, accountEntry(pAcct, "", 1), accountEntry(pAcct, "us-east-1", 1))}, alerts, totals)
	if len(both.Groups) != 2 {
		t.Fatalf("the same account as all regions and as one region are two groups: %+v", both.Groups)
	}
}

func TestBuildPlan_UnreachableOpensTheAccountGroupRegardlessOfThreshold(t *testing.T) {
	acctKey := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "eu-west-1"}}
	unreachable := inc(acctKey, "account_unreachable", model.AlertDanger, 0)
	unreachable.Text = "The role cannot be assumed."
	scopes := map[int64]Scope{1: resolved(t, accountEntry(pAcct, "", 5))}
	plan := build([]store.NotificationRoute{route(1)}, scopes, []store.NotificationIncident{unreachable}, threeServices())
	if len(plan.Groups) != 1 {
		t.Fatalf("groups = %+v, want the account group opened by the unreachable account", plan.Groups)
	}
	g := plan.Groups[0]
	if g.Title != "prod / eu-west-1: unreachable" || g.Severity != model.AlertDanger || !strings.HasPrefix(g.Text, "The role cannot be assumed.") {
		t.Errorf("group = %+v", g)
	}

	// Two regions unreachable under an all-regions entry are joined in the title.
	second := inc(model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: pAcct, Region: "us-east-1"}}, "account_unreachable", model.AlertDanger, 0)
	plan = build([]store.NotificationRoute{route(1)}, scopes, []store.NotificationIncident{unreachable, second}, threeServices())
	if len(plan.Groups) != 1 || plan.Groups[0].Title != "prod / eu-west-1, us-east-1: unreachable" {
		t.Errorf("two regions: %+v", plan.Groups)
	}

	// Kinds that leave account_unreachable out: nothing opens.
	noUnreachable := route(1, func(r *store.NotificationRoute) { r.Kinds = []string{"restart_loop"} })
	if plan := build([]store.NotificationRoute{noUnreachable}, scopes, []store.NotificationIncident{unreachable}, threeServices()); len(plan.Groups) != 0 {
		t.Errorf("a route without the kind opened %+v", plan.Groups)
	}
	// It never opens a cluster group.
	if plan := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 1))}, []store.NotificationIncident{unreachable}, threeServices()); len(plan.Groups) != 0 {
		t.Errorf("an unreachable account opened a cluster group: %+v", plan.Groups)
	}
	// The empty scope takes it per alert (D48).
	if plan := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: {All: true}}, []store.NotificationIncident{unreachable}, threeServices()); len(routeIDs(plan, unreachable)) != 1 {
		t.Errorf("an empty scope must take the account alert per alert: %v", plan.PerAlert)
	}
}

func TestBuildPlan_UnsettledCapacityAlertsNeitherMatchNorCount(t *testing.T) {
	young := inc(svc2, "service_failing", model.AlertDanger, 90*time.Second)
	routes := []store.NotificationRoute{route(1), route(2), route(3, func(r *store.NotificationRoute) { r.Fallback = true })}
	scopes := map[int64]Scope{1: resolved(t, serviceEntry(svc2)), 2: resolved(t, clusterEntry(clusterA, 1)), 3: {All: true}}
	plan := build(routes, scopes, []store.NotificationIncident{young}, threeServices())
	if len(plan.PerAlert) != 0 || len(plan.Groups) != 0 || len(plan.Below) != 0 || len(plan.Covered) != 0 {
		t.Fatalf("an unsettled service_failing produced %+v", plan)
	}
	settled := inc(svc2, "service_failing", model.AlertDanger, 3*time.Minute)
	plan = build(routes, scopes, []store.NotificationIncident{settled}, threeServices())
	if len(routeIDs(plan, settled)) != 1 || len(plan.Groups) != 1 {
		t.Fatalf("a settled one must notify: %+v", plan)
	}
}

func TestBuildPlan_CatchAllTakesWhatNobodyNotified(t *testing.T) {
	catchAll := route(9, func(r *store.NotificationRoute) { r.Fallback = true })
	routes := []store.NotificationRoute{route(1), catchAll}
	scopes := map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 2)), 9: {All: true}}

	one := inc(svc1, "restart_loop", model.AlertWarning, 0)
	plan := build(routes, scopes, []store.NotificationIncident{one}, threeServices())
	if got := routeIDs(plan, one); len(got) != 1 || got[0] != 9 {
		t.Fatalf("below the threshold the catch-all takes the alert: %v", got)
	}

	two := inc(svc2, "restart_loop", model.AlertWarning, 0)
	plan = build(routes, scopes, []store.NotificationIncident{one, two}, threeServices())
	if len(plan.PerAlert) != 0 || len(plan.Groups) != 1 {
		t.Fatalf("once the cluster group is wanted the catch-all takes neither: per-alert %v groups %+v", plan.PerAlert, plan.Groups)
	}

	// A catch-all with its own cluster entry counts only the uncovered services.
	elsewhere := model.ClusterKey{AccountID: pAcct, Region: "us-east-1", Cluster: "B"}
	b1 := model.ServiceKey{ClusterKey: elsewhere, Service: "b1"}
	b2 := model.ServiceKey{ClusterKey: elsewhere, Service: "b2"}
	catchGroup := route(9, func(r *store.NotificationRoute) { r.Fallback = true })
	specific := route(2)
	scopes = map[int64]Scope{2: resolved(t, serviceEntry(b1)), 9: resolved(t, clusterEntry(elsewhere, 2))}
	alerts := []store.NotificationIncident{inc(b1, "restart_loop", model.AlertWarning, 0), inc(b2, "restart_loop", model.AlertWarning, 0)}
	plan = build([]store.NotificationRoute{specific, catchGroup}, scopes, alerts, totalsOf(b1, b2))
	if len(plan.Groups) != 0 || len(plan.Below) != 1 || plan.Below[0].State.Failing != 1 {
		t.Fatalf("the catch-all's cluster entry must count only b2 (b1 is covered): groups %+v below %+v", plan.Groups, plan.Below)
	}
}

func TestBuildPlan_DisabledRoutesDoNothing(t *testing.T) {
	alert := inc(svc1, "restart_loop", model.AlertWarning, 0)
	off := route(1, func(r *store.NotificationRoute) { r.Enabled = false })
	plan := build([]store.NotificationRoute{off}, map[int64]Scope{1: {All: true}}, []store.NotificationIncident{alert}, threeServices())
	if len(plan.PerAlert) != 0 || len(plan.Covered) != 0 {
		t.Fatalf("a disabled route took an alert: %+v", plan)
	}
}

func TestBuildPlan_GroupTextListsTenThenMore(t *testing.T) {
	var alerts []store.NotificationIncident
	var all []model.ServiceKey
	for i := 0; i < 13; i++ {
		k := model.ServiceKey{ClusterKey: clusterA, Service: fmt.Sprintf("s%02d", i)}
		all = append(all, k)
		alerts = append(alerts, inc(k, "restart_loop", model.AlertWarning, 0))
	}
	plan := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 4))}, alerts, totalsOf(all...))
	g := plan.Groups[0]
	lines := strings.Split(g.Text, "\n")
	if g.Failing != 13 || len(g.Members) != 13 {
		t.Fatalf("failing %d members %d", g.Failing, len(g.Members))
	}
	if len(lines) != 12 || lines[10] != "and 3 more" || lines[11] != "Notifies when 4 or more services are failing." {
		t.Fatalf("text lines = %q", lines)
	}
	if !strings.HasPrefix(lines[0], "s00 restart_loop") {
		t.Errorf("first line = %q", lines[0])
	}
	one := build([]store.NotificationRoute{route(1)}, map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 1))}, alerts[:1], totalsOf(all...))
	if !strings.HasSuffix(one.Groups[0].Text, "Notifies when any service is failing.") {
		t.Errorf("threshold 1 text = %q", one.Groups[0].Text)
	}
}

func TestBuildPlan_MembersAreSortedAndStable(t *testing.T) {
	crit := inc(svc3, "cannot_place", model.AlertDanger, time.Minute)
	warnOld := inc(svc1, "restart_loop", model.AlertWarning, 10*time.Minute)
	warnNew := inc(svc1, "unhealthy_task", model.AlertWarning, time.Minute)
	other := inc(svc2, "restart_loop", model.AlertWarning, time.Minute)
	alerts := []store.NotificationIncident{crit, warnOld, warnNew, other}
	scopes := map[int64]Scope{1: resolved(t, clusterEntry(clusterA, 1))}
	reference := build([]store.NotificationRoute{route(1)}, scopes, alerts, threeServices()).Groups[0]
	if reference.Severity != model.AlertDanger || reference.Members[0].Service != "svc3" || !reference.Members[0].Critical {
		t.Fatalf("critical first: %+v", reference.Members)
	}
	if reference.Members[1].Service != "svc1" || reference.Members[1].Kind != "unhealthy_task" {
		t.Errorf("a service shows its newest alert of equal severity: %+v", reference.Members[1])
	}
	for i := 0; i < 20; i++ {
		shuffled := append([]store.NotificationIncident(nil), alerts...)
		rand.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := build([]store.NotificationRoute{route(1)}, scopes, shuffled, threeServices()).Groups[0]
		if fmt.Sprint(got.Members) != fmt.Sprint(reference.Members) || got.Text != reference.Text || got.Title != reference.Title {
			t.Fatalf("input order changed the group:\n%+v\n%+v", got, reference)
		}
	}
}
