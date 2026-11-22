package snapcache

import (
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// There is one filter per model and nothing else decides who sees what from the cache. Each applies the rule the SQL
// applies in store/snapshots_read.go (visibilityFor, clusterVisiblePredicate):
//
//   - a service or task is visible when its effective tag (the service's own tag, else its cluster's, else Dev; a
//     standalone task uses its cluster's) is one the subject may view, and an admin sees everything;
//   - a cluster is visible when the subject is an admin or can see at least one of its services (so an empty cluster
//     is hidden from everyone else);
//   - a ?tag= filter narrows the subject to those of its visible tags (an admin to exactly those), and an admin who
//     narrows no longer sees every cluster, only those with a visible service.
//
// The tag fields the rows come without are filled from the tag snapshot. The filters build new slices and never
// change the model they are given: the reader may hand one decoded model to several requests.

// visibility is one request's reading of the rule above.
type visibility struct {
	everything bool                 // an admin without a ?tag= filter
	allowed    map[authz.TagID]bool // when not everything: the tags that may be seen
}

func newVisibility(s authz.Subject, tagFilter []authz.TagID) visibility {
	if len(tagFilter) == 0 {
		if s.Admin {
			return visibility{everything: true}
		}
		allowed := map[authz.TagID]bool{}
		for _, id := range authz.VisibleTags(s) {
			allowed[id] = true
		}
		return visibility{allowed: allowed}
	}
	allowed := map[authz.TagID]bool{}
	for _, id := range tagFilter {
		if authz.Can(s, authz.View, id) { // an admin may view every tag; anyone else only their own
			allowed[id] = true
		}
	}
	return visibility{allowed: allowed}
}

func (v visibility) sees(tag authz.TagID) bool { return v.everything || v.allowed[tag] }

func clusterWithTag(row store.ClusterRow, tags authz.Tags) store.ClusterRow {
	row.TagID, row.TagSet = tags.ClusterTag(row.Key)
	if !row.TagSet {
		row.TagID = tags.DevTagID()
	}
	return row
}

// visibleServices returns copies of the services the request may see, with their tags filled.
func visibleServices(rows []store.ServiceRow, v visibility, tags authz.Tags) []store.ServiceRow {
	out := make([]store.ServiceRow, 0, len(rows))
	for _, row := range rows {
		tag := authz.EffectiveServiceTag(tags, row.Key)
		if !v.sees(tag) {
			continue
		}
		_, own := tags.ServiceTag(row.Key)
		row.EffectiveTagID, row.TagInherited = tag, !own
		out = append(out, row)
	}
	return out
}

func visibleTasks(rows []store.TaskRow, v visibility, tags authz.Tags) []store.TaskRow {
	out := make([]store.TaskRow, 0, len(rows))
	for _, row := range rows {
		tag := authz.EffectiveTaskTag(tags, row.Cluster, row.ServiceName)
		if !v.sees(tag) {
			continue
		}
		row.EffectiveTagID = tag
		out = append(out, row)
	}
	return out
}

// visibleStamps keeps the event stamps of services the request may see, whether or not the service still exists.
func visibleStamps(stamps []store.EventStamp, v visibility, tags authz.Tags) []store.EventStamp {
	out := make([]store.EventStamp, 0, len(stamps))
	for _, st := range stamps {
		if v.sees(authz.EffectiveServiceTag(tags, st.Key)) {
			out = append(out, st)
		}
	}
	return out
}

func keysOf(services []store.ServiceRow) map[model.ServiceKey]bool {
	out := make(map[model.ServiceKey]bool, len(services))
	for _, svc := range services {
		out[svc.Key] = true
	}
	return out
}

func unhealthyAmong(unhealthy []model.ServiceKey, services []store.ServiceRow) []model.ServiceKey {
	keep := keysOf(services)
	out := make([]model.ServiceKey, 0, len(unhealthy))
	for _, k := range unhealthy {
		if keep[k] {
			out = append(out, k)
		}
	}
	return out
}

// clusterVisible is the cluster rule: everything, or at least one visible service.
func clusterVisible(v visibility, visible []store.ServiceRow) bool {
	return v.everything || len(visible) > 0
}

// FilterCluster returns what the subject may see of the cluster, with tag fields filled from the snapshot, or
// ok=false when the cluster is not visible at all (the page's 404).
func FilterCluster(m ClusterModel, s authz.Subject, tags authz.Tags, tagFilter []authz.TagID) (ClusterModel, bool) {
	v := newVisibility(s, tagFilter)
	services := visibleServices(m.Services, v, tags)
	if !clusterVisible(v, services) {
		return ClusterModel{}, false
	}
	return ClusterModel{
		Cluster:   clusterWithTag(m.Cluster, tags),
		Services:  services,
		Tasks:     visibleTasks(m.Tasks, v, tags),
		Unhealthy: unhealthyAmong(m.Unhealthy, services),
		Events:    visibleStamps(m.Events, v, tags),
	}, true
}

// FilterService is FilterCluster for the service page.
func FilterService(m ServiceModel, s authz.Subject, tags authz.Tags, tagFilter []authz.TagID) (ServiceModel, bool) {
	v := newVisibility(s, tagFilter)
	visible := visibleServices([]store.ServiceRow{m.Service}, v, tags)
	if len(visible) == 0 {
		return ServiceModel{}, false
	}
	return ServiceModel{Service: visible[0], Tasks: visibleTasks(m.Tasks, v, tags), Unhealthy: m.Unhealthy}, true
}

// FilterSlice returns the slice's rows the subject may see: the clusters that are visible, the visible services and
// tasks, and the unhealthy flags and event times of visible services.
func FilterSlice(m SliceSummary, s authz.Subject, tags authz.Tags, tagFilter []authz.TagID) SliceSummary {
	v := newVisibility(s, tagFilter)
	services := visibleServices(m.Services, v, tags)
	withVisibleService := map[model.ClusterKey]bool{}
	for _, svc := range services {
		withVisibleService[svc.Key.ClusterKey] = true
	}
	clusters := make([]store.ClusterRow, 0, len(m.Clusters))
	for _, c := range m.Clusters {
		if v.everything || withVisibleService[c.Key] {
			clusters = append(clusters, clusterWithTag(c, tags))
		}
	}
	return SliceSummary{
		Clusters:  clusters,
		Services:  services,
		Tasks:     visibleTasks(m.Tasks, v, tags),
		Unhealthy: unhealthyAmong(m.Unhealthy, services),
		Events:    visibleStamps(m.Events, v, tags),
	}
}
