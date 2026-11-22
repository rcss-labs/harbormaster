package lease

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// testTiming is the design's timing scaled down about 20 times, keeping its ratios: self-fence is two thirds of the TTL.
func testTiming() Timing {
	return Timing{
		TTL:               1500 * time.Millisecond,
		RenewEvery:        300 * time.Millisecond,
		Tick:              50 * time.Millisecond,
		MemberHeartbeat:   100 * time.Millisecond,
		MemberLive:        800 * time.Millisecond,
		SelfFenceAfter:    time.Second,
		GiveBackEvery:     300 * time.Millisecond,
		GuardMinRemaining: 100 * time.Millisecond,
	}
}

// exclusivity records, across every manager of a test, how many holders run per lease at the same moment.
type exclusivity struct {
	mu         sync.Mutex
	active     map[store.LeaseKey]int
	violations []string
}

func newExclusivity() *exclusivity { return &exclusivity{active: map[store.LeaseKey]int{}} }

func (e *exclusivity) begin(l store.Lease) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[l.Key]++
	if e.active[l.Key] > 1 {
		e.violations = append(e.violations, fmt.Sprintf("%d holders for %v at epoch %d", e.active[l.Key], l.Key, l.Epoch))
	}
}

func (e *exclusivity) end(l store.Lease) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[l.Key]--
}

func (e *exclusivity) check(t testing.TB) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.violations) > 0 {
		t.Fatalf("two holders ran for one lease: %v", e.violations)
	}
}

// recorder is a Holder that runs until cancelled and notes every start and stop.
type recorder struct {
	excl *exclusivity

	mu      sync.Mutex
	started []store.Lease
	running map[store.LeaseKey]context.Context
	stopped []store.Lease
}

func newRecorder(excl *exclusivity) *recorder {
	return &recorder{excl: excl, running: map[store.LeaseKey]context.Context{}}
}

func (r *recorder) Hold(ctx context.Context, l store.Lease) {
	r.excl.begin(l)
	r.mu.Lock()
	r.started = append(r.started, l)
	r.running[l.Key] = ctx
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.stopped = append(r.stopped, l)
	delete(r.running, l.Key)
	r.mu.Unlock()
	r.excl.end(l)
}

func (r *recorder) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started)
}

func (r *recorder) runningKeys() map[store.LeaseKey]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[store.LeaseKey]bool{}
	for k := range r.running {
		out[k] = true
	}
	return out
}

func (r *recorder) startsOf(k store.LeaseKey) []store.Lease {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.Lease
	for _, l := range r.started {
		if l.Key == k {
			out = append(out, l)
		}
	}
	return out
}

func (r *recorder) ctxOf(k store.LeaseKey) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[k]
}

// targetSet is a mutable account x region list for Manager.Targets.
type targetSet struct {
	mu   sync.Mutex
	list []awsx.Target
}

func newTargets(names ...string) *targetSet {
	ts := &targetSet{}
	ts.set(names...)
	return ts
}

func (ts *targetSet) set(names ...string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.list = nil
	for _, n := range names {
		ts.list = append(ts.list, awsx.Target{AccountID: n, Region: "r"})
	}
}

func (ts *targetSet) fn(context.Context) ([]awsx.Target, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]awsx.Target(nil), ts.list...), nil
}

func sliceNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("acct%02d", i)
	}
	return out
}

func sliceKeyOf(name string) store.LeaseKey {
	return store.LeaseKey{Kind: store.LeaseSlice, AccountID: name, Region: "r"}
}

var coordinatorKey = store.LeaseKey{Kind: store.LeaseCoordinator}

type managerOpts struct {
	roles       []string
	slices      Holder
	coordinator Holder
	gate        *Gate
	targets     *targetSet
}

func newManager(st *store.Store, o managerOpts) *Manager {
	m := &Manager{
		Store: st, ID: uuid.New(), Roles: o.roles, Version: "test", Timing: testTiming(), Gate: o.gate,
		Slices: o.slices, Coordinator: o.coordinator, TargetsEvery: 100 * time.Millisecond, Log: discardLogger(),
	}
	if o.targets != nil {
		m.Targets = o.targets.fn
	}
	return m
}

