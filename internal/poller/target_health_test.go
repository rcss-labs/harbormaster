package poller

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const euWestAccountRegion = "eu-west-1"

// observeHarness fills a store from the mockup estate for one target, then returns the
// fake and store so the test can change the estate and poll again.
type observeHarness struct {
	store  *store.Store
	fake   *fake.Fake
	estate *fake.Estate
	target awsx.Target
}

func newObserveHarness(t *testing.T, target awsx.Target) *observeHarness {
	t.Helper()
	estate := fake.MockupEstate(func() time.Time { return loopNow })
	h := &observeHarness{store: storetest.Open(t), fake: fake.New(estate), estate: estate, target: target}
	h.poll(t)
	return h
}

// poll runs every tier once so the store reflects the estate's current services.
func (h *observeHarness) poll(t *testing.T) {
	t.Helper()
	cfg := Config{MaxInFlight: 4}
	if err := PollOnce(context.Background(), h.store, h.fake, cfg, h.target); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
}

func (h *observeHarness) targetHealthLoop() *TargetHealthLoop {
	return &TargetHealthLoop{Store: h.store, Clients: h.fake, Every: 15 * time.Second, Now: func() time.Time { return loopNow }}
}

func serviceKey(target awsx.Target, cluster, service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: cluster}, Service: service}
}

func TestTargetHealthLoop_WritesHealthyUnhealthy(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: euWestAccountRegion}
	h := newObserveHarness(t, target)
	key := serviceKey(target, "eu-apps", "catalog-api")

	if err := h.targetHealthLoop().Run(context.Background(), target); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rows, err := h.store.TargetHealthForService(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("target health rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Healthy != 1 || row.Unhealthy != 1 {
		t.Fatalf("healthy/unhealthy = %d/%d, want 1/1", row.Healthy, row.Unhealthy)
	}
	if row.LoadBalancerARN == "" {
		t.Fatal("LoadBalancerARN is empty")
	}
	var sawUnhealthyReason bool
	for _, target := range row.Targets {
		if target.State == "unhealthy" && target.Reason == "Target.ResponseCodeMismatch" {
			sawUnhealthyReason = true
		}
	}
	if !sawUnhealthyReason {
		t.Fatalf("no unhealthy target with reason Target.ResponseCodeMismatch in %+v", row.Targets)
	}
}

func TestTargetHealthLoop_TwoGroupsOneService(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	key := serviceKey(target, "core-services", "payments-api")
	extraARN := "arn:aws:elasticloadbalancing:us-east-1:" + homelabAccount + ":targetgroup/payments-api-extra/0123456789abcdef"

	estate := fake.MockupEstate(func() time.Time { return loopNow })
	estate.AddTargetGroup(key, fake.TargetGroup{ARN: extraARN, Healthy: 1})
	st := storetest.Open(t)
	f := fake.New(estate)
	h := &observeHarness{store: st, fake: f, estate: estate, target: target}
	h.poll(t)
	loop := h.targetHealthLoop()
	ctx := context.Background()

	if err := loop.Run(ctx, target); err != nil {
		t.Fatalf("Run with two groups: %v", err)
	}
	rows, err := st.TargetHealthForService(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows with two groups = %d, want 2", len(rows))
	}

	estate.RemoveTargetGroup(extraARN)
	h.poll(t)
	if err := loop.Run(ctx, target); err != nil {
		t.Fatalf("Run after removing a group: %v", err)
	}
	rows, err = st.TargetHealthForService(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows after removing a group = %d, want 1", len(rows))
	}
}

func TestTargetHealthLoop_ThrottlePropagates(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	h := newObserveHarness(t, target)
	h.fake.ThrottleELB = 1

	err := h.targetHealthLoop().Run(context.Background(), target)
	if !awsx.IsThrottle(err) {
		t.Fatalf("Run error = %v, want a throttle error", err)
	}
}
