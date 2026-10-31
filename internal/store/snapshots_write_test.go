package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestUpsertClusters_UnchangedRowsAreNotWritten(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	rows := []model.Cluster{
		{Key: clusterKey("staging-web"), ARN: "arn:c1", Status: "ACTIVE", RunningTasks: 3},
		{Key: clusterKey("core-services"), ARN: "arn:c2", Status: "ACTIVE", RunningTasks: 5},
	}
	rows[0].PolledAt = time.Now().Add(-time.Hour)
	rows[1].PolledAt = rows[0].PolledAt

	res, err := s.UpsertClusters(ctx, rows)
	if err != nil || res != (store.WriteResult{Inserted: 2}) {
		t.Fatalf("first upsert = %+v, %v; want 2 inserted", res, err)
	}
	var before time.Time
	if err := s.Pool.QueryRow(ctx, `select polled_at from clusters where cluster_name = 'staging-web'`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	for i := range rows { // only the volatile fields differ
		rows[i].PolledAt = time.Now()
		rows[i].Raw = []byte(`{"volatile":true}`)
	}
	res, err = s.UpsertClusters(ctx, rows)
	if err != nil || res != (store.WriteResult{Unchanged: 2}) {
		t.Fatalf("second upsert = %+v, %v; want 2 unchanged", res, err)
	}
	var after time.Time
	if err := s.Pool.QueryRow(ctx, `select polled_at from clusters where cluster_name = 'staging-web'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("polled_at changed from %v to %v for an unchanged row", before, after)
	}

	rows[0].RunningTasks = 4
	res, err = s.UpsertClusters(ctx, rows)
	if err != nil || res != (store.WriteResult{Updated: 1, Unchanged: 1}) {
		t.Fatalf("third upsert = %+v, %v; want 1 updated 1 unchanged", res, err)
	}
}

func TestUpsertServices_SameNameDifferentClustersStayDistinct(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	rows := []model.Service{
		{Key: serviceKey("staging-web", "api"), Desired: 1},
		{Key: serviceKey("core-services", "api"), Desired: 4},
	}
	if _, err := s.UpsertServices(ctx, rows); err != nil {
		t.Fatalf("UpsertServices: %v", err)
	}
	got := map[string]int{}
	r, err := s.Pool.Query(ctx, `select cluster_name, desired from services where service_name = 'api'`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for r.Next() {
		var cluster string
		var desired int
		if err := r.Scan(&cluster, &desired); err != nil {
			t.Fatal(err)
		}
		got[cluster] = desired
	}
	if len(got) != 2 || got["staging-web"] != 1 || got["core-services"] != 4 {
		t.Fatalf("services = %v, want staging-web=1 core-services=4", got)
	}
}

func stoppedTask(cluster, arn string, stoppedAt time.Time) model.Task {
	exit := 137
	return model.Task{
		Cluster: clusterKey(cluster), ARN: arn, ServiceName: "api", LastStatus: "STOPPED", DesiredStatus: "STOPPED",
		StopCode: "EssentialContainerExited", StoppedReason: "OutOfMemoryError: Container killed",
		StoppedAt: &stoppedAt, Containers: []model.Container{{Name: "app", ExitCode: &exit}},
	}
}

func TestUpsertTasks_StoppedRowsSurviveLaterUpserts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	stoppedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := s.UpsertTasks(ctx, []model.Task{stoppedTask("staging-web", "arn:t/stopped", stoppedAt)}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	running := model.Task{Cluster: clusterKey("staging-web"), ARN: "arn:t/running", ServiceName: "api", LastStatus: "RUNNING"}
	if _, err := s.UpsertTasks(ctx, []model.Task{running}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var got time.Time
	err := s.Pool.QueryRow(ctx, `select stopped_at from tasks where task_arn = 'arn:t/stopped'`).Scan(&got)
	if err != nil {
		t.Fatalf("stopped row missing after later upsert: %v", err)
	}
	if !got.Equal(stoppedAt) {
		t.Fatalf("stopped_at = %v, want %v", got, stoppedAt)
	}
}

func TestUpsertTasks_ComputesStopReasonGroupAndExitCode(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	running := model.Task{Cluster: clusterKey("staging-web"), ARN: "arn:t/running", LastStatus: "RUNNING", StoppedReason: "ignored"}
	rows := []model.Task{stoppedTask("staging-web", "arn:t/stopped", time.Now()), running}
	if _, err := s.UpsertTasks(ctx, rows); err != nil {
		t.Fatalf("UpsertTasks: %v", err)
	}
	var group *string
	var exit *int
	err := s.Pool.QueryRow(ctx, `select stop_reason_group, exit_code from tasks where task_arn = 'arn:t/stopped'`).Scan(&group, &exit)
	if err != nil {
		t.Fatal(err)
	}
	if group == nil || *group != "Out of memory (exit 137)" || exit == nil || *exit != 137 {
		t.Fatalf("stopped task group/exit = %v/%v, want Out of memory (exit 137)/137", group, exit)
	}
	var runGroup, runService *string
	err = s.Pool.QueryRow(ctx, `select stop_reason_group, service_name from tasks where task_arn = 'arn:t/running'`).Scan(&runGroup, &runService)
	if err != nil {
		t.Fatal(err)
	}
	if runGroup != nil || runService != nil {
		t.Fatalf("running task group/service = %v/%v, want NULL/NULL", runGroup, runService)
	}
}

func TestMarkTasksMissing_StopsUnlistedRunning(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	k := clusterKey("staging-web")
	rows := []model.Task{
		{Cluster: k, ARN: "arn:t/gone", LastStatus: "RUNNING", DesiredStatus: "RUNNING"},
		{Cluster: k, ARN: "arn:t/kept", LastStatus: "RUNNING", DesiredStatus: "RUNNING"},
		{Cluster: k, ARN: "arn:t/pending-gone", LastStatus: "PENDING", DesiredStatus: "RUNNING"},
		{Cluster: clusterKey("other"), ARN: "arn:t/other", LastStatus: "RUNNING", DesiredStatus: "RUNNING"},
	}
	if _, err := s.UpsertTasks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	if err := s.MarkTasksMissing(ctx, k, []string{"arn:t/gone", "arn:t/pending-gone", "arn:t/other"}, now); err != nil {
		t.Fatalf("MarkTasksMissing: %v", err)
	}
	running, err := s.KnownTaskARNs(ctx, k, "RUNNING")
	if err != nil || len(running) != 1 || running[0] != "arn:t/kept" {
		t.Fatalf("running = %v, %v; want [arn:t/kept]", running, err)
	}
	if open, err := s.KnownTaskARNs(ctx, k, ""); err != nil || len(open) != 1 || open[0] != "arn:t/kept" {
		t.Fatalf("non-STOPPED = %v, %v; want only [arn:t/kept] (PENDING rows must be stopped too)", open, err)
	}
	var status, code string
	var stoppedAt time.Time
	err = s.Pool.QueryRow(ctx, `select last_status, stop_code, stopped_at from tasks where task_arn = 'arn:t/gone'`).Scan(&status, &code, &stoppedAt)
	if err != nil || status != "STOPPED" || code != "Unknown" || !stoppedAt.Equal(now) {
		t.Fatalf("gone = %v %v %v, %v", status, code, stoppedAt, err)
	}
	otherRunning, _ := s.KnownTaskARNs(ctx, clusterKey("other"), "RUNNING")
	if len(otherRunning) != 1 {
		t.Fatalf("other cluster's task must be untouched, running = %v", otherRunning)
	}
}

func TestDeleteClustersNotIn_RemovesChildrenOfRemovedClusters(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.UpsertClusters(ctx, []model.Cluster{{Key: clusterKey("keep")}, {Key: clusterKey("drop")}}); err != nil {
		t.Fatal(err)
	}
	svc := []model.Service{{Key: serviceKey("keep", "a")}, {Key: serviceKey("drop", "b")}}
	if _, err := s.UpsertServices(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTasks(ctx, []model.Task{
		{Cluster: clusterKey("drop"), ARN: "arn:t/1", LastStatus: "RUNNING"},
		{Cluster: clusterKey("keep"), ARN: "arn:t/2", LastStatus: "RUNNING"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCountsHistory(ctx, time.Now(), svc); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteClustersNotIn(ctx, awsxTarget(), []model.ClusterKey{clusterKey("keep")})
	if err != nil || n != 1 {
		t.Fatalf("deleted = %d, %v; want 1", n, err)
	}
	for table, want := range map[string]int{"clusters": 1, "services": 1, "tasks": 1, "service_counts_history": 1} {
		var got int
		if err := s.Pool.QueryRow(ctx, `select count(*) from `+table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s rows = %d, %v; want %d", table, got, err, want)
		}
	}
}

func TestInsertServiceEvents_ReturnsOnlyNewCount(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	ev := model.ServiceEvent{Key: serviceKey("c", "api"), ID: "e1", CreatedAt: time.Now(), Message: "started"}
	if n, err := s.InsertServiceEvents(ctx, []model.ServiceEvent{ev}); err != nil || n != 1 {
		t.Fatalf("first = %d, %v; want 1", n, err)
	}
	ev2 := ev
	ev2.ID = "e2"
	if n, err := s.InsertServiceEvents(ctx, []model.ServiceEvent{ev, ev2}); err != nil || n != 1 {
		t.Fatalf("second = %d, %v; want 1", n, err)
	}
}

func TestTaskDefinitions_ImmutableUpsert(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	td := model.TaskDefinitionRef{AccountID: "111111111111", Region: "eu-west-1", ARN: "arn:td:1", Family: "api", Revision: 1}
	if ok, err := s.HasTaskDefinition(ctx, td.AccountID, td.Region, td.ARN); err != nil || ok {
		t.Fatalf("before = %v, %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.UpsertTaskDefinition(ctx, td); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	if ok, err := s.HasTaskDefinition(ctx, td.AccountID, td.Region, td.ARN); err != nil || !ok {
		t.Fatalf("after = %v, %v", ok, err)
	}
}
