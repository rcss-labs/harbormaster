package notify

import (
	"slices"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Scope is a route's selectors resolved to sets. internal/notify's ResolveScope builds it from the database.
type Scope struct {
	All      bool                      // the route has no selectors
	Accounts map[string]bool           // account id, every region
	Targets  map[awsx.Target]bool      // account id + region
	Clusters map[model.ClusterKey]bool // full cluster keys
	Services map[model.ServiceKey]bool // picked services plus the services the tag selectors select
}

// Covers reports whether the scope includes the alert's key. An account-level key has no cluster and no service
// and is covered only by an account selector (for its account or its account and region), never by a cluster,
// service or tag selector. Every comparison is on the full key, so a cluster or service of the same name in
// another account or region is a different target.
func (s Scope) Covers(k model.ServiceKey) bool {
	if s.All {
		return true
	}
	if s.Accounts[k.AccountID] || s.Targets[awsx.Target{AccountID: k.AccountID, Region: k.Region}] {
		return true
	}
	if k.Cluster == "" && k.Service == "" {
		return false
	}
	return s.Clusters[k.ClusterKey] || s.Services[k]
}

// Matches reports whether route r wants subject s: severity, kind and scope. It ignores r.Fallback and r.Enabled;
// the caller applies those.
func Matches(r store.NotificationRoute, s Subject, scope Scope) bool {
	switch s.Severity {
	case "critical":
	case "warning":
		if r.MinSeverity != "warning" {
			return false
		}
	default:
		return false
	}
	return slices.Contains(r.Kinds, s.Kind) && scope.Covers(s.Key)
}
