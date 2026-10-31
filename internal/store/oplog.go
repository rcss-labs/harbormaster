package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Operation is one entry in the operation log: a change made by Harbormaster
// or observed as an ECS action. Service nil marks a standalone-task operation.
type Operation struct {
	AccountID, Region, Cluster string
	Service                    *string
	ActorUserID                *int64 // nil = ECS
	Source, Text               string
	Details                    map[string]any
}

// OperationRow is an operation as shown in a history feed. Actor is the
// username, or "ECS" for source ecs. Key is the service, or only the cluster
// (empty Service) for standalone rows.
type OperationRow struct {
	ID      int64
	TS      time.Time
	Source  string
	Actor   string
	Text    string
	Details map[string]any
	Key     model.ServiceKey
}

// operationActorSQL is the actor column for operation_log rows.
const operationActorSQL = `case when o.source = 'ecs' then 'ECS' else coalesce(o.actor_username, '') end`

// AppendOperation records an operation. The actor's username and the service ARN
// are looked up at write time; unknown ones are stored as NULL.
func (s *Store) AppendOperation(ctx context.Context, op Operation) error {
	var details []byte
	if op.Details != nil {
		encoded, err := json.Marshal(op.Details)
		if err != nil {
			return fmt.Errorf("encode operation details: %w", err)
		}
		details = encoded
	}
	_, err := s.Pool.Exec(ctx,
		`insert into operation_log (account_id, region, cluster_name, service_name, service_arn,
		   actor_user_id, actor_username, source, text, details)
		 values ($1, $2, $3, $4,
		   (select sv.arn from services sv where sv.account_id = $1 and sv.region = $2
		      and sv.cluster_name = $3 and sv.service_name = $4),
		   $5,
		   (select u.username from users u where u.id = $5),
		   $6, $7, $8)`,
		op.AccountID, op.Region, op.Cluster, op.Service, op.ActorUserID, op.Source, op.Text, details)
	if err != nil {
		return fmt.Errorf("append operation: %w", err)
	}
	return nil
}

// ListOperations returns a service's operations newest first. With includeECS,
// the service's ECS events are merged in as source ecs. before is a keyset
// cursor (only rows with ts before it); limit 0 means no limit.
func (s *Store) ListOperations(ctx context.Context, k model.ServiceKey, includeECS bool, before *time.Time, limit int) ([]OperationRow, error) {
	var q queryArgs
	account := q.add(k.AccountID)
	region := q.add(k.Region)
	cluster := q.add(k.Cluster)
	service := q.add(k.Service)
	beforeParam := q.add(before)
	limitParam := q.add(limit)

	history := `select o.id, o.ts, o.source, ` + operationActorSQL + ` as actor, o.text, o.details
	  from operation_log o
	  where o.account_id = ` + account + ` and o.region = ` + region + ` and o.cluster_name = ` + cluster +
		` and o.service_name = ` + service
	if includeECS {
		history += ` union all
	  select 0::bigint, e.created_at, 'ecs', 'ECS', coalesce(e.message, ''), null::jsonb
	  from service_events e
	  where e.account_id = ` + account + ` and e.region = ` + region + ` and e.cluster_name = ` + cluster +
			` and e.service_name = ` + service + ` and e.created_at is not null`
	}
	sql := `select hist.id, hist.ts, hist.source, hist.actor, hist.text, hist.details from (` + history + `) hist
	  where (` + beforeParam + `::timestamptz is null or hist.ts < ` + beforeParam + `)
	  order by hist.ts desc, hist.id desc limit nullif(` + limitParam + `, 0)`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	defer rows.Close()
	operations := []OperationRow{}
	for rows.Next() {
		row := OperationRow{Key: k}
		var details []byte
		if err := rows.Scan(&row.ID, &row.TS, &row.Source, &row.Actor, &row.Text, &details); err != nil {
			return nil, fmt.Errorf("scan operation: %w", err)
		}
		if err := unmarshalColumn(details, &row.Details); err != nil {
			return nil, fmt.Errorf("decode operation details: %w", err)
		}
		operations = append(operations, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	return operations, nil
}

// RecentOperations returns the newest Harbormaster operations on visible
// resources; limit 0 means no limit.
func (s *Store) RecentOperations(ctx context.Context, f TargetFilter, vis Visibility, limit int) ([]OperationRow, error) {
	var q queryArgs
	target := q.target("o", f)
	v := q.visibility("o", vis)
	limitParam := q.add(limit)
	sql := `select o.id, o.ts, o.source, ` + operationActorSQL + `, o.text, o.details,
	    o.account_id, o.region, o.cluster_name, coalesce(o.service_name, '')
	  from operation_log o` + v.Joins + `
	  where o.source = 'harbormaster' and ` + target + ` and ` + v.Predicate + `
	  order by o.ts desc, o.id desc limit nullif(` + limitParam + `, 0)`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("recent operations: %w", err)
	}
	defer rows.Close()
	operations := []OperationRow{}
	for rows.Next() {
		var row OperationRow
		var details []byte
		key := &row.Key
		if err := rows.Scan(&row.ID, &row.TS, &row.Source, &row.Actor, &row.Text, &details,
			&key.AccountID, &key.Region, &key.Cluster, &key.Service); err != nil {
			return nil, fmt.Errorf("scan operation: %w", err)
		}
		if err := unmarshalColumn(details, &row.Details); err != nil {
			return nil, fmt.Errorf("decode operation details: %w", err)
		}
		operations = append(operations, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent operations: %w", err)
	}
	return operations, nil
}

// DeleteOperationsBefore removes operation log rows older than t and returns how many.
func (s *Store) DeleteOperationsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from operation_log where ts < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete operations: %w", err)
	}
	return tag.RowsAffected(), nil
}
