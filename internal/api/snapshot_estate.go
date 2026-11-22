package api

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// estateInputs is what the list pages read about AWS: the visible clusters and the estate view over their services and
// tasks. They come from the cached per-slice summaries, filtered for this request, when the cache is on, and from
// Postgres otherwise; either way the pages are built from the same rows by the same code.
type estateInputs struct {
	clusters []store.ClusterRow
	view     *estateView

	// When the cache supplied the rows it also supplied the event times, already limited to visible services.
	cached bool
	stamps []store.EventStamp
}

// estateInputs reads the estate the request may see. withClusters is false for the pages that list services only.
func (s *Server) estateInputs(ctx context.Context, sc scope, withClusters bool) (estateInputs, error) {
	if s.Snap != nil {
		return s.cachedEstateInputs(ctx, sc, withClusters)
	}
	var clusters []store.ClusterRow
	if withClusters {
		var err error
		if clusters, err = s.Store.ListClusters(ctx, sc.Filter, sc.Vis); err != nil {
			return estateInputs{}, err
		}
	}
	view, err := s.loadEstate(ctx, sc.Filter, sc.Vis, "", clusters)
	if err != nil {
		return estateInputs{}, err
	}
	return estateInputs{clusters: clusters, view: view}, nil
}

// lastChange is the newest visible event time per cluster.
func (s *Server) lastChange(ctx context.Context, sc scope, in estateInputs) (map[model.ClusterKey]time.Time, error) {
	if in.cached {
		return snapcache.SliceSummary{Events: in.stamps}.LastEventByCluster(), nil
	}
	return s.Store.LastEventAtByCluster(ctx, sc.Filter, sc.Vis)
}

// firstEvents maps each service to the time of its first event, for the services that have one.
func (s *Server) firstEvents(ctx context.Context, sc scope, in estateInputs) (map[model.ServiceKey]time.Time, error) {
	if in.cached {
		return snapcache.FirstEvents(in.stamps), nil
	}
	events, err := s.Store.RecentServiceEventsPerService(ctx, sc.Filter, 1)
	if err != nil {
		return nil, err
	}
	out := make(map[model.ServiceKey]time.Time, len(events))
	for k, list := range events {
		if len(list) > 0 {
			out[k] = list[0].CreatedAt
		}
	}
	return out, nil
}

// targetKeysTTL is how long a task reuses the list of account x region pairs that hold rows. The list changes only when
// the first cluster of a new account or region is polled, so a new one can be missing from the cached list pages for up
// to this long; that staleness is accepted in exchange for dropping a UNION query from every list request.
const targetKeysTTL = 5 * time.Second

func (s *Server) cachedEstateInputs(ctx context.Context, sc scope, withClusters bool) (estateInputs, error) {
	// The slices are every account x region that holds a row, so a slice whose poller is gone still shows what Postgres
	// has, exactly as it does without the cache.
	keys, err := s.targetKeys.get(s.Now(), targetKeysTTL, func() ([]store.TargetFilter, error) { return s.Store.TargetKeys(ctx) })
	if err != nil {
		return estateInputs{}, err
	}
	var targets []awsx.Target
	for _, k := range keys {
		if (sc.Filter.AccountID == "" || sc.Filter.AccountID == k.AccountID) && (sc.Filter.Region == "" || sc.Filter.Region == k.Region) {
			targets = append(targets, awsx.Target{AccountID: k.AccountID, Region: k.Region})
		}
	}
	summaries, err := s.Snap.Slices(ctx, targets)
	if err != nil {
		return estateInputs{}, err
	}
	var (
		clusters []store.ClusterRow
		services []store.ServiceRow
		tasks    []store.TaskRow
		stamps   []store.EventStamp
	)
	unhealthy := map[model.ServiceKey]bool{}
	for _, m := range summaries {
		m = snapcache.FilterSlice(m, sc.Subject, sc.Tags, sc.TagFilter)
		if withClusters {
			clusters = append(clusters, m.Clusters...)
		}
		services = append(services, m.Services...)
		tasks = append(tasks, m.Tasks...)
		stamps = append(stamps, m.Events...)
		for _, k := range m.Unhealthy {
			unhealthy[k] = true
		}
	}
	return estateInputs{clusters: clusters, view: newEstateView(clusters, services, tasks, unhealthy), cached: true, stamps: stamps}, nil
}
