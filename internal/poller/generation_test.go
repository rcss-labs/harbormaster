package poller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// recordingCache is a snapcache.Cache that keeps tokens in memory and remembers which calls it saw.
type recordingCache struct {
	mu     sync.Mutex
	tokens map[snapcache.GenKey]string
	bumps  []snapcache.GenKey
	touchs []snapcache.GenKey
	// touchCalls counts TouchAll calls, however many keys each carried.
	touchCalls int
	fail       error
	// failBumps makes BumpAll fail while Touch and reads still work.
	failBumps bool
}

func newRecordingCache() *recordingCache {
	return &recordingCache{tokens: map[snapcache.GenKey]string{}}
}

func (c *recordingCache) Bump(ctx context.Context, k snapcache.GenKey, token string) error {
	return c.BumpAll(ctx, []snapcache.GenKey{k}, token)
}

func (c *recordingCache) Touch(ctx context.Context, k snapcache.GenKey, token string) error {
	return c.TouchAll(ctx, []snapcache.GenKey{k}, token)
}

func (c *recordingCache) BumpAll(_ context.Context, ks []snapcache.GenKey, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	if c.failBumps {
		return errors.New("bumps are failing")
	}
	for _, k := range ks {
		c.tokens[k] = token
		c.bumps = append(c.bumps, k)
	}
	return nil
}

func (c *recordingCache) TouchAll(_ context.Context, ks []snapcache.GenKey, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	c.touchCalls++
	for _, k := range ks {
		if _, ok := c.tokens[k]; !ok {
			c.tokens[k] = token
		}
		c.touchs = append(c.touchs, k)
	}
	return nil
}

func (c *recordingCache) Tokens(_ context.Context, ks ...snapcache.GenKey) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = c.tokens[k]
	}
	return out, nil
}

func (c *recordingCache) Get(context.Context, snapcache.Model, string, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (c *recordingCache) Lookup(context.Context, snapcache.Model, string, snapcache.GenKey) (string, []byte, bool, error) {
	return "", nil, false, nil
}

func (c *recordingCache) GetMany(_ context.Context, _ snapcache.Model, ids, _ []string) ([]snapcache.Entry, error) {
	return make([]snapcache.Entry, len(ids)), nil
}

func (c *recordingCache) Put(context.Context, snapcache.Model, string, string, []byte, time.Duration) error {
	return nil
}

func (c *recordingCache) snapshot() map[snapcache.GenKey]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[snapcache.GenKey]string, len(c.tokens))
	for k, v := range c.tokens {
		out[k] = v
	}
	return out
}

func (c *recordingCache) bumpCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bumps)
}

func genKeyOf(cluster string) snapcache.GenKey {
	return snapcache.GenKey{AccountID: loopTarget.AccountID, Region: loopTarget.Region, Cluster: cluster}
}

var sliceGenKey = genKeyOf("")

// genHarness runs the mockup estate's slice through one full cycle with a cache.
func genHarness(t *testing.T, cache snapcache.Cache) *loopHarness {
	t.Helper()
	return genHarnessWith(t, cache, nil)
}

func genHarnessWith(t *testing.T, cache snapcache.Cache, mutate func(*Config)) *loopHarness {
	t.Helper()
	estate := fake.MockupEstate(func() time.Time { return loopNow })
	st := storetest.Open(t)
	f := fake.New(estate)
	cfg := focusConfig()
	cfg.Cache = cache
	if mutate != nil {
		mutate(&cfg)
	}
	p := New(st, f, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := testWriter(t, st, loopTarget)
	return &loopHarness{loop: newLoop(p, loopTarget, w), fake: f, estate: estate, store: st, writer: w}
}

func (h *loopHarness) clusterNames(t *testing.T) []string {
	t.Helper()
	rows, err := h.store.ListClusters(context.Background(), store.TargetFilter{AccountID: loopTarget.AccountID, Region: loopTarget.Region}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Key.Cluster)
	}
	return names
}

func scalePayments(h *loopHarness, desired int32) {
	h.estate.Mu.Lock()
	defer h.estate.Mu.Unlock()
	for i := range h.estate.Services {
		if aws.ToString(h.estate.Services[i].Service.ServiceName) == "payments-api" {
			h.estate.Services[i].Service.DesiredCount = desired
		}
	}
}

func changedKeys(before, after map[snapcache.GenKey]string) []snapcache.GenKey {
	var out []snapcache.GenKey
	for k, v := range after {
		if before[k] != v {
			out = append(out, k)
		}
	}
	return out
}

func hasKey(keys []snapcache.GenKey, want snapcache.GenKey) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestGeneration_FirstCycleSetsTokensForEveryClusterAndTheSlice(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	names := h.clusterNames(t)
	if len(names) < 2 {
		t.Fatalf("the estate has %d clusters in the slice; the test needs several", len(names))
	}
	tokens := cache.snapshot()
	for _, name := range append([]string{""}, names...) {
		if tokens[genKeyOf(name)] == "" {
			t.Errorf("no token for cluster %q after the first cycle", name)
		}
	}
}

func TestGeneration_UnchangedCycleKeepsTheToken(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before, bumped := cache.snapshot(), cache.bumpCount()
	h.runOnce(t)
	after := cache.snapshot()
	if moved := changedKeys(before, after); len(moved) != 0 {
		t.Fatalf("an unchanged cycle changed the tokens of %v", moved)
	}
	if n := cache.bumpCount(); n != bumped {
		t.Fatalf("an unchanged cycle bumped %d tokens", n-bumped)
	}
	if len(cache.touchs) == 0 {
		t.Fatal("an unchanged cycle did not touch the tokens, so they would expire")
	}
}

