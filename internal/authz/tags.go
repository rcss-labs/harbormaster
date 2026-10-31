package authz

import "github.com/rcss-labs/harbormaster/internal/model"

// TagID identifies a tag (Dev is the built-in default).
type TagID int64

// Tags resolves explicit tag assignments; implemented by the store.
type Tags interface {
	DevTagID() TagID
	ClusterTag(model.ClusterKey) (TagID, bool) // false = untagged
	ServiceTag(model.ServiceKey) (TagID, bool) // false = inherit
}

// EffectiveClusterTag returns the cluster's tag, or Dev when untagged.
func EffectiveClusterTag(t Tags, k model.ClusterKey) TagID {
	if id, ok := t.ClusterTag(k); ok {
		return id
	}
	return t.DevTagID()
}

// EffectiveServiceTag returns the service's own tag, else its cluster's effective tag.
func EffectiveServiceTag(t Tags, k model.ServiceKey) TagID {
	if id, ok := t.ServiceTag(k); ok {
		return id
	}
	return EffectiveClusterTag(t, k.ClusterKey)
}

// EffectiveTaskTag resolves a task's tag; serviceName "" means a standalone
// task, which uses the cluster tag.
func EffectiveTaskTag(t Tags, cluster model.ClusterKey, serviceName string) TagID {
	if serviceName == "" {
		return EffectiveClusterTag(t, cluster)
	}
	return EffectiveServiceTag(t, model.ServiceKey{ClusterKey: cluster, Service: serviceName})
}
