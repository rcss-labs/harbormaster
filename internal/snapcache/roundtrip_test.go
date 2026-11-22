package snapcache

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// roundTripKV counts the commands that reach the store: one per Get, MGet or Eval.
type roundTripKV struct {
	kv.Client
	n atomic.Int64
}

func (c *roundTripKV) Get(ctx context.Context, k string) (kv.Value, error) {
	c.n.Add(1)
	return c.Client.Get(ctx, k)
}

func (c *roundTripKV) MGet(ctx context.Context, ks ...string) ([]kv.Value, error) {
	c.n.Add(1)
	return c.Client.MGet(ctx, ks...)
}

func (c *roundTripKV) Eval(ctx context.Context, s *kv.Script, keys, args []string) (kv.Reply, error) {
	c.n.Add(1)
	return c.Client.Eval(ctx, s, keys, args)
}

func TestRead_AHitIsOneRoundTrip(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		counted := &roundTripKV{Client: c}
		r := newReaderRig(t, counted)
		r.bump(t, "1-100")
		ctx := context.Background()
		if _, err := r.reader.Cluster(ctx, r.clusterKey()); err != nil { // miss: fills the cache
			t.Fatal(err)
		}
		counted.n.Store(0)
		got, err := r.reader.Cluster(ctx, r.clusterKey())
		if err != nil || len(got.Services) == 0 {
			t.Fatalf("hit = %d services, err %v", len(got.Services), err)
		}
		if n := counted.n.Load(); n != 1 {
			t.Fatalf("a cache hit took %d store round trips, want 1", n)
		}
		r.requireCount(t, "cluster", "hit", 1)
	})
}

func TestSlices_HitsTakeAtMostTwoRoundTripsWhateverTheCount(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		counted := &roundTripKV{Client: c}
		r := newReaderRig(t, counted)
		targets := []awsx.Target{{AccountID: acctA, Region: "us-east-1"}, {AccountID: acctA, Region: "eu-west-1"}, {AccountID: acctB, Region: "us-east-1"}}
		for _, tg := range targets {
			if err := r.cache.BumpAll(context.Background(), []GenKey{{AccountID: tg.AccountID, Region: tg.Region}}, "1-1"); err != nil {
				t.Fatal(err)
			}
		}
		ctx := context.Background()
		first, err := r.reader.Slices(ctx, targets) // misses: fills the cache
		if err != nil {
			t.Fatal(err)
		}
		counted.n.Store(0)
		second, err := r.reader.Slices(ctx, targets)
		if err != nil {
			t.Fatal(err)
		}
		if n := counted.n.Load(); n > 2 {
			t.Fatalf("%d cached slices took %d store round trips, want at most 2", len(targets), n)
		}
		if len(second) != len(first) || len(second[0].Clusters) != len(first[0].Clusters) {
			t.Fatal("the cached summaries differ from the loaded ones")
		}
		r.requireCount(t, "slice", "hit", 3)
	})
}
