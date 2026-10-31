package authz

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// VisibleTags returns, in ascending order, the tags on which s has View.
// Admins see everything; callers must check Subject.Admin first.
func VisibleTags(s Subject) []TagID {
	var ids []TagID
	for id, set := range s.Perms {
		if set.Has(View) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// CanSeeCluster reports whether s may see a cluster: admins always; others
// only when at least one of its services is visible (so an empty cluster is hidden).
func CanSeeCluster(s Subject, t Tags, k model.ClusterKey, serviceNames []string) bool {
	if s.Admin {
		return true
	}
	for _, name := range serviceNames {
		tag := EffectiveServiceTag(t, model.ServiceKey{ClusterKey: k, Service: name})
		if Can(s, View, tag) {
			return true
		}
	}
	return false
}