// start runs the manager and returns a stop that cancels it and waits for Run to return.
func start(t testing.TB, m *Manager) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

func leaseRow(t testing.TB, st *store.Store, k store.LeaseKey) store.LeaseRow {
	t.Helper()
	rows, err := st.ListLeases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Key == k {
			return r
		}
	}
	t.Fatalf("no lease row for %v", k)
	return store.LeaseRow{}
}

// expireLease makes a held lease expired in the database, as if its owner had stopped renewing a second ago.
func expireLease(t testing.TB, st *store.Store, k store.LeaseKey) {
	t.Helper()
	_, err := st.Pool.Exec(context.Background(),
		`update poll_leases set expires_at = now() - interval '1 second' where kind = $1 and account_id = $2 and region = $3`,
		k.Kind, k.AccountID, k.Region)
	if err != nil {
		t.Fatal(err)
	}
}

func TestManager_SingleManagerClaimsEverySliceAndTheCoordinator(t *testing.T) {
	st := storetest.Open(t)
	excl := newExclusivity()
	slicesRec, coordRec := newRecorder(excl), newRecorder(excl)
	m := newManager(st, managerOpts{roles: []string{"web", "poller", "coordinator"}, slices: slicesRec, coordinator: coordRec, targets: newTargets(sliceNames(5)...)})
	if !m.LastTick().IsZero() {
		t.Fatal("LastTick set before Run")
	}
	start(t, m)

	waitFor(t, 5*time.Second, "6 leases held", func() bool { return len(m.Held()) == 6 && m.HoldsCoordinator() })
	for _, n := range sliceNames(5) {
		row := leaseRow(t, st, sliceKeyOf(n))
		if row.OwnerID != m.ID || row.Epoch != 1 || row.Free {
			t.Fatalf("slice %s row = %+v, want owned by the manager at epoch 1", n, row)
		}
	}
	if row := leaseRow(t, st, coordinatorKey); row.OwnerID != m.ID {
		t.Fatalf("coordinator row = %+v", row)
	}
	if len(slicesRec.runningKeys()) != 5 || len(coordRec.runningKeys()) != 1 {
		t.Fatalf("running holders: slices %d, coordinator %d; want 5 and 1", len(slicesRec.runningKeys()), len(coordRec.runningKeys()))
	}
	if m.LastTick().IsZero() {
		t.Fatal("LastTick still zero after ticks")
	}
	members, err := st.LiveMembers(context.Background(), time.Minute)
	if err != nil || len(members) != 1 || members[0].InstanceID != m.ID {
		t.Fatalf("members = %+v, %v; want the manager", members, err)
	}
	waitFor(t, 2*time.Second, "member gauges", func() bool {
		ms, _ := st.LiveMembers(context.Background(), time.Minute)
		return len(ms) == 1 && ms[0].Slices == 5 && ms[0].Weight == 5
	})
}

func TestManager_ExactlyOneOwnerPerSliceWithThreeManagers(t *testing.T) {
	st := storetest.Open(t)
	excl := newExclusivity()
	targets := newTargets(sliceNames(12)...)
	var managers []*Manager
	var recs []*recorder
	for range 3 {
		rec := newRecorder(excl)
		m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, targets: targets})
		managers, recs = append(managers, m), append(recs, rec)
	}
	for _, m := range managers {
		start(t, m)
	}
	// Run through claims, rebalancing and the give-backs: the counter must never see two holders of one slice. The
	// planner gives back only while the excess is more than the lightest slice, so 12 equal slices over 3 pollers may
	// settle at 5, 4, 3; what matters is that all are owned and every share is within one slice of fair.
	fair := func() bool {
		total := 0
		for _, m := range managers {
			n := len(m.Held())
			if n < 3 || n > 5 {
				return false
			}
			total += n
		}
		return total == 12
	}
	waitFor(t, 20*time.Second, "shares within one slice of fair", fair)
	totalStarts := func() int {
		n := 0
		for _, r := range recs {
			n += r.startCount()
		}
		return n
	}
	time.Sleep(time.Second) // let a give-back that was already planned land
	startsAfterBalance := totalStarts()
	time.Sleep(time.Second) // 20 ticks, three renewal rounds
	if startsLater := totalStarts(); startsLater != startsAfterBalance || !fair() {
		t.Fatalf("slices moved after the shares were fair (%d starts, then %d)", startsAfterBalance, startsLater)
	}
	excl.check(t)
	rows, _ := st.ListLeases(context.Background())
	owners := map[uuid.UUID]int{}
	for _, r := range rows {
		if r.Free {
			t.Fatalf("unowned slice %v", r.Key)
		}
		owners[r.OwnerID]++
	}
	if len(owners) != 3 {
		t.Fatalf("owners = %v, want all 3 pollers to own slices", owners)
	}
}

