package agentjob

import (
	"context"
	"errors"
	"slices"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxSightLookups bounds the services looked up for one decision; any further one counts as not visible.
const maxSightLookups = 64

// Sight is what the agent identity may read, as of now. It is asked anew for each decision and never kept.
type Sight struct {
	ctx     context.Context
	st      *store.Store
	subject authz.Subject
	tags    authz.Tags
	exists  map[model.ServiceKey]bool
	err     error // why a service could not be looked up; every answer since is no
}

// ReadOnlySubject loads what a user may do, narrowed to reading whatever it holds.
func ReadOnlySubject(ctx context.Context, st *store.Store, userID int64) (authz.Subject, error) {
	subject, err := st.LoadSubject(ctx, userID)
	if err != nil {
		return authz.Subject{}, err
	}
	subject.ReadOnly = true
	return subject, nil
}

// SightOf loads the identity's permissions and the current tags.
func SightOf(ctx context.Context, st *store.Store, identityID int64) (*Sight, error) {
	subject, err := ReadOnlySubject(ctx, st, identityID)
	if err != nil {
		return nil, err
	}
	tags, err := st.TagsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &Sight{ctx: ctx, st: st, subject: subject, tags: tags, exists: map[model.ServiceKey]bool{}}, nil
}

// Can reports whether the service exists and the identity holds the action on its current tag.
func (s *Sight) Can(key model.ServiceKey, action authz.Action) bool {
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

// Err is why a service could not be looked up, if one could not: no answer is to be relied on then.
func (s *Sight) Err() error { return s.err }

// ViewTags are the tags the identity may View, from the permissions that Can answers by, in order.
func (s *Sight) ViewTags() []int64 {
	var out []int64
	for tag := range s.subject.Perms {
		if authz.Can(s.subject, authz.View, tag) {
			out = append(out, int64(tag))
		}
	}
	slices.Sort(out)
	return out
}
