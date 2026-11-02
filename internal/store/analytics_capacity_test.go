package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func (fx reliabilityFixture) runningTask(id, cluster, service, cpu, memory, provider, launch string) model.Task {
	started := fx.to.Add(-time.Hour)
	return model.Task{Cluster: clusterKey(cluster), ARN: readTaskARN(cluster, id), ServiceName: service, LastStatus: "RUNNING",
		CPU: cpu, Memory: memory, CapacityProvider: provider, LaunchType: launch, StartedAt: &started, CreatedAt: started, PolledAt: fx.to}
}

func TestAnalyticsRunningTasks_SumsAndSpot(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	stopped := fx.to.Add(-time.Minute)
	stoppedTask := fx.runningTask("gone", "core", "payments", "4096", "8192", "FARGATE", "FARGATE")
	stoppedTask.LastStatus, stoppedTask.StoppedAt = "STOPPED", &stopped
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{
		fx.runningTask("p1", "core", "payments", "512", "1024", "FARGATE", "FARGATE"),
		fx.runningTask("p2", "core", "payments", "512", "1024", "FARGATE", "FARGATE"),
		fx.runningTask("l1", "core", "ledger", "256", "512", "FARGATE_SPOT", "FARGATE"),
		fx.runningTask("b1", "jobs", "batch", "", "", "", "EC2"), // Dev, EC2 without task-level size
		stoppedTask,
	}); err != nil {
		t.Fatal(err)
	}

	prod, err := fx.s.AnalyticsRunningTasks(ctx, store.TargetFilter{}, fx.prodOnlyVi)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod) != 3 {
		t.Fatalf("Prod viewer sees %d running tasks, want 3 (Dev and stopped excluded): %+v", len(prod), prod)
	}
	var spot int
	for _, task := range prod {
		if task.CapacityProvider == "FARGATE_SPOT" {
			spot++
			if task.CPU != "256" || task.Memory != "512" || task.Cluster != clusterKey("core") || int64(task.EffectiveTagID) != fx.prod {
				t.Errorf("spot task = %+v", task)
			}
		}
	}
	if spot != 1 {
		t.Errorf("spot tasks = %d, want 1", spot)
	}

	admin, err := fx.s.AnalyticsRunningTasks(ctx, store.TargetFilter{}, fx.adminVis)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 4 {
		t.Fatalf("admin sees %d running tasks, want 4", len(admin))
	}
	var ec2 *store.CapacityTaskFact
	for i := range admin {
		if admin[i].LaunchType == "EC2" {
			ec2 = &admin[i]
		}
	}
	if ec2 == nil || ec2.CapacityProvider != "" || ec2.ServiceName != "batch" {
		t.Errorf("EC2 task = %+v, want empty provider and service batch", ec2)
	}
}

func TestAnalyticsAtScalingMax_DurationFromHistory(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 3, Running: 3, PolledAt: fx.to},
		{Key: fx.ledger, Desired: 1, Running: 1, PolledAt: fx.to},
		{Key: fx.batch, Desired: 4, Running: 4, PolledAt: fx.to},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.s.UpsertScaling(ctx, []model.Scaling{
		{Key: fx.payments, HasTarget: true, Min: 1, Max: 3, PolledAt: fx.to}, // at max
		{Key: fx.ledger, HasTarget: true, Min: 1, Max: 5, PolledAt: fx.to},   // headroom
		{Key: fx.batch, HasTarget: true, Min: 1, Max: 4, PolledAt: fx.to},    // at max, no history
	}); err != nil {
		t.Fatal(err)
	}
	// payments sat at 2 desired until 3h ago, then 3 (its max) every 5 minutes.
	cut := fx.to.Add(-3 * time.Hour)
	for ts := fx.to.Add(-6 * time.Hour); ts.Before(fx.to); ts = ts.Add(5 * time.Minute) {
		desired := 3
		if ts.Before(cut) {
			desired = 2
		}
		fx.sample(t, ts, fx.payments, desired, desired)
	}

	rows, err := fx.s.AnalyticsAtScalingMax(ctx, store.TargetFilter{}, fx.adminVis)
	if err != nil {
		t.Fatal(err)
	}
	byService := map[string]store.AtMaxRow{}
	for _, row := range rows {
		byService[row.Key.Service] = row
	}
	if len(rows) != 2 {
		t.Fatalf("got %d at-max services, want payments and batch: %+v", len(rows), rows)
	}
	payments := byService["payments"]
	if payments.Desired != 3 || payments.MaxCapacity != 3 || payments.AtMaxSince == nil || !payments.AtMaxSince.Equal(cut) {
		t.Errorf("payments = %+v (since %v), want at max since %v", payments, payments.AtMaxSince, cut)
	}
	if batch := byService["batch"]; batch.AtMaxSince != nil {
		t.Errorf("batch at_max_since = %v, want nil without history", batch.AtMaxSince)
	}

	prod, err := fx.s.AnalyticsAtScalingMax(ctx, store.TargetFilter{}, fx.prodOnlyVi)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod) != 1 || prod[0].Key != fx.payments {
		t.Errorf("Prod viewer sees %+v, want only payments", prod)
	}
}

