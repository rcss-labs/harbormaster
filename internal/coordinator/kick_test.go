package coordinator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// kicked records the jobs a runner woke because of a bus message.
type kicked struct {
	mu   sync.Mutex
	jobs []string
}

func (k *kicked) add(job string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.jobs = append(k.jobs, job)
}

func (k *kicked) snapshot() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.jobs...)
}

func (k *kicked) has(job string) bool {
	for _, j := range k.snapshot() {
		if j == job {
			return true
		}
	}
	return false
}

// kickRunner builds a runner with the three kickable jobs, none of which does any work on its own (hour-long
// intervals), so a wake-up is visible only through OnKick.
func kickRunner(st *store.Store, bus signal.Bus, rec *kicked) *coordinator.Runner {
	aws := fake.New(fake.MockupEstate(time.Now))
	estate := fake.MockupEstate(time.Now)
	scanRunner := scanRunnerFor(st, fake.NewScanner(estate))
	scanRunner.Cfg.Interval = time.Hour
	return &coordinator.Runner{
		Store: st, Timing: timing(), Log: quiet(), Bus: bus, OnKick: rec.add,
		Scan:     scanRunner,
		OffHours: &offhours.Reconciler{Applier: &offhours.Applier{Store: st, Clients: aws, Now: time.Now}, Store: st, Interval: time.Hour, Now: time.Now},
		Notify:   &notify.Dispatcher{Store: st, Senders: &countingSender{}, BaseURL: "https://hm.example.com", Interval: time.Hour, Now: time.Now},
	}
}

func publishUntil(t *testing.T, bus signal.Bus, payload string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("kick %q never arrived", payload)
		}
		if err := bus.Publish(context.Background(), signal.Kick, payload); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Only the process that holds the coordinator lease listens, so a kick published by any web task reaches exactly one
// coordinator, on either engine.
func TestKick_ReachesTheActiveCoordinatorOnly(t *testing.T) {
	st := storetest.Open(t)
	for _, url := range kvtest.URLs() {
		o, err := kv.ParseURL(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("port"+o.Address[len(o.Address)-4:], func(t *testing.T) {
			open := func() kv.Client {
				opts := o
				opts.Prefix = "tkick"
				c, err := kv.Open(opts)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(c.Close)
				return c
			}
			holderRec, standbyRec := &kicked{}, &kicked{}
			holder := kickRunner(st, signal.NewKVBus(open()), holderRec)
			mh, _ := startManager(t, st, holder)
			waitFor(t, 5*time.Second, "the holder to coordinate", mh.HoldsCoordinator)
			standby := kickRunner(st, signal.NewKVBus(open()), standbyRec)
			ms, _ := startManager(t, st, standby)

			web := signal.NewKVBus(open())
			publishUntil(t, web, "offhours", func() bool { return holderRec.has("offhours") })
			time.Sleep(200 * time.Millisecond)
			if ms.HoldsCoordinator() || len(standbyRec.snapshot()) != 0 {
				t.Fatalf("the standby acted on a kick: holds=%v kicked=%v", ms.HoldsCoordinator(), standbyRec.snapshot())
			}
		})
	}
	if len(kvtest.URLs()) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
}

func TestKick_UnknownPayloadIsIgnored(t *testing.T) {
	st := storetest.Open(t)
	bus := signal.NewMemoryBus()
	rec := &kicked{}
	m, _ := startManager(t, st, kickRunner(st, bus, rec))
	waitFor(t, 5*time.Second, "the runner to coordinate", m.HoldsCoordinator)

	for i := 0; i < 5; i++ {
		_ = bus.Publish(context.Background(), signal.Kick, "bogus")
		time.Sleep(10 * time.Millisecond)
	}
	publishUntil(t, bus, "scan", func() bool { return rec.has("scan") })
	for _, j := range rec.snapshot() {
		if j != "scan" {
			t.Fatalf("an unknown payload woke %q", j)
		}
	}
}

// gapBus lets a test call the subscriber's repair hook, as a re-established subscription would.
type gapBus struct {
	signal.Bus
	mu    sync.Mutex
	onGap func()
	ready chan struct{}
	once  sync.Once
}

func (g *gapBus) Subscribe(ctx context.Context, ch signal.Channel, fn func(string), onGap func()) error {
	g.mu.Lock()
	g.onGap = onGap
	g.mu.Unlock()
	g.once.Do(func() { close(g.ready) })
	return g.Bus.Subscribe(ctx, ch, fn, onGap)
}

func (g *gapBus) fireGap() {
	g.mu.Lock()
	fn := g.onGap
	g.mu.Unlock()
	fn()
}

// A subscription that was lost may have missed kicks, so on reconnect every job is woken once.
func TestKick_GapKicksEveryJobOnce(t *testing.T) {
	st := storetest.Open(t)
	bus := &gapBus{Bus: signal.NewMemoryBus(), ready: make(chan struct{})}
	rec := &kicked{}
	m, _ := startManager(t, st, kickRunner(st, bus, rec))
	waitFor(t, 5*time.Second, "the runner to coordinate", m.HoldsCoordinator)
	select {
	case <-bus.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never subscribed")
	}
	bus.fireGap()
	waitFor(t, 3*time.Second, "all three jobs to be kicked", func() bool { return len(rec.snapshot()) >= 3 })
	time.Sleep(100 * time.Millisecond)
	got := map[string]int{}
	for _, j := range rec.snapshot() {
		got[j]++
	}
	if len(got) != 3 || got["scan"] != 1 || got["offhours"] != 1 || got["notify"] != 1 {
		t.Fatalf("kicked %v, want scan, offhours and notify once each", got)
	}
}