func TestManager_GracefulStopHandsOverWithinOneTick(t *testing.T) {
	st := storetest.Open(t)
	excl := newExclusivity()
	targets := newTargets("acct00")
	a := newManager(st, managerOpts{roles: []string{"poller"}, slices: newRecorder(excl), targets: targets})
	bRec := newRecorder(excl)
	b := newManager(st, managerOpts{roles: []string{"poller"}, slices: bRec, targets: targets})
	stopA := start(t, a)
	waitFor(t, 5*time.Second, "a to hold the slice", func() bool { return len(a.Held()) == 1 })
	start(t, b)
	waitFor(t, 5*time.Second, "b to be a live member", func() bool {
		ms, _ := st.LiveMembers(context.Background(), time.Minute)
		return len(ms) == 2
	})

	stopped := time.Now()
	stopA()
	waitFor(t, 5*time.Second, "b to take the slice over", func() bool { return len(b.Held()) == 1 })
	took := time.Since(stopped)
	// The lease TTL is 1.5 s: a hand-over through expiry would take longer than half of it.
	if took > testTiming().TTL/2 {
		t.Fatalf("hand-over took %v, want well under the TTL (%v): a graceful stop must release", took, testTiming().TTL)
	}
	if got := bRec.startsOf(sliceKeyOf("acct00")); len(got) != 1 || got[0].Epoch != 2 {
		t.Fatalf("b's starts = %+v, want one at epoch 2", got)
	}
	members, _ := st.LiveMembers(context.Background(), time.Minute)
	if len(members) != 1 || members[0].InstanceID != b.ID {
		t.Fatalf("members after a stopped = %+v, want only b (a deletes its row)", members)
	}
	excl.check(t)
}

func TestManager_ExpiryHandsOverWithinTTLPlusOneTick(t *testing.T) {
	st := storetest.Open(t)
	excl := newExclusivity()
	targets := newTargets("acct00")
	a := newManager(st, managerOpts{roles: []string{"poller"}, slices: newRecorder(excl), targets: targets})
	a.crash = true // stops its holders but releases nothing, as a killed process would
	bRec := newRecorder(excl)
	b := newManager(st, managerOpts{roles: []string{"poller"}, slices: bRec, targets: targets})
	stopA := start(t, a)
	waitFor(t, 5*time.Second, "a to hold the slice", func() bool { return len(a.Held()) == 1 })
	start(t, b)
	waitFor(t, 5*time.Second, "b to be a live member", func() bool {
		ms, _ := st.LiveMembers(context.Background(), time.Minute)
		return len(ms) == 2
	})

	crashed := time.Now()
	stopA()
	if row := leaseRow(t, st, sliceKeyOf("acct00")); row.Free || row.OwnerID != a.ID {
		t.Fatalf("row right after the crash = %+v, want still owned by a (nothing was released)", row)
	}
	waitFor(t, 10*time.Second, "b to take the slice over", func() bool { return len(b.Held()) == 1 })
	took := time.Since(crashed)
	tm := testTiming()
	if limit := tm.TTL + tm.Tick + 500*time.Millisecond; took > limit {
		t.Fatalf("takeover took %v, want at most TTL + one tick (+ slack) = %v", took, limit)
	}
	if got := bRec.startsOf(sliceKeyOf("acct00")); len(got) != 1 || got[0].Epoch != 2 {
		t.Fatalf("b's starts = %+v, want one at epoch 2", got)
	}
	excl.check(t)
}

