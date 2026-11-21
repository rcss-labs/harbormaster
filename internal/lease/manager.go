package lease

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	// defaultTargetsEvery is how often the account x region set is refreshed and the lease rows are reconciled.
	defaultTargetsEvery = 30 * time.Second
	// memberStaleAfter is when a silent member row is deleted.
	memberStaleAfter = 10 * time.Minute
	// releaseTimeout bounds each release and the final member delete on a stop.
	releaseTimeout = 5 * time.Second
	// maxClaimPasses bounds the re-plans within one tick after a claim lost a race to another member.
	maxClaimPasses = 3
	// slowClaim is how long a claim may take before it counts as blocked behind another transaction's lock; a blocked
	// claim backs its lease off for a few ticks, and a tick makes at most maxSlowClaims of them.
	slowClaim     = time.Second
	maxSlowClaims = 2
)

// Holder runs the work of one held lease. Hold must return soon after ctx is cancelled, and only once its in-flight
// writes are done: the manager releases the lease after Hold returns, and a release waits for open fenced writes.
type Holder interface {
	Hold(ctx context.Context, l store.Lease)
}

// HolderFunc adapts a function to Holder.
type HolderFunc func(ctx context.Context, l store.Lease)

// Hold calls f.
func (f HolderFunc) Hold(ctx context.Context, l store.Lease) { f(ctx, l) }

// leaseStore is the part of *store.Store the manager uses; tests wrap it to stall single calls.
type leaseStore interface {
	EnsureSliceLeases(ctx context.Context, targets []awsx.Target) error
	EnsureCoordinatorLease(ctx context.Context) error
	ClaimLease(ctx context.Context, key store.LeaseKey, owner uuid.UUID, ttl time.Duration) (store.Lease, bool, error)
	RenewLease(ctx context.Context, l store.Lease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, l store.Lease) error
	ListLeases(ctx context.Context) ([]store.LeaseRow, error)
	SliceWeights(ctx context.Context) (map[awsx.Target]int, error)
	HeartbeatMember(ctx context.Context, m store.Member) error
	LiveMembers(ctx context.Context, window time.Duration) ([]store.Member, error)
	DeleteMember(ctx context.Context, id uuid.UUID) error
	DeleteStaleMembers(ctx context.Context, olderThan time.Duration) (int64, error)
}

// Manager claims, renews, rebalances, gives back and releases the leases of one process, and runs a Holder for each
// lease it holds. Slices is the holder of every slice lease (nil: this process does not poll); Coordinator is the holder
// of the coordinator lease (nil: this process does not coordinate).
type Manager struct {
	Store   *store.Store
	ID      uuid.UUID // this process; a new one on every start
	Roles   []string
	Version string
	Timing  Timing
	Gate    *Gate // nil: no gate

	Slices      Holder
	Coordinator Holder
	// Targets lists the enabled account x region pairs; refreshed every TargetsEvery. An error keeps the current set.
	Targets      func(ctx context.Context) ([]awsx.Target, error)
	TargetsEvery time.Duration // zero: 30 s
	Gauges       func() (focusObjects int)

	Telemetry *telemetry.Registry
	Log       *slog.Logger

	// test seams
	ls    leaseStore // overrides Store
	crash bool       // stop without releasing anything, as a killed process would

	runCtx   context.Context
	mu       sync.Mutex // guards held, its entries' fields, weights and lastBeat
	held     map[store.LeaseKey]*holding
	lastTick atomic.Int64
	beatMu   sync.Mutex // serialises member heartbeats (the tick and the renewer both send one)
	lastBeat time.Time

	// owned by the tick goroutine
	started      time.Time
	lastTargets  time.Time
	lastGiveBack time.Time
	targets      map[awsx.Target]bool // nil until the first successful refresh
	weights      map[awsx.Target]int  // guarded by mu
	// claimBackoff holds off claiming a lease whose last claim was blocked.
	claimBackoff map[store.LeaseKey]time.Time
	// priorOwner remembers, per lease, that the row was owned but expired when it was planned, so the claim that
	// follows is counted as a takeover.
	priorOwner map[store.LeaseKey]bool
}

