package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// UpsertTargetHealth stores each target group's health, replacing the row for the same key.
func (s *Store) UpsertTargetHealth(ctx context.Context, rows []model.TargetHealth) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			`insert into target_health (account_id, region, cluster_name, service_name, target_group_arn,
			   load_balancer_arn, healthy, unhealthy, targets, raw, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 on conflict (account_id, region, cluster_name, service_name, target_group_arn) do update set
			   load_balancer_arn = excluded.load_balancer_arn, healthy = excluded.healthy,
			   unhealthy = excluded.unhealthy, targets = excluded.targets, raw = excluded.raw,
			   polled_at = excluded.polled_at`,
			row.Key.AccountID, row.Key.Region, row.Key.Cluster, row.Key.Service, row.TargetGroupARN,
			row.LoadBalancerARN, row.Healthy, row.Unhealthy, jsonArray(row.Targets), rawOrEmpty(row.Raw), row.PolledAt)
	}
	if err := s.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("upsert target health: %w", err)
	}
	return nil
}

// DeleteTargetHealthNotIn removes the target's rows whose target group ARN is not in keep.
func (s *Store) DeleteTargetHealthNotIn(ctx context.Context, t awsx.Target, keep []string) error {
	if keep == nil {
		keep = []string{} // a nil slice would bind NULL and match nothing
	}
	_, err := s.Pool.Exec(ctx,
		`delete from target_health where account_id = $1 and region = $2 and not (target_group_arn = any($3))`,
		t.AccountID, t.Region, keep)
	if err != nil {
		return fmt.Errorf("delete target health: %w", err)
	}
	return nil
}

// TargetHealthForService returns a service's target groups ordered by target group ARN.
func (s *Store) TargetHealthForService(ctx context.Context, k model.ServiceKey) ([]model.TargetHealth, error) {
	rows, err := s.Pool.Query(ctx,
		targetHealthSelect+` where t.account_id = $1 and t.region = $2 and t.cluster_name = $3 and t.service_name = $4
		 order by t.target_group_arn`,
		k.AccountID, k.Region, k.Cluster, k.Service)
	if err != nil {
		return nil, fmt.Errorf("target health for service: %w", err)
	}
	rowsByService, err := scanTargetHealth(rows)
	if err != nil {
		return nil, err
	}
	result := rowsByService[k]
	if result == nil {
		result = []model.TargetHealth{}
	}
	return result, nil
}

// TargetHealthByService returns the target groups of every service in the target filter, grouped by
// service and ordered by target group ARN, in one query. A non-empty cluster narrows it to that cluster.
func (s *Store) TargetHealthByService(ctx context.Context, f TargetFilter, cluster string) (map[model.ServiceKey][]model.TargetHealth, error) {
	var q queryArgs
	target := q.target("t", f)
	clusterParam := q.add(cluster)
	rows, err := s.Pool.Query(ctx,
		targetHealthSelect+` where `+target+` and (`+clusterParam+` = '' or t.cluster_name = `+clusterParam+`)
		 order by t.target_group_arn`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("target health by service: %w", err)
	}
	return scanTargetHealth(rows)
}

const targetHealthSelect = `select t.account_id, t.region, t.cluster_name, t.service_name, t.target_group_arn,
	   t.load_balancer_arn, t.healthy, t.unhealthy, t.targets, t.raw, t.polled_at
	 from target_health t`

func scanTargetHealth(rows pgx.Rows) (map[model.ServiceKey][]model.TargetHealth, error) {
	defer rows.Close()
	result := map[model.ServiceKey][]model.TargetHealth{}
	for rows.Next() {
		var health model.TargetHealth
		var targetsJSON json.RawMessage
		if err := rows.Scan(&health.Key.AccountID, &health.Key.Region, &health.Key.Cluster, &health.Key.Service,
			&health.TargetGroupARN, &health.LoadBalancerARN, &health.Healthy, &health.Unhealthy,
			&targetsJSON, &health.Raw, &health.PolledAt); err != nil {
			return nil, fmt.Errorf("scan target health: %w", err)
		}
		if err := json.Unmarshal(targetsJSON, &health.Targets); err != nil {
			return nil, fmt.Errorf("decode target health targets: %w", err)
		}
		result[health.Key] = append(result[health.Key], health)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("target health: %w", err)
	}
	return result, nil
}

// UnhealthyTargets returns the services in the target filter with at least one target group
// that has an unhealthy target. It applies no tag visibility; callers filter when they present.
func (s *Store) UnhealthyTargets(ctx context.Context, f TargetFilter) (map[model.ServiceKey]bool, error) {
	var q queryArgs
	rows, err := s.Pool.Query(ctx,
		`select distinct t.account_id, t.region, t.cluster_name, t.service_name
		 from target_health t where t.unhealthy > 0 and `+q.target("t", f), q.values...)
	if err != nil {
		return nil, fmt.Errorf("unhealthy targets: %w", err)
	}
	defer rows.Close()
	unhealthy := map[model.ServiceKey]bool{}
	for rows.Next() {
		var key model.ServiceKey
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service); err != nil {
			return nil, fmt.Errorf("scan unhealthy target: %w", err)
		}
		unhealthy[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unhealthy targets: %w", err)
	}
	return unhealthy, nil
}