func TestGeneration_ChangeInOneClusterBumpsOnlyThatClusterAndTheSlice(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	scalePayments(h, 9)
	h.runOnce(t)
	moved := changedKeys(before, cache.snapshot())
	want := map[snapcache.GenKey]bool{genKeyOf("core-services"): true, sliceGenKey: true}
	for _, k := range moved {
		if !want[k] {
			t.Errorf("the token of %+v moved, but only core-services and the slice should have", k)
		}
	}
	for k := range want {
		if !hasKey(moved, k) {
			t.Errorf("the token of %+v did not move", k)
		}
	}
}

func TestGeneration_FocusChangeBumpsTheCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	scalePayments(h, 10)
	if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 1); err != nil {
		t.Fatal(err)
	}
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, genKeyOf("core-services")) || !hasKey(moved, sliceGenKey) || len(moved) != 2 {
		t.Fatalf("a focus change moved %v, want core-services and the slice", moved)
	}
	// A focus cycle that finds nothing new leaves the tokens alone.
	again := cache.snapshot()
	if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 1); err != nil {
		t.Fatal(err)
	}
	if moved := changedKeys(again, cache.snapshot()); len(moved) != 0 {
		t.Fatalf("an unchanged focus cycle moved %v", moved)
	}
}

func TestGeneration_TargetHealthChangeBumpsTheSlice(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	extra := "arn:aws:elasticloadbalancing:us-east-1:" + sandboxAccount + ":targetgroup/payments-api-extra/0123456789abcdef"
	h.estate.AddTargetGroup(paymentsAPI(), fake.TargetGroup{ARN: extra, Unhealthy: 1})
	h.runOnce(t)
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, sliceGenKey) {
		t.Fatalf("a new unhealthy target group did not move the slice token (moved %v)", moved)
	}
	for _, name := range h.clusterNames(t) {
		if !hasKey(moved, genKeyOf(name)) {
			t.Errorf("a target health change did not move the token of cluster %s", name)
		}
	}
}

func TestGeneration_TokenCarriesTheLeaseEpoch(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	prefix := fmt.Sprintf("%d-", h.writer.Lease().Epoch)
	if h.writer.Lease().Epoch == 0 {
		t.Fatal("the test lease has epoch 0: the prefix check proves nothing")
	}
	for k, token := range cache.snapshot() {
		if !strings.HasPrefix(token, prefix) {
			t.Errorf("token %q of %+v does not start with the lease epoch %q", token, k, prefix)
		}
	}
}

func TestGeneration_TwoChangesInOneMillisecondGetDifferentTokens(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		h.loop.markGeneration(context.Background(), "core-services", true)
		token := cache.snapshot()[genKeyOf("core-services")]
		if seen[token] {
			t.Fatalf("token %q was issued twice: a model stored under it between the changes would be served stale", token)
		}
		seen[token] = true
	}
}

func TestGeneration_StoreErrorDoesNotFailTheCycle(t *testing.T) {
	cache := newRecordingCache()
	cache.fail = errors.New("store down")
	h := genHarness(t, cache)
	h.runOnce(t) // runOnce fails the test on any tier error
	if got := h.count(t, `select count(*) from services`); got == 0 {
		t.Fatal("the cycle wrote nothing")
	}
	if _, err := h.loop.focusService(context.Background(), paymentsAPI(), 1); err != nil {
		t.Fatalf("a focus cycle failed because the store was down: %v", err)
	}
}

func TestGeneration_NilCacheDoesNothing(t *testing.T) {
	h := genHarness(t, nil)
	h.runOnce(t)
	h.loop.markGeneration(context.Background(), "", true)
}

func TestGeneration_WritesThroughARealStore(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		cache := snapcache.NewKV(c)
		h := genHarness(t, cache)
		h.runOnce(t)
		keys := []snapcache.GenKey{sliceGenKey}
		for _, name := range h.clusterNames(t) {
			keys = append(keys, genKeyOf(name))
		}
		tokens, err := cache.Tokens(context.Background(), keys...)
		if err != nil {
			t.Fatal(err)
		}
		for i, token := range tokens {
			if token == "" {
				t.Errorf("no token in the store for %+v", keys[i])
			}
		}
		before := tokens
		scalePayments(h, 7)
		h.runOnce(t)
		after, _ := cache.Tokens(context.Background(), keys...)
		moved := 0
		for i := range after {
			if after[i] != before[i] {
				moved++
			}
		}
		if moved != 2 {
			t.Fatalf("%d tokens moved after one cluster changed, want 2 (the cluster and the slice)", moved)
		}
	})
}

func TestTargetHealth_ChangeIsDetectedWithoutChangingTheSQL(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	var polledBefore time.Time
	if err := h.store.Pool.QueryRow(context.Background(), `select max(polled_at) from target_health`).Scan(&polledBefore); err != nil {
		t.Fatal(err)
	}
	// Same health, later: polled_at keeps advancing (the target-health endpoint reports it as the time it was read).
	h.runOnce(t)
	var polledAfter time.Time
	if err := h.store.Pool.QueryRow(context.Background(), `select max(polled_at) from target_health`).Scan(&polledAfter); err != nil {
		t.Fatal(err)
	}
	if !polledAfter.After(polledBefore) {
		t.Fatalf("target_health.polled_at did not advance: %v -> %v", polledBefore, polledAfter)
	}
}
