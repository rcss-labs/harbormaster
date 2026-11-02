package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// reliabilityFixture has three services: payments (Prod), ledger (Prod) and batch (untagged = Dev).
type reliabilityFixture struct {
	s          *store.Store
	prod       int64
	from, to   time.Time
	payments   model.ServiceKey
	ledger     model.ServiceKey
	batch      model.ServiceKey
	adminVis   store.Visibility
	prodOnlyVi store.Visibility
}

func newReliabilityFixture(t *testing.T) reliabilityFixture {
	t.Helper()
	s := storetest.Open(t)
	ctx := context.Background()
	prod, err := s.CreateTag(ctx, "Prod", "red")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	if _, err := s.UpsertClusters(ctx, []model.Cluster{
		{Key: clusterKey("core"), Status: "ACTIVE", PolledAt: now},
		{Key: clusterKey("jobs"), Status: "ACTIVE", PolledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClusterTag(ctx, clusterKey("core"), prod.ID); err != nil {
		t.Fatal(err)
	}
	fx := reliabilityFixture{
		s: s, prod: int64(prod.ID), to: now, from: now.Add(-8 * time.Hour),
		payments: serviceKey("core", "payments"), ledger: serviceKey("core", "ledger"), batch: serviceKey("jobs", "batch"),
		adminVis:   store.Visibility{Admin: true},
		prodOnlyVi: store.Visibility{TagIDs: []int64{int64(prod.ID)}},
	}
	if _, err := s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 2, Running: 2, PolledAt: now},
		{Key: fx.ledger, Desired: 1, Running: 1, PolledAt: now},
		{Key: fx.batch, Desired: 1, Running: 0, PolledAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	return fx
}

func (fx reliabilityFixture) sample(t *testing.T, ts time.Time, key model.ServiceKey, running, desired int) {
	t.Helper()
	svc := model.Service{Key: key, Running: running, Desired: desired}
	if err := fx.s.InsertCountsHistory(context.Background(), ts, []model.Service{svc}); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsAvailabilitySeries_BucketsAndSemiJoin(t *testing.T) {
	fx := newReliabilityFixture(t)
	first := fx.from // bucket 0: [from, from+4h)
	second := fx.from.Add(4 * time.Hour)
	// Bucket 0: payments ok x2, ledger below once; batch (Dev) is below but hidden from a Prod-only viewer.
	fx.sample(t, first.Add(5*time.Minute), fx.payments, 2, 2)
	fx.sample(t, first.Add(10*time.Minute), fx.payments, 2, 2)
	fx.sample(t, first.Add(5*time.Minute), fx.ledger, 0, 1)
	fx.sample(t, first.Add(5*time.Minute), fx.batch, 0, 1)
	// Bucket 1: ledger recovers.
	fx.sample(t, second.Add(5*time.Minute), fx.ledger, 1, 1)
	// Outside the window: must not count.
	fx.sample(t, fx.from.Add(-time.Minute), fx.ledger, 0, 1)

	got, err := fx.s.AnalyticsAvailabilitySeries(context.Background(), store.TargetFilter{}, fx.prodOnlyVi, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if b := got[0]; !b.Bucket.Equal(first) || b.Samples != 3 || b.OK != 2 || b.BelowServices != 1 {
		t.Errorf("bucket 0 = %+v, want samples 3 ok 2 below_services 1 (batch hidden)", b)
	}
	if b := got[1]; !b.Bucket.Equal(second) || b.Samples != 1 || b.OK != 1 || b.BelowServices != 0 {
		t.Errorf("bucket 1 = %+v, want samples 1 ok 1 below_services 0", b)
	}

	admin, err := fx.s.AnalyticsAvailabilitySeries(context.Background(), store.TargetFilter{}, fx.adminVis, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if admin[0].Samples != 4 || admin[0].BelowServices != 2 {
		t.Errorf("admin bucket 0 = %+v, want samples 4 below_services 2", admin[0])
	}
}

func TestAnalyticsAvailabilityByService_MinutesBelowUsesInterval(t *testing.T) {
	fx := newReliabilityFixture(t)
	for i := range 4 {
		running := 1
		if i < 3 {
			running = 0 // three of four samples under desired
		}
		fx.sample(t, fx.from.Add(time.Duration(i+1)*10*time.Minute), fx.ledger, running, 1)
	}
	fx.sample(t, fx.from.Add(time.Hour), fx.batch, 0, 1)

	rows, err := fx.s.AnalyticsAvailabilityByService(context.Background(), store.TargetFilter{}, fx.prodOnlyVi, fx.from, fx.to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != fx.ledger {
		t.Fatalf("rows = %+v, want only ledger (batch is Dev)", rows)
	}
	r := rows[0]
	if r.Samples != 4 || r.OK != 1 || r.Below != 3 || int64(r.EffectiveTagID) != fx.prod {
		t.Fatalf("row = %+v, want samples 4 ok 1 below 3 tag prod", r)
	}
	if got := r.MinutesBelow(10 * time.Minute); got != 30 {
		t.Errorf("MinutesBelow(10m) = %v, want 30", got)
	}
	if got := r.MinutesBelow(5 * time.Minute); got != 15 {
		t.Errorf("MinutesBelow(5m) = %v, want 15", got)
	}
}

func TestAnalyticsStops_GroupsZonesExitCodes(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	code := func(n int) *int { return &n }
	stopped := func(id, service, kind, zone string, at time.Time, codes ...int) model.Task {
		task := model.Task{Cluster: clusterKey("core"), ARN: readTaskARN("core", id), ServiceName: service, LastStatus: "STOPPED",
			AvailabilityZone: zone, StoppedAt: &at, PolledAt: fx.to, CreatedAt: at.Add(-time.Hour)}
		switch kind {
		case "oom":
			task.StoppedReason = "OutOfMemoryError: container killed"
		case "deploy":
			task.StopCode = "ServiceSchedulerInitiated"
			task.StoppedReason = "Scaling activity initiated by deployment ecs-svc/123"
		case "scale":
			task.StoppedReason = "Scaling activity initiated by (desired count) changes"
		case "exit":
			task.StopCode = "EssentialContainerExited"
		}
		for _, c := range codes {
			task.Containers = append(task.Containers, model.Container{Name: "app", ExitCode: code(c)})
		}
		return task
	}
	at := fx.from.Add(30 * time.Minute)
	tasks := []model.Task{
		stopped("t1", "payments", "oom", "eu-west-1a", at, 137),
		stopped("t2", "payments", "exit", "eu-west-1a", at, 1, 0),
		stopped("t3", "ledger", "exit", "eu-west-1b", at.Add(5*time.Hour), 1),
		stopped("t4", "payments", "deploy", "eu-west-1a", at),
		stopped("t5", "ledger", "scale", "eu-west-1b", at),
		stopped("t6", "", "exit", "eu-west-1c", at, 255), // standalone in the Prod-tagged core cluster
		{Cluster: clusterKey("jobs"), ARN: readTaskARN("jobs", "t7"), ServiceName: "batch", LastStatus: "STOPPED",
			StoppedAt: &at, StopCode: "EssentialContainerExited", AvailabilityZone: "eu-west-1a", PolledAt: fx.to,
			CreatedAt: at.Add(-time.Hour), Containers: []model.Container{{Name: "app", ExitCode: code(1)}}}, // Dev
	}
	if _, err := fx.s.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}

	res, err := fx.s.AnalyticsStops(ctx, store.TargetFilter{}, fx.prodOnlyVi, fx.from, fx.to, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]int{}
	for _, c := range res.ByReason {
		reasons[c.Key] += c.Count
	}
	want := map[string]int{
		"Out of memory (exit 137)": 1, "Container exited with an error": 3,
		"Replaced by a deployment": 1, "Scaled in": 1,
	}
	if len(reasons) != len(want) {
		t.Fatalf("reasons = %v, want %v", reasons, want)
	}
	for k, v := range want {
		if reasons[k] != v {
			t.Errorf("reasons[%q] = %d, want %d (all: %v)", k, reasons[k], v, reasons)
		}
	}
	// t3 is in the second 4h bucket.
	var late int
	for _, c := range res.ByReason {
		if c.Bucket.Equal(fx.from.Add(4 * time.Hour)) {
			late += c.Count
		}
	}
	if late != 1 {
		t.Errorf("second bucket holds %d stops, want 1", late)
	}

	zones := map[string]int{}
	for _, c := range res.UnexpectedByZone {
		zones[c.Key] += c.Count
	}
	if zones["eu-west-1a"] != 2 || zones["eu-west-1b"] != 1 || zones["eu-west-1c"] != 1 || len(zones) != 3 {
		t.Errorf("unexpected by zone = %v, want a:2 b:1 c:1 (deploy and scale stops are expected)", zones)
	}

	codes := map[string]int{}
	for _, c := range res.ExitCodes {
		codes[c.Key] += c.Count
	}
	if codes["137"] != 1 || codes["1"] != 2 || codes["255"] != 1 || len(codes) != 3 {
		t.Errorf("exit codes = %v, want 137:1 1:2 255:1 and no zero code", codes)
	}

	perService := map[string]store.ServiceStops{}
	for _, s := range res.ByService {
		perService[s.Key.Service] = s
	}
	if len(perService) != 2 || perService["payments"].Unexpected != 2 || perService["ledger"].Unexpected != 1 {
		t.Errorf("by service = %+v, want payments 2 and ledger 1 unexpected, no standalone row", perService)
	}
	if perService["payments"].TopReason == "" {
		t.Errorf("payments has no top reason")
	}
}

func TestAnalyticsPlacementFailures_MatchesUnableToPlace(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	at := fx.to.Add(-time.Hour)
	events := []model.ServiceEvent{
		{Key: fx.payments, ID: "e1", CreatedAt: at, Message: "(service payments) was unable to place a task because no container instance met requirements."},
		{Key: fx.ledger, ID: "e2", CreatedAt: at, Message: "(service ledger) Was UNABLE TO PLACE a task."},
		{Key: fx.ledger, ID: "e3", CreatedAt: at, Message: "(service ledger) has reached a steady state."},
		{Key: fx.batch, ID: "e4", CreatedAt: at, Message: "(service batch) was unable to place a task."},
		{Key: fx.payments, ID: "e5", CreatedAt: fx.from.Add(-24 * time.Hour), Message: "(service payments) was unable to place a task."},
	}
	if _, err := fx.s.InsertServiceEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	got, err := fx.s.AnalyticsPlacementFailures(ctx, store.TargetFilter{}, fx.prodOnlyVi, fx.from, fx.to)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("Prod-only placement failures = %d, want 2 (steady, Dev and out-of-window events excluded)", got)
	}
	all, err := fx.s.AnalyticsPlacementFailures(ctx, store.TargetFilter{}, fx.adminVis, fx.from.Add(-48*time.Hour), fx.to)
	if err != nil {
		t.Fatal(err)
	}
	if all != 4 {
		t.Errorf("admin placement failures over a wider window = %d, want 4", all)
	}
}
