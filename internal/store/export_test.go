package store

import (
	"context"
	"time"

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
