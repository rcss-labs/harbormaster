package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// DeploymentRecord is one row of a service's deployment history. Outcome is
// empty while the deployment is in progress (stored as NULL).
type DeploymentRecord struct {
	Key               model.ServiceKey
	DeploymentID      string
	TaskDefinitionARN string
	Revision          int
	Kind              string
	StartedAt         time.Time
	FinishedAt        *time.Time
	Outcome           string
	StartedByUserID   *int64
}

// InsertDeploymentIfNew records a deployment unless its ID is already known.
// It reports whether a row was written.
func (s *Store) InsertDeploymentIfNew(ctx context.Context, r DeploymentRecord) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`insert into deployment_history (account_id, region, cluster_name, service_name, deployment_id,
		   task_definition_arn, revision, kind, started_at, finished_at, outcome, started_by_user_id)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), $12)
		 on conflict do nothing`,
		r.Key.AccountID, r.Key.Region, r.Key.Cluster, r.Key.Service, r.DeploymentID,
		r.TaskDefinitionARN, r.Revision, r.Kind, r.StartedAt, r.FinishedAt, r.Outcome, r.StartedByUserID)
	if err != nil {
		return false, fmt.Errorf("insert deployment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListDeployments returns a service's deployments started at or after since, newest first.
func (s *Store) ListDeployments(ctx context.Context, k model.ServiceKey, since time.Time) ([]DeploymentRecord, error) {
	rows, err := s.Pool.Query(ctx,
		`select d.deployment_id, coalesce(d.task_definition_arn, ''), coalesce(d.revision, 0), d.kind,
		   d.started_at, d.finished_at, coalesce(d.outcome, ''), d.started_by_user_id
		 from deployment_history d
		 where d.account_id = $1 and d.region = $2 and d.cluster_name = $3 and d.service_name = $4
		   and d.started_at >= $5
		 order by d.started_at desc`,
		k.AccountID, k.Region, k.Cluster, k.Service, since)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	deployments := []DeploymentRecord{}
	for rows.Next() {
		r := DeploymentRecord{Key: k}
		if err := rows.Scan(&r.DeploymentID, &r.TaskDefinitionARN, &r.Revision, &r.Kind,
			&r.StartedAt, &r.FinishedAt, &r.Outcome, &r.StartedByUserID); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		deployments = append(deployments, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return deployments, nil
}

// DeployDay is one UTC day of deployment activity. Days without deployments are absent.
type DeployDay struct {
	Day                     time.Time
	Deploy, Restart, Failed int
}

// DeploymentsPerDay counts visible deployments started at or after since, grouped
// by UTC day. Deploy and Restart count by kind; Failed counts outcome 'failed' of any kind.
func (s *Store) DeploymentsPerDay(ctx context.Context, f TargetFilter, vis Visibility, since time.Time) ([]DeployDay, error) {
	var q queryArgs
	target := q.target("d", f)
	v := q.visibility("d", vis)
	sinceParam := q.add(since)
	sql := `select date_trunc('day', d.started_at at time zone 'UTC') as day_bucket,
	    count(*) filter (where d.kind = 'deploy'),
	    count(*) filter (where d.kind = 'restart'),
	    count(*) filter (where d.outcome = 'failed')
	  from deployment_history d` + v.Joins + `
	  where ` + target + ` and ` + v.Predicate + ` and d.started_at >= ` + sinceParam + `
	  group by 1 order by 1`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("deployments per day: %w", err)
	}
	defer rows.Close()
	days := []DeployDay{}
	for rows.Next() {
		var day DeployDay
		if err := rows.Scan(&day.Day, &day.Deploy, &day.Restart, &day.Failed); err != nil {
			return nil, fmt.Errorf("scan deployment day: %w", err)
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deployments per day: %w", err)
	}
	return days, nil
}

// DeleteDeploymentsBefore removes deployments started before t and returns how many.
func (s *Store) DeleteDeploymentsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from deployment_history where started_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete deployments: %w", err)
	}
	return tag.RowsAffected(), nil
}