// holding is one lease this process holds and the holder running under it.
type holding struct {
	lease  store.Lease
	cancel context.CancelFunc
	done   chan struct{} // closed when Hold returned

	// guarded by Manager.mu
	renewedAt time.Time // start of the last successful claim or renewal, monotonic
	fenced    bool      // the self-fence cancelled the holder
	lost      bool      // a renewal found the lease taken: the holder is cancelled and nothing is left to release
	stopping  bool      // the tick is stopping this holder: do not renew it
}

func (m *Manager) store() leaseStore {
	if m.ls != nil {
		return m.ls
	}
	return m.Store
}

// LastTick is the end of the last completed tick; zero before the first.
func (m *Manager) LastTick() time.Time {
	n := m.lastTick.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Held returns the leases this process holds right now, ordered by key.
func (m *Manager) Held() []store.Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Lease, 0, len(m.held))
	for _, h := range m.held {
		if !h.lost {
			out = append(out, h.lease)
		}
	}
	slices.SortFunc(out, func(a, b store.Lease) int { return compareKeys(a.Key, b.Key) })
	return out
}

// HoldsCoordinator reports whether this process holds the coordinator lease.
func (m *Manager) HoldsCoordinator() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.held[store.LeaseKey{Kind: store.LeaseCoordinator}]
	return ok && !h.lost
}

// dbTimeout bounds each database call of a tick, so a half-open connection cannot stall renewals for long.
func (m *Manager) dbTimeout() time.Duration {
	return min(max(m.Timing.Tick, time.Second), 3*time.Second)
}

func (m *Manager) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, m.dbTimeout())
}

func (m *Manager) gateHeld() bool { return m.Gate == nil || m.Gate.Held() }

// Run ticks until ctx ends, then stops every holder, releases every lease and removes the member row. The first tick
// runs at once, so a fresh process begins polling within about a second.
func (m *Manager) Run(ctx context.Context) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.Log == nil {
		m.Log = slog.Default()
	}
	if m.TargetsEvery <= 0 {
		m.TargetsEvery = defaultTargetsEvery
	}
	m.runCtx = ctx
	m.mu.Lock()
	m.held = map[store.LeaseKey]*holding{}
	m.mu.Unlock()
	m.started = time.Now()

	watchdogDone := make(chan struct{})
	go func() { defer close(watchdogDone); m.watchdog(ctx) }()
	renewerDone := make(chan struct{})
	go func() { defer close(renewerDone); m.renewer(ctx) }()

	timer := time.NewTicker(m.Timing.Tick)
	defer timer.Stop()
	for {
		var gateChanged <-chan struct{}
		if m.Gate != nil {
			gateChanged = m.Gate.Changed() // taken before the tick, so a change during it is not missed
		}
		m.tick(ctx)
		m.publish()
		m.lastTick.Store(time.Now().UnixNano())
		select {
		case <-ctx.Done():
			<-renewerDone // no renewal or heartbeat may race the release
			m.shutdown()
			m.publish()
			<-watchdogDone
			return nil
		case <-timer.C:
		case <-gateChanged:
		}
	}
}

