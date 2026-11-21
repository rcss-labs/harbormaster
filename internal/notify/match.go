package notify

import (
	"slices"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Scope is a route's entries resolved for matching. Service and tag entries (and an empty scope) take every
// failure alert per alert; cluster and account entries are threshold entries and become Groups, never per-alert
// coverage (D35).
type Scope struct {
	All      bool                      // no entries: every failure, per alert
	Services map[model.ServiceKey]bool // service entries plus the services the tag entries select
	Groups   []GroupSelector           // cluster and account entries, in stored order
}

// GroupSelector is a cluster or account entry: one aggregated notification when at least Threshold of its services
// are failing.
type GroupSelector struct {
	Kind                       string // "cluster" | "account"
	AccountID, Region, Cluster string
	Threshold                  int
}

// Contains reports whether k is inside the entry, compared on the full key: a cluster of the same name in another
// account or region is not contained. An account entry without a region holds every region.
func (g GroupSelector) Contains(k model.ServiceKey) bool {
	if k.AccountID != g.AccountID {
		return false
	}
	if g.Kind == store.NotificationSelectorCluster {
		return k.Region == g.Region && k.Cluster == g.Cluster
	}
	return g.Region == "" || k.Region == g.Region
}

// Key is the group incident's key: the cluster, or the account (with its region when the entry names one).
func (g GroupSelector) Key() model.ServiceKey {
	if g.Kind == store.NotificationSelectorCluster {
		return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: g.AccountID, Region: g.Region, Cluster: g.Cluster}}
	}
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: g.AccountID, Region: g.Region}}
}

// Covers reports whether the scope takes the key per alert: everything under an empty scope, else the services its
// service and tag entries select. An account-level key is covered only by an empty scope.
func (s Scope) Covers(k model.ServiceKey) bool {
	return s.All || s.Services[k]
}

// Matches reports whether route r wants subject s per alert: severity, kind and scope. It ignores r.Fallback and
// r.Enabled; the caller applies those. Cluster and account entries never match here.
func Matches(r store.NotificationRoute, s Subject, scope Scope) bool {
	return passesFilters(r, s) && scope.Covers(s.Key)
}

// passesFilters is the route's severity and kind test.
func passesFilters(r store.NotificationRoute, s Subject) bool {
	switch s.Severity {
	case "critical":
	case "warning":
		if r.MinSeverity != "warning" {
			return false
		}
	default:
		return false
	}
	return slices.Contains(r.Kinds, s.Kind)
}
