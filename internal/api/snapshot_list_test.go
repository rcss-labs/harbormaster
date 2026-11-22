package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// listQueries are the scopes the three list pages are compared under.
func (p *snapPair) listQueries(t *testing.T) []string {
	t.Helper()
	prod := strconv.Itoa(int(p.tagID(t, "Prod")))
	staging := strconv.Itoa(int(p.tagID(t, "Staging")))
	return []string{
		"", "?account=" + homelabAccountID, "?account=" + homelabAccountID + "&region=us-east-1", "?region=eu-west-1",
		"?tag=" + prod, "?tag=" + prod + "&tag=" + staging, "?account=" + homelabAccountID + "&tag=" + staging,
	}
}

func sameListBodies(t *testing.T, p *snapPair, path, counter string) {
	t.Helper()
	for _, user := range []string{"sam", "viewer", "operator", "omer"} {
		on, off := login(t, p.on, user, snapUserPassword), login(t, p.off, user, snapUserPassword)
		for pass := 0; pass < 2; pass++ { // the second pass is served from the cache
			for _, query := range p.listQueries(t) {
				gotStatus, got := body(t, on, path+query)
				wantStatus, want := body(t, off, path+query)
				if gotStatus != http.StatusOK || gotStatus != wantStatus || got != want {
					t.Fatalf("%s pass %d GET %s%s\n cache on  (%d): %.500s\n cache off (%d): %.500s", user, pass, path, query, gotStatus, got, wantStatus, want)
				}
			}
		}
	}
	if p.count(t, "slice", "hit") == 0 {
		t.Fatalf("%s: the cache on server never served a slice from the cache", counter)
	}
}

func TestListClusters_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		sameListBodies(t, p, "/api/clusters", "clusters")
	})
}

func TestOverview_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		sameListBodies(t, p, "/api/overview", "overview")
	})
}

func TestListServicesFlat_SameBodyWithCacheOnAndOff(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		sameListBodies(t, p, "/api/services", "services")
	})
}

// hidingToken is a cache that reports no token for one key, like a slice whose poller is gone or whose key was evicted.
type hidingToken struct {
	snapcache.Cache
	hide snapcache.GenKey
}

func (h hidingToken) Tokens(ctx context.Context, ks ...snapcache.GenKey) ([]string, error) {
	out, err := h.Cache.Tokens(ctx, ks...)
	for i, k := range ks {
		if err == nil && k == h.hide {
			out[i] = ""
		}
	}
	return out, err
}

func TestListClusters_OneSliceDownStillAnswers(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		p.reader.Cache = hidingToken{Cache: p.cache, hide: snapcache.GenKey{AccountID: homelabAccountID, Region: "us-east-1"}}
		sameListBodies(t, p, "/api/clusters", "clusters")
		if p.count(t, "slice", "bypass") == 0 {
			t.Fatal("the slice without a token was not read from Postgres")
		}
	})
}

// A target group with an unhealthy target turns a steady service's status into unhealthy_task, in the lists too.
func TestListPages_UnhealthyTargetReachesStatusesThroughTheCache(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		admin, off := login(t, p.on, "sam", snapUserPassword), login(t, p.off, "sam", snapUserPassword)
		var services ServiceListDTO
		decode(t, off.get("/api/services"), &services)
		var steady *ServiceListItemDTO
		for i, s := range services.Items {
			if s.Status == model.StatusSteady {
				steady = &services.Items[i]
				break
			}
		}
		if steady == nil {
			t.Fatal("the estate has no steady service")
		}
		key, err := model.ParseServiceID(steady.ID)
		if err != nil {
			t.Fatal(err)
		}
		body(t, admin, "/api/services") // warm
		if err := storetest.Seed(p.st).UpsertTargetHealth(t.Context(), []model.TargetHealth{{Key: key, TargetGroupARN: "arn:tg/unhealthy", LoadBalancerARN: "arn:lb",
			Healthy: 1, Unhealthy: 2, Targets: []model.Target{}, PolledAt: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		for _, k := range []snapcache.GenKey{{AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster}, {AccountID: key.AccountID, Region: key.Region}} {
			if err := p.cache.Bump(t.Context(), k, "9-1"); err != nil {
				t.Fatal(err)
			}
		}
		var got ServiceListDTO
		decode(t, admin.get("/api/services"), &got)
		for _, item := range got.Items {
			if item.ID == steady.ID && item.Status != model.StatusUnhealthyTask {
				t.Fatalf("status through the cache = %s, want unhealthy_task", item.Status)
			}
		}
		for _, path := range []string{"/api/services", "/api/clusters", "/api/overview"} {
			if _, a := body(t, admin, path); a == "" {
				t.Fatal("empty body")
			} else if _, b := body(t, off, path); a != b {
				t.Fatalf("GET %s differs after a target health change\n on:  %.300s\n off: %.300s", path, a, b)
			}
		}
	})
}

// Each slice is read from Postgres once per generation: further list requests, by anyone, are served from the store.
func TestListPages_ReadEachSliceOncePerGeneration(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, c kv.Client) {
		p := newSnapPair(t, c)
		admin, viewer := login(t, p.on, "sam", snapUserPassword), login(t, p.on, "viewer", snapUserPassword)
		for _, path := range []string{"/api/clusters", "/api/overview", "/api/services"} {
			if status, _ := body(t, admin, path); status != http.StatusOK {
				t.Fatalf("GET %s = %d", path, status)
			}
		}
		misses := p.count(t, "slice", "miss")
		if misses == 0 {
			t.Fatal("the first list request read no slice through the cache")
		}
		for i := 0; i < 3; i++ {
			for _, path := range []string{"/api/clusters", "/api/overview", "/api/services"} {
				body(t, admin, path)
				body(t, viewer, path)
			}
		}
		if got := p.count(t, "slice", "miss"); got != misses {
			t.Fatalf("slice misses went from %d to %d while nothing changed: a slice was read from Postgres again", misses, got)
		}
		if p.count(t, "slice", "hit") < misses*3 {
			t.Fatalf("only %d slice hits for 18 further list requests over %d slices", p.count(t, "slice", "hit"), misses)
		}
		// A poll that changes nothing leaves the tokens (and the models) alone.
		p.poll(t)
		body(t, admin, "/api/clusters")
		if got := p.count(t, "slice", "miss"); got != misses {
			t.Fatalf("an unchanged poll made the next read a miss (%d -> %d)", misses, got)
		}
	})
}