// watchdog is the self-fence: whatever the tick is doing (it may be stuck in a database call), a holder whose lease was
// last renewed longer than SelfFenceAfter ago is cancelled at once. That is before the database lease can expire (TTL),
// so a partitioned process stops before another process can take over. Correctness never rests on this: the epoch check
// in every write does. This only stops wasted work and bounds how long a stale holder keeps trying.
func (m *Manager) watchdog(ctx context.Context) {
	period := max(m.Timing.SelfFenceAfter/10, time.Millisecond)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		for _, h := range m.held {
			if !h.fenced && time.Since(h.renewedAt) > m.Timing.SelfFenceAfter {
				h.fenced = true
				h.cancel()
				m.Log.Warn("lease: renewals stopped, self-fencing", "kind", h.lease.Key.Kind,
					"account", h.lease.Key.AccountID, "region", h.lease.Key.Region, "epoch", h.lease.Epoch)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) tick(ctx context.Context) {
	if !m.gateHeld() {
		// An old-version leader is alive, or the gate connection dropped: act on nothing.
		m.stopAll(ctx, "gate not held")
		return
	}
	m.reapFinished(ctx)
	m.heartbeat(ctx) // members must be visible before claims are planned
	m.refreshTargets(ctx)
	if m.Slices != nil {
		m.claimSlices(ctx)
	}
	if m.Coordinator != nil {
		m.claimCoordinator(ctx)
	}
}

// reapFinished forgets and releases leases whose holder has already returned (it decided to stop by itself, or the
// self-fence cancelled it), so the slice is not left renewed with nobody working on it. A lost lease has nothing to
// release.
func (m *Manager) reapFinished(ctx context.Context) {
	for _, h := range m.snapshotHeld() {
		select {
		case <-h.done:
			m.forget(h)
			m.mu.Lock()
			lost := h.lost
			m.mu.Unlock()
			if !lost {
				m.release(ctx, h.lease, "holder stopped")
			}
		default:
		}
	}
}
func (m *Manager) snapshotHeld() []*holding {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*holding, 0, len(m.held))
	for _, h := range m.held {
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b *holding) int { return compareKeys(a.lease.Key, b.lease.Key) })
	return out
}

func (m *Manager) forget(h *holding) {
	m.mu.Lock()
	delete(m.held, h.lease.Key)
	m.mu.Unlock()
}

