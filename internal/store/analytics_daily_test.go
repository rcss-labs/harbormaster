package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

type availabilityRow struct{ Samples, OK, MinutesBelow, Unexpected int }

func availabilityRows(t *testing.T, s *store.Store, day string) map[string]availabilityRow {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(),
		`select service_name, samples, samples_ok, minutes_below, unexpected_stops
		 from service_availability_daily where day = $1::date`, day)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]availabilityRow{}
	for rows.Next() {
		var name string
		var r availabilityRow
		if err := rows.Scan(&name, &r.Samples, &r.OK, &r.MinutesBelow, &r.Unexpected); err != nil {
			t.Fatal(err)
		}
		out[name] = r
	}
	return out
}

func TestRollupAvailabilityDay_CountsAndStops(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }

	// payments: 4 samples on the day, 1 below desired; samples on neighbouring days must not count.
	fx.sample(t, at(1, 0), fx.payments, 2, 2)
	fx.sample(t, at(2, 0), fx.payments, 1, 2)
	fx.sample(t, at(3, 0), fx.payments, 2, 2)
	fx.sample(t, at(23, 55), fx.payments, 3, 2) // above desired still counts as ok
	fx.sample(t, at(-1, 0), fx.payments, 0, 2)  // previous day
	fx.sample(t, at(24, 0), fx.payments, 0, 2)  // next day: midnight is exclusive
	// batch: always below desired.
	fx.sample(t, at(5, 0), fx.batch, 0, 1)
	fx.sample(t, at(6, 0), fx.batch, 0, 1)

	stopped := func(id, service string, stopAt time.Time, code, reason string) model.Task {
		return model.Task{Cluster: clusterKey("core"), ARN: readTaskARN("core", id), ServiceName: service, LastStatus: "STOPPED",
			StoppedAt: &stopAt, StopCode: code, StoppedReason: reason, PolledAt: stopAt, CreatedAt: stopAt.Add(-time.Hour)}
	}
	withExit := func(task model.Task, code int) model.Task {
		task.Containers = []model.Container{{Name: "app", ExitCode: &code}}
		return task
	}
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{
		stopped("oom", "payments", at(2, 0), "", "OutOfMemoryError: container killed"),
		withExit(stopped("exit", "payments", at(4, 0), "EssentialContainerExited", ""), 1),
		stopped("deploy", "payments", at(5, 0), "ServiceSchedulerInitiated", "Scaling activity initiated by deployment ecs-svc/1"),
		stopped("scale", "payments", at(6, 0), "", "Scaling activity initiated by (desired count) changes"),
		withExit(stopped("other-day", "payments", at(-2, 0), "EssentialContainerExited", ""), 1),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := fx.s.RollupAvailabilityDay(ctx, day, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got := availabilityRows(t, fx.s, "2026-03-10")
	if n != 2 || len(got) != 2 {
		t.Fatalf("rows written = %d, rows = %v, want 2 (payments and batch)", n, got)
	}
	if want := (availabilityRow{Samples: 4, OK: 3, MinutesBelow: 5, Unexpected: 2}); got["payments"] != want {
		t.Errorf("payments = %+v, want %+v", got["payments"], want)
	}
	if want := (availabilityRow{Samples: 2, OK: 0, MinutesBelow: 10, Unexpected: 0}); got["batch"] != want {
		t.Errorf("batch = %+v, want %+v", got["batch"], want)
	}
}

func TestRollupAvailabilityDay_Idempotent(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	fx.sample(t, day.Add(time.Hour), fx.payments, 1, 2)
	fx.sample(t, day.Add(2*time.Hour), fx.payments, 2, 2)

	if _, err := fx.s.RollupAvailabilityDay(ctx, day, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	first := availabilityRows(t, fx.s, "2026-03-10")
	// A late sample for the same day is picked up by a re-run; nothing is double counted.
	fx.sample(t, day.Add(3*time.Hour), fx.payments, 2, 2)
	for i := 0; i < 2; i++ {
		if _, err := fx.s.RollupAvailabilityDay(ctx, day, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	got := availabilityRows(t, fx.s, "2026-03-10")
	if len(first) != 1 || len(got) != 1 {
		t.Fatalf("rows = %v then %v, want one service", first, got)
	}
	if want := (availabilityRow{Samples: 3, OK: 2, MinutesBelow: 5}); got["payments"] != want {
		t.Errorf("after re-runs = %+v, want %+v", got["payments"], want)
	}
}

func TestDailyDataSinceAndWatermarks(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	since, err := s.DailyDataSince(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{store.TableServiceAvailabilityDaily, store.TableClusterCapacityDaily, store.TableServiceUtilDaily} {
		if v, ok := since[table]; !ok || v != nil {
			t.Errorf("empty %s: DailyDataSince = %v (present %v), want present and nil", table, v, ok)
		}
	}
	for _, day := range []string{"2026-03-08", "2026-03-10", "2026-03-09"} {
		if _, err := s.Pool.Exec(ctx,
			`insert into service_availability_daily values ('a','r','c','s',$1::date,1,1,0,0)`, day); err != nil {
			t.Fatal(err)
		}
	}
	since, err = s.DailyDataSince(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marks, err := s.DailyWatermarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, last := since[store.TableServiceAvailabilityDaily], marks[store.TableServiceAvailabilityDaily]
	if first == nil || !first.Equal(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("data since = %v, want 2026-03-08", first)
	}
	if last == nil || !last.Equal(time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("watermark = %v, want 2026-03-10", last)
	}
	if since[store.TableClusterCapacityDaily] != nil {
		t.Errorf("capacity data since = %v, want nil", since[store.TableClusterCapacityDaily])
	}
}

func TestRunningTaskCapacityByCluster_SumsPerCluster(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := func(cluster, id, cpu, mem, provider, status string) model.Task {
		return model.Task{Cluster: clusterKey(cluster), ARN: readTaskARN(cluster, id), LastStatus: status, CPU: cpu, Memory: mem,
			CapacityProvider: provider, PolledAt: now, CreatedAt: now}
	}
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{
		task("core", "a", "512", "1024", "FARGATE", "RUNNING"),
		task("core", "b", "1024", "2048", "FARGATE_SPOT", "RUNNING"),
		task("core", "c", "4096", "8192", "FARGATE", "PENDING"),
		task("core", "d", "", "", "", "RUNNING"), // EC2 task without a task-level size counts as zero
	}); err != nil {
		t.Fatal(err)
	}
	got, err := fx.s.RunningTaskCapacityByCluster(ctx, store.TargetFilter{AccountID: "111111111111", Region: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.ClusterCapacitySample{}
	for _, c := range got {
		byName[c.Cluster.Cluster] = c
	}
	if len(got) != 2 {
		t.Fatalf("clusters = %d, want 2 (core and the idle jobs cluster)", len(got))
	}
	if core := byName["core"]; core.VCPU != 1.5 || core.MemGiB != 3 || core.Tasks != 3 || core.Spot != 1 {
		t.Errorf("core = %+v, want vcpu 1.5, mem 3 GiB, 3 tasks, 1 spot", core)
	}
	if jobs, ok := byName["jobs"]; !ok || jobs.Tasks != 0 || jobs.VCPU != 0 {
		t.Errorf("jobs = %+v (present %v), want a zero sample", jobs, ok)
	}
}

func TestUpsertClusterCapacitySample_AccumulatesPerDay(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	key := clusterKey("core")
	sample := func(vcpu, mem float64, tasks, spot int) []store.ClusterCapacitySample {
		return []store.ClusterCapacitySample{{Cluster: key, VCPU: vcpu, MemGiB: mem, Tasks: tasks, Spot: spot}}
	}
	for _, smp := range [][]store.ClusterCapacitySample{sample(2, 4, 3, 1), sample(6, 2, 1, 0), sample(1, 1, 2, 2)} {
		if err := s.UpsertClusterCapacitySample(ctx, day, smp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertClusterCapacitySample(ctx, day.AddDate(0, 0, 1), sample(1, 1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	var samples, tasksSum, tasksMax, spotSum int
	var vcpuSum, vcpuMax, memSum, memMax float64
	err := s.Pool.QueryRow(ctx,
		`select samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum
		 from cluster_capacity_daily where cluster_name = 'core' and day = '2026-03-10'`).
		Scan(&samples, &vcpuSum, &vcpuMax, &memSum, &memMax, &tasksSum, &tasksMax, &spotSum)
	if err != nil {
		t.Fatal(err)
	}
	if samples != 3 || vcpuSum != 9 || vcpuMax != 6 || memSum != 7 || memMax != 4 || tasksSum != 6 || tasksMax != 3 || spotSum != 3 {
		t.Errorf("row = samples %d vcpu %v/%v mem %v/%v tasks %d/%d spot %d", samples, vcpuSum, vcpuMax, memSum, memMax, tasksSum, tasksMax, spotSum)
	}
	var rows int
	if err := s.Pool.QueryRow(ctx, `select count(*) from cluster_capacity_daily`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("rows = %d (err %v), want one per cluster per day = 2", rows, err)
	}
}

func TestUpsertServiceUtilDay_ReplacesTheDay(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	key := serviceKey("core", "api")
	rows := []store.ServiceUtilRow{{Key: key, CPUAvg: f(10), CPUP95: f(20), MemAvg: f(30), MemP95: f(40), CPUReservedUnits: n(256), MemReservedMiB: n(512)}}
	for i := 0; i < 2; i++ {
		if err := s.UpsertServiceUtilDay(ctx, day, rows); err != nil {
			t.Fatal(err)
		}
	}
	rows[0].CPUP95 = f(55)
	rows[0].MemAvg = nil
	if err := s.UpsertServiceUtilDay(ctx, day, rows); err != nil {
		t.Fatal(err)
	}
	var count int
	var cpuP95 float64
	var memAvg *float64
	if err := s.Pool.QueryRow(ctx, `select count(*), max(cpu_p95), max(mem_avg) from service_util_daily`).Scan(&count, &cpuP95, &memAvg); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cpuP95 != 55 || memAvg != nil {
		t.Errorf("count %d cpu_p95 %v mem_avg %v, want 1 row, 55, null", count, cpuP95, memAvg)
	}
}
