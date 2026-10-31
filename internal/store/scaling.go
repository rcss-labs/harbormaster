package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// UpsertScaling stores each service's scaling state, replacing the row for the same key.
func (s *Store) UpsertScaling(ctx context.Context, rows []model.Scaling) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			`insert into scaling (account_id, region, cluster_name, service_name, has_target, min_capacity,
			   max_capacity, policies, scheduled_actions, activities, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 on conflict (account_id, region, cluster_name, service_name) do update set
			   has_target = excluded.has_target, min_capacity = excluded.min_capacity,
			   max_capacity = excluded.max_capacity, policies = excluded.policies,
			   scheduled_actions = excluded.scheduled_actions, activities = excluded.activities,
			   polled_at = excluded.polled_at`,
			row.Key.AccountID, row.Key.Region, row.Key.Cluster, row.Key.Service, row.HasTarget,
			row.Min, row.Max, jsonArray(row.Policies), jsonArray(row.Scheduled), jsonArray(row.Activities),
			row.PolledAt)
	}
	if err := s.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("upsert scaling: %w", err)
	}
	return nil
}

// ScalingForService returns a service's scaling state, or ErrNotFound when it has never been polled.
func (s *Store) ScalingForService(ctx context.Context, k model.ServiceKey) (model.Scaling, error) {
	scaling := model.Scaling{Key: k}
	var policiesJSON, scheduledJSON, activitiesJSON []byte
	err := s.Pool.QueryRow(ctx,
		`select has_target, coalesce(min_capacity, 0), coalesce(max_capacity, 0), policies,
		   scheduled_actions, activities, polled_at
		 from scaling
		 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		k.AccountID, k.Region, k.Cluster, k.Service).
		Scan(&scaling.HasTarget, &scaling.Min, &scaling.Max, &policiesJSON, &scheduledJSON,
			&activitiesJSON, &scaling.PolledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Scaling{}, ErrNotFound
	}
	if err != nil {
		return model.Scaling{}, fmt.Errorf("scaling for service: %w", err)
	}
	if err := json.Unmarshal(policiesJSON, &scaling.Policies); err != nil {
		return model.Scaling{}, fmt.Errorf("decode scaling policies: %w", err)
	}
	if err := json.Unmarshal(scheduledJSON, &scaling.Scheduled); err != nil {
		return model.Scaling{}, fmt.Errorf("decode scheduled actions: %w", err)
	}
	if err := json.Unmarshal(activitiesJSON, &scaling.Activities); err != nil {
		return model.Scaling{}, fmt.Errorf("decode scaling activities: %w", err)
	}
	return scaling, nil
}