// stallStore stalls RenewLease on demand, as a partitioned or frozen database path would.
type stallStore struct {
	*store.Store
	blockRenew atomic.Bool
}

func (s *stallStore) RenewLease(ctx context.Context, l store.Lease, ttl time.Duration) error {
	if s.blockRenew.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Store.RenewLease(ctx, l, ttl)
}

func TestManager_SelfFencesWhenRenewalsStop(t *testing.T) {
	st := storetest.Open(t)
	rec := newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, targets: newTargets("acct00")})
	stall := &stallStore{Store: st}
	m.ls = stall
	start(t, m)
	key := sliceKeyOf("acct00")
	waitFor(t, 5*time.Second, "the holder to run", func() bool { return rec.ctxOf(key) != nil })
	hctx := rec.ctxOf(key)

	stall.blockRenew.Store(true)
	blockedAt := time.Now()
	time.Sleep(100 * time.Millisecond) // an in-flight renewal that began before the block finishes
	expires := leaseRow(t, st, key).ExpiresAt

	select {
	case <-hctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the holder was not cancelled although renewals stopped")
	}
	cancelledAt := time.Now()
	tm := testTiming()
	if cancelledAt.After(expires) {
		t.Fatalf("holder cancelled at %v, after the database lease expired at %v: it must stop first", cancelledAt, expires)
	}
	if d := cancelledAt.Sub(blockedAt); d > tm.SelfFenceAfter+300*time.Millisecond {
		t.Fatalf("holder cancelled %v after renewals stopped, want about SelfFenceAfter (%v)", d, tm.SelfFenceAfter)
	}
	stall.blockRenew.Store(false)
}

func TestManager_LostLeaseStopsOnlyThatHolder(t *testing.T) {
	st := storetest.Open(t)
	rec := newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, targets: newTargets(sliceNames(3)...)})
	start(t, m)
	waitFor(t, 5*time.Second, "3 holders", func() bool { return len(m.Held()) == 3 })

	victim := sliceKeyOf("acct01")
	victimCtx := rec.ctxOf(victim)
	// Another process takes the lease over (as after a long pause): expire it, then claim it with a long TTL.
	expireLease(t, st, victim)
	thief := uuid.New()
	if _, ok, err := st.ClaimLease(context.Background(), victim, thief, time.Hour); err != nil || !ok {
		t.Fatalf("thief claim: ok %v err %v", ok, err)
	}

	select {
	case <-victimCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the holder of the lost lease was not stopped")
	}
	waitFor(t, 2*time.Second, "the manager to forget the lost lease", func() bool { return len(m.Held()) == 2 })
	for _, n := range []string{"acct00", "acct02"} {
		if ctx := rec.ctxOf(sliceKeyOf(n)); ctx == nil || ctx.Err() != nil {
			t.Fatalf("holder of %s was stopped by the loss of another slice", n)
		}
	}
	// The thief's lease is live, so the manager must not fight for it.
	stays(t, 500*time.Millisecond, "the manager leaving the stolen slice alone", func() bool {
		return len(m.Held()) == 2 && leaseRow(t, st, victim).OwnerID == thief
	})
}

func TestManager_NoClaimsWhileGateNotHeld(t *testing.T) {
	st := storetest.Open(t)
	base := randomLockKey()
	old, ok := tryOldLeader(t, st, base)
	if !ok {
		t.Fatal("test setup: could not take the exclusive key")
	}
	gate, _ := startGate(t, st.Pool, base)
	rec, coord := newRecorder(newExclusivity()), newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller", "coordinator"}, slices: rec, coordinator: coord, gate: gate, targets: newTargets(sliceNames(3)...)})
	start(t, m)

	stays(t, 600*time.Millisecond, "no lease held behind an old leader", func() bool {
		return len(m.Held()) == 0 && len(rec.runningKeys()) == 0 && len(coord.runningKeys()) == 0
	})
	rows, _ := st.ListLeases(context.Background())
	for _, r := range rows {
		if !r.Free || r.Epoch != 0 {
			t.Fatalf("lease %v was claimed behind an old leader: %+v", r.Key, r)
		}
	}
	if m.LastTick().IsZero() {
		t.Fatal("the manager is not ticking while gated (health would report it stuck)")
	}

	old.stop(t)
	waitFor(t, 5*time.Second, "claims once the old leader is gone", func() bool { return len(m.Held()) == 4 })
}

