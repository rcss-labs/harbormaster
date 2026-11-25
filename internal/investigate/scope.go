package investigate

import (
	"context"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	// The most services a task names, and the longest id of one, that the agent takes.
	maxTaskServices = 20
	maxTaskIDBytes  = 512
)

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
	see, err := agentjob.SightOf(ctx, j.r.Store, j.identity.ID)
	if err != nil {
		return s, err
	}
	taken := map[model.ServiceKey]bool{}
	for _, key := range candidates {
		id := key.ID()
		if taken[key] || len(id) > maxTaskIDBytes || !see.Can(key, authz.View) {
			continue
		}
		taken[key] = true
		s.visible = append(s.visible, key)
		if len(s.subject.ServiceIDs) < maxTaskServices {
			s.subject.ServiceIDs = append(s.subject.ServiceIDs, id)
		}
	}
	return s, see.Err()
}