func TestAnalyticsSubnetsLow_UsesCidrSize(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	subnet := func(id, cidr string, free int) model.Subnet {
		return model.Subnet{AccountID: readAccount, Region: "eu-west-1", SubnetID: id, VPCID: "vpc-1", Name: id + "-name",
			CIDR: cidr, AZ: "eu-west-1a", AvailableIPs: free, PolledAt: fx.to}
	}
	subnets := []model.Subnet{
		subnet("subnet-small-low", "10.0.1.0/24", 20),   // 20 of 251 usable: 8% free
		subnet("subnet-small-ok", "10.0.2.0/24", 60),    // 24% free
		subnet("subnet-large-low", "10.0.16.0/20", 300), // 7% of 4091 free: same free count would pass in a /24
		subnet("subnet-dev", "10.0.3.0/24", 5),          // low, but only Dev services use it
	}
	if err := fx.s.ReplaceNetwork(ctx, readAccount, "eu-west-1", []model.VPC{}, subnets, []model.VPCEdge{}, []model.SecurityGroup{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 2, Running: 2, Subnets: []string{"subnet-small-low", "subnet-small-ok"}, PolledAt: fx.to},
		{Key: fx.ledger, Desired: 1, Running: 1, Subnets: []string{"subnet-large-low"}, PolledAt: fx.to},
		{Key: fx.batch, Desired: 1, Running: 1, Subnets: []string{"subnet-dev"}, PolledAt: fx.to},
	}); err != nil {
		t.Fatal(err)
	}

	admin, err := fx.s.AnalyticsSubnetsLow(ctx, store.TargetFilter{}, fx.adminVis)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, row := range admin {
		ids = append(ids, row.SubnetID)
	}
	if !equalStrings(ids, []string{"subnet-dev", "subnet-large-low", "subnet-small-low"}) {
		t.Fatalf("admin low subnets (most used first) = %v", ids)
	}
	for _, row := range admin {
		if row.SubnetID == "subnet-small-low" && (row.TotalIPs != 251 || row.AvailableIPs != 20 || row.UsedPct < 92 || row.UsedPct > 92.1 || row.Name != "subnet-small-low-name") {
			t.Errorf("small subnet = %+v, want total 251, 20 free, ~92%% used", row)
		}
		if row.SubnetID == "subnet-large-low" && row.TotalIPs != 4091 {
			t.Errorf("large subnet total = %d, want 4091", row.TotalIPs)
		}
	}

	prod, err := fx.s.AnalyticsSubnetsLow(ctx, store.TargetFilter{}, fx.prodOnlyVi)
	if err != nil {
		t.Fatal(err)
	}
	ids = ids[:0]
	for _, row := range prod {
		ids = append(ids, row.SubnetID)
	}
	if !equalStrings(ids, []string{"subnet-large-low", "subnet-small-low"}) {
		t.Fatalf("Prod viewer low subnets = %v, want the two used by Prod services", ids)
	}
}