func TestGate_ConnectionLossStopsHoldersAndReleasesLeases(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	base := randomLockKey()
	gate, _ := startGate(t, st.Pool, base)
	waitFor(t, 2*time.Second, "the gate", gate.Held)
	rec := newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, gate: gate, targets: newTargets("acct00")})
	start(t, m)
	key := sliceKeyOf("acct00")
	waitFor(t, 5*time.Second, "the holder to run", func() bool { return rec.ctxOf(key) != nil })
	hctx := rec.ctxOf(key)

	held, err := ScopedLockKey(ctx, st.Pool, base)
	if err != nil {
		t.Fatal(err)
	}
	var terminated int
	err = st.Pool.QueryRow(ctx, `select count(pg_terminate_backend(pid)) from pg_locks
		where locktype = 'advisory' and granted and pid <> pg_backend_pid()
		and classid = (($1::bigint >> 32) & 4294967295)::oid and objid = ($1::bigint & 4294967295)::oid`, held).Scan(&terminated)
	if err != nil || terminated != 1 {
		t.Fatalf("terminate the gate's backend: terminated=%d err=%v", terminated, err)
	}
	select {
	case <-hctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the holder kept running after the gate connection was lost")
	}
	// The lease was released, not left to expire: the gate comes back and the slice is claimed again at the next epoch
	// well before the 1.5 s TTL could have run out.
	waitFor(t, 1200*time.Millisecond, "the slice to be re-claimed after the release", func() bool {
		return len(rec.startsOf(key)) == 2
	})
	if got := rec.startsOf(key); got[1].Epoch != got[0].Epoch+1 {
		t.Fatalf("epochs = %d then %d, want one change of owner", got[0].Epoch, got[1].Epoch)
	}
}

func TestManager_NewTargetGetsALeaseAndRemovedTargetIsDropped(t *testing.T) {
	st := storetest.Open(t)
	rec := newRecorder(newExclusivity())
	targets := newTargets("acct00", "acct01")
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: rec, targets: targets})
	start(t, m)
	waitFor(t, 5*time.Second, "2 holders", func() bool { return len(m.Held()) == 2 })

	targets.set("acct01", "acct02") // acct00 removed, acct02 added
	waitFor(t, 5*time.Second, "acct02 to be held", func() bool { return rec.ctxOf(sliceKeyOf("acct02")) != nil })
	waitFor(t, 5*time.Second, "acct00 to be dropped", func() bool { return rec.ctxOf(sliceKeyOf("acct00")) == nil })
	waitFor(t, 5*time.Second, "acct00's row to be deleted", func() bool {
		rows, _ := st.ListLeases(context.Background())
		for _, r := range rows {
			if r.Key == sliceKeyOf("acct00") {
				return false
			}
		}
		return len(rows) == 2
	})
	if len(m.Held()) != 2 {
		t.Fatalf("held = %v, want acct01 and acct02", m.Held())
	}
}

func TestManager_TwoCoordinatorsOneHolder(t *testing.T) {
	st := storetest.Open(t)
	excl := newExclusivity()
	aRec, bRec := newRecorder(excl), newRecorder(excl)
	a := newManager(st, managerOpts{roles: []string{"coordinator"}, coordinator: aRec})
	b := newManager(st, managerOpts{roles: []string{"coordinator"}, coordinator: bRec})
	stopA := start(t, a)
	waitFor(t, 5*time.Second, "a to coordinate", a.HoldsCoordinator)
	start(t, b)
	stays(t, 600*time.Millisecond, "b staying on standby", func() bool { return !b.HoldsCoordinator() && a.HoldsCoordinator() })

	stopA()
	waitFor(t, 2*time.Second, "b to take over", b.HoldsCoordinator)
	// The lease is held before the holder goroutine has recorded its start; wait for the record.
	waitFor(t, 2*time.Second, "b's holder to start", func() bool { return len(bRec.startsOf(coordinatorKey)) >= 1 })
	if len(bRec.startsOf(coordinatorKey)) != 1 {
		t.Fatalf("b started %d times", len(bRec.startsOf(coordinatorKey)))
	}
	excl.check(t)
}

