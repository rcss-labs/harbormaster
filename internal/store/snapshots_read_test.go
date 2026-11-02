package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const readAccount = "111111111111"

type readFixture struct {
	s                  *store.Store
	dev, prod, staging authz.TagID
	now                time.Time
}

func (f readFixture) vis(ids ...authz.TagID) store.Visibility {
	v := store.Visibility{TagIDs: []int64{}}
	for _, id := range ids {
		v.TagIDs = append(v.TagIDs, int64(id))
	}
	return v
}

func intPtr(n int) *int { return &n }

func readTaskARN(cluster, id string) string {
	return "arn:aws:ecs:eu-west-1:" + readAccount + ":task/" + cluster + "/" + id
}

// newReadFixture builds: core-services (Prod) with payments-api and notifications;
// background-jobs (untagged) with image-resizer and a standalone db-migrate task;
// staging-web (Staging) with web tagged Prod directly; plus an empty cluster.
func newReadFixture(t *testing.T) readFixture {
	t.Helper()
	s := storetest.Open(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dev, err := s.DevTag(ctx)
	must(err)
	prod, err := s.CreateTag(ctx, "Prod", "red")
	must(err)
	staging, err := s.CreateTag(ctx, "Staging", "blue")
	must(err)
	now := time.Now().UTC().Truncate(time.Second)

	_, err = s.UpsertClusters(ctx, []model.Cluster{
		{Key: clusterKey("core-services"), Status: "ACTIVE", PolledAt: now},
		{Key: clusterKey("background-jobs"), Status: "ACTIVE", PolledAt: now},
		{Key: clusterKey("staging-web"), Status: "ACTIVE", PolledAt: now},
		{Key: clusterKey("empty-cluster"), Status: "ACTIVE", PolledAt: now},
	})
	must(err)
	must(s.SetClusterTag(ctx, clusterKey("core-services"), prod.ID))
	must(s.SetClusterTag(ctx, clusterKey("staging-web"), staging.ID))

	services := []model.Service{
		{Key: serviceKey("core-services", "payments-api"), Desired: 2, Running: 2, EnableExecuteCommand: true,
			LoadBalancers: []model.ServiceLoadBalancer{{TargetGroupARN: "arn:tg", ContainerName: "app", ContainerPort: 80}},
			Deployments:   []model.Deployment{{ID: "d1", Status: "PRIMARY", Running: 2}}, PolledAt: now},
		{Key: serviceKey("core-services", "notifications"), Desired: 1, Running: 1, PolledAt: now},
		{Key: serviceKey("background-jobs", "image-resizer"), Desired: 1, Running: 1, PolledAt: now},
		{Key: serviceKey("staging-web", "web"), Desired: 1, Running: 1, PolledAt: now},
	}
	_, err = s.UpsertServices(ctx, services)
	must(err)
	prodTag := prod.ID
	must(s.SetServiceTag(ctx, serviceKey("staging-web", "web"), &prodTag))

	stoppedAt := now.Add(-time.Hour)
	startedAt := now.Add(-2 * time.Hour)
	task := func(cluster, service, id, status, ip string) model.Task {
		return model.Task{Cluster: clusterKey(cluster), ARN: readTaskARN(cluster, id), ServiceName: service,
			LastStatus: status, PrivateIP: ip, Family: service + "-fam", AvailabilityZone: "eu-west-1a",
			CapacityProvider: "FARGATE", CreatedAt: startedAt, StartedAt: &startedAt, PolledAt: now}
	}
	failed := task("core-services", "payments-api", "p2failed", "STOPPED", "10.0.1.2")
	failed.StoppedAt = &stoppedAt
	failed.Containers = []model.Container{{Name: "app", ExitCode: intPtr(1)}}
	unhealthy := task("core-services", "payments-api", "p3unhealthy", "RUNNING", "10.0.1.3")
	unhealthy.HealthStatus = "UNHEALTHY"
	spot := task("core-services", "notifications", "n1spot", "RUNNING", "10.0.1.4")
	spot.CapacityProvider = "FARGATE_SPOT"
	spot.AvailabilityZone = "eu-west-1b"
	resized := task("background-jobs", "image-resizer", "i1done", "STOPPED", "10.0.2.2")
	resized.StoppedAt = &stoppedAt
	resized.Containers = []model.Container{{Name: "app", ExitCode: intPtr(0)}}
	migrate := task("background-jobs", "", "m1migrate", "STOPPED", "10.0.2.3")
	migrate.Group = "family:db-migrate"
	migrate.StoppedAt = &stoppedAt
	migrate.Containers = []model.Container{{Name: "migrate", ExitCode: intPtr(137)}}
	_, err = s.UpsertTasks(ctx, []model.Task{
		task("core-services", "payments-api", "p1ok", "RUNNING", "10.0.1.1"),
		failed, unhealthy, spot, resized, migrate,
		task("staging-web", "web", "w1ok", "RUNNING", "10.0.3.1"),
	})
	must(err)

	_, err = s.InsertServiceEvents(ctx, []model.ServiceEvent{
		{Key: serviceKey("core-services", "payments-api"), ID: "e1", CreatedAt: now.Add(-3 * time.Minute), Message: "steady"},
		{Key: serviceKey("background-jobs", "image-resizer"), ID: "e2", CreatedAt: now.Add(-2 * time.Minute), Message: "started"},
	})
	must(err)
	must(s.InsertCountsHistory(ctx, now, services[:2]))
	return readFixture{s: s, dev: dev.ID, prod: prod.ID, staging: staging.ID, now: now}
}

func clusterNames(rows []store.ClusterRow) []string {
	names := []string{}
	for _, r := range rows {
		names = append(names, r.Key.Cluster)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListClusters_VisibleWhenAnyServiceVisible(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	cases := []struct {
		name string
		vis  store.Visibility
		want []string
	}{
		{"prod", fx.vis(fx.prod), []string{"core-services", "staging-web"}},
		{"dev", fx.vis(fx.dev), []string{"background-jobs"}},
		{"admin", store.Visibility{Admin: true}, []string{"background-jobs", "core-services", "empty-cluster", "staging-web"}},
	}
	for _, c := range cases {
		rows, err := fx.s.ListClusters(ctx, store.TargetFilter{}, c.vis)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := clusterNames(rows); !equalStrings(got, c.want) {
			t.Errorf("%s: clusters = %v, want %v", c.name, got, c.want)
		}
	}
	rows, err := fx.s.ListClusters(ctx, store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r.Key.Cluster {
		case "core-services":
			if !r.TagSet || r.TagID != fx.prod {
				t.Errorf("core-services tag = %v set=%v, want Prod set", r.TagID, r.TagSet)
			}
		case "background-jobs":
			if r.TagSet || r.TagID != fx.dev {
				t.Errorf("background-jobs tag = %v set=%v, want Dev unset", r.TagID, r.TagSet)
			}
		}
	}
	if _, err := fx.s.GetCluster(ctx, clusterKey("background-jobs"), fx.vis(fx.prod)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetCluster invisible error = %v, want ErrNotFound", err)
	}
}

func TestGetServiceView_InvisibleIsNotFound(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	_, err := fx.s.GetServiceView(ctx, serviceKey("background-jobs", "image-resizer"), fx.vis(fx.prod))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invisible error = %v, want ErrNotFound", err)
	}
	row, err := fx.s.GetServiceView(ctx, serviceKey("core-services", "payments-api"), fx.vis(fx.prod))
	if err != nil {
		t.Fatal(err)
	}
	if len(row.LoadBalancers) != 1 || len(row.Deployments) != 1 || !row.EnableExecuteCommand {
		t.Errorf("service columns not decoded: %+v", row.Service)
	}
}

func TestGetService_And_GetTask_IgnoreVisibility(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	svc, err := fx.s.GetService(ctx, serviceKey("background-jobs", "image-resizer"))
	if err != nil || svc.Key.Service != "image-resizer" {
		t.Fatalf("GetService = %+v, %v", svc, err)
	}
	task, err := fx.s.GetTask(ctx, readAccount, "eu-west-1", readTaskARN("background-jobs", "m1migrate"))
	if err != nil || task.Group != "family:db-migrate" || len(task.Containers) != 1 || task.CreatedAt.IsZero() {
		t.Fatalf("GetTask = %+v, %v", task, err)
	}
	if _, err := fx.s.GetService(ctx, serviceKey("core-services", "nope")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetService missing = %v, want ErrNotFound", err)
	}
	if _, err := fx.s.GetTask(ctx, readAccount, "eu-west-1", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTask missing = %v, want ErrNotFound", err)
	}
	if _, err := fx.s.GetTaskDefinition(ctx, readAccount, "eu-west-1", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTaskDefinition missing = %v, want ErrNotFound", err)
	}
	td := model.TaskDefinitionRef{AccountID: readAccount, Region: "eu-west-1", ARN: "arn:td", Family: "f", Revision: 3,
		Containers: []model.ContainerDef{{Name: "app", Image: "img:1"}}, PolledAt: fx.now}
	if err := fx.s.UpsertTaskDefinition(ctx, td); err != nil {
		t.Fatal(err)
	}
	got, err := fx.s.GetTaskDefinition(ctx, readAccount, "eu-west-1", "arn:td")
	if err != nil || got.Revision != 3 || len(got.Containers) != 1 || got.Containers[0].Image != "img:1" {
		t.Fatalf("GetTaskDefinition = %+v, %v", got, err)
	}
}

func TestListServices_InheritedFlag(t *testing.T) {
	fx := newReadFixture(t)
	rows, err := fx.s.ListServices(context.Background(), store.TargetFilter{}, "", store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	inherited := map[string]bool{}
	effective := map[string]authz.TagID{}
	for _, r := range rows {
		inherited[r.Key.Service] = r.TagInherited
		effective[r.Key.Service] = r.EffectiveTagID
	}
	if len(rows) != 4 || inherited["web"] || !inherited["payments-api"] || !inherited["image-resizer"] {
		t.Errorf("inherited = %v (rows %d)", inherited, len(rows))
	}
	if effective["web"] != fx.prod || effective["image-resizer"] != fx.dev || effective["payments-api"] != fx.prod {
		t.Errorf("effective tags = %v", effective)
	}
	only, err := fx.s.ListServices(context.Background(), store.TargetFilter{}, "core-services", store.Visibility{Admin: true})
	if err != nil || len(only) != 2 || only[0].Key.Service != "notifications" {
		t.Errorf("cluster filter/order = %v", only)
	}
}

func listAll(t *testing.T, fx readFixture, tq store.TaskQuery) ([]store.TaskRow, int) {
	t.Helper()
	items, total, err := fx.s.ListTasks(context.Background(), tq)
	if err != nil {
		t.Fatal(err)
	}
	return items, total
}

func TestListTasks_StandaloneUsesClusterTag(t *testing.T) {
	fx := newReadFixture(t)
	hasMigrate := func(vis store.Visibility) bool {
		items, _ := listAll(t, fx, store.TaskQuery{Vis: vis, Filter: "all"})
		for _, it := range items {
			if it.ShortID() == "m1migrate" {
				return true
			}
		}
		return false
	}
	if !hasMigrate(fx.vis(fx.dev)) {
		t.Error("Dev viewer should see standalone db-migrate task")
	}
	if hasMigrate(fx.vis(fx.prod)) {
		t.Error("Prod viewer must not see standalone db-migrate task")
	}
}

func TestListTasks_ProblemsFirstAndPaged(t *testing.T) {
	fx := newReadFixture(t)
	admin := store.Visibility{Admin: true}
	page, total := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "problems", Limit: 2})
	full, fullTotal := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "problems"})
	if total != fullTotal || total != len(full) || total != 6 {
		t.Fatalf("total = %d, unpaged = %d/%d; want 6 (4 running + 2 failed stops)", total, fullTotal, len(full))
	}
	if len(page) != 2 {
		t.Fatalf("page size = %d, want 2", len(page))
	}
	for _, row := range page {
		failedStop := row.LastStatus == "STOPPED" && row.ExitCode != nil && *row.ExitCode != 0
		if !failedStop && row.HealthStatus != "UNHEALTHY" {
			t.Errorf("page row %s is not a problem", row.ShortID())
		}
	}
	// problems filter excludes the clean stopped task; stopped filter holds three.
	for _, row := range full {
		if row.ShortID() == "i1done" {
			t.Error("clean stopped task listed under problems")
		}
	}
	stopped, stoppedTotal := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "stopped"})
	if stoppedTotal != 3 || len(stopped) != 3 {
		t.Errorf("stopped = %d/%d, want 3", len(stopped), stoppedTotal)
	}
	second, _ := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "problems", Limit: 2, Offset: 2})
	if len(second) != 2 || second[0].ARN == page[0].ARN {
		t.Errorf("second page = %d rows", len(second))
	}
	withExec, _ := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "running", Service: "payments-api"})
	if len(withExec) != 2 || !withExec[0].ServiceEnableExec {
		t.Errorf("running payments-api = %+v", withExec)
	}
}

