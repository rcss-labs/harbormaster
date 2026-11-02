package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// DeploymentFact is one visible deployment_history row for the analytics Delivery tab. Outcome is empty
// while the rollout is still open. TagID is the effective tag; TagInherited is true when the service has
// no tag of its own.
type DeploymentFact struct {
	Key             model.ServiceKey
	DeploymentID    string
	Kind            string
	Revision        int
	StartedAt       time.Time
	FinishedAt      *time.Time
	Outcome         string
	StartedByUserID *int64
	TagID           int64
	TagInherited    bool
}

// AnalyticsDeployments returns the visible deployments started in [from, to), ordered by service and then
// start time so callers can compare each rollout with the previous one of the same service. Rows are tiny
// (about 20 a day), so the aggregation is done by the caller.
func (s *Store) AnalyticsDeployments(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]DeploymentFact, error) {
	var q queryArgs
	target := q.target("d", f)
	v := q.visibility("d", vis)
	fromParam, toParam := q.add(from), q.add(to)
	sql := `select d.account_id, d.region, d.cluster_name, d.service_name, d.deployment_id, d.kind,
	    coalesce(d.revision, 0), d.started_at, d.finished_at, coalesce(d.outcome, ''), d.started_by_user_id,
	    ` + v.Effective + `, ` + v.Inherited + `
	  from deployment_history d` + v.Joins + `
	  where ` + target + ` and ` + v.Predicate + `
	    and d.started_at >= ` + fromParam + ` and d.started_at < ` + toParam + `
	  order by d.account_id, d.region, d.cluster_name, d.service_name, d.started_at, d.deployment_id`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics deployments: %w", err)
	}
	defer rows.Close()
	facts := []DeploymentFact{}
	for rows.Next() {
		var fact DeploymentFact
		key := &fact.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &fact.DeploymentID, &fact.Kind,
			&fact.Revision, &fact.StartedAt, &fact.FinishedAt, &fact.Outcome, &fact.StartedByUserID,
			&fact.TagID, &fact.TagInherited); err != nil {
			return nil, fmt.Errorf("scan analytics deployment: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics deployments: %w", err)
	}
	return facts, nil
}
