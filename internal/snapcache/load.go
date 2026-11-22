package snapcache

import (
	"context"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// everyone reads as an admin: the loaders return rows for all users, and filters decide per request.
var everyone = store.Visibility{Admin: true}

// The loaders read through the existing store queries and then drop what a model must not carry: the tag fields (the
// store fills them from the database) and the raw AWS documents no page renders.

func stripCluster(r store.ClusterRow) store.ClusterRow {
	r.TagID, r.TagSet, r.Raw = 0, false, nil
	return r
}

func stripService(r store.ServiceRow, keepRaw bool) store.ServiceRow {
	r.EffectiveTagID, r.TagInherited = 0, false
	if !keepRaw {
		r.Raw = nil
	}
	return r
}

func stripTask(r store.TaskRow) store.TaskRow {
	r.EffectiveTagID, r.Raw = 0, nil
	return r
}

func stripServices(rows []store.ServiceRow) []store.ServiceRow {
	out := make([]store.ServiceRow, len(rows))
	for i, r := range rows {
		out[i] = stripService(r, false)
	}
	return out
}

func stripTasks(rows []store.TaskRow) []store.TaskRow {
	out := make([]store.TaskRow, len(rows))
	for i, r := range rows {
		out[i] = stripTask(r)
	}
	return out
}

// unhealthyKeys lists the keys passing keep, in a stable order (the services' order).
func unhealthyKeys(unhealthy map[model.ServiceKey]bool, services []store.ServiceRow) []model.ServiceKey {
	var out []model.ServiceKey
	for _, svc := range services {
		if unhealthy[svc.Key] {
			out = append(out, svc.Key)
		}
	}
	return out
}

// LoadCluster reads the cluster for everyone (admin visibility); store.ErrNotFound when there is no such cluster.
func LoadCluster(ctx context.Context, st *store.Store, k model.ClusterKey) (ClusterModel, error) {
	row, err := st.GetCluster(ctx, k, everyone)
	if err != nil {
		return ClusterModel{}, err
	}
	slice := store.TargetFilter{AccountID: k.AccountID, Region: k.Region}
	services, err := st.ListServices(ctx, slice, k.Cluster, everyone)
	if err != nil {
		return ClusterModel{}, err
	}
	tasks, err := st.RunningTasksIn(ctx, slice, k.Cluster, everyone)
	if err != nil {
		return ClusterModel{}, err
	}
	unhealthy, err := st.UnhealthyTargets(ctx, slice)
	if err != nil {
		return ClusterModel{}, err
	}
	stamps, err := st.ServiceEventStamps(ctx, slice, k.Cluster)
	if err != nil {
		return ClusterModel{}, err
	}
	return ClusterModel{
		Cluster: stripCluster(row), Services: stripServices(services), Tasks: stripTasks(tasks),
		Unhealthy: unhealthyKeys(unhealthy, services), Events: stamps,
	}, nil
}

// LoadService reads the service for everyone; store.ErrNotFound when there is no such service. The service keeps its
// raw document (the page parses it); its tasks keep their containers (the page reads the exec agent) but not theirs.
func LoadService(ctx context.Context, st *store.Store, k model.ServiceKey) (ServiceModel, error) {
	row, err := st.GetServiceView(ctx, k, everyone)
	if err != nil {
		return ServiceModel{}, err
	}
	slice := store.TargetFilter{AccountID: k.AccountID, Region: k.Region}
	tasks, _, err := st.ListTasks(ctx, store.TaskQuery{TargetFilter: slice, Vis: everyone, Cluster: k.Cluster, Service: k.Service, Filter: "running"})
	if err != nil {
		return ServiceModel{}, err
	}
	unhealthy, err := st.UnhealthyTargets(ctx, slice)
	if err != nil {
		return ServiceModel{}, err
	}
	return ServiceModel{Service: stripService(row, true), Tasks: stripTasks(tasks), Unhealthy: unhealthy[k]}, nil
}

// LoadSlice reads one account x region for everyone.
func LoadSlice(ctx context.Context, st *store.Store, t awsx.Target) (SliceSummary, error) {
	slice := store.TargetFilter{AccountID: t.AccountID, Region: t.Region}
	clusters, err := st.ListClusters(ctx, slice, everyone)
	if err != nil {
		return SliceSummary{}, err
	}
	services, err := st.ListServices(ctx, slice, "", everyone)
	if err != nil {
		return SliceSummary{}, err
	}
	tasks, err := st.RunningTasks(ctx, slice, everyone)
	if err != nil {
		return SliceSummary{}, err
	}
	unhealthy, err := st.UnhealthyTargets(ctx, slice)
	if err != nil {
		return SliceSummary{}, err
	}
	stamps, err := st.ServiceEventStamps(ctx, slice, "")
	if err != nil {
		return SliceSummary{}, err
	}
	out := SliceSummary{Services: stripServices(services), Tasks: stripTasks(tasks), Unhealthy: unhealthyKeys(unhealthy, services), Events: stamps}
	out.Clusters = make([]store.ClusterRow, len(clusters))
	for i, c := range clusters {
		out.Clusters[i] = stripCluster(c)
	}
	return out, nil
}