func TestCountsHistory_SumsAcrossServices(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	points, err := fx.s.CountsHistory(ctx, store.TargetFilter{}, "", "", fx.vis(fx.prod), fx.now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Running != 3 || points[0].Desired != 3 {
		t.Fatalf("points = %+v, want one point running 3 desired 3", points)
	}
	none, err := fx.s.CountsHistory(ctx, store.TargetFilter{}, "", "", fx.vis(fx.dev), fx.now.Add(-time.Hour))
	if err != nil || len(none) != 0 {
		t.Errorf("Dev viewer sees Prod history: %+v", none)
	}
}

func TestSearch_MatchesTaskIDPrefixAndIP(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	hits, err := fx.s.Search(ctx, store.TargetFilter{}, admin, "p1o", 10)
	if err != nil || len(hits) != 1 || hits[0].Kind != "task" || hits[0].Label != "p1ok" || hits[0].Sub != "payments-api" {
		t.Fatalf("id prefix hits = %+v, %v", hits, err)
	}
	hits, err = fx.s.Search(ctx, store.TargetFilter{}, admin, "10.0.3.", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].TaskARN != readTaskARN("staging-web", "w1ok") {
		t.Errorf("ip prefix hits = %+v", hits)
	}
	hits, err = fx.s.Search(ctx, store.TargetFilter{}, admin, "core", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Kind != "cluster" || hits[0].Sub != readAccount+" · eu-west-1" {
		t.Errorf("cluster hits = %+v", hits)
	}
	hits, err = fx.s.Search(ctx, store.TargetFilter{}, admin, "-", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || hits[0].Kind != "cluster" {
		t.Errorf("limited hits = %+v", hits)
	}
	hits, err = fx.s.Search(ctx, store.TargetFilter{}, fx.vis(fx.dev), "payments", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("Dev viewer found Prod service: %+v", hits)
	}
	// stopped tasks are not searchable
	hits, err = fx.s.Search(ctx, store.TargetFilter{}, admin, "m1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("stopped task found: %+v", hits)
	}
}

func TestTaskSummary_And_Events_And_Stops(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	since := fx.now.Add(-72 * time.Hour)
	sum, err := fx.s.TaskSummary(ctx, store.TargetFilter{}, admin, since)
	if err != nil {
		t.Fatal(err)
	}
	if sum.RunningHealthy != 3 || sum.RunningUnhealthy != 1 || sum.StoppedError != 2 || sum.StoppedNormal != 1 {
		t.Errorf("summary counts = %+v", sum)
	}
	if sum.ByAZ["eu-west-1a"] != 3 || sum.ByAZ["eu-west-1b"] != 1 {
		t.Errorf("ByAZ = %v", sum.ByAZ)
	}
	if c := sum.ByCluster[clusterKey("core-services")]; c.Fargate != 2 || c.Spot != 1 {
		t.Errorf("ByCluster core-services = %+v", c)
	}
	total := 0
	for _, n := range sum.StopReasons {
		total += n
	}
	if total != 3 {
		t.Errorf("StopReasons = %v, want 3 stops", sum.StopReasons)
	}

	events, err := fx.s.RecentServiceEvents(ctx, store.TargetFilter{}, fx.vis(fx.prod), 10)
	if err != nil || len(events) != 1 || events[0].ID != "e1" {
		t.Errorf("recent events (Prod) = %+v, %v", events, err)
	}
	last, err := fx.s.LastEventAtByCluster(ctx, store.TargetFilter{}, admin)
	if err != nil || len(last) != 2 {
		t.Errorf("last event by cluster = %v, %v", last, err)
	}
	perSvc, err := fx.s.ServiceEvents(ctx, serviceKey("background-jobs", "image-resizer"), 0, 0)
	if err != nil || len(perSvc) != 1 || perSvc[0].Message != "started" {
		t.Errorf("service events = %+v, %v", perSvc, err)
	}

	buckets, err := fx.s.StopsPerHour(ctx, store.TargetFilter{}, "", fx.vis(fx.dev), since)
	if err != nil || len(buckets) != 1 || buckets[0].Normal != 1 || buckets[0].Error != 1 {
		t.Errorf("stops per hour (Dev) = %+v, %v", buckets, err)
	}

	running, err := fx.s.RunningTasks(ctx, store.TargetFilter{}, admin)
	if err != nil || len(running) != 4 {
		t.Fatalf("running tasks = %d, %v", len(running), err)
	}
	for _, r := range running {
		if len(r.Raw) != 0 || len(r.Containers) != 0 {
			t.Errorf("running task %s carries raw/containers", r.ShortID())
		}
	}
}

func TestTaskSummaryFor_ServiceScopeAddsUpToTheTotals(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	since := fx.now.Add(-72 * time.Hour)
	global, err := fx.s.TaskSummary(ctx, store.TargetFilter{}, admin, since)
	if err != nil {
		t.Fatal(err)
	}
	services, err := fx.s.ListServices(ctx, store.TargetFilter{}, "", admin)
	if err != nil {
		t.Fatal(err)
	}
	var sum store.TaskSummary
	azTotal, narrowed := 0, false
	for _, svc := range services {
		part, err := fx.s.TaskSummaryFor(ctx, store.TaskSummaryQuery{
			TargetFilter: store.TargetFilter{AccountID: svc.Key.AccountID, Region: svc.Key.Region},
			Vis:          admin, Cluster: svc.Key.Cluster, Service: svc.Key.Service, Since: since,
		})
		if err != nil {
			t.Fatal(err)
		}
		sum.RunningHealthy += part.RunningHealthy
		sum.RunningUnhealthy += part.RunningUnhealthy
		sum.StoppedError += part.StoppedError
		sum.StoppedNormal += part.StoppedNormal
		for _, n := range part.ByAZ {
			azTotal += n
		}
		narrowed = narrowed || part.RunningHealthy < global.RunningHealthy
	}
	if !narrowed {
		t.Error("service scope did not narrow the summary")
	}
	// standalone tasks belong to no service, so the parts add up to the totals minus those
	if sum.RunningHealthy != global.RunningHealthy || sum.RunningUnhealthy != global.RunningUnhealthy ||
		sum.StoppedNormal != global.StoppedNormal || sum.StoppedError < 1 || sum.StoppedError > global.StoppedError {
		t.Errorf("sum of services %+v inconsistent with global %+v", sum, global)
	}
	if want := global.RunningHealthy + global.RunningUnhealthy; azTotal > want {
		t.Errorf("az total %d exceeds running %d", azTotal, want)
	}
}

// upsertWindowTasks adds a recent (5 min) and an old (30 min) failed stop to payments-api.
func upsertWindowTasks(t *testing.T, fx readFixture) {
	t.Helper()
	started := fx.now.Add(-2 * time.Hour)
	var tasks []model.Task
	for id, ago := range map[string]time.Duration{"p5recent": 5 * time.Minute, "p6old": 30 * time.Minute} {
		stopped := fx.now.Add(-ago)
		tasks = append(tasks, model.Task{Cluster: clusterKey("core-services"), ARN: readTaskARN("core-services", id),
			ServiceName: "payments-api", LastStatus: "STOPPED", Family: "payments-api-fam", AvailabilityZone: "eu-west-1a",
			CapacityProvider: "FARGATE", CreatedAt: started, StartedAt: &started, StoppedAt: &stopped, PolledAt: fx.now,
			Containers: []model.Container{{Name: "app", ExitCode: intPtr(1)}}})
	}
	if _, err := fx.s.UpsertTasks(context.Background(), tasks); err != nil {
		t.Fatal(err)
	}
}

func shortIDs(rows []store.TaskRow) map[string]bool {
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ShortID()] = true
	}
	return ids
}

func TestListTasks_StoppedAfterHidesOldStoppedOnly(t *testing.T) {
	fx := newReadFixture(t)
	upsertWindowTasks(t, fx)
	admin := store.Visibility{Admin: true}
	cutoff := fx.now.Add(-10 * time.Minute)

	all, _ := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "all"})
	unwindowed := shortIDs(all)
	windowed, _ := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "all", StoppedAfter: cutoff})
	ids := shortIDs(windowed)
	if !ids["p5recent"] || ids["p6old"] {
		t.Errorf("all: p5recent=%v p6old=%v, want true/false", ids["p5recent"], ids["p6old"])
	}
	for _, row := range all {
		if row.LastStatus == "RUNNING" && !ids[row.ShortID()] {
			t.Errorf("running task %s hidden by the window", row.ShortID())
		}
	}
	if !unwindowed["p6old"] {
		t.Fatal("fixture: p6old should exist without a window")
	}

	problems, _ := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "problems", StoppedAfter: cutoff})
	pids := shortIDs(problems)
	if !pids["p5recent"] || pids["p6old"] {
		t.Errorf("problems: p5recent=%v p6old=%v, want true/false", pids["p5recent"], pids["p6old"])
	}

	stopped, total := listAll(t, fx, store.TaskQuery{Vis: admin, Filter: "stopped", StoppedAfter: cutoff})
	// Fixture stops are all 1 h old; only the 5-minute stop is inside the window.
	if total != 1 || len(stopped) != 1 || stopped[0].ShortID() != "p5recent" {
		t.Errorf("stopped = %d/%d, want only p5recent", len(stopped), total)
	}
}

