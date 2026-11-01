package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// StatusRun records how long a service has held its current derived status.
type StatusRun struct {
	Key    model.ServiceKey
	Status model.Status
	Since  time.Time
	Polls  int
}

// StatusRuns returns the status runs of every service in the target, keyed by service.
func (s *Store) StatusRuns(ctx context.Context, t awsx.Target) (map[model.ServiceKey]StatusRun, error) {
	rows, err := s.Pool.Query(ctx,
		`select cluster_name, service_name, status, since, polls
		 from service_status_runs where account_id = $1 and region = $2`,
		t.AccountID, t.Region)
	if err != nil {
		return nil, fmt.Errorf("status runs: %w", err)
	}
	defer rows.Close()
	runs := map[model.ServiceKey]StatusRun{}
	for rows.Next() {
		var run StatusRun
		var status string
		if err := rows.Scan(&run.Key.Cluster, &run.Key.Service, &status, &run.Since, &run.Polls); err != nil {
			return nil, fmt.Errorf("scan status run: %w", err)
		}
		run.Key.AccountID, run.Key.Region = t.AccountID, t.Region
		run.Status = model.Status(status)
		runs[run.Key] = run
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("status runs: %w", err)
	}
	return runs, nil
}

// UpsertStatusRuns stores each run, replacing the row for the same service.
func (s *Store) UpsertStatusRuns(ctx context.Context, runs []StatusRun) error {
	if len(runs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, run := range runs {
		batch.Queue(
			`insert into service_status_runs (account_id, region, cluster_name, service_name, status, since, polls)
			 values ($1, $2, $3, $4, $5, $6, $7)
			 on conflict (account_id, region, cluster_name, service_name) do update set
			   status = excluded.status, since = excluded.since, polls = excluded.polls`,
			run.Key.AccountID, run.Key.Region, run.Key.Cluster, run.Key.Service, string(run.Status), run.Since, run.Polls)
	}
	if err := s.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("upsert status runs: %w", err)
	}
	return nil
}
