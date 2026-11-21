package lease

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LegacyLockKey is the Postgres advisory lock key old tasks hold in exclusive mode as the poller leader ("HMPOLL").
const LegacyLockKey int64 = 0x484D504F4C4C

// unlockTimeout bounds the unlock on a clean stop.
const unlockTimeout = 5 * time.Second

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ScopedLockKey mixes the connection's current schema into the base advisory lock key so that
// deployments sharing one Postgres server (one schema each) get independent keys. The
// advisory lock namespace is per-database, not per-schema, so an unscoped key would let one
// schema's holder block every other schema.
func ScopedLockKey(ctx context.Context, q queryRower, base int64) (int64, error) {
	var schema string
	if err := q.QueryRow(ctx, `select coalesce(current_schema(), '')`).Scan(&schema); err != nil {
		return 0, err
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	return base ^ int64(h.Sum64()), nil
}

// Gate keeps old and new tasks from both acting during an upgrade or a rollback. Old tasks take the leader key in
// exclusive mode; a new task that polls or coordinates first takes the same key in shared mode on a dedicated
// connection and may only claim leases while it holds it. So while an old leader lives no new task claims anything,
// and while any new task lives no old task can lead.
type Gate struct {
	pool    *pgxpool.Pool
	key     int64
	retry   time.Duration
	log     *slog.Logger
	held    atomic.Bool
	mu      sync.Mutex
	changed chan struct{}
}

// NewGate builds a gate on baseKey (the legacy key; the schema is mixed in), retried every retry.
func NewGate(pool *pgxpool.Pool, baseKey int64, retry time.Duration, log *slog.Logger) *Gate {
	return &Gate{pool: pool, key: baseKey, retry: retry, log: log, changed: make(chan struct{})}
}

// Held reports whether the shared lock is currently held on a healthy connection.
func (g *Gate) Held() bool { return g.held.Load() }

// Changed returns a channel that is closed on the next change of Held. Take it, then read Held: a change after the
// channel was taken always closes it.
func (g *Gate) Changed() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.changed
}

func (g *Gate) set(held bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held.Swap(held) != held {
		close(g.changed)
		g.changed = make(chan struct{})
	}
}

// opTimeout bounds each per-tick database call so a half-open connection cannot hide the loss of the lock.
func (g *Gate) opTimeout() time.Duration { return min(g.retry, 3*time.Second) }

// Run takes and keeps the shared lock until ctx ends, then unlocks. Every retry it either tries to take the lock or
// pings the connection that holds it; a failed ping closes the connection (which drops the lock server-side) and
// turns Held false.
func (g *Gate) Run(ctx context.Context) {
	var (
		conn    *pgxpool.Conn
		heldKey int64
	)
	drop := func(healthy bool) {
		g.set(false)
		if conn == nil {
			return
		}
		if healthy {
			uctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
			_, err := conn.Exec(uctx, `select pg_advisory_unlock_shared($1)`, heldKey)
			cancel()
			if err != nil {
				g.log.Warn("lease: gate unlock failed, closing connection", "err", err)
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
			pctx, cancel := context.WithTimeout(ctx, g.opTimeout())
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					g.log.Warn("lease: gate connection lost", "err", err)
				}
				drop(false)
			}
			return
		}
		actx, cancel := context.WithTimeout(ctx, g.opTimeout())
		defer cancel()
		c, err := g.pool.Acquire(actx)
		if err != nil {
			if ctx.Err() == nil {
				g.log.Warn("lease: acquire connection for the gate", "err", err)
			}
			return
		}
		key, err := ScopedLockKey(actx, c, g.key)
		if err != nil {
			if ctx.Err() == nil {
				g.log.Warn("lease: resolve gate key", "err", err)
			}
			_ = c.Hijack().Close(context.Background())
			return
		}
		var got bool
		if err := c.QueryRow(actx, `select pg_try_advisory_lock_shared($1)`, key).Scan(&got); err != nil {
			if ctx.Err() == nil {
				g.log.Warn("lease: try shared gate lock", "err", err)
			}
			// The statement may have been granted just before the error; closing drops it.
			_ = c.Hijack().Close(context.Background())
			return
		}
		if !got {
			c.Release()
			return
		}
		conn, heldKey = c, key
		g.set(true)
		g.log.Info("lease: gate held, no old-version leader is active")
	}

	timer := time.NewTicker(g.retry)
	defer timer.Stop()
	for {
		tick()
		select {
		case <-ctx.Done():
			// If the cancel aborted a query mid-flight the unlock fails and drop closes the connection, which drops the lock.
			drop(true)
			return
		case <-timer.C:
		}
	}
}
