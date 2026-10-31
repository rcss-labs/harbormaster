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
