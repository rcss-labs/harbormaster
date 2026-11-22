package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// focusConfig is a poller configuration with the design's focus settings and a 30 s services tier.
func focusConfig() Config {
	return Config{
		MaxInFlight: 4,
		Intervals:   Intervals{Services: 30 * time.Second},
		Focus:       config.FocusConfig{Interval: 5 * time.Second, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 5},
	}
}

// focusHarness fills the store with one full cycle of the mockup estate and returns a loop with focus settings.
func focusHarness(t *testing.T) *loopHarness {
	t.Helper()
	estate := fake.MockupEstate(func() time.Time { return loopNow })
	st := storetest.Open(t)
	f := fake.New(estate)
	p := New(st, f, focusConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := testWriter(t, st, loopTarget)
	h := &loopHarness{loop: newLoop(p, loopTarget, w), fake: f, estate: estate, store: st, writer: w}
	h.runOnce(t)
	return h
}

func paymentsAPI() model.ServiceKey {
	return model.ServiceKey{ClusterKey: coreServices(), Service: "payments-api"}
}

// spyClients records the inputs of the ECS and ELB reads a cycle makes.
type spyClients struct {
	awsx.Clients
	ecs *recordingECS
	elb *recordingELB
}

func (c spyClients) ECS(ctx context.Context, t awsx.Target) (awsx.ECSAPI, error) {
	inner, err := c.Clients.ECS(ctx, t)
	if err != nil {
		return nil, err
	}
	c.ecs.ECSAPI = inner
	return c.ecs, nil
}

func (c spyClients) ELB(ctx context.Context, t awsx.Target) (awsx.ELBAPI, error) {
	inner, err := c.Clients.ELB(ctx, t)
	if err != nil {
		return nil, err
	}
	c.elb.ELBAPI = inner
	return c.elb, nil
}

func (h *loopHarness) spy() spyClients {
	s := spyClients{Clients: h.fake, ecs: &recordingECS{}, elb: &recordingELB{}}
	h.loop.p.clients = s
	return s
}

// callDelta runs fn and returns how many calls of each operation the fake saw meanwhile.
func (h *loopHarness) callDelta(fn func()) map[string]int {
	before := h.fake.Calls()
	fn()
	delta := map[string]int{}
	for op, n := range h.fake.Calls() {
		if n != before[op] {
			delta[op] = n - before[op]
		}
	}
	return delta
}

func TestFocusCluster_MakesExactlyTheTableCalls(t *testing.T) {
	h := focusHarness(t)
	for cycle := 0; cycle <= 6; cycle++ {
		delta := h.callDelta(func() {
			if _, err := h.loop.focusCluster(context.Background(), coreServices(), cycle); err != nil {
				t.Fatalf("cycle %d: %v", cycle, err)
			}
		})
		if delta["DescribeClusters"] != 1 {
			t.Errorf("cycle %d: DescribeClusters = %d, want 1 (%v)", cycle, delta["DescribeClusters"], delta)
		}
		if delta["DescribeServices"] < 1 {
			t.Errorf("cycle %d: no DescribeServices (%v)", cycle, delta)
		}
		if wantList := cycle%6 == 0; (delta["ListServices"] == 1) != wantList || delta["ListServices"] > 1 {
			t.Errorf("cycle %d: ListServices = %d, want %v", cycle, delta["ListServices"], wantList)
		}
		if delta["ListTasks"] != 1 {
			t.Errorf("cycle %d: ListTasks = %d, want 1 (RUNNING only) (%v)", cycle, delta["ListTasks"], delta)
		}
		for _, op := range []string{"ListClusters", "DescribeTargetHealth", "DescribeTargetGroups", "DescribeTaskDefinition"} {
			if delta[op] != 0 {
				t.Errorf("cycle %d: %s = %d, want none (%v)", cycle, op, delta[op], delta)
			}
		}
	}
}

func TestFocusService_MakesExactlyTheTableCalls(t *testing.T) {
	h := focusHarness(t)
	services, err := h.store.ListServices(context.Background(), store.TargetFilter{AccountID: loopTarget.AccountID, Region: loopTarget.Region}, "core-services", store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	var groups int
	for _, s := range services {
		if s.Key == paymentsAPI() {
			groups = len(s.LoadBalancers)
		}
	}
	if groups == 0 {
		t.Fatal("test estate: payments-api has no target group")
	}
	spy := h.spy()

	for cycle := 0; cycle <= 3; cycle++ {
		spy.ecs.listTasks, spy.ecs.describeServices, spy.elb.healthGroups = nil, nil, nil
		delta := h.callDelta(func() {
			if _, err := h.loop.focusService(context.Background(), paymentsAPI(), cycle); err != nil {
				t.Fatalf("cycle %d: %v", cycle, err)
			}
		})
		wantListTasks := 1
		if cycle%3 == 0 {
			wantListTasks = 2 // RUNNING and STOPPED
		}
		if delta["DescribeServices"] != 1 || delta["ListTasks"] != wantListTasks || delta["DescribeTargetHealth"] != groups {
			t.Errorf("cycle %d: calls = %v, want DescribeServices 1, ListTasks %d, DescribeTargetHealth %d", cycle, delta, wantListTasks, groups)
		}
		for _, op := range []string{"ListClusters", "DescribeClusters", "ListServices", "DescribeTargetGroups"} {
			if delta[op] != 0 {
				t.Errorf("cycle %d: %s = %d, want none (%v)", cycle, op, delta[op], delta)
			}
		}
		for _, c := range spy.ecs.describeServices {
			if c.cluster != "core-services" || len(c.services) != 1 || c.services[0] != "payments-api" {
				t.Errorf("cycle %d: DescribeServices %+v names more than the service", cycle, c)
			}
		}
		for _, c := range spy.ecs.listTasks {
			if c.cluster != "core-services" || c.service != "payments-api" {
				t.Errorf("cycle %d: ListTasks %+v names more than the service", cycle, c)
			}
		}
	}
}

func TestFocus_UnchangedObjectWritesNothing(t *testing.T) {
	h := focusHarness(t)
	ctx := context.Background()
	if _, err := h.loop.focusService(ctx, paymentsAPI(), 0); err != nil {
		t.Fatal(err)
	}
	before := h.loop.writeTotals()
	res, err := h.loop.focusService(ctx, paymentsAPI(), 1)
	if err != nil {
		t.Fatal(err)
	}
	after := h.loop.writeTotals()
	if after.Inserted != before.Inserted || after.Updated != before.Updated {
		t.Fatalf("an unchanged object wrote rows: %+v -> %+v", before, after)
	}
	if res.changed {
		t.Fatal("an unchanged object was reported as changed")
	}
}

func TestFocus_ChangeIsWrittenAndReported(t *testing.T) {
	h := focusHarness(t)
	h.estate.Mu.Lock()
	for i := range h.estate.Services {
		if aws.ToString(h.estate.Services[i].Service.ServiceName) == "payments-api" {
			h.estate.Services[i].Service.DesiredCount = 11
		}
	}
	h.estate.Mu.Unlock()

	res, err := h.loop.focusService(context.Background(), paymentsAPI(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.changed {
		t.Fatal("a scaled service was not reported as changed")
	}
	if got := h.count(t, `select desired from services where service_name = 'payments-api'`); got != 11 {
		t.Fatalf("stored desired count = %d, want 11", got)
	}
}

func TestFocus_ThrottleDoublesIntervalAndRecovers(t *testing.T) {
	h := focusHarness(t)
	ctx := context.Background()
	if got, throttled := h.loop.focusInterval(); got != 5*time.Second || throttled {
		t.Fatalf("starting interval = %v throttled %v, want 5s", got, throttled)
	}
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second} {
		h.fake.Throttle = 1
		if _, err := h.loop.focusService(ctx, paymentsAPI(), 1); err == nil {
			t.Fatal("a throttled cycle returned no error")
		}
		if got, throttled := h.loop.focusInterval(); got != want || !throttled {
			t.Fatalf("interval after a throttle = %v throttled %v, want %v throttled (capped at the services interval)", got, throttled, want)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := h.loop.focusService(ctx, paymentsAPI(), 1); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := h.loop.focusInterval(); got != 15*time.Second {
		t.Fatalf("interval after five clean cycles = %v, want 15s", got)
	}
	for i := 0; i < 15; i++ {
		if _, err := h.loop.focusService(ctx, paymentsAPI(), 1); err != nil {
			t.Fatal(err)
		}
	}
	if got, throttled := h.loop.focusInterval(); got != 5*time.Second || throttled {
		t.Fatalf("interval after recovery = %v throttled %v, want 5s", got, throttled)
	}
}

func TestFocus_TierThrottleAlsoSlowsFocus(t *testing.T) {
	h := focusHarness(t)
	h.fake.Throttle = 1
	var services tierDef
	for _, d := range h.loop.defs {
		if d.tier.name == tierServices {
			services = d
		}
	}
	if err := h.loop.runTier(context.Background(), services); err == nil {
		t.Fatal("the throttled tier returned no error")
	}
	if got, throttled := h.loop.focusInterval(); got != 10*time.Second || !throttled {
		t.Fatalf("focus interval after a tier throttle = %v throttled %v, want 10s throttled", got, throttled)
	}
}

func TestFocus_MeasuredRoundTimeStretchesTheInterval(t *testing.T) {
	h := focusHarness(t)
	h.loop.setFocusRound(7300 * time.Millisecond)
	if got, throttled := h.loop.focusInterval(); got != 8*time.Second || throttled {
		t.Fatalf("interval with a 7.3 s round = %v throttled %v, want 8s (rounded up) not throttled", got, throttled)
	}
}

func TestFocus_CallsAreLabelledFocus(t *testing.T) {
	h := focusHarness(t)
	var mu sync.Mutex
	counts := map[awsx.Source]int{}
	h.fake.OnCall = func(ctx context.Context, _ awsx.Target, _, _, _ string) {
		mu.Lock()
		defer mu.Unlock()
		counts[awsx.SourceFrom(ctx)]++
	}
	if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.loop.focusCluster(context.Background(), coreServices(), 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts[awsx.SourceFocus] == 0 || len(counts) != 1 {
		t.Fatalf("calls by source = %v, want every call labelled %q", counts, awsx.SourceFocus)
	}
}

func TestFocus_LostLeaseStopsTheCycle(t *testing.T) {
	h := focusHarness(t)
	if _, err := h.store.Pool.Exec(context.Background(),
		`update poll_leases set owner_id = $1, epoch = epoch + 1 where kind = 'slice' and account_id = $2 and region = $3`,
		uuid.New(), loopTarget.AccountID, loopTarget.Region); err != nil {
		t.Fatal(err)
	}
	h.estate.Mu.Lock()
	for i := range h.estate.Services {
		if aws.ToString(h.estate.Services[i].Service.ServiceName) == "payments-api" {
			h.estate.Services[i].Service.DesiredCount = 12 // something to write, so the fence is reached
		}
	}
	h.estate.Mu.Unlock()

	_, err := h.loop.focusService(context.Background(), paymentsAPI(), 1)
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("cycle with a lost lease = %v, want ErrLeaseLost", err)
	}
	if got := h.count(t, `select desired from services where service_name = 'payments-api'`); got == 12 {
		t.Fatal("a write got through after the lease was lost")
	}
}