func TestListTasks_StoppedAfterZeroMeansNoWindow(t *testing.T) {
	fx := newReadFixture(t)
	upsertWindowTasks(t, fx)
	rows, _ := listAll(t, fx, store.TaskQuery{Vis: store.Visibility{Admin: true}, Filter: "all"})
	if !shortIDs(rows)["p6old"] {
		t.Error("zero StoppedAfter must not hide old stopped tasks")
	}
}

func TestTaskSummaryFor_StatusSinceNarrowsOnlyTheStoppedCounts(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	started := fx.now.Add(-2 * time.Hour)
	recentStop := fx.now.Add(-5 * time.Minute)
	stopped := func(id string, exitCode int) model.Task {
		return model.Task{Cluster: clusterKey("core-services"), ARN: readTaskARN("core-services", id),
			ServiceName: "payments-api", LastStatus: "STOPPED", Family: "payments-api-fam", AvailabilityZone: "eu-west-1a",
			CapacityProvider: "FARGATE", CreatedAt: started, StartedAt: &started, StoppedAt: &recentStop, PolledAt: fx.now,
			Containers: []model.Container{{Name: "app", ExitCode: intPtr(exitCode)}}}
	}
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{stopped("p7recenterr", 1), stopped("p8recentok", 0)}); err != nil {
		t.Fatal(err)
	}
	summarise := func(statusSince time.Time) store.TaskSummary {
		t.Helper()
		sum, err := fx.s.TaskSummaryFor(ctx, store.TaskSummaryQuery{
			Vis: admin, Since: fx.now.Add(-72 * time.Hour), StatusSince: statusSince,
		})
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	stopReasons := func(sum store.TaskSummary) int {
		total := 0
		for _, n := range sum.StopReasons {
			total += n
		}
		return total
	}

	// The fixture already stopped two errors and one normal task an hour ago.
	all := summarise(time.Time{})
	if all.StoppedError != 3 || all.StoppedNormal != 2 || stopReasons(all) != 5 {
		t.Errorf("zero StatusSince = error %d normal %d reasons %d, want 3/2/5", all.StoppedError, all.StoppedNormal, stopReasons(all))
	}
	recent := summarise(fx.now.Add(-10 * time.Minute))
	if recent.StoppedError != 1 || recent.StoppedNormal != 1 {
		t.Errorf("StatusSince 10 min = error %d normal %d, want 1/1", recent.StoppedError, recent.StoppedNormal)
	}
	if stopReasons(recent) != 5 {
		t.Errorf("StatusSince changed stop reasons: %d, want 5", stopReasons(recent))
	}
}
