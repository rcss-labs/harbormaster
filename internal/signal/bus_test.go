package signal_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/signal"
)

// inbox collects what a subscriber saw.
type inbox struct {
	mu   sync.Mutex
	seen []string
}

func (b *inbox) add(p string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, p)
}

func (b *inbox) has(p string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.seen {
		if s == p {
			return true
		}
	}
	return false
}

func (b *inbox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.seen)
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// subscribe starts a subscriber and returns its inbox. Publishing right away can race the registration (the server
// subscription is asynchronous), so tests that need delivery use publishUntil.
func subscribe(t *testing.T, ctx context.Context, b signal.Bus, ch signal.Channel, onGap func()) *inbox {
	t.Helper()
	box := &inbox{}
	go func() { _ = b.Subscribe(ctx, ch, box.add, onGap) }()
	return box
}

// publishUntil publishes payload every 20 ms until cond holds: delivery is at most once and a subscription may not be
// registered yet, so a single publish proves nothing.
func publishUntil(t *testing.T, b signal.Bus, ch signal.Channel, payload string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%q was not delivered", payload)
		}
		if err := b.Publish(context.Background(), ch, payload); err != nil {
			t.Fatalf("publish: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func cancelCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// runBusContract is the behaviour every implementation shares.
func runBusContract(t *testing.T, newBus func(t *testing.T) signal.Bus) {
	t.Run("DeliversToEverySubscriber", func(t *testing.T) {
		b := newBus(t)
		ctx := cancelCtx(t)
		a := subscribe(t, ctx, b, signal.Kick, nil)
		c := subscribe(t, ctx, b, signal.Kick, nil)
		publishUntil(t, b, signal.Kick, "scan", func() bool { return a.has("scan") && c.has("scan") })
	})
	t.Run("ChannelsAreSeparate", func(t *testing.T) {
		b := newBus(t)
		ctx := cancelCtx(t)
		kick := subscribe(t, ctx, b, signal.Kick, nil)
		pollNow := subscribe(t, ctx, b, signal.PollNow, nil)
		publishUntil(t, b, signal.Kick, "offhours", func() bool { return kick.has("offhours") })
		time.Sleep(100 * time.Millisecond)
		if pollNow.count() != 0 {
			t.Fatalf("poll-now subscriber saw %v from the kick channel", pollNow.seen)
		}
	})
	t.Run("PublishWithoutSubscribersSucceeds", func(t *testing.T) {
		b := newBus(t)
		if err := b.Publish(context.Background(), signal.Kick, "notify"); err != nil {
			t.Fatalf("publish with no subscriber: %v", err)
		}
	})
	t.Run("SubscribeReturnsWhenContextEnds", func(t *testing.T) {
		b := newBus(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- b.Subscribe(ctx, signal.Kick, func(string) {}, nil) }()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Subscribe did not return after its context ended")
		}
	})
}

func TestMemoryBus(t *testing.T) {
	runBusContract(t, func(*testing.T) signal.Bus { return signal.NewMemoryBus() })
}

func TestBus_SlowSubscriberDoesNotBlockPublish(t *testing.T) {
	b := signal.NewMemoryBus()
	ctx := cancelCtx(t)
	release := make(chan struct{})
	var got sync.WaitGroup
	got.Add(1)
	var once sync.Once
	go func() {
		_ = b.Subscribe(ctx, signal.Kick, func(string) {
			once.Do(got.Done)
			<-release
		}, nil)
	}()
	t.Cleanup(func() { close(release) })
	publishFirst := time.Now().Add(5 * time.Second)
	for {
		_ = b.Publish(context.Background(), signal.Kick, "x")
		done := make(chan struct{})
		go func() { got.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(publishFirst) {
				t.Fatal("the subscriber never started")
			}
			continue
		}
		break
	}
	// The subscriber is now stuck inside its handler; far more than its buffer must still publish at once.
	start := time.Now()
	for i := 0; i < 500; i++ {
		if err := b.Publish(context.Background(), signal.Kick, "y"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("500 publishes took %v with a stuck subscriber", d)
	}
}

func eachKV(t *testing.T, fn func(t *testing.T, rawURL string)) {
	t.Helper()
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	for _, u := range urls {
		o, err := kv.ParseURL(u)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("port"+portOf(o.Address), func(t *testing.T) { fn(t, u) })
	}
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

// openWithPrefix opens a client under a chosen prefix, so two clients can share an install.
func openWithPrefix(t *testing.T, rawURL, prefix string) kv.Client {
	t.Helper()
	o, err := kv.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	o.Prefix = prefix
	c, err := kv.Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestKVBus(t *testing.T) {
	eachKV(t, func(t *testing.T, rawURL string) {
		runBusContract(t, func(t *testing.T) signal.Bus { return signal.NewKVBus(kvtest.Open(t, rawURL)) })
	})
}

func TestBus_CrossesClients(t *testing.T) {
	eachKV(t, func(t *testing.T, rawURL string) {
		sub := signal.NewKVBus(openWithPrefix(t, rawURL, "tcross"))
		pub := signal.NewKVBus(openWithPrefix(t, rawURL, "tcross"))
		box := subscribe(t, cancelCtx(t), sub, signal.Kick, nil)
		publishUntil(t, pub, signal.Kick, "scan", func() bool { return box.has("scan") })
	})
}

func TestBus_PrefixIsolatesInstalls(t *testing.T) {
	eachKV(t, func(t *testing.T, rawURL string) {
		a := signal.NewKVBus(openWithPrefix(t, rawURL, "tiso-a"))
		b := signal.NewKVBus(openWithPrefix(t, rawURL, "tiso-b"))
		ctx := cancelCtx(t)
		boxA := subscribe(t, ctx, a, signal.Kick, nil)
		boxB := subscribe(t, ctx, b, signal.Kick, nil)
		publishUntil(t, a, signal.Kick, "scan", func() bool { return boxA.has("scan") })
		time.Sleep(100 * time.Millisecond)
		if boxB.count() != 0 {
			t.Fatalf("install B saw install A's kick: %v", boxB.seen)
		}
	})
}

func TestBus_CallsOnGapAfterReconnect(t *testing.T) {
	eachKV(t, func(t *testing.T, rawURL string) {
		bus := signal.NewKVBus(openWithPrefix(t, rawURL, "tgap"))
		var mu sync.Mutex
		gaps := 0
		box := subscribe(t, cancelCtx(t), bus, signal.Kick, func() { mu.Lock(); gaps++; mu.Unlock() })
		publishUntil(t, bus, signal.Kick, "scan", func() bool { return box.has("scan") })
		kvtest.DropPubSub(t, rawURL)
		eventually(t, 10*time.Second, "onGap after the subscription was dropped", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return gaps >= 1
		})
		publishUntil(t, bus, signal.Kick, "again", func() bool { return box.has("again") })
	})
}

// fakeBus records publishes and lets a test play the primary.
type fakeBus struct {
	mu        sync.Mutex
	published []string
	subs      inbox
	err       error
}

func (f *fakeBus) Publish(_ context.Context, _ signal.Channel, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, payload)
	return nil
}

func (f *fakeBus) Subscribe(ctx context.Context, _ signal.Channel, fn func(string), _ func()) error {
	<-ctx.Done()
	return nil
}

func (f *fakeBus) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

type toggle struct {
	mu sync.Mutex
	up bool
}

func (g *toggle) Up() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.up }
func (g *toggle) set(v bool) {
	g.mu.Lock()
	g.up = v
	g.mu.Unlock()
}

func TestBusFallback_PublishesToMemoryWhileDown(t *testing.T) {
	primary := &fakeBus{}
	memory := signal.NewMemoryBus()
	health := &toggle{}
	bus := signal.WithFallback(primary, memory, health)
	box := subscribe(t, cancelCtx(t), bus, signal.Kick, nil)

	publishUntil(t, bus, signal.Kick, "scan", func() bool { return box.has("scan") })
	if primary.count() != 0 {
		t.Fatalf("primary got %d publishes while the store was down", primary.count())
	}

	health.set(true)
	if err := bus.Publish(context.Background(), signal.Kick, "offhours"); err != nil {
		t.Fatal(err)
	}
	if primary.count() != 1 {
		t.Fatalf("primary got %d publishes while the store was up, want 1", primary.count())
	}
}

func TestBusFallback_FallsBackToMemoryWhenTheStoreErrors(t *testing.T) {
	primary := &fakeBus{err: context.DeadlineExceeded}
	health := &toggle{up: true}
	bus := signal.WithFallback(primary, signal.NewMemoryBus(), health)
	box := subscribe(t, cancelCtx(t), bus, signal.Kick, nil)
	publishUntil(t, bus, signal.Kick, "notify", func() bool { return box.has("notify") })
}