// stopHolder cancels a holder and waits for it to return, which is when its in-flight writes are done.
func (m *Manager) stopHolder(h *holding) {
	m.mu.Lock()
	h.stopping = true
	m.mu.Unlock()
	h.cancel()
	<-h.done
}
func (m *Manager) release(ctx context.Context, l store.Lease, why string) {
	if m.crash {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := m.store().ReleaseLease(rctx, l); err != nil {
		m.Log.Warn("lease: release failed, it will expire", "kind", l.Key.Kind, "account", l.Key.AccountID,
			"region", l.Key.Region, "epoch", l.Epoch, "err", err)
		return
	}
	m.event(l.Key.Kind, "released")
	m.Log.Info("lease: released", "why", why, "kind", l.Key.Kind, "account", l.Key.AccountID, "region", l.Key.Region, "epoch", l.Epoch)
}

// event counts a lease event in the registry, when there is one.
func (m *Manager) event(kind, event string) {
	if m.Telemetry != nil {
		m.Telemetry.CountLeaseEvent(kind, event)
	}
}

// publish mirrors what this process holds into the registry: the slice gauges, the coordinator gauge and the old
// poller_leader series (1 on the holder of the coordinator lease).
func (m *Manager) publish() {
	if m.Telemetry == nil {
		return
	}
	slicesHeld, weight := m.ownedGauges()
	coordinator := false
	m.mu.Lock()
	if h, ok := m.held[store.LeaseKey{Kind: store.LeaseCoordinator}]; ok && !h.lost {
		coordinator = true
	}
	m.mu.Unlock()
	m.Telemetry.SetLeaseOwned(slicesHeld, weight)
	m.Telemetry.SetCoordinatorActive(coordinator)
	m.Telemetry.SetLeader(coordinator)
}

// stopAll stops every holder and releases every lease.
func (m *Manager) stopAll(ctx context.Context, why string) {
	for _, h := range m.snapshotHeld() {
		m.stopHolder(h)
		m.forget(h)
		m.release(ctx, h.lease, why)
	}
}

func (m *Manager) shutdown() {
	m.stopAll(m.runCtx, "shutting down")
	if m.crash {
		return
	}
	dctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if err := m.store().DeleteMember(dctx, m.ID); err != nil {
		m.Log.Warn("lease: delete member row", "err", err)
	}
}

// renewer renews held leases and beats the member row on its own goroutine, so a tick that is busy (claims waiting on a
// row lock, a holder that takes long to stop) never delays them: that is what keeps healthy slices from being
// self-fenced.
func (m *Manager) renewer(ctx context.Context) {
	period := max(min(m.Timing.RenewEvery/3, m.Timing.MemberHeartbeat, m.Timing.Tick), time.Millisecond)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		if m.gateHeld() {
			m.renewDue(ctx)
			m.heartbeat(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// renewDue renews every lease whose last renewal is older than RenewEvery. A lost lease cancels only its own holder; the
// tick forgets it once the holder has returned.
func (m *Manager) renewDue(ctx context.Context) {
	for _, h := range m.snapshotHeld() {
		m.mu.Lock()
		due := !h.lost && !h.fenced && !h.stopping && time.Since(h.renewedAt) >= m.Timing.RenewEvery
		m.mu.Unlock()
		if !due {
			continue
		}
		start := time.Now()
		cctx, cancel := m.call(ctx)
		err := m.store().RenewLease(cctx, h.lease, m.Timing.TTL)
		cancel()
		switch {
		case err == nil:
			m.mu.Lock()
			h.renewedAt = start
			m.mu.Unlock()
		case errors.Is(err, store.ErrLeaseLost):
			m.mu.Lock()
			already := h.lost || h.stopping
			h.lost = true
			m.mu.Unlock()
			if already {
				continue
			}
			m.Log.Warn("lease: lost", "kind", h.lease.Key.Kind, "account", h.lease.Key.AccountID,
				"region", h.lease.Key.Region, "epoch", h.lease.Epoch)
			m.event(h.lease.Key.Kind, "lost")
			h.cancel()
		default:
			if ctx.Err() == nil {
				if m.Telemetry != nil {
					m.Telemetry.CountLeaseRenewFailure(h.lease.Key.Kind)
				}
				m.Log.Warn("lease: renewal failed, will retry", "kind", h.lease.Key.Kind,
					"account", h.lease.Key.AccountID, "region", h.lease.Key.Region, "epoch", h.lease.Epoch, "err", err)
			}
		}
	}
}
func (m *Manager) heartbeat(ctx context.Context) {
	m.beatMu.Lock()
	defer m.beatMu.Unlock()
	if !m.lastBeat.IsZero() && time.Since(m.lastBeat) < m.Timing.MemberHeartbeat {
		return
	}
	slicesHeld, weight := m.ownedGauges()
	focus := 0
	if m.Gauges != nil {
		focus = m.Gauges()
	}
	cctx, cancel := m.call(ctx)
	defer cancel()
	err := m.store().HeartbeatMember(cctx, store.Member{
		InstanceID: m.ID, Roles: m.Roles, Version: m.Version, StartedAt: m.started,
		Slices: slicesHeld, Weight: weight, FocusObjects: focus,
	})
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Warn("lease: member heartbeat", "err", err)
		}
		return
	}
	m.lastBeat = time.Now()
	if m.Telemetry != nil {
		if members, err := m.store().LiveMembers(cctx, m.Timing.MemberLive); err == nil {
			byRole := map[string]int{}
			for _, mem := range members {
				for _, role := range mem.Roles {
					byRole[role]++
				}
			}
			m.Telemetry.SetPollMembers(byRole)
		}
	}
	if _, err := m.store().DeleteStaleMembers(cctx, memberStaleAfter); err != nil && ctx.Err() == nil {
		m.Log.Warn("lease: delete stale members", "err", err)
	}
}

// ownedGauges returns how many slices this process holds and their weight.
func (m *Manager) ownedGauges() (slicesHeld, weight int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.held {
		if h.lease.Key.Kind == store.LeaseSlice && !h.lost {
			slicesHeld++
			weight += max(m.weights[awsx.Target{AccountID: h.lease.Key.AccountID, Region: h.lease.Key.Region}], 1)
		}
	}
	return slicesHeld, weight
}

// refreshTargets reads the account x region set, gives up held slices whose target is gone and reconciles the rows.
// The coordinator row is ensured on the same cadence even when this process has no targets to list.
func (m *Manager) refreshTargets(ctx context.Context) {
	if m.Slices == nil && m.Coordinator == nil {
		return
	}
	if !m.lastTargets.IsZero() && time.Since(m.lastTargets) < m.TargetsEvery {
		return
	}
	cctx, cancel := m.call(ctx)
	defer cancel()
	if m.Coordinator != nil {
		if err := m.store().EnsureCoordinatorLease(cctx); err != nil {
			if ctx.Err() == nil {
				m.Log.Warn("lease: ensure coordinator lease", "err", err)
			}
			return
		}
	}
	if m.Targets == nil {
		m.lastTargets = time.Now()
		return
	}
	list, err := m.Targets(cctx)
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Error("lease: list targets", "err", err)
		}
		return // keep the current set rather than dropping slices on a transient error
	}
	m.lastTargets = time.Now()
	set := make(map[awsx.Target]bool, len(list))
	for _, t := range list {
		set[t] = true
	}
	m.targets = set

	for _, h := range m.snapshotHeld() {
		k := h.lease.Key
		if k.Kind == store.LeaseSlice && !set[awsx.Target{AccountID: k.AccountID, Region: k.Region}] {
			m.stopHolder(h)
			m.forget(h)
			m.release(ctx, h.lease, "slice removed")
		}
	}
	if m.Slices != nil {
		if err := m.store().EnsureSliceLeases(cctx, list); err != nil && ctx.Err() == nil {
			m.Log.Warn("lease: ensure slice leases", "err", err)
		}
	}
}

