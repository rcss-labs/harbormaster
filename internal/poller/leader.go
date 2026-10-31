package poller

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const unlockTimeout = 5 * time.Second

// opTimeout bounds each per-tick database call so a half-open connection cannot stall
// leader-loss detection (which would risk two instances polling at once).
func (p *Poller) opTimeout() time.Duration { return min(p.retryInterval, 3*time.Second) }

// Run competes for the leader lock until ctx is cancelled. The lock is a session-level advisory
// lock on a dedicated connection; every retry tick the holder pings that connection, so a lost
// connection (which silently drops the lock server-side) demotes this instance and stops the
// supervisor. Run returns nil on ctx cancellation (after releasing the lock).
func (p *Poller) Run(ctx context.Context) error {
	var (
		conn      *pgxpool.Conn
		stopSuper context.CancelFunc
		superDone chan struct{}
		heldKey   int64
	)

	// demote stops the supervisor (waiting for it) and releases the connection.
	// healthy=false closes the connection instead of returning it to the pool.
	demote := func(healthy bool) {
		p.leader.Store(false)
		if stopSuper != nil {
			stopSuper()
			<-superDone
			stopSuper, superDone = nil, nil
		}
		if conn == nil {
			return
		}
		if healthy {
			uctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
			_, err := conn.Exec(uctx, `select pg_advisory_unlock($1)`, heldKey)
			cancel()
			if err != nil {
				p.log.Warn("poller: unlock failed, closing connection", "err", err)
				healthy = false
			}
		}
		if healthy {
			conn.Release()
		} else {
			_ = conn.Hijack().Close(context.Background())
		}
		conn = nil
	}

	tick := func() {
		if conn != nil {
			pctx, cancel := context.WithTimeout(ctx, p.opTimeout())
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					p.log.Warn("poller: leader connection lost, stepping down", "err", err)
				}
				demote(false)
			}
			return
		}
		actx, acancel := context.WithTimeout(ctx, p.opTimeout())
		defer acancel()
		c, err := p.store.Pool.Acquire(actx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Warn("poller: acquire connection for leader lock", "err", err)
			}
			return
		}
		key, err := scopedLockKey(actx, c, p.lockKey)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Warn("poller: resolve leader lock key", "err", err)
			}
			_ = c.Hijack().Close(context.Background())
			return
		}
		var got bool
		if err := c.QueryRow(actx, `select pg_try_advisory_lock($1)`, key).Scan(&got); err != nil || !got {
			if err != nil && ctx.Err() == nil {
				p.log.Warn("poller: try advisory lock", "err", err)
				_ = c.Hijack().Close(context.Background())
				return
			}
			c.Release()
			return
		}
		conn, heldKey = c, key
		p.leader.Store(true)
		p.log.Info("poller: became leader")
		superCtx, cancel := context.WithCancel(ctx)
		stopSuper, superDone = cancel, make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			p.runSupervisor(superCtx)
		}(superDone)
	}

	timer := time.NewTicker(p.retryInterval)
	defer timer.Stop()
	for {
		tick()
		select {
		case <-ctx.Done():
			// If the cancel aborted a query mid-flight the unlock fails and demote closes the connection, which drops the lock.
			demote(true)
			return nil
		case <-timer.C:
		}
	}
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// scopedLockKey mixes the connection's current schema into the base advisory lock key so that
// deployments sharing one Postgres server (one schema each) elect independent leaders. The
// advisory lock namespace is per-database, not per-schema, so an unscoped key would let one
// schema's leader block every other schema.
func scopedLockKey(ctx context.Context, q queryRower, base int64) (int64, error) {
	var schema string
	if err := q.QueryRow(ctx, `select coalesce(current_schema(), '')`).Scan(&schema); err != nil {
		return 0, err
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	return base ^ int64(h.Sum64()), nil
}
