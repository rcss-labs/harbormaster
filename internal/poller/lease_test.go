package poller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// exclusiveLoops is a loop factory that records, across every poller using it, how many loops run per target at once.
type exclusiveLoops struct {
	mu         sync.Mutex
	active     map[awsx.Target]int
	violations []string
}

func newExclusiveLoops() *exclusiveLoops { return &exclusiveLoops{active: map[awsx.Target]int{}} }

func (e *exclusiveLoops) factory(ctx context.Context, t awsx.Target, _ *store.SliceWriter) {
	e.mu.Lock()
	e.active[t]++
	if e.active[t] > 1 {
		e.violations = append(e.violations, fmt.Sprintf("%d loops for %v", e.active[t], t))
	}
	e.mu.Unlock()
	<-ctx.Done()
	e.mu.Lock()
	e.active[t]--
	e.mu.Unlock()
}

func (e *exclusiveLoops) activeTargets() map[awsx.Target]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[awsx.Target]bool{}
	for t, n := range e.active {
		if n > 0 {
			out[t] = true
		}
	}
	return out
}

func (e *exclusiveLoops) violated() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.violations...)
}

// waitForCancel is a coordinator holder that does nothing, so a poller under test holds the coordinator lease too.
var waitForCancel = lease.HolderFunc(func(ctx context.Context, _ store.Lease) { <-ctx.Done() })

func heldCount(p *Poller) int {
	if m := p.Manager(); m != nil {
		return len(m.Held())
	}
	return 0
}

func createAccounts(t *testing.T, st *store.Store, n int, regions ...string) []store.AWSAccount {
	t.Helper()
	var out []store.AWSAccount
	for i := range n {
		id := fmt.Sprintf("%012d", 111111111111*(i+1))
		a, err := st.CreateAccount(context.Background(), store.AWSAccount{Name: "a" + id, AccountID: id,
			RoleARN: "arn:aws:iam::" + id + ":role/x", ExternalID: "e", Regions: regions, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestPoller_SingleProcessPollsEverySlice(t *testing.T) {
	st := storetest.Open(t)
	estate := fake.Generate(1, fake.Scale{Accounts: 2, RegionsPerAccount: 2, ClustersPerRegion: 1, ServicesPerCluster: 1, TasksPerService: 1})
	var want []awsx.Target
	for _, a := range estate.Accounts {
		if _, err := st.CreateAccount(context.Background(), store.AWSAccount{Name: a.Name, AccountID: a.ID,
			RoleARN: "arn:aws:iam::" + a.ID + ":role/x", ExternalID: "e", Regions: a.Regions, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		for _, r := range a.Regions {
			want = append(want, awsx.Target{AccountID: a.ID, Region: r})
		}
	}
	base := 100 * time.Millisecond
	cfg := Config{MaxInFlight: 4, AccountsRefresh: 100 * time.Millisecond, Coordinator: waitForCancel,
		Intervals: Intervals{Services: base, RunningTasks: base, FullTasks: base, StoppedTasks: base, Clusters: base, ServiceList: base,
			TargetHealth: base, Scaling: base, Alerts: base, Network: base, Storage: base}}
	p := New(st, fake.New(estate), cfg, discardLogger())
	p.timing = testTiming()
	p.retryInterval = 100 * time.Millisecond
	startRun(t, p)

	waitFor(t, 20*time.Second, "every slice to have a services poll row and an owner", func() bool {
		var polled, owned int
		ctx := context.Background()
		if st.Pool.QueryRow(ctx, `select count(*) from poll_loops where tier = 'services' and last_ok_at is not null`).Scan(&polled) != nil {
			return false
		}
		if st.Pool.QueryRow(ctx, `select count(*) from poll_leases where kind = 'slice' and owner_id is not null`).Scan(&owned) != nil {
			return false
		}
		return polled == len(want) && owned == len(want)
	})
	m := p.Manager()
	if m == nil || len(m.Held()) != len(want)+1 || !m.HoldsCoordinator() {
		t.Fatalf("manager holds %d leases, want every slice and the coordinator (%d)", heldCount(p), len(want)+1)
	}
}

func TestPoller_StartsAndStopsLoopsFromAccounts(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acct := createAccounts(t, st, 1, "eu-west-1", "us-east-1")[0]
	rec := newExclusiveLoops()
	p := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, Coordinator: waitForCancel}, randomLockKey())
	p.loopFactory = rec.factory
	startRun(t, p)

	waitFor(t, 5*time.Second, "2 loops", func() bool { return len(rec.activeTargets()) == 2 })

	if err := st.UpdateAccount(ctx, acct.ID, acct.Name, []string{"eu-west-1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "1 loop", func() bool {
		ts := rec.activeTargets()
		return len(ts) == 1 && ts[awsx.Target{AccountID: acct.AccountID, Region: "eu-west-1"}]
	})

	if _, err := st.Pool.Exec(ctx, `update aws_accounts set enabled=false where id=$1`, acct.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "0 loops", func() bool { return len(rec.activeTargets()) == 0 })
}

func TestPoller_TwoPollersNeverPollOneSliceTogether(t *testing.T) {
	st := storetest.Open(t)
	createAccounts(t, st, 6, "eu-west-1")
	rec := newExclusiveLoops()
	key := randomLockKey()
	a := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, Coordinator: waitForCancel}, key)
	b := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, Coordinator: waitForCancel}, key)
	a.loopFactory, b.loopFactory = rec.factory, rec.factory
	cancelA, doneA := startRun(t, a)
	startRun(t, b)

	waitFor(t, 15*time.Second, "both pollers to hold slices", func() bool {
		ma, mb := a.Manager(), b.Manager()
		return ma != nil && mb != nil && len(ma.Held()) > 0 && len(mb.Held()) > 0 && len(rec.activeTargets()) == 6
	})
	time.Sleep(time.Second) // rebalancing and several renewal rounds, under watch
	if v := rec.violated(); len(v) > 0 {
		t.Fatalf("two loops ran for one slice: %v", v)
	}

	cancelA()
	<-doneA
	waitFor(t, 5*time.Second, "b to take over every slice", func() bool { return heldCount(b) == 7 && len(rec.activeTargets()) == 6 })
	if v := rec.violated(); len(v) > 0 {
		t.Fatalf("two loops ran for one slice: %v", v)
	}
}