// notePrior remembers that a free row still names an owner (an expired lease, not a released one).
func (m *Manager) notePrior(r store.LeaseRow) {
	if m.priorOwner == nil {
		m.priorOwner = map[store.LeaseKey]bool{}
	}
	m.priorOwner[r.Key] = r.OwnerID != uuid.Nil
}

func (m *Manager) weightOf(t awsx.Target) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.weights[t]
}

// claimCall bounds a claim by the store's own deadline plus a margin, so a blocked claim ends with the database's
// answer (or the store's deadline) rather than a cut-off by this context.
func (m *Manager) claimCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, store.ClaimDeadline+2*time.Second)
}

// backedOff reports whether a recent blocked claim of k still holds it back.
func (m *Manager) backedOff(k store.LeaseKey) bool {
	until, ok := m.claimBackoff[k]
	return ok && time.Now().Before(until)
}

// noteClaim records how long a claim took: a blocked one backs the lease off for a few ticks and counts against the
// tick's budget of slow claims. It reports whether the budget is spent.
func (m *Manager) noteClaim(k store.LeaseKey, took time.Duration, claimed bool, slow *int) (spent bool) {
	if claimed || took < slowClaim {
		return false
	}
	if m.claimBackoff == nil {
		m.claimBackoff = map[store.LeaseKey]time.Time{}
	}
	m.claimBackoff[k] = time.Now().Add(4 * m.Timing.Tick)
	*slow++
	return *slow >= maxSlowClaims
}

// activePollers lists the live poller members for the fair share. A member whose heartbeat is older than two intervals
// and that owns nothing is a crashed poller whose row has not aged out yet; counting it would stall the survivors.
func (m *Manager) activePollers(members []store.Member, rows []store.LeaseRow) []uuid.UUID {
	owners := map[uuid.UUID]bool{}
	for _, r := range rows {
		if !r.Free && r.OwnerID != uuid.Nil {
			owners[r.OwnerID] = true
		}
	}
	var out []uuid.UUID
	for _, mem := range members {
		if !slices.Contains(mem.Roles, "poller") {
			continue
		}
		if mem.InstanceID != m.ID && mem.Age > 2*m.Timing.MemberHeartbeat && !owners[mem.InstanceID] {
			continue
		}
		out = append(out, mem.InstanceID)
	}
	return out
}

func (m *Manager) isHeld(k store.LeaseKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.held[k]
	return ok
}

func (m *Manager) start(holder Holder, l store.Lease, claimStart time.Time) {
	hctx, cancel := context.WithCancel(m.runCtx)
	h := &holding{lease: l, cancel: cancel, done: make(chan struct{}), renewedAt: claimStart}
	m.mu.Lock()
	m.held[l.Key] = h
	m.mu.Unlock()
	go func() {
		defer close(h.done)
		defer cancel()
		holder.Hold(hctx, l)
	}()
	m.event(l.Key.Kind, "acquired")
	if m.priorOwner[l.Key] {
		m.event(l.Key.Kind, "takeover")
	}
	delete(m.priorOwner, l.Key)
	m.Log.Info("lease: acquired", "kind", l.Key.Kind, "account", l.Key.AccountID, "region", l.Key.Region, "epoch", l.Epoch)
}

