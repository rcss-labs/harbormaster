package kv_test

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClient_StringsSetNXAndTTL(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx := ctxFor(t)
		key := c.Key("strings", "a")
		if v, err := c.Get(ctx, key); err != nil || v.OK {
			t.Fatalf("missing key: %+v, %v", v, err)
		}
		if stored, err := c.Set(ctx, key, "one", kv.SetOptions{NX: true, TTL: 300 * time.Millisecond}); err != nil || !stored {
			t.Fatalf("first NX set: stored=%v err=%v", stored, err)
		}
		if stored, err := c.Set(ctx, key, "two", kv.SetOptions{NX: true}); err != nil || stored {
			t.Fatalf("second NX set: stored=%v err=%v", stored, err)
		}
		if v, _ := c.Get(ctx, key); !v.OK || v.S != "one" {
			t.Fatalf("value = %+v, want one", v)
		}
		time.Sleep(450 * time.Millisecond)
		if v, _ := c.Get(ctx, key); v.OK {
			t.Fatalf("key should have expired, got %+v", v)
		}
		if stored, err := c.Set(ctx, key, "plain", kv.SetOptions{}); err != nil || !stored {
			t.Fatalf("plain set: stored=%v err=%v", stored, err)
		}
		if err := c.PExpire(ctx, key, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if v, _ := c.Get(ctx, key); v.OK {
			t.Fatal("PExpire did not expire the key")
		}
		if err := c.Del(ctx, key); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClient_MGetReportsMissingKeys(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx := ctxFor(t)
		a, b, missing := c.Key("m", "a"), c.Key("m", "b"), c.Key("m", "none")
		for k, v := range map[string]string{a: "1", b: "2"} {
			if _, err := c.Set(ctx, k, v, kv.SetOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.MGet(ctx, a, missing, b)
		if err != nil {
			t.Fatal(err)
		}
		want := []kv.Value{{S: "1", OK: true}, {}, {S: "2", OK: true}}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if err := c.Del(ctx, a, b); err != nil {
			t.Fatal(err)
		}
		if v, _ := c.Get(ctx, a); v.OK {
			t.Fatal("Del did not remove the key")
		}
	})
}

var zaddScript = kv.NewScript(`return redis.call('ZADD', KEYS[1], ARGV[1], ARGV[2])`)

func TestClient_SortedSetRangeAndTrim(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx := ctxFor(t)
		key := c.Key("z")
		for i, m := range []string{"a", "b", "c", "d"} {
			if _, err := c.Eval(ctx, zaddScript, []string{key}, []string{strconv.Itoa(i + 1), m}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.ZRangeByScore(ctx, key, 2, 3)
		if err != nil || strings.Join(got, ",") != "b,c" {
			t.Fatalf("range 2..3 = %v, %v", got, err)
		}
		inf := math.Inf(1)
		all, _ := c.ZRangeByScore(ctx, key, -inf, inf)
		if strings.Join(all, ",") != "a,b,c,d" {
			t.Fatalf("unbounded range = %v", all)
		}
		if err := c.ZRem(ctx, key, "a", "d", "nope"); err != nil {
			t.Fatal(err)
		}
		rest, _ := c.ZRangeByScore(ctx, key, -inf, inf)
		if strings.Join(rest, ",") != "b,c" {
			t.Fatalf("after trim = %v, want b,c", rest)
		}
	})
}

func TestClient_HashRoundTrip(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx := ctxFor(t)
		key := c.Key("h")
		if _, err := c.Eval(ctx, kv.NewScript(`return redis.call('HSET', KEYS[1], 'x', '1', 'y', 'two')`), []string{key}, nil); err != nil {
			t.Fatal(err)
		}
		got, err := c.HMGet(ctx, key, "y", "nope", "x")
		if err != nil {
			t.Fatal(err)
		}
		if got[0] != (kv.Value{S: "two", OK: true}) || got[1].OK || got[2] != (kv.Value{S: "1", OK: true}) {
			t.Fatalf("HMGet = %+v", got)
		}
	})
}