func TestPoller_GracefulStopReleasesLeases(t *testing.T) {
	st := storetest.Open(t)
	createAccounts(t, st, 3, "eu-west-1")
	rec := newExclusiveLoops()
	p := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, Coordinator: waitForCancel}, randomLockKey())
	p.loopFactory = rec.factory
	cancel, done := startRun(t, p)
	waitFor(t, 5*time.Second, "all slices held", func() bool { return heldCount(p) == 4 })

	cancel()
	<-done
	rows, err := st.ListLeases(context.Background())
	if err != nil || len(rows) != 4 {
		t.Fatalf("ListLeases = %v, %v", rows, err)
	}
	for _, r := range rows {
		if !r.Free || r.OwnerID != uuid.Nil {
			t.Errorf("lease %v is still held after a graceful stop: %+v", r.Key, r)
		}
	}
	if len(rec.activeTargets()) != 0 {
		t.Fatal("loops still running after Run returned")
	}
}

func TestPoller_LegacyGateBlocksPollingBehindAnOldLeader(t *testing.T) {
	st := storetest.Open(t)
	createAccounts(t, st, 2, "eu-west-1")
	key := randomLockKey()
	ctx := context.Background()
	// An old-version task: the leader key in exclusive mode, on its own connection.
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := lease.ScopedLockKey(ctx, conn, key)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, scoped).Scan(&got); err != nil || !got {
		t.Fatalf("take the old leader key: %v %v", got, err)
	}

	rec := newExclusiveLoops()
	p := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, LegacyGate: true}, key)
	p.loopFactory = rec.factory
	startRun(t, p)
	time.Sleep(600 * time.Millisecond) // many gate retries and manager ticks
	if n := len(rec.activeTargets()); n != 0 {
		t.Fatalf("%d loops ran while an old-version leader was alive", n)
	}
	rows, _ := st.ListLeases(ctx)
	for _, r := range rows {
		if !r.Free {
			t.Fatalf("lease %v claimed while an old-version leader was alive", r.Key)
		}
	}

	if err := conn.QueryRow(ctx, `select pg_advisory_unlock($1)`, scoped).Scan(&got); err != nil || !got {
		t.Fatalf("unlock: %v %v", got, err)
	}
	conn.Release()
	waitFor(t, 5*time.Second, "polling once the old leader is gone", func() bool { return len(rec.activeTargets()) == 2 })
}

func TestPollOnce_ClaimsAndReleasesALease(t *testing.T) {
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	if err := PollOnce(context.Background(), st, f, Config{MaxInFlight: 4}, loopTarget); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	rows, err := st.ListLeases(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("leases = %v, %v; want the slice's row", rows, err)
	}
	if r := rows[0]; !r.Free || r.Epoch != 1 || r.Key.AccountID != loopTarget.AccountID || r.Key.Region != loopTarget.Region {
		t.Fatalf("lease after PollOnce = %+v, want released at epoch 1", r)
	}
	var n int
	if err := st.Pool.QueryRow(context.Background(), `select count(*) from clusters`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("clusters = %d, %v; PollOnce must still fill the store", n, err)
	}
}

func TestPollOnce_FailsWhenALiveOwnerHoldsTheSlice(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	k := store.LeaseKey{Kind: store.LeaseSlice, AccountID: loopTarget.AccountID, Region: loopTarget.Region}
	if err := st.EnsureSliceLease(ctx, loopTarget); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimLease(ctx, k, uuid.New(), time.Minute); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	err := PollOnce(ctx, st, f, Config{MaxInFlight: 4}, loopTarget)
	if err == nil {
		t.Fatal("PollOnce succeeded although a live owner holds the slice")
	}
	want := loopTarget.AccountID + "/" + loopTarget.Region
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the slice %s", err, want)
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `select count(*) from clusters`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("clusters = %d, %v; nothing may be written without the lease", n, err)
	}
}

// The idle monitor belongs to the process, not to leadership: a poller that owns no slice still tracks activity (the
// coordinator's image resolver reads it).
func TestPoller_IdleMonitorRunsWithoutOwningAnything(t *testing.T) {
	st := storetest.Open(t)
	p := newTestPoller(st, Config{AccountsRefresh: time.Hour, IdleAfter: time.Minute, IdleInterval: 5 * time.Minute}, randomLockKey())
	p.idleCheckEvery = 20 * time.Millisecond
	p.sessionActivity = func(context.Context, time.Time) (time.Time, bool, error) { return time.Time{}, false, nil }
	p.loopFactory = newExclusiveLoops().factory
	startRun(t, p)
	waitFor(t, 3*time.Second, "idle mode", p.IsIdle)
}
