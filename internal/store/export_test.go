package store

import (
	"context"
	"time"
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
