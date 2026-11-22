package poller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func bulkObject(i int) focus.Object {
	return focus.Object{Kind: focus.KindService, Cluster: bulkCluster(), Service: fmt.Sprintf("svc-%02d", i)}
}

// A burst of poll-now signals for objects that are not registered must not start a cycle each: one-off cycles count
// against the slice's in-flight cap and only a few wait their turn, so real live pages keep their call budget.
func TestFocusManager_OneOffCyclesAreBoundedAndTheRestDropped(t *testing.T) {
	cfg := scaledFocusConfig(time.Hour, 30*time.Second)
	cfg.Focus.MaxCallsPerSec = 1000 // the limiter is not what bounds this
	r := newFocusRig(t, bulkEstate(60), cfg, 50*time.Millisecond)
	r.fake.CallLatency = 150 * time.Millisecond // keep started cycles in flight while the burst arrives

	for i := 0; i < 60; i++ {
		r.publishPollNow(bulkObject(i))
	}
	waitFor(t, 5*time.Second, "the queued one-off cycles to finish", func() bool {
		return strings.Contains(r.metrics(), `harbormaster_pollnow_signals_total{result="dropped"}`) && r.focusCalls("DescribeServices") >= maxFocusInFlight
	})
	time.Sleep(2 * time.Second)

	if got := r.focusCalls("DescribeServices"); got > maxFocusInFlight+maxOneOffQueue {
		t.Fatalf("60 signals made %d cycles, want at most %d (in flight %d plus queued %d)", got, maxFocusInFlight+maxOneOffQueue, maxFocusInFlight, maxOneOffQueue)
	}
	if got := r.fake.MaxInFlight(loopTarget); got > maxFocusInFlight {
		t.Fatalf("%d AWS calls in flight at once, want at most %d", got, maxFocusInFlight)
	}
	if !strings.Contains(r.metrics(), `harbormaster_pollnow_signals_total{result="dropped"} 5`) {
		t.Fatalf("dropped signals not counted as expected:\n%s", r.metrics())
	}
}

// Opening a page registers the object and sends poll-now. Whichever the poller sees first, the page must cost one cycle.
func TestFocusManager_OneOpenMakesExactlyOneCycle(t *testing.T) {
	for _, order := range []string{"signal first", "registry first"} {
		t.Run(order, func(t *testing.T) {
			r := newFocusRig(t, mockupEstate(), scaledFocusConfig(time.Hour, 30*time.Second), 300*time.Millisecond)
			obj := paymentsObject()
			if order == "signal first" {
				r.publishPollNow(obj)
				time.Sleep(50 * time.Millisecond)
				r.heartbeat(obj, 1)
			} else {
				r.heartbeat(obj, 1)
				time.Sleep(450 * time.Millisecond)
				r.publishPollNow(obj)
			}
			waitFor(t, 3*time.Second, "the first cycle", func() bool { return r.focusCalls("DescribeServices") >= 1 })
			time.Sleep(1800 * time.Millisecond)

			if got := r.focusCalls("DescribeServices"); got != 1 {
				t.Errorf("DescribeServices = %d, want 1", got)
			}
			if got := r.focusCalls("DescribeTargetHealth"); got != 1 {
				t.Errorf("DescribeTargetHealth = %d, want 1 (one group)", got)
			}
			if got := r.focusCalls("ListTasks"); got != 2 {
				t.Errorf("ListTasks = %d, want 2 (RUNNING and STOPPED in the first cycle)", got)
			}
		})
	}
}

// runRig runs a real Loop.Run (first tiers included) with a slow fake and the focus manager.
type runRig struct {
	t       *testing.T
	fake    *fake.Fake
	p       *Poller
	loop    *Loop
	bus     signal.Bus
	reg     focus.Registry
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	firstAt map[string]time.Time // first call time by "source/op"
	calls   map[string]int
}

func newRunRig(t *testing.T, latency time.Duration) *runRig {
	t.Helper()
	st := storetest.Open(t)
	f := fake.New(mockupEstate())
	f.CallLatency = latency
	r := &runRig{t: t, fake: f, bus: signal.NewMemoryBus(), reg: focus.NewMemoryRegistry(time.Now),
		firstAt: map[string]time.Time{}, calls: map[string]int{}}
	f.OnCall = func(ctx context.Context, _ awsx.Target, _, op, _ string) {
		key := string(awsx.SourceFrom(ctx)) + "/" + op
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, ok := r.firstAt[key]; !ok {
			r.firstAt[key] = time.Now()
		}
		r.calls[key]++
	}
	cfg := scaledFocusConfig(time.Hour, 30*time.Second)
	cfg.Telemetry, cfg.Registry, cfg.Bus = telemetry.New(), r.reg, r.bus
	r.p = New(st, f, cfg, discardLogger())
	r.p.focusReadEvery = 50 * time.Millisecond
	r.loop = newLoop(r.p, loopTarget, testWriter(t, st, loopTarget))
	unregister := r.p.registerLoop(r.loop)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.p.runFocusManager(ctx) }()
	go func() { r.loop.Run(ctx); wg.Wait(); close(r.done) }()
	t.Cleanup(func() { cancel(); <-r.done; unregister() })
	time.Sleep(100 * time.Millisecond) // the in-memory bus drops a message with no subscriber yet
	return r
}

func (r *runRig) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

