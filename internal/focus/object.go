// Package focus keeps the set of cluster and service pages that are open in a visible tab, so the poller that owns
// their slice can read just those objects every few seconds. One member stands for an object however many people view
// it.
package focus

import (
	"errors"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Kind is the kind of object a page shows.
type Kind string

const (
	KindCluster Kind = "cluster"
	KindService Kind = "service"
)

// Object is one focusable cluster or service.
type Object struct {
	Kind    Kind
	Cluster model.ClusterKey
	Service string // "" for a cluster
}

const memberSeparator = "|"

// Member is the wire form: <kind>|<account>|<region>|<cluster>[|<service>].
func (o Object) Member() string {
	parts := []string{string(o.Kind), o.Cluster.AccountID, o.Cluster.Region, o.Cluster.Cluster}
	if o.Kind == KindService {
		parts = append(parts, o.Service)
	}
	return strings.Join(parts, memberSeparator)
}

// Valid reports whether the object has the shape of its kind and no name that would break the member form. ECS names
// allow only letters, digits, - and _, so a separator inside a name cannot occur in real data.
func (o Object) Valid() bool {
	fields := []string{o.Cluster.AccountID, o.Cluster.Region, o.Cluster.Cluster}
	switch o.Kind {
	case KindCluster:
		if o.Service != "" {
			return false
		}
	case KindService:
		fields = append(fields, o.Service)
	default:
		return false
	}
	for _, f := range fields {
		if f == "" || strings.Contains(f, memberSeparator) {
			return false
		}
	}
	return true
}

// ParseMember reads the wire form back; it accepts only the two shapes.
func ParseMember(s string) (Object, error) {
	parts := strings.Split(s, memberSeparator)
	var o Object
	switch {
	case len(parts) == 4 && parts[0] == string(KindCluster):
		o = Object{Kind: KindCluster, Cluster: model.ClusterKey{AccountID: parts[1], Region: parts[2], Cluster: parts[3]}}
	case len(parts) == 5 && parts[0] == string(KindService):
		o = Object{Kind: KindService, Cluster: model.ClusterKey{AccountID: parts[1], Region: parts[2], Cluster: parts[3]}, Service: parts[4]}
	default:
		return Object{}, errors.New("focus: malformed member")
	}
	if !o.Valid() {
		return Object{}, errors.New("focus: malformed member")
	}
	return o, nil
}

// Target is the slice that owns the object.
func (o Object) Target() awsx.Target {
	return awsx.Target{AccountID: o.Cluster.AccountID, Region: o.Cluster.Region}
}

// ServiceKey is the object's service; meaningful only for KindService.
func (o Object) ServiceKey() model.ServiceKey {
	return model.ServiceKey{ClusterKey: o.Cluster, Service: o.Service}
}
