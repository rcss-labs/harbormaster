package snapcache

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const propertyRounds = 200

// roundOf runs one random round: new tags, a subject and an optional ?tag= filter, all derived from the seed so a
// failure can be replayed.
type round struct {
	seed      int64
	n         int
	subject   authz.Subject
	tagFilter []authz.TagID
	tags      authz.Tags
	vis       store.Visibility
}

func (r round) label() string {
	return fmt.Sprintf("seed=%d round=%d admin=%v perms=%v filter=%v", r.seed, r.n, r.subject.Admin, r.subject.Perms, r.tagFilter)
}

func eachRound(t *testing.T, f *estateFixture, fn func(r round)) {
	t.Helper()
	const base = 20261121
	for n := 0; n < propertyRounds; n++ {
		seed := int64(base + n)
		rng := rand.New(rand.NewSource(seed))
		f.randomizeTags(t, rng)
		subject := f.randomSubject(rng)
		filter := f.randomTagFilter(rng)
		tags, err := f.st.TagsSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		fn(round{seed: seed, n: n, subject: subject, tagFilter: filter, tags: tags, vis: f.vis(subject, filter)})
	}
}

// roundTrip passes a model through the codec the way the cache does, so the property covers what a hit serves.
func roundTrip[T any](t *testing.T, m T) T {
	t.Helper()
	body, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := Decode(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFilter_ClusterModelEqualsSQLVisibility(t *testing.T) {
	f := newEstateFixture(t)
	ctx := context.Background()
	models := map[model.ClusterKey]ClusterModel{}
	for _, k := range f.clusters {
		m, err := LoadCluster(ctx, f.st, k)
		if err != nil {
			t.Fatalf("LoadCluster(%s): %v", k.ID(), err)
		}
		models[k] = roundTrip(t, m)
	}
	visibleCount := 0
	eachRound(t, f, func(r round) {
		for _, k := range f.clusters {
			label := r.label() + " cluster=" + k.Cluster
			slice := store.TargetFilter{AccountID: k.AccountID, Region: k.Region}
			wantCluster, err := f.st.GetCluster(ctx, k, r.vis)
			got, ok := FilterCluster(models[k], r.subject, r.tags, r.tagFilter)
			if errors.Is(err, store.ErrNotFound) {
				if ok {
					t.Fatalf("%s: the filter shows a cluster that SQL hides", label)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("%s: the filter hides a cluster that SQL shows", label)
			}
			visibleCount++
			sameRows(t, "cluster", label, clearRawClusters([]store.ClusterRow{got.Cluster}), clearRawClusters([]store.ClusterRow{wantCluster}),
				clusterTagSig([]store.ClusterRow{got.Cluster}), clusterTagSig([]store.ClusterRow{wantCluster}))

			wantServices, err := f.st.ListServices(ctx, slice, k.Cluster, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "services", label, clearRawServices(got.Services), clearRawServices(wantServices), serviceTagSig(got.Services), serviceTagSig(wantServices))

			wantTasks, err := f.st.RunningTasksIn(ctx, slice, k.Cluster, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "tasks", label, clearRawTasks(got.Tasks), clearRawTasks(wantTasks), taskTagSig(got.Tasks), taskTagSig(wantTasks))

			unhealthy, err := f.st.UnhealthyTargets(ctx, slice)
			if err != nil {
				t.Fatal(err)
			}
			var wantUnhealthy []model.ServiceKey
			for _, svc := range wantServices {
				if unhealthy[svc.Key] {
					wantUnhealthy = append(wantUnhealthy, svc.Key)
				}
			}
			if g, w := sortedKeys(got.Unhealthy), sortedKeys(wantUnhealthy); jsonOf(t, g) != jsonOf(t, w) {
				t.Fatalf("%s: unhealthy %v, want %v", label, g, w)
			}

			// What the page shows as the last change: the cluster's newest visible event, and each service's first.
			lastByCluster, err := f.st.LastEventAtByCluster(ctx, slice, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			wantAt, wantHas := lastByCluster[k]
			if gotAt, gotHas := got.LastEvent(); gotHas != wantHas || !gotAt.Equal(wantAt) {
				t.Fatalf("%s: last event %v/%v, want %v/%v", label, gotAt, gotHas, wantAt, wantHas)
			}
			assertFirstEvents(t, ctx, f, label, slice, wantServices, FirstEvents(got.Events))
		}
	})
	if visibleCount == 0 {
		t.Fatal("the rounds never made a cluster visible: the property is vacuous")
	}
}

// assertFirstEvents checks each visible service's first event time against what ServiceEvents (the page's query) reads.
func assertFirstEvents(t *testing.T, ctx context.Context, f *estateFixture, label string, slice store.TargetFilter, visible []store.ServiceRow, got map[model.ServiceKey]time.Time) {
	t.Helper()
	for _, svc := range visible {
		events, err := f.st.ServiceEvents(ctx, svc.Key, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		at, has := got[svc.Key]
		if has != (len(events) > 0) {
			t.Fatalf("%s: %s has events %v in the model, want %v", label, svc.Key.ID(), has, len(events) > 0)
		}
		if has && !at.Equal(events[0].CreatedAt) {
			t.Fatalf("%s: %s first event %v, want %v", label, svc.Key.ID(), at, events[0].CreatedAt)
		}
	}
}

func TestFilter_ServiceModelEqualsSQLVisibility(t *testing.T) {
	f := newEstateFixture(t)
	ctx := context.Background()
	models := map[model.ServiceKey]ServiceModel{}
	for _, k := range f.services {
		m, err := LoadService(ctx, f.st, k)
		if err != nil {
			t.Fatalf("LoadService(%s): %v", k.ID(), err)
		}
		models[k] = roundTrip(t, m)
	}
	visibleCount := 0
	eachRound(t, f, func(r round) {
		for _, k := range f.services {
			label := r.label() + " service=" + k.Cluster + "/" + k.Service
			slice := store.TargetFilter{AccountID: k.AccountID, Region: k.Region}
			want, err := f.st.GetServiceView(ctx, k, r.vis)
			got, ok := FilterService(models[k], r.subject, r.tags, r.tagFilter)
			if errors.Is(err, store.ErrNotFound) {
				if ok {
					t.Fatalf("%s: the filter shows a service that SQL hides", label)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("%s: the filter hides a service that SQL shows", label)
			}
			visibleCount++
			if len(got.Service.Raw) == 0 {
				t.Fatalf("%s: the service model lost the raw document the page parses", label)
			}
			sameRows(t, "service", label, got.Service, want, serviceTagSig([]store.ServiceRow{got.Service}), serviceTagSig([]store.ServiceRow{want}))

			wantTasks, _, err := f.st.ListTasks(ctx, store.TaskQuery{TargetFilter: slice, Vis: r.vis, Cluster: k.Cluster, Service: k.Service, Filter: "running"})
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "tasks", label, clearRawTasks(got.Tasks), clearRawTasks(wantTasks), taskTagSig(got.Tasks), taskTagSig(wantTasks))
			unhealthy, err := f.st.UnhealthyTargets(ctx, slice)
			if err != nil {
				t.Fatal(err)
			}
			if got.Unhealthy != unhealthy[k] {
				t.Fatalf("%s: unhealthy %v, want %v", label, got.Unhealthy, unhealthy[k])
			}
		}
	})
	if visibleCount == 0 {
		t.Fatal("the rounds never made a service visible: the property is vacuous")
	}
}

func TestFilter_SliceSummaryEqualsSQLVisibility(t *testing.T) {
	f := newEstateFixture(t)
	ctx := context.Background()
	models := map[store.TargetFilter]SliceSummary{}
	for _, sl := range f.slices() {
		m, err := LoadSlice(ctx, f.st, targetOf(sl))
		if err != nil {
			t.Fatalf("LoadSlice(%v): %v", sl, err)
		}
		models[sl] = roundTrip(t, m)
	}
	nonEmpty := 0
	eachRound(t, f, func(r round) {
		for _, sl := range f.slices() {
			label := r.label() + " slice=" + sl.AccountID + "/" + sl.Region
			got := FilterSlice(models[sl], r.subject, r.tags, r.tagFilter)

			wantClusters, err := f.st.ListClusters(ctx, sl, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "clusters", label, clearRawClusters(got.Clusters), clearRawClusters(wantClusters), clusterTagSig(got.Clusters), clusterTagSig(wantClusters))
			wantServices, err := f.st.ListServices(ctx, sl, "", r.vis)
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "services", label, clearRawServices(got.Services), clearRawServices(wantServices), serviceTagSig(got.Services), serviceTagSig(wantServices))
			wantTasks, err := f.st.RunningTasks(ctx, sl, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			sameRows(t, "tasks", label, clearRawTasks(got.Tasks), clearRawTasks(wantTasks), taskTagSig(got.Tasks), taskTagSig(wantTasks))

			unhealthy, err := f.st.UnhealthyTargets(ctx, sl)
			if err != nil {
				t.Fatal(err)
			}
			var wantUnhealthy []model.ServiceKey
			for _, svc := range wantServices {
				if unhealthy[svc.Key] {
					wantUnhealthy = append(wantUnhealthy, svc.Key)
				}
			}
			if g, w := sortedKeys(got.Unhealthy), sortedKeys(wantUnhealthy); jsonOf(t, g) != jsonOf(t, w) {
				t.Fatalf("%s: unhealthy %v, want %v", label, g, w)
			}

			wantLast, err := f.st.LastEventAtByCluster(ctx, sl, r.vis)
			if err != nil {
				t.Fatal(err)
			}
			gotLast := got.LastEventByCluster()
			if len(gotLast) != len(wantLast) {
				t.Fatalf("%s: last events for %d clusters, want %d", label, len(gotLast), len(wantLast))
			}
			for k, at := range wantLast {
				if g, ok := gotLast[k]; !ok || !g.Equal(at) {
					t.Fatalf("%s: last event of %s = %v/%v, want %v", label, k.ID(), g, ok, at)
				}
			}
			assertFirstEvents(t, ctx, f, label, sl, wantServices, FirstEvents(got.Events))
			if len(got.Services) > 0 {
				nonEmpty++
			}
		}
	})
	if nonEmpty == 0 {
		t.Fatal("the rounds never made a service visible: the property is vacuous")
	}
}

func TestLoadCluster_NotFound(t *testing.T) {
	f := newEstateFixture(t)
	if _, err := LoadCluster(context.Background(), f.st, ck(acctA, "us-east-1", "nope")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if _, err := LoadService(context.Background(), f.st, model.ServiceKey{ClusterKey: f.clusters[0], Service: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}
