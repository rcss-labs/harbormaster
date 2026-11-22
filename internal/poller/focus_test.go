package poller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// flakyRegistry fails List on demand and records every Fresh it is given.
type flakyRegistry struct {
	focus.Registry
	failList atomic.Bool
	mu       sync.Mutex
	fresh    []focus.Fresh
}

func (r *flakyRegistry) List(ctx context.Context) ([]focus.Object, error) {
	if r.failList.Load() {
		return nil, errors.New("registry down")
	}
	return r.Registry.List(ctx)
}

func (r *flakyRegistry) SetFresh(ctx context.Context, o focus.Object, f focus.Fresh) error {
	r.mu.Lock()
	r.fresh = append(r.fresh, f)
	r.mu.Unlock()
	return r.Registry.SetFresh(ctx, o, f)
}

func (r *flakyRegistry) freshStates() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for _, f := range r.fresh {
		out[f.State] = true
	}
	return out
}

type focusRig struct {
	t      *testing.T
	st     *store.Store
	fake   *fake.Fake
	p      *Poller
	loop   *Loop
	reg    *flakyRegistry
	bus    signal.Bus
	tel    *telemetry.Registry
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	calls map[string]int // focus-labelled calls by operation
}

// newFocusRig runs one slice loop's focus scheduler and the focus manager against the fake estate. The registry is read
// every readEvery.
func newFocusRig(t *testing.T, estate *fake.Estate, cfg Config, readEvery time.Duration) *focusRig {
	t.Helper()
	st := storetest.Open(t)
	f := fake.New(estate)
	r := &focusRig{t: t, st: st, fake: f, tel: telemetry.New(), bus: signal.NewMemoryBus(), calls: map[string]int{}}
	r.reg = &flakyRegistry{Registry: focus.NewMemoryRegistry(time.Now)}
	cfg.Telemetry, cfg.Registry, cfg.Bus = r.tel, r.reg, r.bus
	r.cfg = cfg
	f.OnCall = func(ctx context.Context, _ awsx.Target, _, op, _ string) {
		if awsx.SourceFrom(ctx) != awsx.SourceFocus {
			return
		}
		r.mu.Lock()
		r.calls[op]++
		r.mu.Unlock()
	}
	r.p = New(st, f, cfg, discardLogger())
	r.p.focusReadEvery = readEvery
	w := testWriter(t, st, loopTarget)
	r.loop = newLoop(r.p, loopTarget, w)
	if err := r.loop.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	unregister := r.p.registerLoop(r.loop)
	r.ctx, r.cancel = context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.loop.runFocus(r.ctx) }()
	go func() { defer wg.Done(); r.p.runFocusManager(r.ctx) }()
	t.Cleanup(func() { r.cancel(); wg.Wait(); unregister() })
	// The in-memory bus drops a message with no subscriber yet; give the manager's subscription a moment to start.
	time.Sleep(100 * time.Millisecond)
	return r
}

func scaledFocusConfig(interval, ttl time.Duration) Config {
	cfg := focusConfig()
	cfg.Focus.Interval, cfg.Focus.TTL = interval, ttl
	return cfg
}

func (r *focusRig) focusCalls(op string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[op]
}

func (r *focusRig) heartbeat(o focus.Object, user int64) {
	r.t.Helper()
	lim := focus.Limits{TTL: r.cfg.Focus.TTL, MaxObjects: r.cfg.Focus.MaxObjects, MaxPerUser: r.cfg.Focus.MaxPerUser}
	if _, err := r.reg.Heartbeat(context.Background(), o, user, lim); err != nil {
		r.t.Fatal(err)
	}
}

