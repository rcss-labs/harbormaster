// Package poller keeps the database snapshot of the ECS estate current. Each account x region (a slice) is polled by
// the one instance that holds its lease; every write is fenced by the lease's epoch.
package poller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	defaultRetryInterval = 5 * time.Second
	// defaultStartStagger spaces the task tiers' first cycles: 1 s refills 20 tokens of ECS's cluster-resource-read bucket.
	defaultStartStagger    = time.Second
	defaultAccountsRefresh = 30 * time.Second
)

// alertsInterval is fixed: the alerts tier reads only the database, so it is not configurable.
const alertsInterval = 5 * time.Second

// Intervals are the per-resource polling periods.
type Intervals struct {
	Services, RunningTasks, FullTasks, StoppedTasks, Clusters, ServiceList time.Duration
	TargetHealth, Scaling, Alerts                                          time.Duration
	Network, Storage                                                       time.Duration
}

// Config is the poller's runtime configuration.
type Config struct {
	Intervals Intervals
	// IdleAfter is how long without session activity before polling goes idle; 0 disables idle mode.
	// While idle every tier waits at least IdleInterval between runs.
	IdleAfter, IdleInterval                time.Duration
	CountsHistory, Jitter, AccountsRefresh time.Duration
	MaxInFlight                            int
	// Telemetry receives loop timings, lease ownership and write counts; nil means telemetry.Default.
	Telemetry *telemetry.Registry
	// StartStagger spaces the first cycles of the task tiers (runningTasks, stoppedTasks, fullTasks)
	// so a fresh loop does not drain ECS's cluster-resource-read bucket; zero disables staggering.
	StartStagger time.Duration
	// Coordinator runs the run-once jobs while this process holds the coordinator lease; nil means this process does not
	// coordinate (role=poller).
	Coordinator lease.Holder

	// NoSlices builds a manager that owns no slices (role=coordinator): it keeps its member row and competes for the
	// coordinator lease only. The zero value polls slices.
	NoSlices bool

	// InstanceID identifies this process in poll_leases and poll_members; zero means a new one per Run.
	InstanceID uuid.UUID
	// Version is reported in poll_members.
	Version string
	// Roles are this process's roles in poll_members; empty means web, poller and coordinator (role=all).
	Roles []string
	// LegacyGate makes the lease manager claim only while it holds the old leader key in shared mode
	// (HM_LEGACY_LEADER_GATE), so old and new versions never both act during an upgrade or a rollback.
	LegacyGate bool

	// Focus sets the live refresh of pages that are open: the interval, and the AWS call budget per slice. A zero
	// interval means 5 s and a zero MaxCallsPerSec means 5.
	Focus config.FocusConfig
	// Registry and Bus carry live page refresh: the objects that are open, and the poll-now signals. A nil Registry
	// turns focus off for this process.
	Registry focus.Registry
	Bus      signal.Bus
	// Cache receives a generation token whenever a cluster's rows change (see generation.go); nil means no snapshot cache.
	Cache snapcache.Cache
}

// ConfigFrom derives the poller configuration from the server configuration.
func ConfigFrom(c config.Config) Config {
	return Config{
		Intervals: Intervals{
			Services:     c.Poll.Services,
			RunningTasks: c.Poll.RunningTasks,
			FullTasks:    c.Poll.FullTasks,
			StoppedTasks: c.Poll.StoppedTasks,
			Clusters:     c.Poll.Clusters,
			ServiceList:  c.Poll.ServiceList,
			TargetHealth: c.Poll.TargetHealth,
			Scaling:      c.Poll.Scaling,
			Alerts:       alertsInterval,
			Network:      c.Poll.Network,
			Storage:      c.Poll.Storage,
		},
		IdleAfter:       c.Poll.IdleAfter,
		IdleInterval:    c.Poll.IdleInterval,
		CountsHistory:   c.Poll.CountsHistory,
		Jitter:          c.Poll.Jitter,
		MaxInFlight:     c.Poll.MaxInFlight,
		LegacyGate:      c.LegacyLeaderGate,
		Focus:           c.Focus,
		StartStagger:    defaultStartStagger,
		AccountsRefresh: defaultAccountsRefresh,
	}
}

