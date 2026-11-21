package limits

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

type fakeHealth struct{ up atomic.Bool }

func (f *fakeHealth) Up() bool { return f.up.Load() }

// scriptedConcurrency is a primary whose behaviour the test controls.
type scriptedConcurrency struct {
	err      error
	grants   atomic.Int32
	released atomic.Int32
}

func (s *scriptedConcurrency) Acquire(context.Context, string, int64, int) (func(), bool, error) {
	if s.err != nil {
		return func() {}, false, s.err
	}
	s.grants.Add(1)
	return func() { s.released.Add(1) }, true, nil
}

func (s *scriptedConcurrency) Count(context.Context, string, int64) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	return int(s.grants.Load() - s.released.Load()), nil
}

func decisions(t *testing.T, reg *telemetry.Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestFallback_UsesPrimaryWhileUp(t *testing.T) {
	primary, health := &scriptedConcurrency{}, &fakeHealth{}
	health.up.Store(true)
	c := WithFallback(primary, NewMemoryConcurrency(), health, telemetry.New())
	if _, ok, err := c.Acquire(context.Background(), "exec", 1, 3); !ok || err != nil {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	if primary.grants.Load() != 1 {
		t.Fatal("the store was not used while up")
	}
}

func TestFallback_UsesMemoryWhileDown(t *testing.T) {
	primary, health := &scriptedConcurrency{}, &fakeHealth{} // health down
	c := WithFallback(primary, NewMemoryConcurrency(), health, telemetry.New())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, ok, err := c.Acquire(ctx, "exec", 1, 3); !ok || err != nil {
			t.Fatalf("acquire %d: ok=%v err=%v", i, ok, err)
		}
	}
	if _, ok, _ := c.Acquire(ctx, "exec", 1, 3); ok {
		t.Fatal("the per-task limit of 3 was not enforced while the store is down")
	}
	if primary.grants.Load() != 0 {
		t.Fatal("the store was used while down")
	}
}

func TestFallback_PrimaryErrorFallsBackForThatCall(t *testing.T) {
	primary, health := &scriptedConcurrency{err: errors.New("boom")}, &fakeHealth{}
	health.up.Store(true)
	c := WithFallback(primary, NewMemoryConcurrency(), health, telemetry.New())
	if _, ok, err := c.Acquire(context.Background(), "tail", 1, 2); !ok || err != nil {
		t.Fatalf("a failing store must not refuse the user: ok=%v err=%v", ok, err)
	}
	primary.err = nil
	if _, ok, _ := c.Acquire(context.Background(), "tail", 1, 2); !ok || primary.grants.Load() != 1 {
		t.Fatal("the next call should use the store again")
	}
}

func TestFallback_ReleaseGoesToTheBackendThatGranted(t *testing.T) {
	primary, health := &scriptedConcurrency{}, &fakeHealth{}
	memory := NewMemoryConcurrency()
	c := WithFallback(primary, memory, health, telemetry.New())
	ctx := context.Background()

	releaseMem, ok, _ := c.Acquire(ctx, "exec", 1, 3) // down: memory grants
	if !ok {
		t.Fatal("refused")
	}
	health.up.Store(true) // the store comes back
	releaseKV, ok, _ := c.Acquire(ctx, "exec", 1, 3)
	if !ok {
		t.Fatal("refused")
	}
	releaseMem()
	if n, _ := memory.Count(ctx, "exec", 1); n != 0 {
		t.Fatalf("the memory slot was not released: %d", n)
	}
	if primary.released.Load() != 0 {
		t.Fatal("a memory grant was released against the store")
	}
	releaseKV()
	if primary.released.Load() != 1 {
		t.Fatal("the store grant was not released against the store")
	}
}

func TestFallback_CountSumsBothBackends(t *testing.T) {
	primary, health := &scriptedConcurrency{}, &fakeHealth{}
	c := WithFallback(primary, NewMemoryConcurrency(), health, telemetry.New())
	ctx := context.Background()
	_, _, _ = c.Acquire(ctx, "exec", 1, 3) // memory
	health.up.Store(true)
	_, _, _ = c.Acquire(ctx, "exec", 1, 3) // store
	if n, err := c.Count(ctx, "exec", 1); err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2", n, err)
	}
}

func TestFallback_CountsDecisionsByBackend(t *testing.T) {
	primary, health, reg := &scriptedConcurrency{}, &fakeHealth{}, telemetry.New()
	c := WithFallback(primary, NewMemoryConcurrency(), health, reg)
	ctx := context.Background()
	_, _, _ = c.Acquire(ctx, "exec", 1, 1) // memory, allowed
	_, _, _ = c.Acquire(ctx, "exec", 1, 1) // memory, refused
	health.up.Store(true)
	_, _, _ = c.Acquire(ctx, "tail", 2, 1) // kv, allowed
	out := decisions(t, reg)
	for _, want := range []string{
		`harbormaster_limit_decisions_total{limit="exec",result="allowed",backend="memory"} 1`,
		`harbormaster_limit_decisions_total{limit="exec",result="refused",backend="memory"} 1`,
		`harbormaster_limit_decisions_total{limit="tail",result="allowed",backend="kv"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q:\n%s", want, out)
		}
	}
}

type scriptedRate struct {
	err   error
	calls atomic.Int32
}

func (s *scriptedRate) Allow(context.Context, string, string, int, float64) (bool, error) {
	s.calls.Add(1)
	return true, s.err
}

func (s *scriptedRate) Reserve(context.Context, string, string, time.Duration) (time.Duration, error) {
	s.calls.Add(1)
	return 7 * time.Second, s.err
}

func TestRateFallback_ReserveFallsBack(t *testing.T) {
	primary, health := &scriptedRate{}, &fakeHealth{}
	r := RateWithFallback(primary, NewMemoryRate(time.Now), health, telemetry.New())
	ctx := context.Background()

	wait, err := r.Reserve(ctx, "errorcount", "a|r", time.Second) // down: memory, first slot is immediate
	if err != nil || wait != 0 || primary.calls.Load() != 0 {
		t.Fatalf("down: wait=%v err=%v storeCalls=%d", wait, err, primary.calls.Load())
	}
	health.up.Store(true)
	if wait, _ := r.Reserve(ctx, "errorcount", "a|r", time.Second); wait != 7*time.Second {
		t.Fatalf("up: wait=%v, want the store's answer", wait)
	}
	primary.err = errors.New("boom")
	wait, err = r.Reserve(ctx, "errorcount", "other", time.Second)
	if err != nil || wait != 0 {
		t.Fatalf("a failing store must fall back for the call: wait=%v err=%v", wait, err)
	}
	if ok, err := r.Allow(ctx, "logsearch", "u", 1, 0.01); !ok || err != nil {
		t.Fatalf("allow on a failing store: ok=%v err=%v", ok, err)
	}
}
