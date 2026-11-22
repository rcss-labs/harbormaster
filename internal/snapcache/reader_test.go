package snapcache

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// readerRig is a Reader over a real store and a real Valkey or Redis, whose loads are counted.
type readerRig struct {
	f      *estateFixture
	cache  Cache
	reader *Reader
	tel    *telemetry.Registry
	loads  atomic.Int64
	delay  time.Duration
}

func newReaderRig(t *testing.T, c kv.Client) *readerRig {
	t.Helper()
	r := &readerRig{f: newEstateFixture(t), tel: telemetry.New()}
	r.cache = NewKV(c)
	r.reader = &Reader{Cache: r.cache, Store: r.f.st, TTL: time.Minute, Telemetry: r.tel}
	r.reader.loadCluster = func(ctx context.Context, st *store.Store, k model.ClusterKey) (ClusterModel, error) {
		r.loads.Add(1)
		time.Sleep(r.delay)
		return LoadCluster(ctx, st, k)
	}
	r.reader.loadService = func(ctx context.Context, st *store.Store, k model.ServiceKey) (ServiceModel, error) {
		r.loads.Add(1)
		return LoadService(ctx, st, k)
	}
	r.reader.loadSlice = func(ctx context.Context, st *store.Store, tg awsx.Target) (SliceSummary, error) {
		r.loads.Add(1)
		return LoadSlice(ctx, st, tg)
	}
	return r
}

func (r *readerRig) metrics(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.tel.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func (r *readerRig) requireCount(t *testing.T, model, result string, want int) {
	t.Helper()
	line := `harbormaster_snapshot_cache_requests_total{model="` + model + `",result="` + result + `"} `
	out := r.metrics(t)
	i := strings.Index(out, line)
	got := "0"
	if i >= 0 {
		got = strings.TrimSpace(strings.SplitN(out[i+len(line):], "\n", 2)[0])
	}
	if got != strconv.Itoa(want) {
		t.Fatalf("%s/%s count = %s, want %d", model, result, got, want)
	}
}

func (r *readerRig) clusterKey() model.ClusterKey { return r.f.clusters[0] }

func (r *readerRig) genKey() GenKey {
	k := r.clusterKey()
	return GenKey{AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster}
}

func (r *readerRig) bump(t *testing.T, token string) {
	t.Helper()
	if err := r.cache.BumpAll(context.Background(), []GenKey{r.genKey()}, token); err != nil {
		t.Fatal(err)
	}
}

func TestRead_MissLoadsStoresThenHits(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		r.bump(t, "1-100")
		first, err := r.reader.Cluster(context.Background(), r.clusterKey())
		if err != nil {
			t.Fatal(err)
		}
		second, err := r.reader.Cluster(context.Background(), r.clusterKey())
		if err != nil {
			t.Fatal(err)
		}
		if got := r.loads.Load(); got != 1 {
			t.Fatalf("Postgres was read %d times for two reads under one token, want 1", got)
		}
		if jsonOf(t, first) != jsonOf(t, second) || len(second.Services) == 0 {
			t.Fatal("the hit returned a different (or empty) model")
		}
		r.requireCount(t, "cluster", "miss", 1)
		r.requireCount(t, "cluster", "hit", 1)
	})
}

func TestRead_MissingTokenReadsPostgresAndDoesNotCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		for i := 0; i < 2; i++ {
			if _, err := r.reader.Cluster(context.Background(), r.clusterKey()); err != nil {
				t.Fatal(err)
			}
		}
		if got := r.loads.Load(); got != 2 {
			t.Fatalf("Postgres was read %d times for two reads with no token, want 2", got)
		}
		r.requireCount(t, "cluster", "bypass", 2)
		// Nothing was stored: a token that appears now still finds no model under it.
		r.bump(t, "1-200")
		if _, ok, _ := r.cache.Get(context.Background(), ModelCluster, r.clusterKey().ID(), "1-200"); ok {
			t.Fatal("a read without a token stored a model")
		}
	})
}