// Poller claims slice leases and runs one polling loop per slice it holds, plus the run-once jobs while it holds the
// coordinator lease.
type Poller struct {
	// store is the concrete store: the lease machinery, the gate's connection and the slice writers come from it. The
	// polling loops never touch it; they read through reads, which has no write methods.
	store   *store.Store
	reads   readStore
	clients awsx.Clients
	cfg     Config
	log     *slog.Logger
	mgrMu   sync.Mutex
	manager *lease.Manager

	// idle slows every tier while nobody uses the UI; the idle monitor maintains it.
	idle *idleState
	// sessionActivity reads the newest activity of unexpired sessions; tests replace it.
	sessionActivity func(ctx context.Context, now time.Time) (time.Time, bool, error)
	now             func() time.Time
	idleCheckEvery  time.Duration

	// Unexported so tests can isolate themselves from other lanes sharing one Postgres.
	lockKey       int64
	retryInterval time.Duration
	timing        lease.Timing
	// loopFactory runs one slice's polling loop until ctx is cancelled; w is fenced by the slice's lease.
	loopFactory func(ctx context.Context, t awsx.Target, w *store.SliceWriter)

	// loops are the slice loops this process runs now, for the focus manager to route to.
	loopsMu sync.Mutex
	loops   map[awsx.Target]*Loop
	// focusReadEvery is how often the focus manager reads the registry; tests shorten it.
	focusReadEvery time.Duration
}

// New creates a Poller. Call Run to start claiming leases and polling.
func New(st *store.Store, clients awsx.Clients, cfg Config, log *slog.Logger) *Poller {
	if cfg.AccountsRefresh <= 0 {
		cfg.AccountsRefresh = defaultAccountsRefresh
	}
	if cfg.Telemetry == nil {
		cfg.Telemetry = telemetry.Default
	}
	p := &Poller{
		store: st, reads: st, clients: clients, cfg: cfg, log: log,
		lockKey: lease.LegacyLockKey, retryInterval: defaultRetryInterval, timing: lease.DefaultTiming(),
		idle: newIdleState(), now: time.Now, idleCheckEvery: defaultIdleCheckEvery,
		loops: map[awsx.Target]*Loop{}, focusReadEvery: time.Second,
	}
	p.sessionActivity = func(ctx context.Context, now time.Time) (time.Time, bool, error) {
		return p.store.LastSessionActivity(ctx, now)
	}
	p.loopFactory = p.defaultLoopFactory
	return p
}

// defaultLoopFactory runs the real per-slice polling loop until ctx is cancelled.
func (p *Poller) defaultLoopFactory(ctx context.Context, t awsx.Target, w *store.SliceWriter) {
	l := newLoop(p, t, w)
	defer p.registerLoop(l)()
	l.Run(ctx)
}

// IsIdle reports whether polling is in idle mode (nobody uses the UI).
func (p *Poller) IsIdle() bool { return p.idle.isIdle() }

// Manager returns the lease manager of the running poller, nil before Run starts.
func (p *Poller) Manager() *lease.Manager {
	p.mgrMu.Lock()
	defer p.mgrMu.Unlock()
	return p.manager
}

// Run claims and polls slices until ctx is cancelled: it holds the legacy gate (when configured), runs the lease
// manager, which starts one loop per held slice and the run-once jobs under the coordinator lease, and runs the idle
// monitor. On return every loop has stopped, every lease is released and the gate is dropped.
func (p *Poller) Run(ctx context.Context) error {
	id := p.cfg.InstanceID
	if id == uuid.Nil {
		id = uuid.New()
	}
	roles := p.cfg.Roles
	if len(roles) == 0 {
		roles = []string{"web", "poller", "coordinator"}
	}

	// The gate outlives the manager: leases are released while the shared key is still held.
	var gate *lease.Gate
	var gateDone chan struct{}
	gateCtx, stopGate := context.WithCancel(context.WithoutCancel(ctx))
	defer stopGate()
	if p.cfg.LegacyGate {
		gate = lease.NewGate(p.store.Pool, p.lockKey, p.retryInterval, p.log)
		gateDone = make(chan struct{})
		go func() { defer close(gateDone); gate.Run(gateCtx) }()
	}

	m := &lease.Manager{
		Store: p.store, ID: id, Roles: roles, Version: p.cfg.Version, Timing: p.timing, Gate: gate,
		Coordinator: p.cfg.Coordinator,
		Targets:     p.targets, TargetsEvery: p.cfg.AccountsRefresh, Telemetry: p.cfg.Telemetry, Log: p.log,
		Gauges: p.focusObjectCount,
	}
	if !p.cfg.NoSlices {
		m.Slices = lease.HolderFunc(p.holdSlice)
	}
	p.mgrMu.Lock()
	p.manager = m
	p.mgrMu.Unlock()

	var focusDone sync.WaitGroup
	if p.cfg.Registry != nil && !p.cfg.NoSlices {
		focusDone.Add(1)
		go func() {
			defer focusDone.Done()
			p.runFocusManager(ctx)
		}()
	}

	var idleDone sync.WaitGroup
	idleDone.Add(1)
	go func() {
		defer idleDone.Done()
		p.runIdleMonitor(ctx)
	}()

	err := m.Run(ctx)
	idleDone.Wait()
	focusDone.Wait()
	stopGate()
	if gateDone != nil {
		<-gateDone
	}
	return err
}
