package limits

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// eachConcurrency runs fn against the in-memory implementation and every configured store.
func eachConcurrency(t *testing.T, fn func(t *testing.T, c Concurrency)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, NewMemoryConcurrency()) })
	for _, u := range kvtest.URLs() {
		u := u
		t.Run("kv-"+kvtest.Label(u), func(t *testing.T) {
			k := NewKVConcurrency(kvtest.Open(t, u))
			t.Cleanup(k.Close)
			fn(t, k)
		})
	}
}

func TestConcurrency_AcquireUpToMaxThenRefuse(t *testing.T) {
	eachConcurrency(t, func(t *testing.T, c Concurrency) {
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if _, ok, err := c.Acquire(ctx, "exec", 7, 3); err != nil || !ok {
				t.Fatalf("acquire %d: ok=%v err=%v", i, ok, err)
			}
		}
		if _, ok, err := c.Acquire(ctx, "exec", 7, 3); err != nil || ok {
			t.Fatalf("fourth acquire: ok=%v err=%v, want refused", ok, err)
		}
	})
}

func TestConcurrency_ReleaseFreesASlotAndIsIdempotent(t *testing.T) {
	eachConcurrency(t, func(t *testing.T, c Concurrency) {
		ctx := context.Background()
		release, ok, _ := c.Acquire(ctx, "tail", 1, 1)
		if !ok {
			t.Fatal("first acquire refused")
		}
		release()
		release() // must not free a slot twice
		r2, ok, _ := c.Acquire(ctx, "tail", 1, 1)
		if !ok {
			t.Fatal("slot not freed by release")
		}
		release()
		if _, ok, _ := c.Acquire(ctx, "tail", 1, 1); ok {
			t.Fatal("a repeated release freed a slot it did not hold")
		}
		r2()
	})
}

func TestConcurrency_LimitsAndUsersAreIndependent(t *testing.T) {
	eachConcurrency(t, func(t *testing.T, c Concurrency) {
		ctx := context.Background()
		if _, ok, _ := c.Acquire(ctx, "exec", 1, 1); !ok {
			t.Fatal("exec user 1 refused")
		}
		for _, tc := range []struct {
			limit string
			user  int64
		}{{"tail", 1}, {"exec", 2}} {
			if _, ok, _ := c.Acquire(ctx, tc.limit, tc.user, 1); !ok {
				t.Fatalf("%s user %d was refused by an unrelated holder", tc.limit, tc.user)
			}
		}
	})
}

func TestConcurrency_CountReportsHeld(t *testing.T) {
	eachConcurrency(t, func(t *testing.T, c Concurrency) {
		ctx := context.Background()
		r1, _, _ := c.Acquire(ctx, "exec", 5, 3)
		_, _, _ = c.Acquire(ctx, "exec", 5, 3)
		if n, err := c.Count(ctx, "exec", 5); err != nil || n != 2 {
			t.Fatalf("count = %d, %v; want 2", n, err)
		}
		r1()
		if n, _ := c.Count(ctx, "exec", 5); n != 1 {
			t.Fatalf("count after release = %d; want 1", n)
		}
		if n, _ := c.Count(ctx, "exec", 99); n != 0 {
			t.Fatalf("count for an idle user = %d; want 0", n)
		}
	})
}

func TestConcurrency_SelfHealsAfterHolderStopsRefreshing(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		c := NewKVConcurrency(client)
		c.Refresh, c.Expiry = 50*time.Millisecond, 300*time.Millisecond
		ctx := context.Background()
		if _, ok, _ := c.Acquire(ctx, "exec", 1, 1); !ok {
			t.Fatal("first acquire refused")
		}
		if _, ok, _ := c.Acquire(ctx, "exec", 1, 1); ok {
			t.Fatal("second acquire granted while the holder is alive")
		}
		c.Close() // the holder's task dies: no release, no more refreshes
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, ok, _ := NewKVConcurrency(client).Acquire(ctx, "exec", 1, 1); ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the dead holder's slot never expired")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func TestConcurrency_RefreshKeepsALongSessionAlive(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		c := NewKVConcurrency(client)
		c.Refresh, c.Expiry = 50*time.Millisecond, 150*time.Millisecond
		t.Cleanup(c.Close)
		ctx := context.Background()
		release, ok, _ := c.Acquire(ctx, "tail", 3, 1)
		if !ok {
			t.Fatal("acquire refused")
		}
		defer release()
		time.Sleep(500 * time.Millisecond)
		if n, err := c.Count(ctx, "tail", 3); err != nil || n != 1 {
			t.Fatalf("count after 500ms = %d, %v; want the session still held", n, err)
		}
	})
}

func TestConcurrency_UsesServerTime(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		c := NewKVConcurrency(client)
		t.Cleanup(c.Close)
		ctx := context.Background()
		if _, ok, _ := c.Acquire(ctx, "exec", 9, 1); !ok {
			t.Fatal("acquire refused")
		}
		now, err := client.ServerTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// The member's score is its expiry on the server's clock: now + Expiry, give or take the call latency.
		want := float64(now.UnixMilli() + c.Expiry.Milliseconds())
		members, err := client.ZRangeByScore(ctx, client.Key("lim", "exec", "9"), want-2000, want+2000)
		if err != nil || len(members) != 1 {
			t.Fatalf("members scored near server now+expiry = %v, %v; want exactly one", members, err)
		}
	})
}

func TestConcurrency_TwoInstancesShareOneLimit(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		a, b := NewKVConcurrency(client), NewKVConcurrency(client)
		t.Cleanup(a.Close)
		t.Cleanup(b.Close)
		ctx := context.Background()
		for i := 0; i < 2; i++ {
			if _, ok, _ := a.Acquire(ctx, "exec", 4, 3); !ok {
				t.Fatal("a refused")
			}
		}
		if _, ok, _ := b.Acquire(ctx, "exec", 4, 3); !ok {
			t.Fatal("b refused its slot")
		}
		if _, ok, _ := b.Acquire(ctx, "exec", 4, 3); ok {
			t.Fatal("the fourth slot was granted across instances")
		}
	})
}
