package kv_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// scripted is a kv.Client whose Ping and Get follow a script. The other methods are not used by these tests.
type scripted struct {
	kv.Client
	pingErr atomic.Value // error or nil
	getFn   func(ctx context.Context) error
}

func (s *scripted) setPing(err error) { s.pingErr.Store(struct{ error }{err}) }

func (s *scripted) Ping(ctx context.Context) error {
	if v, ok := s.pingErr.Load().(struct{ error }); ok {
		return v.error
	}
	return nil
}

func (s *scripted) Get(ctx context.Context, key string) (kv.Value, error) {
	if s.getFn != nil {
		return kv.Value{}, s.getFn(ctx)
	}
	return kv.Value{}, nil
}

func (s *scripted) Close() {}

type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), sub)
}

func newHealth(c kv.Client) (*kv.Health, *telemetry.Registry, *logSink) {
	reg := telemetry.New()
	sink := &logSink{}
	h := kv.NewHealth(c, reg, slog.New(slog.NewTextHandler(sink, nil)))
	return h, reg, sink
}

func runHealth(t *testing.T, h *kv.Health) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHealth_StartsDownUntilFirstPing(t *testing.T) {
	fake := &scripted{}
	h, _, _ := newHealth(fake)
	if h.Up() {
		t.Fatal("a new Health must start down")
	}
	kv.SetHealthTimings(h, 250*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond)
	runHealth(t, h)
	eventually(t, "up after the first successful PING", h.Up)
}

func TestHealth_DownAfterThreeFailedCommands(t *testing.T) {
	fake := &scripted{getFn: func(context.Context) error { return errors.New("boom") }}
	h, _, _ := newHealth(fake)
	// One ping at start brings it up; no later ping can mask the command failures.
	kv.SetHealthTimings(h, 250*time.Millisecond, time.Hour, 100*time.Millisecond)
	runHealth(t, h)
	eventually(t, "up", h.Up)

	c := h.Client()
	for i := 0; i < 2; i++ {
		_, _ = c.Get(context.Background(), "k")
	}
	if !h.Up() {
		t.Fatal("two failed commands must not mark the store down")
	}
	_, _ = c.Get(context.Background(), "k")
	if h.Up() {
		t.Fatal("three consecutive failed commands must mark the store down")
	}
}

func TestHealth_AGoodCommandResetsTheFailureCount(t *testing.T) {
	var fail atomic.Bool
	fake := &scripted{getFn: func(context.Context) error {
		if fail.Load() {
			return errors.New("boom")
		}
		return nil
	}}
	h, _, _ := newHealth(fake)
	kv.SetHealthTimings(h, 250*time.Millisecond, time.Hour, 100*time.Millisecond)
	runHealth(t, h)
	eventually(t, "up", h.Up)
	c := h.Client()
	fail.Store(true)
	_, _ = c.Get(context.Background(), "k")
	_, _ = c.Get(context.Background(), "k")
	fail.Store(false)
	_, _ = c.Get(context.Background(), "k")
	fail.Store(true)
	_, _ = c.Get(context.Background(), "k")
	_, _ = c.Get(context.Background(), "k")
	if !h.Up() {
		t.Fatal("failures separated by a success must not add up to three")
	}
}

func TestHealth_DownAfterOneFailedPing(t *testing.T) {
	fake := &scripted{}
	h, _, _ := newHealth(fake)
	kv.SetHealthTimings(h, 250*time.Millisecond, 5*time.Millisecond, 100*time.Millisecond)
	runHealth(t, h)
	eventually(t, "up", h.Up)
	fake.setPing(errors.New("refused"))
	eventually(t, "down after one failed PING", func() bool { return !h.Up() })
}

func TestHealth_UpAfterThreeGoodPings(t *testing.T) {
	fake := &scripted{}
	fake.setPing(errors.New("refused"))
	h, _, _ := newHealth(fake)
	kv.SetHealthTimings(h, 250*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond)
	runHealth(t, h)
	time.Sleep(80 * time.Millisecond)
	if h.Up() {
		t.Fatal("must stay down while pings fail")
	}
	// One good ping at first start would bring it up; after a failure it takes three in a row.
	fake.setPing(nil)
	start := time.Now()
	eventually(t, "up", h.Up)
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("came up after %v, want at least two more ping intervals after the first good one", elapsed)
	}
}

func TestHealth_CommandTimesOutAt250ms(t *testing.T) {
	fake := &scripted{getFn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	h, _, _ := newHealth(fake)
	// The outer deadline only keeps a broken Health from hanging the test; the 250ms bound must come first.
	outer, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := h.Client().Get(outer, "k")
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("Get took %v, want it bounded near 250ms", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}

func TestHealth_TransitionsAreLoggedOnce(t *testing.T) {
	fake := &scripted{getFn: func(context.Context) error { return errors.New("boom") }}
	h, _, sink := newHealth(fake)
	kv.SetHealthTimings(h, 250*time.Millisecond, time.Hour, 100*time.Millisecond)
	runHealth(t, h)
	eventually(t, "up", h.Up)
	c := h.Client()
	for i := 0; i < 10; i++ {
		_, _ = c.Get(context.Background(), "k")
	}
	if got := sink.count("kv: store is down"); got != 1 {
		t.Fatalf("down logged %d times, want 1", got)
	}
	if got := sink.count("kv: store is up"); got != 1 {
		t.Fatalf("up logged %d times, want 1", got)
	}
}

func TestHealth_RecordsMetrics(t *testing.T) {
	fake := &scripted{getFn: func(context.Context) error { return errors.New("boom") }}
	h, reg, _ := newHealth(fake)
	_, _ = h.Client().Get(context.Background(), "k")
	var out bytes.Buffer
	if err := reg.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"harbormaster_kv_up 0", `harbormaster_kv_errors_total{op="get"} 1`, `harbormaster_kv_command_duration_seconds_count{op="get"} 1`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestHealth_RecoversWhenServerReturns(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	for _, url := range urls {
		url := url
		t.Run("engine", func(t *testing.T) {
			proxied, cut, restore := kvtest.Proxy(t, url)
			c := kvtest.Open(t, proxied)
			h, _, _ := newHealth(c)
			kv.SetHealthTimings(h, 250*time.Millisecond, 25*time.Millisecond, 200*time.Millisecond)
			runHealth(t, h)
			eventually(t, "up", h.Up)
			if err := h.Client().Ping(context.Background()); err != nil {
				t.Fatalf("Ping while up: %v", err)
			}
			cut()
			eventually(t, "down after the connection is cut", func() bool { return !h.Up() })
			restore()
			eventually(t, "up again without a restart", h.Up)
			if err := h.Client().Ping(context.Background()); err != nil {
				t.Fatalf("Ping after recovery: %v", err)
			}
		})
	}
}