func TestRead_OldGenerationIsNeverServedAfterABump(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		ctx := context.Background()
		key := r.clusterKey()
		r.bump(t, "1-100")
		old, err := r.reader.Cluster(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		oldBody, err := Encode(old)
		if err != nil {
			t.Fatal(err)
		}
		// The poller commits a change and bumps the token.
		if _, err := r.f.st.UpsertServices(ctx, []model.Service{{Key: model.ServiceKey{ClusterKey: key, Service: "brand-new"}, Status: "ACTIVE", PolledAt: r.f.now}}); err != nil {
			t.Fatal(err)
		}
		r.bump(t, "1-200")
		// A slow web task that read before the commit now stores what it read, under the token it asked for.
		if err := r.cache.Put(ctx, ModelCluster, key.ID(), "1-100", oldBody, time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := r.reader.Cluster(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, svc := range got.Services {
			found = found || svc.Key.Service == "brand-new"
		}
		if !found {
			t.Fatal("the read after a bump returned the model of the old generation")
		}
	})
}

func TestRead_ConcurrentMissesShareOneLoad(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		r.delay = 200 * time.Millisecond
		r.bump(t, "1-100")
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := r.reader.Cluster(context.Background(), r.clusterKey()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if got := r.loads.Load(); got != 1 {
			t.Fatalf("50 concurrent misses made %d Postgres reads, want 1", got)
		}
	})
}

// failingCache is a store that errors on every call.
type failingCache struct{ Cache }

func (failingCache) Tokens(context.Context, ...GenKey) ([]string, error) {
	return nil, errors.New("store down")
}

func (failingCache) Lookup(context.Context, Model, string, GenKey) (string, []byte, bool, error) {
	return "", nil, false, errors.New("store down")
}

func TestRead_StoreDownReadsPostgres(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		r.reader.Cache = failingCache{r.cache}
		got, err := r.reader.Cluster(context.Background(), r.clusterKey())
		if err != nil || len(got.Services) == 0 {
			t.Fatalf("a failing store broke the read: %v / %d services", err, len(got.Services))
		}
		r.requireCount(t, "cluster", "error", 1)

		// A store the health tracker reports as down is not even asked.
		r.reader.Cache = failingCache{r.cache}
		r.reader.Up = func() bool { return false }
		if _, err := r.reader.Cluster(context.Background(), r.clusterKey()); err != nil {
			t.Fatal(err)
		}
		r.requireCount(t, "cluster", "bypass", 1)
	})
}

func TestRead_UndecodableEntryReadsPostgres(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		r.bump(t, "1-100")
		if err := r.cache.Put(context.Background(), ModelCluster, r.clusterKey().ID(), "1-100", []byte("garbage, not gzip"), time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := r.reader.Cluster(context.Background(), r.clusterKey())
		if err != nil || len(got.Services) == 0 {
			t.Fatalf("a garbage entry broke the read: %v / %d services", err, len(got.Services))
		}
		r.requireCount(t, "cluster", "error", 1)
	})
}

// countingTokens counts the token reads that reach the store.
type countingTokens struct {
	Cache
	calls atomic.Int64
	keys  atomic.Int64
}

func (c *countingTokens) Tokens(ctx context.Context, ks ...GenKey) ([]string, error) {
	c.calls.Add(1)
	c.keys.Add(int64(len(ks)))
	return c.Cache.Tokens(ctx, ks...)
}