func TestManager_HolderThatReturnsEarlyIsReleasedAndRestarted(t *testing.T) {
	st := storetest.Open(t)
	var starts atomic.Int32
	holder := HolderFunc(func(ctx context.Context, l store.Lease) {
		if starts.Add(1) == 1 {
			return // gives up at once, as a poller does when it sees a lost lease
		}
		<-ctx.Done()
	})
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: holder, targets: newTargets("acct00")})
	start(t, m)
	waitFor(t, 5*time.Second, "a second start", func() bool { return starts.Load() == 2 })
	if row := leaseRow(t, st, sliceKeyOf("acct00")); row.Epoch != 2 {
		t.Fatalf("epoch = %d, want 2 (released after the first holder returned, then claimed again)", row.Epoch)
	}
}

// gateRenewStore holds the first RenewLease call after it started, so a test can release the lease while a renewal is in
// flight.
type gateRenewStore struct {
	*store.Store
	entered chan struct{}
	proceed chan struct{}
	once    atomic.Bool
}

func (s *gateRenewStore) RenewLease(ctx context.Context, l store.Lease, ttl time.Duration) error {
	if s.once.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.proceed
	}
	return s.Store.RenewLease(ctx, l, ttl)
}

// A holder that returns on its own is released by the tick. A renewal that was already in flight then finds the lease
// gone; that is our own release, not a loss, and must not be logged or counted as one.
func TestManager_ReleaseOfAFinishedHolderIsNotCountedAsLost(t *testing.T) {
	st := storetest.Open(t)
	reg := telemetry.New()
	giveUp := make(chan struct{})
	var starts atomic.Int32
	holder := HolderFunc(func(ctx context.Context, l store.Lease) {
		if starts.Add(1) == 1 {
			select {
			case <-giveUp:
			case <-ctx.Done():
			}
			return
		}
		<-ctx.Done()
	})
	m := newManager(st, managerOpts{roles: []string{"poller"}, slices: holder, targets: newTargets("acct00")})
	m.Telemetry = reg
	gated := &gateRenewStore{Store: st, entered: make(chan struct{}), proceed: make(chan struct{})}
	m.ls = gated
	start(t, m)

	waitFor(t, 5*time.Second, "a renewal to be in flight", func() bool {
		select {
		case <-gated.entered:
			return true
		default:
			return false
		}
	})
	close(giveUp)
	waitFor(t, 5*time.Second, "the finished holder to be released", func() bool {
		return hasLine(metricsText(t, reg), `harbormaster_lease_events_total{kind="slice",event="released"} 1`)
	})
	close(gated.proceed)
	stays(t, 500*time.Millisecond, "no lost event for our own release", func() bool {
		return !hasLine(metricsText(t, reg), `harbormaster_lease_events_total{kind="slice",event="lost"} 1`)
	})
}

