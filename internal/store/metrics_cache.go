package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// CachedSeries is one cached metrics payload, keyed by the caller's series key.
type CachedSeries struct {
	Key       string
	Target    awsx.Target
	FetchedAt time.Time
	Payload   json.RawMessage
}

// GetMetricsCache returns the cached rows for keys, keyed by series key. Missing keys are absent.
func (s *Store) GetMetricsCache(ctx context.Context, keys []string) (map[string]CachedSeries, error) {
	found := map[string]CachedSeries{}
	if len(keys) == 0 {
		return found, nil
	}
	rows, err := s.Pool.Query(ctx,
		`select key, account_id, region, fetched_at, payload from metrics_cache where key = any($1)`,
		keys)
	if err != nil {
		return nil, fmt.Errorf("get metrics cache: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var series CachedSeries
		if err := rows.Scan(&series.Key, &series.Target.AccountID, &series.Target.Region,
			&series.FetchedAt, &series.Payload); err != nil {
			return nil, fmt.Errorf("scan metrics cache: %w", err)
		}
		found[series.Key] = series
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get metrics cache: %w", err)
	}
	return found, nil
}

// PutMetricsCache upserts rows in one batch, replacing any existing row with the same key.
func (s *Store) PutMetricsCache(ctx context.Context, rows []CachedSeries) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			`insert into metrics_cache (key, account_id, region, fetched_at, payload)
			 values ($1, $2, $3, $4, $5)
			 on conflict (key) do update set
			   account_id = excluded.account_id, region = excluded.region,
			   fetched_at = excluded.fetched_at, payload = excluded.payload`,
			row.Key, row.Target.AccountID, row.Target.Region, row.FetchedAt, rawOrEmpty(row.Payload))
	}
	if err := s.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("put metrics cache: %w", err)
	}
	return nil
}

// PruneMetricsCache deletes rows fetched more than olderThan ago and returns how many.
func (s *Store) PruneMetricsCache(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from metrics_cache where fetched_at < $1`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("prune metrics cache: %w", err)
	}
	return tag.RowsAffected(), nil
}

// execBatch sends batch and returns the first failing statement's error, if any.
func (s *Store) execBatch(ctx context.Context, batch *pgx.Batch) error {
	results := s.Pool.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return err
		}
	}
	return results.Close()
}