func TestSlices_OneTokenReadForAllSlicesAndEachLoadedOnce(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		counting := &countingTokens{Cache: r.cache}
		r.reader.Cache = counting
		targets := []awsx.Target{{AccountID: acctA, Region: "us-east-1"}, {AccountID: acctA, Region: "eu-west-1"}, {AccountID: acctB, Region: "us-east-1"}}
		for _, tg := range targets {
			if err := r.cache.BumpAll(context.Background(), []GenKey{{AccountID: tg.AccountID, Region: tg.Region}}, "1-1"); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			got, err := r.reader.Slices(context.Background(), targets)
			if err != nil || len(got) != 3 {
				t.Fatalf("Slices = %d summaries, err %v", len(got), err)
			}
			if len(got[0].Clusters) == 0 || len(got[2].Clusters) == 0 {
				t.Fatal("a summary came back empty")
			}
		}
		if got := counting.calls.Load(); got != 2 {
			t.Fatalf("two Slices calls made %d token reads, want 2 (one each)", got)
		}
		if got := counting.keys.Load(); got != 6 {
			t.Fatalf("token reads carried %d keys, want 6", got)
		}
		if got := r.loads.Load(); got != 3 {
			t.Fatalf("Postgres was read %d times for 3 slices read twice, want 3", got)
		}
		r.requireCount(t, "slice", "miss", 3)
		r.requireCount(t, "slice", "hit", 3)
	})
}

func TestSlices_StoreDownOrTokenErrorReadsEverySliceFromPostgres(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		targets := []awsx.Target{{AccountID: acctA, Region: "us-east-1"}, {AccountID: acctB, Region: "us-east-1"}}
		r.reader.Cache = failingCache{r.cache}
		if got, err := r.reader.Slices(context.Background(), targets); err != nil || len(got) != 2 || len(got[0].Clusters) == 0 {
			t.Fatalf("a failing store broke Slices: %v", err)
		}
		r.requireCount(t, "slice", "error", 2)
		r.reader.Up = func() bool { return false }
		if _, err := r.reader.Slices(context.Background(), targets); err != nil {
			t.Fatal(err)
		}
		r.requireCount(t, "slice", "bypass", 2)
		if got := r.loads.Load(); got != 4 {
			t.Fatalf("Postgres reads = %d, want 4", got)
		}
	})
}

// A model outlives its generation by at most the configured TTL: that bounds how stale a vanished cluster's page can be.
func TestRead_StoredModelExpiresAfterTheConfiguredTTL(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		r.reader.TTL = 1200 * time.Millisecond
		r.bump(t, "1-100")
		if _, err := r.reader.Cluster(context.Background(), r.clusterKey()); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := r.cache.Get(context.Background(), ModelCluster, r.clusterKey().ID(), "1-100"); !ok {
			t.Fatal("the model was not stored")
		}
		time.Sleep(1700 * time.Millisecond)
		if _, ok, _ := r.cache.Get(context.Background(), ModelCluster, r.clusterKey().ID(), "1-100"); ok {
			t.Fatal("the model outlived the TTL")
		}
	})
}

func TestRead_NotFoundIsReturnedAndNotCached(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		gone := ck(acctA, "us-east-1", "vanished")
		if err := r.cache.BumpAll(context.Background(), []GenKey{{AccountID: gone.AccountID, Region: gone.Region, Cluster: gone.Cluster}}, "1-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.reader.Cluster(context.Background(), gone); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want store.ErrNotFound", err)
		}
		if _, ok, _ := r.cache.Get(context.Background(), ModelCluster, gone.ID(), "1-1"); ok {
			t.Fatal("a not-found was stored")
		}
	})
}

func TestRead_ServiceUsesItsClusterTokenAndSliceItsOwn(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		r := newReaderRig(t, c)
		ctx := context.Background()
		svc := r.f.services[0]
		r.bump(t, "1-100") // the cluster's token
		for i := 0; i < 2; i++ {
			if _, err := r.reader.Service(ctx, svc); err != nil {
				t.Fatal(err)
			}
		}
		if got := r.loads.Load(); got != 1 {
			t.Fatalf("service reads under the cluster's token loaded %d times, want 1", got)
		}
		sliceKey := GenKey{AccountID: acctA, Region: "us-east-1"}
		if err := r.cache.BumpAll(ctx, []GenKey{sliceKey}, "1-300"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := r.reader.Slice(ctx, awsx.Target{AccountID: acctA, Region: "us-east-1"}); err != nil {
				t.Fatal(err)
			}
		}
		if got := r.loads.Load(); got != 2 {
			t.Fatalf("slice reads loaded %d times in total, want 2", got)
		}
	})
}