func TestGuardFor_FailsAfterTakeoverAndNearExpiry(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.EnsureCoordinatorLease(ctx); err != nil {
		t.Fatal(err)
	}
	l, ok, err := st.ClaimLease(ctx, coordinatorKey, uuid.New(), 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok %v err %v", ok, err)
	}
	if err := GuardFor(st, l, time.Second)(ctx); err != nil {
		t.Fatalf("guard with 5 s left and 1 s needed = %v, want nil", err)
	}
	if err := GuardFor(st, l, 30*time.Second)(ctx); err == nil {
		t.Fatal("guard passed with less lease left than needed")
	}
	if _, err := st.Pool.Exec(ctx, `update poll_leases set expires_at = now() - interval '1 second' where kind = 'coordinator'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimLease(ctx, coordinatorKey, uuid.New(), time.Minute); err != nil || !ok {
		t.Fatalf("takeover: ok %v err %v", ok, err)
	}
	if err := GuardFor(st, l, 0)(ctx); err == nil {
		t.Fatal("guard of the old owner passed after a takeover")
	}
}

func metricsText(t testing.TB, reg *telemetry.Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func hasLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func TestManager_ReportsLeaseEvents(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	reg := telemetry.New()
	rec := newRecorder(newExclusivity())
	coord := newRecorder(newExclusivity())
	m := newManager(st, managerOpts{roles: []string{"poller", "coordinator"}, slices: rec, coordinator: coord, targets: newTargets(sliceNames(2)...)})
	m.Telemetry = reg
	stop := start(t, m)

	waitFor(t, 5*time.Second, "3 leases", func() bool { return len(m.Held()) == 3 })
	waitFor(t, 5*time.Second, "the gauges", func() bool {
		text := metricsText(t, reg)
		return hasLine(text, "harbormaster_lease_slices_owned 2") && hasLine(text, "harbormaster_coordinator_active 1") &&
			hasLine(text, `harbormaster_poll_members{role="poller"} 1`) && hasLine(text, `harbormaster_poll_members{role="coordinator"} 1`)
	})
	text := metricsText(t, reg)
	for _, want := range []string{
		`harbormaster_lease_events_total{kind="slice",event="acquired"} 2`,
		`harbormaster_lease_events_total{kind="coordinator",event="acquired"} 1`,
		"harbormaster_lease_weight_owned 2", // two slices without clusters weigh 1 each
		"harbormaster_poller_leader 1",      // follows the coordinator lease
	} {
		if !hasLine(text, want) {
			t.Errorf("metrics lack %q:\n%s", want, text)
		}
	}

	// A lease taken away is counted as lost, and only that slice leaves the gauge.
	victim := sliceKeyOf("acct01")
	expireLease(t, st, victim)
	if _, ok, err := st.ClaimLease(ctx, victim, uuid.New(), time.Hour); err != nil || !ok {
		t.Fatalf("thief claim: %v %v", ok, err)
	}
	waitFor(t, 5*time.Second, "the loss to be counted", func() bool {
		return hasLine(metricsText(t, reg), `harbormaster_lease_events_total{kind="slice",event="lost"} 1`)
	})
	waitFor(t, 5*time.Second, "the gauge to drop", func() bool { return hasLine(metricsText(t, reg), "harbormaster_lease_slices_owned 1") })

	// A graceful stop releases what is left and the coordinator gauges go to zero.
	stop()
	text = metricsText(t, reg)
	for _, want := range []string{
		`harbormaster_lease_events_total{kind="slice",event="released"} 1`,
		`harbormaster_lease_events_total{kind="coordinator",event="released"} 1`,
		"harbormaster_coordinator_active 0",
		"harbormaster_poller_leader 0",
		"harbormaster_lease_slices_owned 0",
	} {
		if !hasLine(text, want) {
			t.Errorf("after the stop, metrics lack %q:\n%s", want, text)
		}
	}
}

func TestManager_CountsATakeoverOfAnExpiredOwner(t *testing.T) {
	st := storetest.Open(t)
	reg := telemetry.New()
	excl := newExclusivity()
	targets := newTargets("acct00")
	a := newManager(st, managerOpts{roles: []string{"poller"}, slices: newRecorder(excl), targets: targets})
	a.crash = true
	stopA := start(t, a)
	waitFor(t, 5*time.Second, "a to hold the slice", func() bool { return len(a.Held()) == 1 })
	b := newManager(st, managerOpts{roles: []string{"poller"}, slices: newRecorder(excl), targets: targets})
	b.Telemetry = reg
	start(t, b)
	waitFor(t, 5*time.Second, "b to be a live member", func() bool {
		ms, _ := st.LiveMembers(context.Background(), time.Minute)
		return len(ms) == 2
	})
	stopA()
	waitFor(t, 10*time.Second, "b to take over", func() bool { return len(b.Held()) == 1 })
	text := metricsText(t, reg)
	for _, want := range []string{
		`harbormaster_lease_events_total{kind="slice",event="acquired"} 1`,
		`harbormaster_lease_events_total{kind="slice",event="takeover"} 1`,
	} {
		if !hasLine(text, want) {
			t.Errorf("metrics lack %q:\n%s", want, text)
		}
	}
}