func (r *runRig) first(key string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.firstAt[key]
	return at, ok
}

func TestFocus_RunsWhileTheFirstTiersAreStillRunning(t *testing.T) {
	r := newRunRig(t, 250*time.Millisecond)
	if _, err := r.reg.Heartbeat(context.Background(), paymentsObject(), 1, focusLimits()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, "a focus cycle and the first pass's services tier", func() bool {
		_, focused := r.first("focus/DescribeServices")
		_, tiered := r.first("tier/DescribeServices")
		return focused && tiered
	})
	focusAt, _ := r.first("focus/DescribeServices")
	tierAt, _ := r.first("tier/DescribeServices")
	if !focusAt.Before(tierAt) {
		t.Fatalf("the first focus cycle (%v) waited for the first pass's services tier (%v)", focusAt, tierAt)
	}
}

func TestFocus_PollNowDuringTheFirstPassStopsWithTheLoop(t *testing.T) {
	r := newRunRig(t, 250*time.Millisecond)
	if err := r.bus.Publish(context.Background(), signal.PollNow, paymentsObject().Member()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the one-off cycle to start", func() bool { return r.count("focus/DescribeServices") >= 1 })
	total := func() int {
		return r.count("focus/DescribeServices") + r.count("focus/ListTasks") + r.count("focus/DescribeTargetHealth") + r.count("focus/DescribeTasks")
	}
	r.cancel()
	atCancel := total()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop with a one-off cycle in flight")
	}
	time.Sleep(1200 * time.Millisecond)
	// One call may already have been in flight when the loop stopped; the cycle must not start another.
	if got := total(); got > atCancel+1 {
		t.Fatalf("a one-off cycle kept calling AWS after the loop stopped: %d -> %d", atCancel, got)
	}
}

func focusLimits() focus.Limits {
	return focus.Limits{TTL: time.Hour, MaxObjects: 50, MaxPerUser: 5}
}

// The limiter, not the interval stretching, must hold the calls to the budget: drive cycles back to back and count.
func TestFocus_RealCallRateStaysWithinTheBudget(t *testing.T) {
	h := focusHarness(t)
	// A tight budget (1 per second, burst 2) so that unthrottled cycles would exceed it however slow they are.
	h.loop.p.cfg.Focus.MaxCallsPerSec = 1
	h.loop = newLoop(h.loop.p, loopTarget, h.writer)
	const seconds = 3
	var mu sync.Mutex
	counts := map[string]int{}
	deadline := time.Now().Add(seconds * time.Second)
	h.fake.OnCall = func(ctx context.Context, _ awsx.Target, _, op, _ string) {
		if awsx.SourceFrom(ctx) == awsx.SourceFocus && time.Now().Before(deadline) { // the window the budget is counted over
			mu.Lock()
			counts[op]++
			mu.Unlock()
		}
	}
	var wg sync.WaitGroup
	for range 6 { // enough demand that only the limiter can hold the rate
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 1); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// Budget: 1 per second per category plus a burst of 2.
	limit := 2 + 1*seconds + 1
	mu.Lock()
	defer mu.Unlock()
	for _, op := range []string{"DescribeServices", "ListTasks", "DescribeTargetHealth"} {
		if counts[op] > limit {
			t.Errorf("%s made %d focus calls in %d s, want at most %d", op, counts[op], seconds, limit)
		}
		if counts[op] == 0 {
			t.Errorf("%s made no calls: the test did not drive the cycle", op)
		}
	}
}

func TestFocusManager_NoFreshAfterAFailedCycle(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	r.fake.FailAlways("DescribeServices", errors.New("boom"))
	r.heartbeat(paymentsObject(), 1)
	waitFor(t, 3*time.Second, "failing cycles", func() bool { return r.focusCalls("DescribeServices") >= 3 })
	r.reg.mu.Lock()
	n := len(r.reg.fresh)
	r.reg.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d fresh entries were written for cycles that failed", n)
	}
	if _, ok, _ := r.reg.Fresh(context.Background(), paymentsObject()); ok {
		t.Fatal("a fresh entry exists for an object whose reads all failed")
	}
}

// With no rows to write the fence is skipped, so only an explicit check stops a poller that lost its lease from saying
// the object was read.
func TestFocusManager_NoFreshFromAPollerThatLostItsLease(t *testing.T) {
	r := newFocusRig(t, mockupEstate(), scaledFocusConfig(100*time.Millisecond, 30*time.Second), 50*time.Millisecond)
	if _, err := r.st.Pool.Exec(context.Background(),
		`update poll_leases set owner_id = $1, epoch = epoch + 1 where kind = 'slice' and account_id = $2 and region = $3`,
		uuid.New(), loopTarget.AccountID, loopTarget.Region); err != nil {
		t.Fatal(err)
	}
	ghost := focus.Object{Kind: focus.KindService, Cluster: coreServices(), Service: "no-such-service"} // no rows to write
	r.heartbeat(ghost, 1)
	waitFor(t, 3*time.Second, "cycles for the object", func() bool { return r.focusCalls("DescribeServices") >= 2 })
	if _, ok, _ := r.reg.Fresh(context.Background(), ghost); ok {
		t.Fatal("a poller that lost its lease recorded a fresh read")
	}
}

var _ = model.ClusterKey{}
var _ = store.ErrLeaseLost