func TestClient_EvalFallsBackAfterScriptFlush(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	for _, url := range urls {
		url := url
		t.Run(url[strings.LastIndex(url, ":")+1:], func(t *testing.T) {
			c := kvtest.Open(t, url)
			ctx := ctxFor(t)
			script := kv.NewScript(`redis.call('SET', KEYS[1], ARGV[1]); return {redis.call('GET', KEYS[1]), 7}`)
			key := c.Key("eval")
			run := func() {
				t.Helper()
				reply, err := c.Eval(ctx, script, []string{key}, []string{"hello"})
				if err != nil {
					t.Fatalf("Eval: %v", err)
				}
				if _, err := reply.Strings(); err != nil {
					t.Fatalf("Strings: %v", err)
				}
			}
			run() // loads the script
			if _, err := kvtest.OpenAdmin(t, url).Do("SCRIPT", "FLUSH"); err != nil {
				t.Fatal(err)
			}
			run() // EVALSHA hits NOSCRIPT, EVAL must take over
			ints, err := func() ([]int64, error) {
				r, err := c.Eval(ctx, kv.NewScript(`return {1, 2}`), nil, nil)
				if err != nil {
					return nil, err
				}
				return r.Ints()
			}()
			if err != nil || len(ints) != 2 || ints[0] != 1 || ints[1] != 2 {
				t.Fatalf("Ints = %v, %v", ints, err)
			}
		})
	}
}

func TestClient_ServerTimeIsCloseToNow(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		got, err := c.ServerTime(ctxFor(t))
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(got); d < -5*time.Second || d > 5*time.Second {
			t.Fatalf("server time %v is %v from now", got, d)
		}
	})
}

func TestClient_KeysCarryPrefixAndVersion(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		key := c.Key("focus", "a1")
		parts := strings.Split(key, ":")
		if len(parts) != 4 || !strings.HasPrefix(parts[0], "t") || parts[1] != "v1" || parts[2] != "focus" || parts[3] != "a1" {
			t.Fatalf("Key = %q, want <prefix>:v1:focus:a1", key)
		}
	})
}

type inbox struct {
	mu   sync.Mutex
	msgs []string
}

func (i *inbox) add(m string) { i.mu.Lock(); i.msgs = append(i.msgs, m); i.mu.Unlock() }
func (i *inbox) has(m string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, x := range i.msgs {
		if x == m {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSubscribe_DeliversPublishedMessages(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		var box inbox
		channel := c.Key("kick")
		go func() { done <- c.Subscribe(ctx, channel, box.add, func() {}) }()
		// Publish until the subscription is live; the first messages may precede it.
		waitFor(t, "first message", func() bool {
			_ = c.Publish(context.Background(), channel, "hello")
			time.Sleep(30 * time.Millisecond)
			return box.has("hello")
		})
		cancel()
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Fatalf("Subscribe returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Subscribe did not return after ctx ended")
		}
	})
}

func TestSubscribe_ResubscribesAfterDroppedConnection(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	for _, url := range urls {
		url := url
		t.Run(url[strings.LastIndex(url, ":")+1:], func(t *testing.T) {
			c := kvtest.Open(t, url)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var box inbox
			var mu sync.Mutex
			resubs := 0
			channel := c.Key("kick")
			go func() {
				_ = c.Subscribe(ctx, channel, box.add, func() { mu.Lock(); resubs++; mu.Unlock() })
			}()
			waitFor(t, "initial delivery", func() bool {
				_ = c.Publish(context.Background(), channel, "before")
				time.Sleep(30 * time.Millisecond)
				return box.has("before")
			})
			kvtest.DropPubSub(t, url)
			waitFor(t, "onResubscribe", func() bool { mu.Lock(); defer mu.Unlock(); return resubs >= 1 })
			waitFor(t, "delivery after reconnect", func() bool {
				_ = c.Publish(context.Background(), channel, "after")
				time.Sleep(30 * time.Millisecond)
				return box.has("after")
			})
		})
	}
}

func TestOpen_UnreachableServerYieldsFailingClient(t *testing.T) {
	c, err := kv.Open(kv.Options{Address: "127.0.0.1:1", Prefix: "t"})
	if err != nil {
		t.Fatalf("Open must not fail for an unreachable server: %v", err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		if err := c.Ping(ctx); err == nil {
			t.Fatal("Ping against a closed port must fail")
		}
		cancel()
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Fatalf("Ping took %v, want a fast failure", d)
		}
	}
}
