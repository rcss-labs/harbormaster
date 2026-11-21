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
	mu       sync.Mutex
	held     map[store.LeaseKey]*holding
	lastTick atomic.Int64

	// owned by the tick goroutine
	started      time.Time
	lastBeat     time.Time
	lastTargets  time.Time
	lastGiveBack time.Time
	targets      map[awsx.Target]bool // nil until the first successful refresh
	weights      map[awsx.Target]int
}

// holding is one lease this process holds and the holder running under it.
type holding struct {
	lease  store.Lease
	cancel context.CancelFunc
	done   chan struct{} // closed when Hold returned

	// guarded by Manager.mu
	renewedAt time.Time // start of the last successful claim or renewal, monotonic
	fenced    bool      // the self-fence cancelled the holder
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
		out = append(out, h.lease)
	}
	slices.SortFunc(out, func(a, b store.Lease) int { return compareKeys(a.Key, b.Key) })
	return out
}

// HoldsCoordinator reports whether this process holds the coordinator lease.
func (m *Manager) HoldsCoordinator() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.held[store.LeaseKey{Kind: store.LeaseCoordinator}]
	return ok
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

	timer := time.NewTicker(m.Timing.Tick)
	defer timer.Stop()
	for {
		var gateChanged <-chan struct{}
		if m.Gate != nil {
			gateChanged = m.Gate.Changed() // taken before the tick, so a change during it is not missed
		}
		m.tick(ctx)
		m.lastTick.Store(time.Now().UnixNano())
		select {
		case <-ctx.Done():
			m.shutdown()
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
	m.renewAll(ctx)
	m.heartbeat(ctx)
	m.refreshTargets(ctx)
	if m.Slices != nil {
		m.claimSlices(ctx)
	}
	if m.Coordinator != nil {
		m.claimCoordinator(ctx)
	}
}

// reapFinished forgets and releases leases whose holder has already returned (it decided to stop by itself, or the
// self-fence cancelled it), so the slice is not left renewed with nobody working on it.
func (m *Manager) reapFinished(ctx context.Context) {
	for _, h := range m.snapshotHeld() {
		select {
		case <-h.done:
			m.forget(h)
			m.release(ctx, h.lease, "holder stopped")
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
	m.Log.Info("lease: released", "why", why, "kind", l.Key.Kind, "account", l.Key.AccountID, "region", l.Key.Region, "epoch", l.Epoch)
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

// renewAll renews every lease whose last renewal is older than RenewEvery. A lost lease stops only its own holder.
func (m *Manager) renewAll(ctx context.Context) {
	for _, h := range m.snapshotHeld() {
		m.mu.Lock()
		due := time.Since(h.renewedAt) >= m.Timing.RenewEvery
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
			m.Log.Warn("lease: lost", "kind", h.lease.Key.Kind, "account", h.lease.Key.AccountID,
				"region", h.lease.Key.Region, "epoch", h.lease.Epoch)
			m.stopHolder(h)
			m.forget(h)
		default:
			if ctx.Err() == nil {
				m.Log.Warn("lease: renewal failed, will retry", "kind", h.lease.Key.Kind,
					"account", h.lease.Key.AccountID, "region", h.lease.Key.Region, "epoch", h.lease.Epoch, "err", err)
			}
		}
	}
}

func (m *Manager) heartbeat(ctx context.Context) {
	if !m.lastBeat.IsZero() && time.Since(m.lastBeat) < m.Timing.MemberHeartbeat {
		return
	}
	slicesHeld, weight := 0, 0
	for _, h := range m.snapshotHeld() {
		if h.lease.Key.Kind == store.LeaseSlice {
			slicesHeld++
			weight += max(m.weights[awsx.Target{AccountID: h.lease.Key.AccountID, Region: h.lease.Key.Region}], 1)
		}
	}
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
	if _, err := m.store().DeleteStaleMembers(cctx, memberStaleAfter); err != nil && ctx.Err() == nil {
		m.Log.Warn("lease: delete stale members", "err", err)
	}
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
	m.Log.Info("lease: acquired", "kind", l.Key.Kind, "account", l.Key.AccountID, "region", l.Key.Region, "epoch", l.Epoch)
}

// claimSlices plans with the weighted fair share and claims or gives back. A claim that lost a race to another member
// is re-planned against fresh state, a few times per tick.
func (m *Manager) claimSlices(ctx context.Context) {
	mayGiveBack := m.lastGiveBack.IsZero() || time.Since(m.lastGiveBack) >= m.Timing.GiveBackEvery
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
				m.weights = w
			}
		}
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				m.Log.Warn("lease: read leases", "err", err)
			}
			return
		}

		var pollers []uuid.UUID
		for _, mem := range members {
			if slices.Contains(mem.Roles, "poller") {
				pollers = append(pollers, mem.InstanceID)
			}
		}
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
				owner = uuid.Nil
			}
			if m.isHeld(r.Key) {
				owner = m.ID // still ours (maybe late renewing); never plan to claim it
			}
			states = append(states, SliceState{Key: r.Key, Weight: max(m.weights[t], 1), Owner: owner})
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
			if m.isHeld(k) || !m.gateHeld() || ctx.Err() != nil {
				continue
			}
			start := time.Now()
			cctx, cancel := m.call(ctx)
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
		}
		if !lost {
			return
		}
	}
}

func (m *Manager) claimCoordinator(ctx context.Context) {
	key := store.LeaseKey{Kind: store.LeaseCoordinator}
	if m.isHeld(key) || !m.gateHeld() {
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
		}
	}
	if !free {
		return
	}
	start := time.Now()
	cctx, cancel = m.call(ctx)
	l, ok, err := m.store().ClaimLease(cctx, key, m.ID, m.Timing.TTL)
	cancel()
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