// claimSlices plans with the weighted fair share and claims or gives back. A claim that lost a race to another member
// is re-planned against fresh state, a few times per tick.
func (m *Manager) claimSlices(ctx context.Context) {
	mayGiveBack := m.lastGiveBack.IsZero() || time.Since(m.lastGiveBack) >= m.Timing.GiveBackEvery
	slowClaims := 0
	for range maxClaimPasses {
		cctx, cancel := m.call(ctx)
		rows, err := m.store().ListLeases(cctx)
		var members []store.Member
		if err == nil {
			members, err = m.store().LiveMembers(cctx, m.Timing.MemberLive)
		}
		if err == nil {
			var w map[awsx.Target]int
			if w, err = m.store().SliceWeights(cctx); err == nil {
				m.mu.Lock()
				m.weights = w
				m.mu.Unlock()
			}
		}
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				m.Log.Warn("lease: read leases", "err", err)
			}
			return
		}

		pollers := m.activePollers(members, rows)
		var states []SliceState
		for _, r := range rows {
			if r.Key.Kind != store.LeaseSlice {
				continue
			}
			t := awsx.Target{AccountID: r.Key.AccountID, Region: r.Key.Region}
			if m.targets != nil && !m.targets[t] {
				continue
			}
			owner := r.OwnerID
			if r.Free {
				m.notePrior(r)
				owner = uuid.Nil
			}
			if m.isHeld(r.Key) {
				owner = m.ID // still ours (maybe late renewing); never plan to claim it
			}
			states = append(states, SliceState{Key: r.Key, Weight: max(m.weightOf(t), 1), Owner: owner})
		}

		plan := Rebalance(m.ID, pollers, states, mayGiveBack)
		if plan.GiveBack != nil {
			m.mu.Lock()
			h := m.held[*plan.GiveBack]
			m.mu.Unlock()
			if h != nil {
				m.stopHolder(h)
				m.forget(h)
				m.release(ctx, h.lease, "rebalance")
				m.lastGiveBack = time.Now()
			}
			return
		}
		lost := false
		for _, k := range plan.Claim {
			if m.isHeld(k) || m.backedOff(k) || !m.gateHeld() || ctx.Err() != nil {
				continue
			}
			start := time.Now()
			cctx, cancel := m.claimCall(ctx)
			l, ok, err := m.store().ClaimLease(cctx, k, m.ID, m.Timing.TTL)
			cancel()
			switch {
			case err != nil:
				if ctx.Err() == nil {
					m.Log.Warn("lease: claim failed", "account", k.AccountID, "region", k.Region, "err", err)
				}
			case !ok:
				lost = true
			default:
				m.start(m.Slices, l, start)
			}
			if m.noteClaim(k, time.Since(start), ok, &slowClaims) {
				return // enough blocked claims for one tick; the rest wait for the next
			}
		}
		if !lost {
			return
		}
	}
}

func (m *Manager) claimCoordinator(ctx context.Context) {
	key := store.LeaseKey{Kind: store.LeaseCoordinator}
	if m.isHeld(key) || m.backedOff(key) || !m.gateHeld() {
		return
	}
	cctx, cancel := m.call(ctx)
	rows, err := m.store().ListLeases(cctx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Warn("lease: read coordinator lease", "err", err)
		}
		return
	}
	free := false
	for _, r := range rows {
		if r.Key == key && r.Free {
			free = true
			m.notePrior(r)
		}
	}
	if !free {
		return
	}
	start := time.Now()
	cctx, cancel = m.claimCall(ctx)
	l, ok, err := m.store().ClaimLease(cctx, key, m.ID, m.Timing.TTL)
	cancel()
	slow := 0
	m.noteClaim(key, time.Since(start), ok, &slow)
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Warn("lease: claim coordinator failed", "err", err)
		}
		return
	}
	if ok {
		m.start(m.Coordinator, l, start)
	}
}