func (r *focusRig) publishPollNow(o focus.Object) {
	r.t.Helper()
	if err := r.bus.Publish(context.Background(), signal.PollNow, o.Member()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *focusRig) metrics() string {
	var buf bytes.Buffer
	if err := r.tel.WritePrometheus(&buf); err != nil {
		r.t.Fatal(err)
	}
	return buf.String()
}

func paymentsObject() focus.Object {
	return focus.Object{Kind: focus.KindService, Cluster: coreServices(), Service: "payments-api"}
}

func bulkCluster() model.ClusterKey {
	return model.ClusterKey{AccountID: homelabAccount, Region: "us-east-1", Cluster: "bulk"}
}

func mockupEstate() *fake.Estate { return fake.MockupEstate(func() time.Time { return loopNow }) }

func TestFocusManager_PollNowRunsACycleWithinOneSecond(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(time.Hour, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	r.heartbeat(obj, 1)
	waitFor(t, 3*time.Second, "the first focus cycle", func() bool { return r.focusCalls("DescribeServices") >= 1 })

	time.Sleep(1100 * time.Millisecond) // past the one-second coalescing window
	before := r.focusCalls("DescribeServices")
	r.publishPollNow(obj)
	waitFor(t, time.Second, "a cycle caused by the poll-now signal", func() bool { return r.focusCalls("DescribeServices") > before })
	waitFor(t, time.Second, "the latency sample", func() bool {
		return strings.Contains(r.metrics(), "harbormaster_pollnow_latency_seconds_count 1")
	})
}

func TestFocusManager_OneLoopForManyHeartbeats(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(200*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		for user := int64(1); user <= 3; user++ {
			r.heartbeat(obj, user)
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := r.focusCalls("DescribeServices")
	if got < 3 || got > 9 {
		t.Fatalf("DescribeServices from focus = %d over 1.2 s at a 200 ms interval with three viewers, want about 6 (one loop)", got)
	}
}

func TestFocusManager_LoopEndsWhenObjectLeavesTheRegistry(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	lim := focus.Limits{TTL: 800 * time.Millisecond, MaxObjects: 50, MaxPerUser: 5}
	if _, err := r.reg.Heartbeat(context.Background(), obj, 1, lim); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "focus cycles", func() bool { return r.focusCalls("DescribeServices") >= 1 })
	time.Sleep(1200 * time.Millisecond) // the TTL plus two registry reads and a running cycle
	settled := r.focusCalls("DescribeServices")
	time.Sleep(500 * time.Millisecond)
	if got := r.focusCalls("DescribeServices"); got != settled {
		t.Fatalf("focus calls kept coming after the object expired: %d -> %d", settled, got)
	}
	if n := r.loop.focusEntries(); n != 0 {
		t.Fatalf("%d focus entries remain after the object expired", n)
	}
}

func TestFocusManager_RegistryReadRepairsALostSignal(t *testing.T) {
	// The default one-second read, and no poll-now signal at all.
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(time.Hour, 30*time.Second), time.Second)
	r.heartbeat(paymentsObject(), 1)
	waitFor(t, 1500*time.Millisecond, "a cycle from the registry read alone", func() bool { return r.focusCalls("DescribeServices") >= 1 })
}

func TestFocusManager_SignalForUnownedSliceIsDroppedAndCounted(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(time.Hour, 30*time.Second), 50*time.Millisecond)
	foreign := focus.Object{Kind: focus.KindService, Cluster: coreServices(), Service: "payments-api"}
	foreign.Cluster.AccountID = "999999999999"
	r.publishPollNow(foreign)
	if err := r.bus.Publish(context.Background(), signal.PollNow, "not a member"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, "both signals counted as not_owner", func() bool {
		return strings.Contains(r.metrics(), `harbormaster_pollnow_signals_total{result="not_owner"} 2`)
	})
	time.Sleep(200 * time.Millisecond)
	if n := r.focusCalls("DescribeServices") + r.focusCalls("DescribeClusters"); n != 0 {
		t.Fatalf("a foreign signal caused %d focus calls", n)
	}
}

func TestFocusManager_CoalescesRepeatedSignals(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(time.Hour, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	r.heartbeat(obj, 1)
	waitFor(t, 3*time.Second, "the first focus cycle", func() bool { return r.focusCalls("DescribeServices") >= 1 })
	time.Sleep(1100 * time.Millisecond)
	before := r.focusCalls("DescribeServices")

	for i := 0; i < 5; i++ {
		r.publishPollNow(obj)
		time.Sleep(40 * time.Millisecond)
	}
	time.Sleep(600 * time.Millisecond)
	if got := r.focusCalls("DescribeServices") - before; got != 1 {
		t.Fatalf("five signals in 200 ms caused %d cycles, want 1", got)
	}
	out := r.metrics()
	for _, want := range []string{`harbormaster_pollnow_signals_total{result="handled"} 1`, `harbormaster_pollnow_signals_total{result="coalesced"} 4`} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q\n%s", want, out)
		}
	}
}

