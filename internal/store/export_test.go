package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// TxForTest runs fn inside a fenced write transaction of the writer, the way a poller write does, so a test can keep
// the transaction open (a frozen or partitioned owner) or make it overlap with others. exec runs one statement in it.
func (w *SliceWriter) TxForTest(ctx context.Context, fn func(exec func(sql string, args ...any) error) error) error {
	_, err := fencedDo(ctx, w, func(q db) (struct{}, error) {
		return struct{}{}, fn(func(sql string, args ...any) error {
			_, err := q.Exec(ctx, sql, args...)
			return err
		})
	})
	return err
}

// SetFenceTimeoutsForTest shortens the server-side timeouts of a fenced write transaction and returns the restore.
func SetFenceTimeoutsForTest(idle, statement time.Duration) (restore func()) {
	oldIdle, oldStatement := fenceIdleTimeout, fenceStatementTimeout
	fenceIdleTimeout, fenceStatementTimeout = idle, statement
	return func() { fenceIdleTimeout, fenceStatementTimeout = oldIdle, oldStatement }
}

// Test-only helpers: no production code calls these, so they live here instead of the shipped package.

// DeleteSAMLSessions removes every session opened through SSO and returns how many were removed.
func (s *Store) DeleteSAMLSessions(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from sessions where auth_source = 'saml'`)
	if err != nil {
		return 0, fmt.Errorf("delete saml sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteSAMLSessionsForUser removes the SSO sessions of one user and returns how many were removed.
func (s *Store) DeleteSAMLSessionsForUser(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from sessions where auth_source = 'saml' and user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete saml sessions for user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountAdmins returns the number of global admins.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `select count(*) from users where is_admin`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// PruneMetricsCache deletes rows fetched more than olderThan ago and returns how many.
func (s *Store) PruneMetricsCache(ctx context.Context, olderThan time.Duration) (int64, error) {
	return s.pruneMetricsCacheBefore(ctx, time.Now().Add(-olderThan))
}

// DeleteOrphanedTagsBefore removes assignments orphaned before t and returns how many.
func (s *Store) DeleteOrphanedTagsBefore(ctx context.Context, t time.Time) (int64, error) {
	clusters, services, err := s.deleteOrphanedTagsBeforeCounts(ctx, t)
	return clusters + services, err
}

func (s *Store) LatestScanRun(ctx context.Context, kind string) (ScanRun, error) {
	r, err := scanScanRun(s.Pool.QueryRow(ctx, `select `+scanRunColumns+` from scan_runs where kind = $1
		order by started_at desc, id desc limit 1`, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanRun{}, ErrNotFound
	}
	if err != nil {
		return ScanRun{}, fmt.Errorf("latest scan run: %w", err)
	}
	return r, nil
}

// UpsertStatusRuns stores each run, replacing the row for the same service.
func (s *Store) UpsertStatusRuns(ctx context.Context, runs []StatusRun) error {
	if len(runs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, run := range runs {
		batch.Queue(upsertStatusRunSQL,
			run.Key.AccountID, run.Key.Region, run.Key.Cluster, run.Key.Service, string(run.Status), run.Since, run.Polls)
	}
	if err := s.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("upsert status runs: %w", err)
	}
	return nil
}

// LastChangeBefore returns the newest Harbormaster operation or deployment of the service with
// at < before and before-at <= window, or nil when there is none. It never guesses.
func (s *Store) LastChangeBefore(ctx context.Context, key model.ServiceKey, before time.Time, window time.Duration) (*ChangeRef, error) {
	query := ChangeQuery{Key: key, Before: before}
	found, err := s.LastChangesBefore(ctx, []ChangeQuery{query}, window)
	if err != nil {
		return nil, err
	}
	return found[query], nil
}

// Unfenced writers for the store package's own tests. Production code writes snapshot rows only through a SliceWriter;
// other packages' tests seed through storetest.Seed, which goes through one too.
func (s *Store) UpsertClusters(ctx context.Context, rows []model.Cluster) (WriteResult, error) {
	return upsertClusters(ctx, s.Pool, rows)
}

func (s *Store) DeleteClustersNotIn(ctx context.Context, t awsx.Target, keep []model.ClusterKey) (int64, error) {
	return deleteClustersNotIn(ctx, s.Pool, t, keep)
}

func (s *Store) UpsertServices(ctx context.Context, rows []model.Service) (WriteResult, error) {
	return upsertServices(ctx, s.Pool, rows)
}

func (s *Store) DeleteServicesNotIn(ctx context.Context, k model.ClusterKey, keep []string) (int64, error) {
	return deleteServicesNotIn(ctx, s.Pool, k, keep)
}

func (s *Store) InsertServiceEvents(ctx context.Context, rows []model.ServiceEvent) (int, error) {
	return insertServiceEvents(ctx, s.Pool, rows)
}

func (s *Store) UpsertTasks(ctx context.Context, rows []model.Task) (WriteResult, error) {
	return upsertTasks(ctx, s.Pool, rows)
}

func (s *Store) MarkTasksMissing(ctx context.Context, k model.ClusterKey, arns []string, now time.Time) error {
	return markTasksMissing(ctx, s.Pool, k, arns, now)
}

func (s *Store) InsertCountsHistory(ctx context.Context, ts time.Time, rows []model.Service) error {
	return insertCountsHistory(ctx, s.Pool, ts, rows)
}

func (s *Store) UpsertClusterCapacitySample(ctx context.Context, day time.Time, samples []ClusterCapacitySample) error {
	return upsertClusterCapacitySample(ctx, s.Pool, day, samples)
}

func (s *Store) UpsertPollLoop(ctx context.Context, p PollLoop) error {
	return upsertPollLoop(ctx, s.Pool, p)
}

func (s *Store) SetAccountHealth(ctx context.Context, accountID string, okAt *time.Time, lastError string) error {
	return setAccountHealth(ctx, s.Pool, accountID, okAt, lastError)
}

func (s *Store) UpsertTargetHealth(ctx context.Context, rows []model.TargetHealth) error {
	return upsertTargetHealth(ctx, s.Pool, rows)
}

func (s *Store) DeleteTargetHealthNotIn(ctx context.Context, t awsx.Target, keep []string) error {
	return deleteTargetHealthNotIn(ctx, s.Pool, t, keep)
}
