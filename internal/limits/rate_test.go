package limits

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// eachRate runs fn against the in-memory implementation (on the real clock) and every configured store.
func eachRate(t *testing.T, fn func(t *testing.T, r Rate)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, NewMemoryRate(time.Now)) })
	for _, u := range kvtest.URLs() {
		u := u
		t.Run("kv-"+kvtest.Label(u), func(t *testing.T) { fn(t, NewKVRate(kvtest.Open(t, u))) })
	}
}

func TestRate_BurstThenRefuse(t *testing.T) {
	eachRate(t, func(t *testing.T, r Rate) {
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if ok, err := r.Allow(ctx, "logsearch", "u1", 3, 0.01); err != nil || !ok {
				t.Fatalf("request %d within capacity: ok=%v err=%v", i+1, ok, err)
			}
		}
		if ok, _ := r.Allow(ctx, "logsearch", "u1", 3, 0.01); ok {
			t.Fatal("request beyond capacity was allowed")
		}
	})
}

func TestRate_RefillsOverTime(t *testing.T) {
	eachRate(t, func(t *testing.T, r Rate) {
		ctx := context.Background()
		for i := 0; i < 2; i++ {
			_, _ = r.Allow(ctx, "notifytest", "u1", 2, 10) // 10 tokens/s: one every 100 ms
		}
		if ok, _ := r.Allow(ctx, "notifytest", "u1", 2, 10); ok {
			t.Fatal("empty bucket allowed a request")
		}
		time.Sleep(250 * time.Millisecond)
		if ok, _ := r.Allow(ctx, "notifytest", "u1", 2, 10); !ok {
			t.Fatal("no token refilled after 250 ms")
		}
	})
}

func TestRate_KeysAndNamesAreIndependent(t *testing.T) {
	eachRate(t, func(t *testing.T, r Rate) {
		ctx := context.Background()
		if ok, _ := r.Allow(ctx, "samllogin", "203.0.113.1", 1, 0.01); !ok {
			t.Fatal("first request refused")
		}
		if ok, _ := r.Allow(ctx, "samllogin", "203.0.113.2", 1, 0.01); !ok {
			t.Fatal("another key must have its own bucket")
		}
		if ok, _ := r.Allow(ctx, "logsearch", "203.0.113.1", 1, 0.01); !ok {
			t.Fatal("another name must have its own bucket")
		}
	})
}

func TestRate_ReserveSpacesCallers(t *testing.T) {
	eachRate(t, func(t *testing.T, r Rate) {
		ctx := context.Background()
		const every = 100 * time.Millisecond
		for i := 0; i < 4; i++ {
			wait, err := r.Reserve(ctx, "errorcount", "acct|eu-west-1", every)
			if err != nil {
				t.Fatal(err)
			}
			want := time.Duration(i) * every
			if wait < want-60*time.Millisecond || wait > want+60*time.Millisecond {
				t.Fatalf("reservation %d waits %v, want about %v", i, wait, want)
			}
		}
	})
}

func TestRate_ReserveDoesNotAccumulateWhenIdle(t *testing.T) {
	eachRate(t, func(t *testing.T, r Rate) {
		ctx := context.Background()
		const every = 50 * time.Millisecond
		_, _ = r.Reserve(ctx, "errorcount", "idle", every)
		time.Sleep(4 * every)
		first, _ := r.Reserve(ctx, "errorcount", "idle", every)
		second, _ := r.Reserve(ctx, "errorcount", "idle", every)
		if first > 30*time.Millisecond {
			t.Fatalf("an idle target made the next caller wait %v", first)
		}
		if second < 20*time.Millisecond || second > 90*time.Millisecond {
			t.Fatalf("the caller after an idle gap waits %v, want about one interval", second)
		}
	})
}

func TestRate_RefillUsesServerTime(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := NewKVRate(c)
		ctx := context.Background()
		_, _ = r.Allow(ctx, "logsearch", "u", 2, 1)
		now, err := c.ServerTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		vals, err := c.HMGet(ctx, c.Key("rate", "logsearch", "u"), "last")
		if err != nil || len(vals) != 1 || !vals[0].OK {
			t.Fatalf("bucket has no last-refill field: %v, %v", vals, err)
		}
		last, err := time.ParseDuration(vals[0].S + "ms")
		if err != nil {
			t.Fatalf("last = %q: %v", vals[0].S, err)
		}
		if d := now.Sub(time.UnixMilli(0).Add(last)); d < -2*time.Second || d > 2*time.Second {
			t.Fatalf("last refill is %v away from the server clock", d)
		}
	})
}

func TestRate_TwoInstancesShareOneBucket(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		a, b := NewKVRate(c), NewKVRate(c)
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if ok, _ := a.Allow(ctx, "logsearch", "u", 6, 0.01); !ok {
				t.Fatal("a refused")
			}
		}
		for i := 0; i < 3; i++ {
			if ok, _ := b.Allow(ctx, "logsearch", "u", 6, 0.01); !ok {
				t.Fatal("b refused")
			}
		}
		if ok, _ := a.Allow(ctx, "logsearch", "u", 6, 0.01); ok {
			t.Fatal("the seventh request was allowed across instances")
		}
	})
}

func TestRate_LongKeysAreHashed(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := NewKVRate(c)
		ctx := context.Background()
		long := strings.Repeat("x", 4000)
		if ok, err := r.Allow(ctx, "samllogin", long, 1, 0.01); err != nil || !ok {
			t.Fatalf("allow: %v, %v", ok, err)
		}
		sum := sha256.Sum256([]byte(long))
		vals, err := c.HMGet(ctx, c.Key("rate", "samllogin", hex.EncodeToString(sum[:])), "tokens")
		if err != nil || len(vals) != 1 || !vals[0].OK {
			t.Fatalf("no bucket under the hashed key: %v, %v", vals, err)
		}
		if ok, _ := r.Allow(ctx, "samllogin", long, 1, 0.01); ok {
			t.Fatal("a hashed key must still refuse past capacity")
		}
	})
}
