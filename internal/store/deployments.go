package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// AttributedDeployment is a deployment started through Harbormaster, recorded
// with its actor. Kind is "deploy" or "restart".
type AttributedDeployment struct {
	Key               model.ServiceKey
	DeploymentID      string
	TaskDefinitionARN string
	Revision          int
	Kind              string
	StartedByUserID   int64
	StartedAt         time.Time
}

// AttributeDeployment records a deployment's kind and actor. If the poller has
// already stored the deployment, only kind and actor are overwritten.
func (s *Store) AttributeDeployment(ctx context.Context, d AttributedDeployment) error {
	_, err := s.Pool.Exec(ctx,
		`insert into deployment_history (account_id, region, cluster_name, service_name, deployment_id,
		   task_definition_arn, revision, kind, started_at, started_by_user_id)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 on conflict (account_id, region, cluster_name, service_name, deployment_id)
		 do update set kind = excluded.kind, started_by_user_id = excluded.started_by_user_id`,
		d.Key.AccountID, d.Key.Region, d.Key.Cluster, d.Key.Service, d.DeploymentID,
		d.TaskDefinitionARN, d.Revision, d.Kind, d.StartedAt, d.StartedByUserID)
	if err != nil {
		return fmt.Errorf("attribute deployment: %w", err)
	}
	return nil
}

// OpenDeployment is a deployment without an outcome yet. ServiceRaw is the
// service's last stored snapshot, or nil when that snapshot is gone.
type OpenDeployment struct {
	Key               model.ServiceKey
	DeploymentID      string
	TaskDefinitionARN string
	Kind              string
	StartedByUserID   *int64
	StartedAt         time.Time
	ServiceRaw        json.RawMessage
}

// OpenDeployments returns every deployment whose outcome is still unknown.
func (s *Store) OpenDeployments(ctx context.Context) ([]OpenDeployment, error) {
	rows, err := s.Pool.Query(ctx,
		`select d.account_id, d.region, d.cluster_name, d.service_name, d.deployment_id,
		   coalesce(d.task_definition_arn, ''), d.kind, d.started_by_user_id, d.started_at, sv.raw
		 from deployment_history d
		 left join services sv on sv.account_id = d.account_id and sv.region = d.region
		   and sv.cluster_name = d.cluster_name and sv.service_name = d.service_name
		 where d.outcome is null
		 order by d.started_at`)
	if err != nil {
		return nil, fmt.Errorf("open deployments: %w", err)
	}
	defer rows.Close()
	open := []OpenDeployment{}
	for rows.Next() {
		var o OpenDeployment
		var raw []byte
		if err := rows.Scan(&o.Key.AccountID, &o.Key.Region, &o.Key.Cluster, &o.Key.Service,
			&o.DeploymentID, &o.TaskDefinitionARN, &o.Kind, &o.StartedByUserID, &o.StartedAt, &raw); err != nil {
			return nil, fmt.Errorf("scan open deployment: %w", err)
		}
		o.ServiceRaw = json.RawMessage(raw)
		open = append(open, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("open deployments: %w", err)
	}
	return open, nil
}

// FinishDeployment sets a deployment's outcome if none is set yet. claimed is
// false when another task finished it first.
func (s *Store) FinishDeployment(ctx context.Context, k model.ServiceKey, deploymentID, outcome string, finishedAt time.Time) (claimed bool, err error) {
	tag, err := s.Pool.Exec(ctx,
		`update deployment_history set outcome = $6, finished_at = $7
		 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4
		   and deployment_id = $5 and outcome is null`,
		k.AccountID, k.Region, k.Cluster, k.Service, deploymentID, outcome, finishedAt)
	if err != nil {
		return false, fmt.Errorf("finish deployment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