func TestFocusManager_CappedObjectGetsOneCycleAndNoLoop(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	r.publishPollNow(paymentsObject()) // never registered: the page was capped
	waitFor(t, 2*time.Second, "the one cycle", func() bool { return r.focusCalls("DescribeServices") >= 1 })
	time.Sleep(700 * time.Millisecond)
	if got := r.focusCalls("DescribeServices"); got != 1 {
		t.Fatalf("an unregistered object got %d cycles, want exactly 1", got)
	}
	if n := r.loop.focusEntries(); n != 0 {
		t.Fatalf("%d focus entries for an unregistered object", n)
	}
}

// bulkEstate has one cluster of n services with no tasks and no load balancers, so each service cycle costs one
// ECS-service-read call and one or two cluster-resource-read calls.
func bulkEstate(n int) *fake.Estate {
	estate := &fake.Estate{
		Now:      func() time.Time { return loopNow },
		Accounts: []fake.Account{{ID: homelabAccount, Name: "homelab-admin", Regions: []string{"us-east-1"}}},
		Events:   map[string][]ecstypes.ServiceEvent{},
	}
	clusterARN := "arn:aws:ecs:us-east-1:" + homelabAccount + ":cluster/bulk"
	estate.Clusters = []fake.Cluster{{AccountID: homelabAccount, Region: "us-east-1",
		Cluster: ecstypes.Cluster{ClusterArn: aws.String(clusterARN), ClusterName: aws.String("bulk"), Status: aws.String("ACTIVE")}}}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("svc-%02d", i)
		estate.Services = append(estate.Services, fake.Service{AccountID: homelabAccount, Region: "us-east-1", ClusterName: "bulk",
			Service: ecstypes.Service{ServiceName: aws.String(name), Status: aws.String("ACTIVE"),
				ServiceArn: aws.String("arn:aws:ecs:us-east-1:" + homelabAccount + ":service/bulk/" + name)}})
	}
	return estate
}

func TestFocus_StretchesIntervalsFairlyWhenOverBudget(t *testing.T) {
	const objects = 20
	base := 2 * time.Second
	cfg := scaledFocusConfig(base, 60*time.Second)
	cfg.Focus.MaxCallsPerSec = 6
	r := newFocusRig(t, bulkEstate(objects), cfg, 100*time.Millisecond)
	var all []focus.Object
	for i := 0; i < objects; i++ {
		o := focus.Object{Kind: focus.KindService, Cluster: bulkCluster(), Service: fmt.Sprintf("svc-%02d", i)}
		all = append(all, o)
		r.heartbeat(o, int64(i%4+1)) // spread over users: the per-user cap is not what this test is about
	}
	waitFor(t, 90*time.Second, "three cycles of every object", func() bool {
		for _, o := range all {
			if r.loop.focusCycles(o) < 3 {
				return false
			}
		}
		return true
	})
	for _, o := range all {
		fresh, ok, err := r.reg.Fresh(context.Background(), o)
		if err != nil || !ok {
			t.Fatalf("no fresh entry for %s: %v", o.Member(), err)
		}
		if fresh.Interval < base*3/2 || fresh.Interval > base*3 {
			t.Errorf("%s: effective interval %v, want between 1.5 and 3 times %v", o.Service, fresh.Interval, base)
		}
	}
}

func TestFocusManager_WritesFreshWithStateAndInterval(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	r.heartbeat(obj, 1)
	var fresh focus.Fresh
	waitFor(t, 3*time.Second, "a fresh entry", func() bool {
		f, ok, _ := r.reg.Fresh(context.Background(), obj)
		fresh = f
		return ok
	})
	if fresh.State != "live" || fresh.Interval != time.Second || time.Since(fresh.At) > 5*time.Second {
		t.Fatalf("fresh = %+v, want live, interval 1s (100 ms rounded up) and a recent time", fresh)
	}
}

func TestFocusManager_ThrottledStateIsReported(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	obj := paymentsObject()
	r.heartbeat(obj, 1)
	waitFor(t, 3*time.Second, "a first live read", func() bool { _, ok, _ := r.reg.Fresh(context.Background(), obj); return ok })
	atomic.StoreInt32(&r.fake.Throttle, 1)
	waitFor(t, 5*time.Second, "a read reported as throttled", func() bool { return r.reg.freshStates()["throttled"] })
}