func TestAnalyticsReservedTrend_StackedByTagUsesCurrentTaskDefinition(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	for _, td := range []model.TaskDefinitionRef{
		{AccountID: readAccount, Region: "eu-west-1", ARN: "arn:td/payments:9", CPU: "1024", Memory: "2048"},
		{AccountID: readAccount, Region: "eu-west-1", ARN: "arn:td/batch:2", CPU: "256", Memory: "512"},
	} {
		if err := fx.s.UpsertTaskDefinition(ctx, td); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 2, Running: 2, TaskDefinitionARN: "arn:td/payments:9", PolledAt: fx.to},
		{Key: fx.ledger, Desired: 1, Running: 1, PolledAt: fx.to}, // no task definition: reserves nothing
		{Key: fx.batch, Desired: 4, Running: 4, TaskDefinitionARN: "arn:td/batch:2", PolledAt: fx.to},
	}); err != nil {
		t.Fatal(err)
	}
	first := fx.from.Add(10 * time.Minute)
	second := fx.from.Add(20 * time.Minute)
	later := fx.from.Add(4*time.Hour + 10*time.Minute)
	fx.sample(t, first, fx.payments, 1, 2)
	fx.sample(t, second, fx.payments, 3, 3)
	fx.sample(t, first, fx.batch, 4, 4)
	fx.sample(t, second, fx.batch, 4, 4)
	fx.sample(t, later, fx.payments, 2, 2)
	fx.sample(t, first, fx.ledger, 1, 1)

	points, err := fx.s.AnalyticsReservedTrend(ctx, store.TargetFilter{}, fx.adminVis, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]map[time.Time]float64{}
	for _, p := range points {
		if got[int64(p.TagID)] == nil {
			got[int64(p.TagID)] = map[time.Time]float64{}
		}
		got[int64(p.TagID)][p.Bucket.UTC()] = p.VCPU
	}
	dev, err := fx.s.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bucket0, bucket1 := fx.from.UTC(), fx.from.Add(4*time.Hour).UTC()
	// Prod bucket 0: payments averages (1+3)/2 = 2 tasks x 1 vCPU; ledger has no task definition.
	if v := got[fx.prod][bucket0]; v != 2 {
		t.Errorf("Prod bucket 0 = %v vCPU, want 2", v)
	}
	if v := got[fx.prod][bucket1]; v != 2 {
		t.Errorf("Prod bucket 1 = %v vCPU, want 2", v)
	}
	// Dev: 4 tasks x 256/1024.
	if v := got[int64(dev.ID)][bucket0]; v != 1 {
		t.Errorf("Dev bucket 0 = %v vCPU, want 1", v)
	}
	if _, ok := got[int64(dev.ID)][bucket1]; ok {
		t.Errorf("Dev has a point in bucket 1 without samples")
	}

	prodOnly, err := fx.s.AnalyticsReservedTrend(ctx, store.TargetFilter{}, fx.prodOnlyVi, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prodOnly {
		if int64(p.TagID) != fx.prod {
			t.Errorf("Prod viewer got a point for tag %d", p.TagID)
		}
	}
}

func TestAnalyticsVisibleServiceCount(t *testing.T) {
	fx := newReliabilityFixture(t)
	for vis, want := range map[string]int{"admin": 3, "prod": 2} {
		v := fx.adminVis
		if vis == "prod" {
			v = fx.prodOnlyVi
		}
		got, err := fx.s.AnalyticsServiceCount(context.Background(), store.TargetFilter{}, v)
		if err != nil || got != want {
			t.Errorf("%s: count %d, %v; want %d", vis, got, err, want)
		}
	}
}

// Each target's poll loop stamps its own timestamps, so a bucket holds samples from several targets at different
// instants. The trend must be the sum of per-target averages, not a total divided by every distinct timestamp.
func TestAnalyticsReservedTrend_SumsPerTargetAveragesWhenTimestampsDiffer(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	otherCluster := model.ClusterKey{AccountID: "222222222222", Region: "eu-central-1", Cluster: "core"}
	other := model.ServiceKey{ClusterKey: otherCluster, Service: "payments"}
	if _, err := fx.s.UpsertClusters(ctx, []model.Cluster{{Key: otherCluster, Status: "ACTIVE", PolledAt: fx.to}}); err != nil {
		t.Fatal(err)
	}
	if err := fx.s.SetClusterTag(ctx, otherCluster, authz.TagID(fx.prod)); err != nil {
		t.Fatal(err)
	}
	for _, td := range []model.TaskDefinitionRef{
		{AccountID: readAccount, Region: "eu-west-1", ARN: "arn:td/payments:9", CPU: "1024", Memory: "2048"},
		{AccountID: "222222222222", Region: "eu-central-1", ARN: "arn:td/other:1", CPU: "1024", Memory: "2048"},
	} {
		if err := fx.s.UpsertTaskDefinition(ctx, td); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 2, Running: 2, TaskDefinitionARN: "arn:td/payments:9", PolledAt: fx.to},
		{Key: other, Desired: 2, Running: 2, TaskDefinitionARN: "arn:td/other:1", PolledAt: fx.to},
	}); err != nil {
		t.Fatal(err)
	}
	// Target A (eu-west-1) and target B (eu-central-1) poll at different instants: 2 samples each, 4 timestamps.
	fx.sample(t, fx.from.Add(10*time.Minute), fx.payments, 2, 2)
	fx.sample(t, fx.from.Add(20*time.Minute), fx.payments, 2, 2)
	fx.sample(t, fx.from.Add(11*time.Minute), other, 3, 3)
	fx.sample(t, fx.from.Add(21*time.Minute), other, 3, 3)

	points, err := fx.s.AnalyticsReservedTrend(ctx, store.TargetFilter{}, fx.adminVis, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var prod float64
	for _, p := range points {
		if int64(p.TagID) == fx.prod && p.Bucket.Equal(fx.from) {
			prod = p.VCPU
		}
	}
	if prod != 5 { // 2 tasks x 1 vCPU + 3 tasks x 1 vCPU
		t.Errorf("Prod bucket 0 = %v vCPU, want 5 (2 + 3), not divided by the number of targets", prod)
	}
}
