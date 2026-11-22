package poller

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func metricValue(t *testing.T, reg *telemetry.Registry, name string) int {
	t.Helper()
	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			n := 0
			for _, ch := range rest {
				n = n*10 + int(ch-'0')
			}
			return n
		}
	}
	return 0
}

// A bump that fails (the store is down for a moment) must not be forgotten: the next pass over the cluster, even one
// that finds nothing new, bumps again, or readers would keep the old token and its stale model for good.
func TestGeneration_FailedBumpIsRetriedByTheNextCycle(t *testing.T) {
	cache := newRecordingCache()
	reg := telemetry.New()
	h := genHarnessWith(t, cache, func(c *Config) { c.Telemetry = reg })
	h.runOnce(t)
	before := cache.snapshot()

	scalePayments(h, 9)
	cache.mu.Lock()
	cache.failBumps = true
	cache.mu.Unlock()
	h.runOnce(t) // the change is committed; every bump fails
	if moved := changedKeys(before, cache.snapshot()); len(moved) != 0 {
		t.Fatalf("a failing store moved %v: the scenario does not exercise the path", moved)
	}
	if got := metricValue(t, reg, "harbormaster_snapshot_cache_token_write_failures_total"); got == 0 {
		t.Fatal("failed token writes were not counted")
	}

	cache.mu.Lock()
	cache.failBumps = false
	cache.mu.Unlock()
	h.runOnce(t) // nothing new in AWS: only the retry can move the tokens
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, genKeyOf("core-services")) || !hasKey(moved, sliceGenKey) {
		t.Fatalf("the next cycle moved %v, want core-services and the slice (the retry of the failed bump)", moved)
	}
	// And once it succeeded it is not retried again.
	settled := cache.snapshot()
	h.runOnce(t)
	if moved := changedKeys(settled, cache.snapshot()); len(moved) != 0 {
		t.Fatalf("a settled cluster's token kept moving: %v", moved)
	}
}

// slowKV delays every command; delay is set after the harness has done its first cycle.
type slowKV struct {
	kv.Client
	delay atomic.Int64
	cmds  atomic.Int64
}

func (s *slowKV) pause() { s.cmds.Add(1); time.Sleep(time.Duration(s.delay.Load())) }
func (s *slowKV) Set(c context.Context, k, v string, o kv.SetOptions) (bool, error) {
	s.pause()
	return s.Client.Set(c, k, v, o)
}
func (s *slowKV) PExpire(c context.Context, k string, d time.Duration) error {
	s.pause()
	return s.Client.PExpire(c, k, d)
}
func (s *slowKV) Eval(c context.Context, sc *kv.Script, keys, args []string) (kv.Reply, error) {
	s.pause()
	return s.Client.Eval(c, sc, keys, args)
}

// With a store that takes 70 ms per command, a slice-wide bump of the slice key and every cluster key must still write
// all of them inside its 250 ms bound, not stop after the third.
func TestGeneration_SliceWideBumpWritesEveryKeyUnderPerCommandDelay(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, raw kv.Client) {
		slow := &slowKV{Client: raw}
		cache := snapcache.NewKV(slow)
		h := genHarness(t, cache)
		h.runOnce(t)
		names := h.clusterNames(t)
		if len(names) < 3 {
			t.Fatalf("the slice has %d clusters; the test needs several", len(names))
		}
		slow.delay.Store(int64(70 * time.Millisecond))
		slow.cmds.Store(0)
		before, _ := cache.Tokens(context.Background(), append([]snapcache.GenKey{sliceGenKey}, clusterKeys(names)...)...)
		h.loop.markGeneration(context.Background(), "", true)
		after, err := cache.Tokens(context.Background(), append([]snapcache.GenKey{sliceGenKey}, clusterKeys(names)...)...)
		if err != nil {
			t.Fatal(err)
		}
		for i := range after {
			if after[i] == before[i] || after[i] == "" {
				t.Errorf("key %d was not bumped (%q -> %q)", i, before[i], after[i])
			}
			if after[i] != after[0] {
				t.Errorf("key %d has token %q, the others %q", i, after[i], after[0])
			}
		}
		if got := slow.cmds.Load(); got != 1 {
			t.Errorf("a slice-wide bump sent %d commands, want 1", got)
		}
	})
}

func clusterKeys(names []string) []snapcache.GenKey {
	keys := make([]snapcache.GenKey, len(names))
	for i, n := range names {
		keys[i] = genKeyOf(n)
	}
	return keys
}
