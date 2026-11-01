package poller

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestPoller builds a Poller with a per-test lock key (shared by callers that pass the same key)
// and a short retry interval.
func newTestPoller(st *store.Store, cfg Config, key int64) *Poller {
	p := New(st, fake.New(fake.MockupEstate(time.Now)), cfg, discardLogger())
	p.lockKey = key
	p.retryInterval = 100 * time.Millisecond
	return p
}

func randomLockKey() int64 { return rand.Int64N(1<<40) + 1<<41 }

func startRun(t *testing.T, p *Poller) (cancel context.CancelFunc, done chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

func TestLeader_OnlyOneHolder(t *testing.T) {
	st := storetest.Open(t)
	key := randomLockKey()
	p1 := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour}, key)
	p2 := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour}, key)
	cancel1, done1 := startRun(t, p1)
	cancel2, done2 := startRun(t, p2)

	waitFor(t, 2*time.Second, "one leader", func() bool { return p1.IsLeader() || p2.IsLeader() })
	time.Sleep(400 * time.Millisecond) // give the loser several retry ticks to wrongly acquire
	if p1.IsLeader() == p2.IsLeader() {
		t.Fatalf("want exactly one leader, got p1=%v p2=%v", p1.IsLeader(), p2.IsLeader())
	}

	leaderCancel, leaderDone, other := cancel1, done1, p2
	if p2.IsLeader() {
		leaderCancel, leaderDone, other = cancel2, done2, p1
	}
	leaderCancel()
	<-leaderDone
	waitFor(t, 6*time.Second, "the other poller to take over", other.IsLeader)
}

// Lanes sharing one Postgres server each run in their own schema and must elect their own leader.
func TestLeader_SchemasElectIndependentLeaders(t *testing.T) {
	stA := storetest.Open(t)
	stB := storetest.Open(t)
	key := randomLockKey()
	pA := newTestPoller(stA, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour}, key)
	pB := newTestPoller(stB, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour}, key)
	startRun(t, pA)
	startRun(t, pB)

	waitFor(t, 2*time.Second, "a leader in each schema", func() bool { return pA.IsLeader() && pB.IsLeader() })
}

func TestLeader_StepsDownWhenConnectionDropped(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "a", AccountID: "111111111111", RoleARN: "arn:aws:iam::111111111111:role/x", ExternalID: "e", Regions: []string{"eu-west-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := randomLockKey()
	rec := newLoopRecorder()
	p := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, RetentionEvery: time.Hour}, key)
	p.loopFactory = rec.factory
	startRun(t, p)

	waitFor(t, 2*time.Second, "leadership", p.IsLeader)
	waitFor(t, 2*time.Second, "1 loop", func() bool { return len(rec.targets()) == 1 })

	// Postgres splits a bigint advisory key into classid (high 32 bits) and objid (low 32 bits).
	heldKey, err := scopedLockKey(ctx, st.Pool, key)
	if err != nil {
		t.Fatal(err)
	}
	var terminated int
	err = st.Pool.QueryRow(ctx, `select count(pg_terminate_backend(pid)) from pg_locks
		where locktype = 'advisory' and granted and pid <> pg_backend_pid()
		and classid = (($1::bigint >> 32) & 4294967295)::oid and objid = ($1::bigint & 4294967295)::oid`, heldKey).Scan(&terminated)
	if err != nil || terminated != 1 {
		t.Fatalf("terminate lock holder: terminated=%d err=%v", terminated, err)
	}

	waitFor(t, 2*time.Second, "step down", func() bool { return !p.IsLeader() || len(rec.targets()) == 0 })
	// Leadership and loops must return after the poller reconnects.
	waitFor(t, 3*time.Second, "leadership regained", p.IsLeader)
	waitFor(t, 3*time.Second, "loops regained", func() bool { return len(rec.targets()) == 1 })
}

// plan 06: leadership transitions are mirrored into telemetry.
func TestLeader_ReportsTelemetry(t *testing.T) {
	st := storetest.Open(t)
	reg := telemetry.New()
	p := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, Telemetry: reg}, randomLockKey())
	cancel, done := startRun(t, p)
	waitFor(t, 2*time.Second, "leadership", p.IsLeader)
	if !reg.Snapshot().Leader {
		t.Error("telemetry leader = false while leading")
	}
	cancel()
	<-done
	if reg.Snapshot().Leader {
		t.Error("telemetry leader = true after release")
	}
}
