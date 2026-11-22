package focus

import (
	"context"
	"testing"
	"time"
)

// fakeTime is a clock the limiter's sleep advances, so a test measures waiting without waiting.
type fakeTime struct{ t time.Time }

func (c *fakeTime) now() time.Time { return c.t }

func newTestLimiter(rate int) (*Limiter, *fakeTime) {
	clock := &fakeTime{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	l := NewLimiter(rate, clock.now)
	l.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.t = clock.t.Add(d)
		return nil
	}
	return l, clock
}

func TestLimiter_HoldsEachCategoryToItsRate(t *testing.T) {
	l, clock := newTestLimiter(5)
	start := clock.now()
	for i := 0; i < 100; i++ {
		if err := l.Wait(context.Background(), ECSServiceRead); err != nil {
			t.Fatal(err)
		}
	}
	// The burst of 10 is free; the other 90 calls refill at 5 per second: 18 s.
	elapsed := clock.now().Sub(start)
	if elapsed < 17*time.Second || elapsed > 19*time.Second {
		t.Fatalf("100 waits at 5/s took %v of fake time, want about 18 s", elapsed)
	}
}

func TestLimiter_BurstIsTwiceTheRate(t *testing.T) {
	l, clock := newTestLimiter(5)
	start := clock.now()
	for i := 0; i < 10; i++ {
		if err := l.Wait(context.Background(), ELB); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := clock.now().Sub(start); elapsed != 0 {
		t.Fatalf("the burst of 10 waited %v, want no wait", elapsed)
	}
}

func TestLimiter_CategoriesAreIndependent(t *testing.T) {
	l, clock := newTestLimiter(5)
	for i := 0; i < 10; i++ { // drain one category
		if err := l.Wait(context.Background(), ECSServiceRead); err != nil {
			t.Fatal(err)
		}
	}
	start := clock.now()
	for _, c := range []Category{ECSClusterResourceRead, ECSClusterRead, ELB} {
		if err := l.Wait(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := clock.now().Sub(start); elapsed != 0 {
		t.Fatalf("other categories waited %v because one was drained", elapsed)
	}
}

func TestLimiter_WaitHonoursContext(t *testing.T) {
	l := NewLimiter(1, time.Now)
	for i := 0; i < 2; i++ { // burst of 2 at rate 1
		if err := l.Wait(context.Background(), ELB); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := l.Wait(ctx, ELB); err == nil {
		t.Fatal("Wait returned nil with an empty bucket and an expiring context")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("Wait outlived its context by %v", time.Since(started))
	}
}
