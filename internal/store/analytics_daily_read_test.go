package store_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var readDay = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func (fx reliabilityFixture) availabilityDay(t *testing.T, key model.ServiceKey, day time.Time, samples, ok, minutesBelow int) {
	t.Helper()
	_, err := fx.s.Pool.Exec(context.Background(),
		`insert into service_availability_daily (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops)
		 values ($1, $2, $3, $4, $5::date, $6, $7, $8, 0)`,
		key.AccountID, key.Region, key.Cluster, key.Service, day, samples, ok, minutesBelow)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsAvailabilityDaily_SumsVisibleServicesPerDay(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	fx.availabilityDay(t, fx.payments, readDay, 288, 288, 0)
	fx.availabilityDay(t, fx.ledger, readDay, 288, 278, 50)
	fx.availabilityDay(t, fx.batch, readDay, 100, 0, 500) // untagged = Dev
	fx.availabilityDay(t, fx.payments, readDay.AddDate(0, 0, 1), 288, 144, 720)
	fx.availabilityDay(t, fx.payments, readDay.AddDate(0, 0, 5), 10, 10, 0) // outside [from, to)

	from, to := readDay, readDay.AddDate(0, 0, 2)
	admin, err := fx.s.AnalyticsAvailabilityDaily(ctx, store.TargetFilter{}, fx.adminVis, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 2 || !admin[0].Day.Equal(readDay) {
		t.Fatalf("admin days = %+v, want two days starting %v", admin, readDay)
	}
	if got := admin[0]; got.Samples != 676 || got.OK != 566 || got.MinutesBelow != 550 {
		t.Errorf("day 1 admin = %+v, want samples 676 ok 566 minutes 550", got)
	}

	// A viewer with only Prod must not see the Dev service's samples.
	prodOnly, err := fx.s.AnalyticsAvailabilityDaily(ctx, store.TargetFilter{}, fx.prodOnlyVi, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := prodOnly[0]; got.Samples != 576 || got.OK != 566 || got.MinutesBelow != 50 {
		t.Errorf("day 1 prod-only = %+v, want samples 576 ok 566 minutes 50", got)
	}

	other, err := fx.s.AnalyticsAvailabilityDaily(ctx, store.TargetFilter{Region: "us-east-1"}, fx.adminVis, from, to)
	if err != nil || len(other) != 0 {
		t.Errorf("region us-east-1 = %+v (%v), want no days", other, err)
	}
}

func TestAnalyticsAvailabilityTrend_ComparesThirtyDayPeriods(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	today := readDay
	current, previous := today.AddDate(0, 0, -5), today.AddDate(0, 0, -40)
	// payments worsens 100% -> 90%; ledger improves; batch (Dev) worsens but only has a current period.
	fx.availabilityDay(t, fx.payments, previous, 100, 100, 0)
	fx.availabilityDay(t, fx.payments, current, 100, 90, 50)
	fx.availabilityDay(t, fx.ledger, previous, 100, 80, 100)
	fx.availabilityDay(t, fx.ledger, current, 100, 100, 0)
	fx.availabilityDay(t, fx.batch, current, 100, 50, 250)
	fx.availabilityDay(t, fx.payments, today.AddDate(0, 0, -61), 100, 0, 0) // older than 60 days: ignored
	fx.availabilityDay(t, fx.payments, today, 100, 0, 0)                    // today is not a complete day: ignored

	trend, err := fx.s.AnalyticsAvailabilityTrend(ctx, store.TargetFilter{}, fx.adminVis, today)
	if err != nil {
		t.Fatal(err)
	}
	byService := map[string]store.AvailabilityTrendFact{}
	for _, row := range trend {
		byService[row.Key.Service] = row
	}
	if len(byService) != 2 {
		t.Fatalf("trend services = %v, want payments and ledger (batch has no previous period)", byService)
	}
	if p := byService["payments"]; !near(p.Previous, 1) || !near(p.Current, 0.9) {
		t.Errorf("payments previous/current = %v/%v, want fractions 1 and 0.9", p.Previous, p.Current)
	}
	if l := byService["ledger"]; !near(l.Previous, 0.8) || !near(l.Current, 1) || l.TagID != fx.prod || !l.TagInherited {
		t.Errorf("ledger = %+v, want 0.8 -> 1 with inherited Prod tag", l)
	}

	hidden, err := fx.s.AnalyticsAvailabilityTrend(ctx, store.TargetFilter{}, store.Visibility{TagIDs: []int64{fx.prod + 1000}}, today)
	if err != nil || len(hidden) != 0 {
		t.Errorf("viewer without the tag sees %+v (%v), want nothing", hidden, err)
	}
}

func (fx reliabilityFixture) capacityDay(t *testing.T, cluster string, day time.Time, samples int, vcpuSum, vcpuMax, memSum, memMax float64, tasksSum, tasksMax, spotSum int) {
	t.Helper()
	key := clusterKey(cluster)
	_, err := fx.s.Pool.Exec(context.Background(),
		`insert into cluster_capacity_daily (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
		 values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11, $12)`,
		key.AccountID, key.Region, key.Cluster, day, samples, vcpuSum, vcpuMax, memSum, memMax, tasksSum, tasksMax, spotSum)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsCapacityDaily_AveragesPerClusterThenSums(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	// core: 4 samples, average 2 vCPU, 4 GiB, 3 tasks, 1 spot. jobs (Dev): average 10 vCPU.
	fx.capacityDay(t, "core", readDay, 4, 8, 3, 16, 5, 12, 4, 4)
	fx.capacityDay(t, "jobs", readDay, 2, 20, 12, 8, 5, 6, 4, 2)

	points, err := fx.s.AnalyticsCapacityDaily(ctx, store.TargetFilter{}, fx.adminVis, readDay, readDay.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %+v, want one day", points)
	}
	if p := points[0]; !near(p.VCPU, 12) || !near(p.MemGiB, 8) || !near(p.Tasks, 6) || !near(p.Spot, 2) || !near(p.VCPUMax, 15) {
		t.Errorf("admin point = %+v, want vcpu 12 (max 15) mem 8 tasks 6 spot 2", p)
	}

	prodOnly, err := fx.s.AnalyticsCapacityDaily(ctx, store.TargetFilter{}, fx.prodOnlyVi, readDay, readDay.AddDate(0, 0, 1))
	if err != nil || len(prodOnly) != 1 || !near(prodOnly[0].VCPU, 2) {
		t.Errorf("prod-only = %+v (%v), want only the core cluster (2 vCPU)", prodOnly, err)
	}
}

func (fx reliabilityFixture) utilDay(t *testing.T, key model.ServiceKey, day time.Time, cpuAvg, cpuP95, memAvg, memP95 *float64, cpuUnits, memMiB *int) {
	t.Helper()
	err := fx.s.UpsertServiceUtilDay(context.Background(), day, []store.ServiceUtilRow{{
		Key: key, CPUAvg: cpuAvg, CPUP95: cpuP95, MemAvg: memAvg, MemP95: memP95, CPUReservedUnits: cpuUnits, MemReservedMiB: memMiB,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

func TestAnalyticsUtilDaily_ConvertsPercentOfReservedToAbsolute(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	// payments reserves 1 vCPU / 2 GiB; ledger 0.5 vCPU / 1 GiB with no memory datapoint; batch (Dev) 2 vCPU.
	fx.utilDay(t, fx.payments, readDay, ptrF(20), ptrF(50), ptrF(30), ptrF(60), ptrI(1024), ptrI(2048))
	fx.utilDay(t, fx.ledger, readDay, ptrF(40), ptrF(80), nil, nil, ptrI(512), ptrI(1024))
	fx.utilDay(t, fx.batch, readDay, ptrF(100), ptrF(100), ptrF(100), ptrF(100), ptrI(2048), ptrI(4096))

	points, err := fx.s.AnalyticsUtilDaily(ctx, store.TargetFilter{}, fx.prodOnlyVi, readDay, readDay.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %+v, want one day", points)
	}
	p := points[0]
	if p.CPUAvgVCPU == nil || !near(*p.CPUAvgVCPU, 0.2+0.2) || p.CPUP95VCPU == nil || !near(*p.CPUP95VCPU, 0.5+0.4) {
		t.Errorf("cpu avg/p95 = %v/%v, want 0.4/0.9 vCPU", p.CPUAvgVCPU, p.CPUP95VCPU)
	}
	if p.MemP95GiB == nil || !near(*p.MemP95GiB, 1.2) {
		t.Errorf("mem p95 = %v, want 1.2 GiB (services without a datapoint add nothing)", p.MemP95GiB)
	}
	all, err := fx.s.AnalyticsUtilDaily(ctx, store.TargetFilter{}, fx.adminVis, readDay, readDay.AddDate(0, 0, 1))
	if err != nil || len(all) != 1 || !near(*all[0].CPUP95VCPU, 0.9+2) {
		t.Errorf("admin = %+v (%v), want Dev service included (2.9 vCPU)", all, err)
	}
}

func TestAnalyticsOverReserved_RanksUnusedVCPUWithRunningCount(t *testing.T) {
	fx := newReliabilityFixture(t) // payments runs 2 tasks, ledger 1, batch 0
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		day := readDay.AddDate(0, 0, -i)
		// payments: p95 stays 25% of 1 vCPU / 50% of 2 GiB except one spike day; the latest reservation wins.
		p95 := 25.0
		if i == 9 {
			p95 = 100
		}
		fx.utilDay(t, fx.payments, day, ptrF(10), ptrF(p95), ptrF(20), ptrF(50), ptrI(1024), ptrI(2048))
		fx.utilDay(t, fx.ledger, day, ptrF(10), ptrF(90), ptrF(20), ptrF(90), ptrI(1024), ptrI(1024))
		fx.utilDay(t, fx.batch, day, ptrF(1), ptrF(1), ptrF(1), ptrF(1), ptrI(4096), ptrI(8192))
	}
	fx.utilDay(t, fx.payments, readDay.AddDate(0, 0, -100), ptrF(1), ptrF(1), ptrF(1), ptrF(1), ptrI(1024), ptrI(2048)) // before from

	rows, err := fx.s.AnalyticsOverReserved(ctx, store.TargetFilter{}, fx.prodOnlyVi, readDay.AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want payments and ledger only for a Prod viewer", rows)
	}
	var payments, ledger store.OverReservedFact
	for _, row := range rows {
		switch row.Key.Service {
		case "payments":
			payments = row
		case "ledger":
			ledger = row
		}
	}
	// 95th percentile of ten daily p95s (nine at 25, one at 100) interpolates to 25 + 0.55*75 = 66.25.
	wantP95 := (25 + 0.55*75) / 100
	if !near(payments.ReservedVCPU, 1) || !near(payments.UsedP95VCPU, wantP95) || !near(payments.ReservedMemGiB, 2) || !near(payments.UsedP95MemGiB, 1) || payments.Running != 2 {
		t.Errorf("payments = %+v, want reserved 1 vCPU/2 GiB, p95 %.4f vCPU/1 GiB, 2 running", payments, wantP95)
	}
	if !near(ledger.UsedP95VCPU, 0.9) || ledger.Running != 1 || ledger.TagID != fx.prod {
		t.Errorf("ledger = %+v", ledger)
	}
}

func (fx reliabilityFixture) failedDeployment(t *testing.T, key model.ServiceKey, id string, rev int, kind string, started time.Time, outcome, reason, detail string) {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.s.InsertDeploymentIfNew(ctx, store.DeploymentRecord{Key: key, DeploymentID: id, Revision: rev, Kind: kind, StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	if outcome == "" {
		return
	}
	var failure *store.FailureInfo
	if reason != "" {
		failure = &store.FailureInfo{Group: reason, Detail: detail}
	}
	if _, err := fx.s.FinishDeployment(ctx, key, id, outcome, started.Add(3*time.Minute), failure); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsRecentFailures_ReasonRevisionsAndScope(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	base := deliveryBase
	fx.failedDeployment(t, fx.payments, "ok-1", 4, "deploy", base.Add(-48*time.Hour), "completed", "", "")    // before the window: still the "from" revision
	fx.failedDeployment(t, fx.payments, "scale-1", 99, "scale", base.Add(-24*time.Hour), "completed", "", "") // scale rows never count as the previous revision
	fx.failedDeployment(t, fx.payments, "fail-1", 5, "deploy", base.Add(time.Hour), "failed", "circuit_breaker", "ECS deployment circuit breaker: tasks failed to start.")
	fx.failedDeployment(t, fx.payments, "fail-2", 6, "deploy", base.Add(3*time.Hour), "failed", "out_of_memory", "OutOfMemoryError")
	fx.failedDeployment(t, fx.payments, "fail-old", 7, "deploy", base.Add(5*time.Hour), "failed", "", "") // failed before reasons were recorded
	fx.failedDeployment(t, fx.batch, "dev-fail", 1, "deploy", base.Add(2*time.Hour), "failed", "other", "x")
	fx.failedDeployment(t, fx.payments, "done", 8, "deploy", base.Add(6*time.Hour), "completed", "", "")

	to := base.Add(24 * time.Hour)
	rows, total, err := fx.s.AnalyticsRecentFailures(ctx, store.TargetFilter{}, fx.adminVis, base, to, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("admin total/rows = %d/%d, want 3 failures with a reason", total, len(rows))
	}
	if rows[0].DeploymentID != "fail-2" || rows[1].DeploymentID != "dev-fail" || rows[2].DeploymentID != "fail-1" {
		t.Fatalf("order = %s, %s, %s; want newest first", rows[0].DeploymentID, rows[1].DeploymentID, rows[2].DeploymentID)
	}
	first := rows[2]
	if first.FromRevision == nil || *first.FromRevision != 4 || first.ToRevision != 5 || first.Reason != "circuit_breaker" || first.Detail == "" {
		t.Errorf("fail-1 = %+v, want from 4 to 5, circuit_breaker with detail", first)
	}
	if first.RanFor == nil || *first.RanFor != 3*time.Minute {
		t.Errorf("ran for = %v, want 3m", first.RanFor)
	}
	if second := rows[0]; second.FromRevision == nil || *second.FromRevision != 5 {
		t.Errorf("fail-2 from = %v, want 5 (the previous deploy, failed or not)", second.FromRevision)
	}
	if devRow := rows[1]; devRow.FromRevision != nil {
		t.Errorf("first rollout of a service has from %v, want nil", devRow.FromRevision)
	}

	limited, total, err := fx.s.AnalyticsRecentFailures(ctx, store.TargetFilter{}, fx.adminVis, base, to, 1)
	if err != nil || len(limited) != 1 || total != 3 {
		t.Errorf("limit 1 = %d rows, total %d (%v), want 1 row and total 3", len(limited), total, err)
	}
	prodRows, prodTotal, err := fx.s.AnalyticsRecentFailures(ctx, store.TargetFilter{}, fx.prodOnlyVi, base, to, 50)
	if err != nil || prodTotal != 2 || len(prodRows) != 2 {
		t.Errorf("prod-only = %d rows, total %d (%v), want the 2 Prod failures", len(prodRows), prodTotal, err)
	}
}

func TestAnalyticsFailureReasonSince_FirstRowWithAReason(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	since, err := fx.s.AnalyticsFailureReasonSince(ctx, store.TargetFilter{}, fx.adminVis)
	if err != nil || since != nil {
		t.Fatalf("empty history since = %v (%v), want nil", since, err)
	}
	base := deliveryBase
	fx.failedDeployment(t, fx.payments, "old", 1, "deploy", base.Add(-72*time.Hour), "failed", "", "")
	fx.failedDeployment(t, fx.payments, "first", 2, "deploy", base.Add(-24*time.Hour), "failed", "health_check", "unhealthy")
	fx.failedDeployment(t, fx.payments, "later", 3, "deploy", base, "failed", "other", "x")
	since, err = fx.s.AnalyticsFailureReasonSince(ctx, store.TargetFilter{}, fx.adminVis)
	if err != nil || since == nil || !since.Equal(base.Add(-24*time.Hour)) {
		t.Errorf("since = %v (%v), want %v", since, err, base.Add(-24*time.Hour))
	}
	hidden, err := fx.s.AnalyticsFailureReasonSince(ctx, store.TargetFilter{}, store.Visibility{TagIDs: []int64{fx.prod + 1000}})
	if err != nil || hidden != nil {
		t.Errorf("viewer without the tag sees since = %v (%v), want nil", hidden, err)
	}
}

func TestAnalyticsDeployments_CarriesFailureReason(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	fx.failedDeployment(t, fx.payments, "fail", 1, "deploy", deliveryBase.Add(time.Hour), "failed", "health_check", "unhealthy")
	fx.failedDeployment(t, fx.payments, "ok", 2, "deploy", deliveryBase.Add(2*time.Hour), "completed", "", "")
	facts, err := fx.s.AnalyticsDeployments(ctx, store.TargetFilter{}, fx.adminVis, deliveryBase, deliveryBase.Add(24*time.Hour))
	if err != nil || len(facts) != 2 {
		t.Fatalf("facts = %+v (%v)", facts, err)
	}
	if facts[0].FailureReason != "health_check" || facts[1].FailureReason != "" {
		t.Errorf("reasons = %q, %q; want health_check and empty", facts[0].FailureReason, facts[1].FailureReason)
	}
}
