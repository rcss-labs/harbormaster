package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The timeline reads are bounded by since and limit and select only the columns the timeline shows, so seven
// days of task stops never load the tasks' raw payloads. None of them filters by visibility: the caller authorizes
// the service first, as for ListOperations. All return newest first.

// TimelineOperations returns a service's Harbormaster operations (not ECS-observed ones) at or after since.
func (s *Store) TimelineOperations(ctx context.Context, k model.ServiceKey, since time.Time, limit int) ([]OperationRow, error) {
	rows, err := s.Pool.Query(ctx,
		`select o.id, o.ts, o.source, `+operationActorSQL+`, o.text, o.details
		 from operation_log o
		 where o.account_id = $1 and o.region = $2 and o.cluster_name = $3 and o.service_name = $4
		   and o.source <> 'ecs' and o.ts >= $5
		 order by o.ts desc, o.id desc limit $6`,
		k.AccountID, k.Region, k.Cluster, k.Service, since, limit)
	if err != nil {
		return nil, fmt.Errorf("timeline operations: %w", err)
	}
	defer rows.Close()
	operations := []OperationRow{}
	for rows.Next() {
		row := OperationRow{Key: k}
		var details []byte
		if err := rows.Scan(&row.ID, &row.TS, &row.Source, &row.Actor, &row.Text, &details); err != nil {
			return nil, fmt.Errorf("scan timeline operation: %w", err)
		}
		if err := unmarshalColumn(details, &row.Details); err != nil {
			return nil, fmt.Errorf("decode timeline operation details: %w", err)
		}
		operations = append(operations, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeline operations: %w", err)
	}
	return operations, nil
}

// TimelineDeployment is a deployment with the username that started it ("" when unattributed) and the
// recorded failure.
type TimelineDeployment struct {
	DeploymentRecord
	StartedBy, FailureReason, FailureDetail string
}

// TimelineDeployments returns a service's deployments started at or after since.
func (s *Store) TimelineDeployments(ctx context.Context, k model.ServiceKey, since time.Time, limit int) ([]TimelineDeployment, error) {
	rows, err := s.Pool.Query(ctx,
		`select d.deployment_id, coalesce(d.task_definition_arn, ''), coalesce(d.revision, 0), d.kind,
		   d.started_at, d.finished_at, coalesce(d.outcome, ''), d.started_by_user_id, coalesce(d.reason, ''),
		   coalesce(u.username, ''), coalesce(d.failure_reason, ''), coalesce(d.failure_detail, '')
		 from deployment_history d
		 left join users u on u.id = d.started_by_user_id
		 where d.account_id = $1 and d.region = $2 and d.cluster_name = $3 and d.service_name = $4
		   and d.started_at >= $5
		 order by d.started_at desc limit $6`,
		k.AccountID, k.Region, k.Cluster, k.Service, since, limit)
	if err != nil {
		return nil, fmt.Errorf("timeline deployments: %w", err)
	}
	defer rows.Close()
	deployments := []TimelineDeployment{}
	for rows.Next() {
		d := TimelineDeployment{DeploymentRecord: DeploymentRecord{Key: k}}
		if err := rows.Scan(&d.DeploymentID, &d.TaskDefinitionARN, &d.Revision, &d.Kind,
			&d.StartedAt, &d.FinishedAt, &d.Outcome, &d.StartedByUserID, &d.Reason,
			&d.StartedBy, &d.FailureReason, &d.FailureDetail); err != nil {
			return nil, fmt.Errorf("scan timeline deployment: %w", err)
		}
		deployments = append(deployments, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeline deployments: %w", err)
	}
	return deployments, nil
}

// TimelineStop is one stopped task. Kind is "error", "expected" or "" (a normal stop).
type TimelineStop struct {
	TaskARN                                           string
	StoppedAt                                         time.Time
	StopCode, StoppedReason, ReasonGroup, Kind, Cause string
	ExitCode                                          *int
	Revision                                          int
}

// TimelineStops returns a service's tasks stopped at or after since.
func (s *Store) TimelineStops(ctx context.Context, k model.ServiceKey, since time.Time, limit int) ([]TimelineStop, error) {
	rows, err := s.Pool.Query(ctx,
		`select t.task_arn, t.stopped_at, coalesce(t.stop_code, ''), coalesce(t.stopped_reason, ''),
		   coalesce(t.stop_reason_group, ''), coalesce(t.stop_kind, ''), coalesce(t.stop_cause, ''),
		   t.exit_code, coalesce(t.revision, 0)
		 from tasks t
		 where t.account_id = $1 and t.region = $2 and t.cluster_name = $3 and t.service_name = $4
		   and t.last_status = 'STOPPED' and t.stopped_at >= $5
		 order by t.stopped_at desc, t.task_arn limit $6`,
		k.AccountID, k.Region, k.Cluster, k.Service, since, limit)
	if err != nil {
		return nil, fmt.Errorf("timeline stops: %w", err)
	}
	defer rows.Close()
	stops := []TimelineStop{}
	for rows.Next() {
		var stop TimelineStop
		if err := rows.Scan(&stop.TaskARN, &stop.StoppedAt, &stop.StopCode, &stop.StoppedReason,
			&stop.ReasonGroup, &stop.Kind, &stop.Cause, &stop.ExitCode, &stop.Revision); err != nil {
			return nil, fmt.Errorf("scan timeline stop: %w", err)
		}
		stops = append(stops, stop)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeline stops: %w", err)
	}
	return stops, nil
}

// FailureMark is one failure on the timeline: an error stop or a failed deployment.
type FailureMark struct {
	At     time.Time
	Reason string
}

// TimelineFailures returns the error stops and failed deployments at or after since. Expected stops (spot
// interruption, deployment replacement) are not failures.
func (s *Store) TimelineFailures(ctx context.Context, k model.ServiceKey, since time.Time, limit int) ([]FailureMark, error) {
	rows, err := s.Pool.Query(ctx,
		`select marks.at, marks.reason from (
		   select t.stopped_at as at, coalesce(t.stop_reason_group, '') as reason
		   from tasks t
		   where t.account_id = $1 and t.region = $2 and t.cluster_name = $3 and t.service_name = $4
		     and t.last_status = 'STOPPED' and t.stop_kind = 'error' and t.stopped_at >= $5
		   union all
		   select d.finished_at, coalesce(d.failure_reason, '')
		   from deployment_history d
		   where d.account_id = $1 and d.region = $2 and d.cluster_name = $3 and d.service_name = $4
		     and d.outcome = 'failed' and d.finished_at >= $5
		 ) marks
		 order by marks.at desc limit $6`,
		k.AccountID, k.Region, k.Cluster, k.Service, since, limit)
	if err != nil {
		return nil, fmt.Errorf("timeline failures: %w", err)
	}
	defer rows.Close()
	marks := []FailureMark{}
	for rows.Next() {
		var mark FailureMark
		if err := rows.Scan(&mark.At, &mark.Reason); err != nil {
			return nil, fmt.Errorf("scan timeline failure: %w", err)
		}
		marks = append(marks, mark)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeline failures: %w", err)
	}
	return marks, nil
}
