package investigate

import (
	"context"
	"errors"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// The most services a task names, and the longest id of one, that the agent takes.
	maxTaskServices = 20
	maxTaskIDBytes  = 512
	// maxSightLookups bounds the services looked up for one decision; any further one counts as not visible.
	maxSightLookups = 64
)

// sight is what the agent identity may read, as of now. It is asked anew for each decision and never kept.
type sight struct {
	ctx     context.Context
	st      *store.Store
	subject authz.Subject
	tags    authz.Tags
	exists  map[model.ServiceKey]bool
	err     error // why a service could not be looked up; every answer since is no
}

// sightOf loads the identity's permissions and the current tags.
func (r *Runner) sightOf(ctx context.Context, identityID int64) (*sight, error) {
	subject, err := r.Store.LoadSubject(ctx, identityID)
	if err != nil {
		return nil, err
	}
	subject.ReadOnly = true
	tags, err := r.Store.TagsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &sight{ctx: ctx, st: r.Store, subject: subject, tags: tags, exists: map[model.ServiceKey]bool{}}, nil
}

// can reports whether the service exists and the identity holds the action on its current tag.
func (s *sight) can(key model.ServiceKey, action authz.Action) bool {
	if s.err != nil || !authz.Can(s.subject, action, authz.EffectiveServiceTag(s.tags, key)) {
		return false
	}
	known, asked := s.exists[key]
	if asked {
		return known
	}
	if len(s.exists) >= maxSightLookups {
		return false
	}
	_, err := s.st.GetService(s.ctx, key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.err = err
		return false
	}
	s.exists[key] = err == nil
	return err == nil
}

// scope is the part of an investigation's subject that the identity can see.
type scope struct {
	subject agentclient.TaskSubject
	visible []model.ServiceKey // every visible service; the subject names the first maxTaskServices of them
}

// scope works out what the identity can see now; it holds no service when there is none.
func (j *job) scope(ctx context.Context) (scope, error) {
	v := j.inv
	cluster := model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: v.ClusterName}
	s := scope{subject: agentclient.TaskSubject{Kind: v.SubjectKind, AccountID: v.AccountID, Region: v.Region}}
	var candidates []model.ServiceKey
	switch v.SubjectKind {
	case "service":
		s.subject.ClusterID = cluster.ID()
		candidates = []model.ServiceKey{{ClusterKey: cluster, Service: v.ServiceName}}
	case "cluster", "account":
		if v.SubjectKind == "cluster" {
			s.subject.ClusterID = cluster.ID()
		}
		for _, m := range v.Members {
			member := model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: m.Cluster}
			candidates = append(candidates, model.ServiceKey{ClusterKey: member, Service: m.Service})
		}
	default:
		return s, nil
	}
	see, err := j.r.sightOf(ctx, j.identity.ID)
	if err != nil {
		return s, err
	}
	taken := map[model.ServiceKey]bool{}
	for _, key := range candidates {
		id := key.ID()
		if taken[key] || len(id) > maxTaskIDBytes || !see.can(key, authz.View) {
			continue
		}
		taken[key] = true
		s.visible = append(s.visible, key)
		if len(s.subject.ServiceIDs) < maxTaskServices {
			s.subject.ServiceIDs = append(s.subject.ServiceIDs, id)
		}
	}
	return s, see.err
}