func TestFocusManager_StopsLoopsAfterRegistryFailsForOneTTL(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 400*time.Millisecond), 50*time.Millisecond)
	obj := paymentsObject()
	lim := focus.Limits{TTL: time.Hour, MaxObjects: 50, MaxPerUser: 5}
	if _, err := r.reg.Heartbeat(context.Background(), obj, 1, lim); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "focus cycles", func() bool { return r.focusCalls("DescribeServices") >= 2 })

	r.reg.failList.Store(true)
	waitFor(t, 3*time.Second, "the loops to stop", func() bool { return r.loop.focusEntries() == 0 })
	settled := r.focusCalls("DescribeServices")
	time.Sleep(400 * time.Millisecond)
	if got := r.focusCalls("DescribeServices"); got != settled {
		t.Fatalf("focus calls continued with the registry down: %d -> %d", settled, got)
	}

	r.reg.failList.Store(false)
	waitFor(t, 3*time.Second, "focus to resume", func() bool { return r.focusCalls("DescribeServices") > settled })
}

func TestFocusManager_KeepsLoopsThroughAShortRegistryFailure(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 5*time.Second), 50*time.Millisecond)
	r.heartbeat(paymentsObject(), 1)
	waitFor(t, 2*time.Second, "focus cycles", func() bool { return r.focusCalls("DescribeServices") >= 1 })
	r.reg.failList.Store(true)
	time.Sleep(300 * time.Millisecond) // far below the TTL
	r.reg.failList.Store(false)
	if n := r.loop.focusEntries(); n != 1 {
		t.Fatalf("focus entries after a brief registry failure = %d, want the loop kept", n)
	}
}

func TestFocusManager_ReportsMetrics(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	r.heartbeat(paymentsObject(), 1)
	waitFor(t, 3*time.Second, "focus series", func() bool {
		out := r.metrics()
		return strings.Contains(out, `harbormaster_focus_objects{kind="service"} 1`) &&
			strings.Contains(out, `harbormaster_focus_interval_seconds{account="`+homelabAccount+`",region="us-east-1"} 1`) &&
			strings.Contains(out, `harbormaster_focus_cycle_duration_seconds_count{kind="service"}`)
	})
}

func TestFocusManager_NewOwnerPicksUpFocusAfterTakeover(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		st := storetest.Open(t)
		ctx := context.Background()
		if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "homelab", AccountID: homelabAccount,
			RoleARN: "arn:aws:iam::" + homelabAccount + ":role/x", ExternalID: "e", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		reg, bus := focus.NewKVRegistry(c), signal.NewKVBus(c)
		key := randomLockKey()
		type node struct {
			p      *Poller
			cancel context.CancelFunc
			calls  atomic.Int64
		}
		var nodes []*node
		for range 2 {
			cfg := Config{MaxInFlight: 4, Intervals: Intervals{Services: 30 * time.Second}, Registry: reg, Bus: bus,
				Focus: config.FocusConfig{Interval: 100 * time.Millisecond, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 50}}
			p := newTestPoller(st, cfg, key)
			p.focusReadEvery = 100 * time.Millisecond
			n := &node{p: p}
			p.clients.(*fake.Fake).OnCall = func(ctx context.Context, _ awsx.Target, _, op, _ string) {
				if op == "DescribeServices" && awsx.SourceFrom(ctx) == awsx.SourceFocus {
					n.calls.Add(1)
				}
			}
			n.cancel, _ = startRun(t, p)
			nodes = append(nodes, n)
		}
		waitFor(t, 20*time.Second, "one poller to hold the slice", func() bool { return heldCount(nodes[0].p)+heldCount(nodes[1].p) == 1 })

		obj := paymentsObject()
		if _, err := reg.Heartbeat(ctx, obj, 1, focus.Limits{TTL: time.Hour, MaxObjects: 50, MaxPerUser: 5}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 20*time.Second, "the owner to run focus cycles", func() bool { return nodes[0].calls.Load()+nodes[1].calls.Load() >= 2 })
		owner, other := nodes[0], nodes[1]
		if heldCount(owner.p) == 0 {
			owner, other = other, owner
		}
		if other.calls.Load() != 0 {
			t.Fatalf("the poller without the slice ran %d focus cycles", other.calls.Load())
		}

		owner.cancel()
		waitFor(t, 30*time.Second, "the new owner to run focus cycles for the object", func() bool { return other.calls.Load() >= 2 })
	})
}
