package lease

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const gateRetry = 50 * time.Millisecond

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// randomLockKey keeps lanes sharing one Postgres server from seeing each other's gate keys.
func randomLockKey() int64 { return rand.Int64N(1<<40) + 1<<41 }

func waitFor(t testing.TB, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

// stays asserts cond is true for the whole window (used to prove something does NOT happen).
func stays(t testing.TB, window time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s did not hold for %v", what, window)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startGate(t testing.TB, pool *pgxpool.Pool, key int64) (g *Gate, stop func()) {
	t.Helper()
	g = NewGate(pool, key, gateRetry, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return g, stop
}

// oldLeader plays an old-version task: it takes the advisory key in exclusive mode on its own connection.
type oldLeader struct {
	conn *pgxpool.Conn
	key  int64
}

func tryOldLeader(t testing.TB, st *store.Store, base int64) (*oldLeader, bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ScopedLockKey(ctx, conn, base)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		conn.Release()
		return nil, false
	}
	return &oldLeader{conn: conn, key: key}, true
}

func (o *oldLeader) stop(t testing.TB) {
	t.Helper()
	var ok bool
	if err := o.conn.QueryRow(context.Background(), `select pg_advisory_unlock($1)`, o.key).Scan(&ok); err != nil || !ok {
		t.Fatalf("unlock: ok=%v err=%v", ok, err)
	}
	o.conn.Release()
}

func TestGate_AcquiresSharedLockWhenNoOldLeader(t *testing.T) {
	st := storetest.Open(t)
	g, _ := startGate(t, st.Pool, randomLockKey())
	waitFor(t, 2*time.Second, "the gate to be held", g.Held)
}

func TestGate_OldExclusiveHolderBlocksNewClaims(t *testing.T) {
	st := storetest.Open(t)
	key := randomLockKey()
	old, ok := tryOldLeader(t, st, key)
	if !ok {
		t.Fatal("test setup: could not take the exclusive key")
	}
	g, _ := startGate(t, st.Pool, key)
	stays(t, 10*gateRetry, "Held staying false behind an old leader", func() bool { return !g.Held() })

	changed := g.Changed()
	old.stop(t)
	waitFor(t, 2*time.Second, "the gate to be held once the old leader stopped", g.Held)
	select {
	case <-changed:
	default:
		t.Fatal("Changed was not closed when Held turned true")
	}
}

func TestGate_NewSharedHoldersBlockAnOldLeader(t *testing.T) {
	st := storetest.Open(t)
	key := randomLockKey()
	g1, stop1 := startGate(t, st.Pool, key)
	g2, stop2 := startGate(t, st.Pool, key)
	waitFor(t, 2*time.Second, "both gates held", func() bool { return g1.Held() && g2.Held() })

	if old, ok := tryOldLeader(t, st, key); ok {
		old.stop(t)
		t.Fatal("an old-style exclusive lock was granted while two new tasks hold the gate")
	}
	stop1()
	if old, ok := tryOldLeader(t, st, key); ok {
		old.stop(t)
		t.Fatal("an old-style exclusive lock was granted while one new task still holds the gate")
	}
	stop2()
	old, ok := tryOldLeader(t, st, key)
	if !ok {
		t.Fatal("old-style exclusive lock not granted after every gate stopped (the gate must unlock on exit)")
	}
	old.stop(t)
	if g1.Held() || g2.Held() {
		t.Fatal("Held still true after Run returned")
	}
}

func TestGate_ConnectionLossDropsHeld(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	base := randomLockKey()
	g, _ := startGate(t, st.Pool, base)
	waitFor(t, 2*time.Second, "the gate to be held", g.Held)

	held, err := ScopedLockKey(ctx, st.Pool, base)
	if err != nil {
		t.Fatal(err)
	}
	changed := g.Changed()
	// Postgres splits a bigint advisory key into classid (high 32 bits) and objid (low 32 bits).
	var terminated int
	err = st.Pool.QueryRow(ctx, `select count(pg_terminate_backend(pid)) from pg_locks
		where locktype = 'advisory' and granted and pid <> pg_backend_pid()
		and classid = (($1::bigint >> 32) & 4294967295)::oid and objid = ($1::bigint & 4294967295)::oid`, held).Scan(&terminated)
	if err != nil || terminated != 1 {
		t.Fatalf("terminate the gate's backend: terminated=%d err=%v", terminated, err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("Changed not closed after the connection was lost")
	}
	// It reconnects and takes the gate again.
	waitFor(t, 3*time.Second, "the gate to be held again", g.Held)
}

func TestGate_SchemasAreIndependent(t *testing.T) {
	stA := storetest.Open(t)
	stB := storetest.Open(t)
	key := randomLockKey()
	old, ok := tryOldLeader(t, stA, key)
	if !ok {
		t.Fatal("test setup: could not take the exclusive key")
	}
	defer old.stop(t)
	gA, _ := startGate(t, stA.Pool, key)
	gB, _ := startGate(t, stB.Pool, key)
	waitFor(t, 2*time.Second, "schema B's gate to be held", gB.Held)
	if gA.Held() {
		t.Fatal("schema A's gate is held although schema A has an old leader")
	}
}
