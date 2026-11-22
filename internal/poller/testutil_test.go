package poller

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testTiming is the lease timing scaled down about 20 times, with the design's ratios.
func testTiming() lease.Timing {
	return lease.Timing{
		TTL: 1500 * time.Millisecond, RenewEvery: 300 * time.Millisecond, Tick: 50 * time.Millisecond,
		MemberHeartbeat: 100 * time.Millisecond, MemberLive: 800 * time.Millisecond, SelfFenceAfter: time.Second,
		GiveBackEvery: 300 * time.Millisecond, GuardMinRemaining: 100 * time.Millisecond,
	}
}

// newTestPoller builds a Poller with a per-test lock key (shared by callers that pass the same key), a short retry
// interval and scaled lease timing.
func newTestPoller(st *store.Store, cfg Config, key int64) *Poller {
	p := New(st, fake.New(fake.MockupEstate(time.Now)), cfg, discardLogger())
	p.lockKey = key
	p.retryInterval = 100 * time.Millisecond
	p.timing = testTiming()
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

// holdsCoordinator reports whether the poller's lease manager holds the coordinator lease.
func holdsCoordinator(p *Poller) bool {
	m := p.Manager()
	return m != nil && m.HoldsCoordinator()
}

// connectAccount gives the target's account a row: a slice lease exists only for a connected account.
func connectAccount(t testing.TB, st *store.Store, target awsx.Target) {
	t.Helper()
	if _, err := st.GetAccountByAccountID(context.Background(), target.AccountID); err == nil {
		return
	}
	if _, err := st.CreateAccount(context.Background(), store.AWSAccount{Name: "t" + target.AccountID, AccountID: target.AccountID,
		RoleARN: "arn:aws:iam::" + target.AccountID + ":role/x", ExternalID: "e", Regions: []string{target.Region}, Enabled: true}); err != nil {
		t.Fatalf("connect account %s: %v", target.AccountID, err)
	}
}

// testWriter claims the slice's lease under a throwaway owner and returns its fenced writer. The lease is released when
// the test ends. A loop under test writes through it exactly as a real one does.
func testWriter(t testing.TB, st *store.Store, target awsx.Target) *store.SliceWriter {
	t.Helper()
	w, release, err := claimOnce(context.Background(), st, target)
	if err != nil {
		t.Fatalf("claim %v: %v", target, err)
	}
	t.Cleanup(release)
	return w
}
