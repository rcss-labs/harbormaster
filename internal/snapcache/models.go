package snapcache

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// The models hold what the poller wrote and nothing about who may see it: no tag, no permission, no user. The tag
// fields of the rows are zero here (and are not serialised); a filter fills them per request from the tag snapshot.
// Raw AWS documents are left out except the one the service page parses.

// ClusterModel is one cluster for everyone: the cluster page and the cluster part of the service page.
type ClusterModel struct {
	Cluster   store.ClusterRow
	Services  []store.ServiceRow // of this cluster, ordered by name
	Tasks     []store.TaskRow    // non-stopped tasks of this cluster, standalone included, ordered by ARN
	Unhealthy []model.ServiceKey // services with a target group that has an unhealthy target
	Events    []store.EventStamp // services that have events
}

// ServiceModel is one service for everyone: the service page.
type ServiceModel struct {
	Service store.ServiceRow // Raw kept: the page parses it
	// Tasks are the non-stopped tasks with their containers, in the order the task list reads them.
	Tasks     []store.TaskRow
	Unhealthy bool
}

// SliceSummary is one account x region for the list pages: its clusters, services, running tasks and event times.
type SliceSummary struct {
	Clusters  []store.ClusterRow
	Services  []store.ServiceRow
	Tasks     []store.TaskRow // non-stopped, standalone included, without containers
	Unhealthy []model.ServiceKey
	Events    []store.EventStamp
}

// LastEventByCluster is the newest event time among the services this model holds, per cluster: what the cluster
// list shows as the last change.
func (m SliceSummary) LastEventByCluster() map[model.ClusterKey]time.Time {
	return lastEventByCluster(m.Events)
}

// LastEvent is the newest event time among the services this model holds.
func (m ClusterModel) LastEvent() (time.Time, bool) {
	var newest time.Time
	var ok bool
	for _, st := range m.Events {
		if st.HasMax && (!ok || st.Max.After(newest)) {
			newest, ok = st.Max, true
		}
	}
	return newest, ok
}

// FirstEvents maps each service to the time of its first event in newest-first order.
func FirstEvents(stamps []store.EventStamp) map[model.ServiceKey]time.Time {
	out := make(map[model.ServiceKey]time.Time, len(stamps))
	for _, st := range stamps {
		out[st.Key] = st.First
	}
	return out
}

func lastEventByCluster(stamps []store.EventStamp) map[model.ClusterKey]time.Time {
	out := map[model.ClusterKey]time.Time{}
	for _, st := range stamps {
		if !st.HasMax {
			continue
		}
		if cur, ok := out[st.Key.ClusterKey]; !ok || st.Max.After(cur) {
			out[st.Key.ClusterKey] = st.Max
		}
	}
	return out
}
