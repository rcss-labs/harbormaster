package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// These tests need HM_TEST_DATABASE_URL; storetest.Open skips without it.

func clientWithStore(c awsx.Clients, s *store.Store, now *time.Time) *Client {
	client := New(c, s, 60*time.Second)
	client.Now = func() time.Time { return *now }
	return client
}

func TestFetch_ServesFromCacheWithinTTL(t *testing.T) {
	s := storetest.Open(t)
	f := newFakeClients()
	now := testNow
	client := clientWithStore(f, s, &now)
	ctx := context.Background()
	first, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	now = testNow.Add(30 * time.Second)
	second, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()["GetMetricData"]; got != 1 {
		t.Fatalf("calls after +30s = %d, want 1", got)
	}
	if second.Stale || !second.FetchedAt.Equal(first.FetchedAt) {
		t.Errorf("stale=%v fetchedAt=%v want %v", second.Stale, second.FetchedAt, first.FetchedAt)
	}
	// The grid has not moved a whole minute yet: values are identical.
	for i := range first.Series[0].Values {
		a, b := first.Series[0].Values[i], second.Series[0].Values[i]
		if (a == nil) != (b == nil) || (a != nil && *a != *b) {
			t.Fatalf("bucket %d differs: %v vs %v", i, a, b)
		}
	}
	now = testNow.Add(61 * time.Second)
	if _, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()["GetMetricData"]; got != 2 {
		t.Fatalf("calls after +61s = %d, want 2", got)
	}
}

func TestFetch_CacheIsSharedAcrossClients(t *testing.T) {
	s := storetest.Open(t)
	fa, fb := newFakeClients(), newFakeClients()
	now := testNow
	a, b := clientWithStore(fa, s, &now), clientWithStore(fb, s, &now)
	if _, err := a.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("cpu")}); err != nil {
		t.Fatal(err)
	}
	res, err := b.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("other_id")})
	if err != nil {
		t.Fatal(err)
	}
	if got := fb.Calls()["GetMetricData"]; got != 0 {
		t.Errorf("client B made %d CloudWatch calls, want 0", got)
	}
	if res.Series[0].ID != "other_id" || res.Series[0].Values[0] == nil {
		t.Errorf("series = %+v", res.Series[0])
	}
}

func TestFetch_StaleOnThrottleWithCache(t *testing.T) {
	s := storetest.Open(t)
	f := newFakeClients()
	now := testNow
	client := clientWithStore(f, s, &now)
	ctx := context.Background()
	warm, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	now = testNow.Add(5 * time.Minute)
	f.ThrottleCloudWatch = 1
	res, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stale || res.StaleReason != "CloudWatch is throttling requests" {
		t.Fatalf("stale=%v reason=%q", res.Stale, res.StaleReason)
	}
	if !res.FetchedAt.Equal(warm.FetchedAt) {
		t.Errorf("FetchedAt = %v, want the cached %v", res.FetchedAt, warm.FetchedAt)
	}
	// Re-aligned to the new grid (5 buckets later): cached bucket i+5 is now bucket i.
	n := len(res.Timestamps)
	if n != len(warm.Timestamps) {
		t.Fatalf("grid length %d != %d", n, len(warm.Timestamps))
	}
	for i := 0; i < n-5; i++ {
		got, want := res.Series[0].Values[i], warm.Series[0].Values[i+5]
		if got == nil || want == nil || *got != *want {
			t.Fatalf("bucket %d = %v, want %v", i, got, want)
		}
	}
	for i := n - 5; i < n; i++ {
		if res.Series[0].Values[i] != nil {
			t.Errorf("bucket %d should be nil (newer than the cache)", i)
		}
	}
}

func TestFetch_ErrorWhenCacheOlderThanMaxStale(t *testing.T) {
	s := storetest.Open(t)
	f := newFakeClients()
	now := testNow
	client := clientWithStore(f, s, &now)
	ctx := context.Background()
	if _, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")}); err != nil {
		t.Fatal(err)
	}
	now = testNow.Add(11 * time.Minute)
	f.ThrottleCloudWatch = 1
	if _, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")}); !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle error", err)
	}
}

func TestFetch_StaleRequiresEveryUncachedQueryCached(t *testing.T) {
	s := storetest.Open(t)
	f := newFakeClients()
	now := testNow
	client := clientWithStore(f, s, &now)
	ctx := context.Background()
	if _, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu")}); err != nil {
		t.Fatal(err)
	}
	now = testNow.Add(5 * time.Minute)
	f.ThrottleCloudWatch = 1
	mem := cpuQuery("mem")
	mem.MetricName = "MemoryUtilized"
	if _, err := client.Fetch(ctx, homelab, Span1h, []Query{cpuQuery("cpu"), mem}); !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle error (mem has no cache row)", err)
	}
}
