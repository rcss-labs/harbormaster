// Package poller keeps the database snapshot of the ECS estate current. One Harbormaster
// instance at a time (the leader, chosen by a Postgres advisory lock) polls AWS.
package poller

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// LockKey is the Postgres advisory lock key held by the leader ("HMPOLL").
const LockKey int64 = 0x484D504F4C4C

const (
	defaultRetryInterval  = 5 * time.Second
	defaultRetentionEvery = time.Hour
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
	Network, Storage                                                       time.Duration // plan 05
}

// Config is the poller's runtime configuration.
type Config struct {
	Intervals                                              Intervals
	CountsHistory, Jitter, RetentionEvery, AccountsRefresh time.Duration
	MaxInFlight                                            int
	Retention                                              store.Retention
	// AnalyticsUtil enables the daily CloudWatch utilisation rollup (HM_ANALYTICS_UTIL); read by the rollup runner.
	AnalyticsUtil bool
	// Telemetry receives loop timings, leadership and write counts (plan 06); nil means telemetry.Default.
	Telemetry *telemetry.Registry
	// StartStagger spaces the first cycles of the task tiers (runningTasks, stoppedTasks, fullTasks)
	// so a fresh loop does not drain ECS's cluster-resource-read bucket; zero disables staggering.
	StartStagger time.Duration
	// Scan launches and reconciles image scans; nil when scanning is off. It runs only while this instance leads.
	Scan *scanjob.Runner
	// OffHours reconciles off-hours scheduled actions and capacity; nil leaves it off. It runs only while this instance leads.
	OffHours *offhours.Reconciler
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
			Network:      c.Poll.Network, // plan 05
			Storage:      c.Poll.Storage, // plan 05
		},
		CountsHistory: c.Poll.CountsHistory,
		Jitter:        c.Poll.Jitter,
		MaxInFlight:   c.Poll.MaxInFlight,
		Retention: store.Retention{
			StoppedTasks:      c.Retention.StoppedTasks,
			ServiceEvents:     c.Retention.ServiceEvents,
			CountsHistory:     c.Retention.CountsHistory,
			OperationLog:      c.Retention.OperationLog,
			DeploymentHistory: c.Retention.DeploymentHistory,
			AuditLog:          c.Retention.AuditLog,
			OrphanTags:        c.Retention.OrphanTags,
			AvailabilityDaily: c.Retention.AvailabilityDaily,
			CapacityDaily:     c.Retention.CapacityDaily,
			UtilDaily:         c.Retention.UtilDaily,
		},
		AnalyticsUtil:   c.Analytics.Util,
		StartStagger:    defaultStartStagger,
		RetentionEvery:  defaultRetentionEvery,
		AccountsRefresh: defaultAccountsRefresh,
	}
}

// Poller elects a leader and, while leading, runs one polling loop per account x region.
type Poller struct {
	store   *store.Store
	clients awsx.Clients
	cfg     Config
	log     *slog.Logger
	// rollups computes the daily analytics tables; it runs in the retention tick, before retention deletes sources.
	rollups *rollup.Runner

	leader atomic.Bool

	// Unexported so tests can isolate themselves from other lanes sharing one Postgres.
	lockKey       int64
	retryInterval time.Duration
	// loopFactory runs one target's polling loop until ctx is cancelled.
	loopFactory func(ctx context.Context, t awsx.Target)
}

// New creates a Poller. Call Run to start electing and polling.
func New(st *store.Store, clients awsx.Clients, cfg Config, log *slog.Logger) *Poller {
	if cfg.RetentionEvery <= 0 {
		cfg.RetentionEvery = defaultRetentionEvery
	}
	if cfg.AccountsRefresh <= 0 {
		cfg.AccountsRefresh = defaultAccountsRefresh
	}
	if cfg.Telemetry == nil {
		cfg.Telemetry = telemetry.Default
	}
	p := &Poller{
		store: st, clients: clients, cfg: cfg, log: log,
		lockKey: LockKey, retryInterval: defaultRetryInterval,
	}
	p.loopFactory = p.defaultLoopFactory
	p.rollups = &rollup.Runner{
		Store: st, Clients: clients, Now: time.Now, UtilEnabled: cfg.AnalyticsUtil,
		SampleInterval: cfg.CountsHistory, CountsRetention: cfg.Retention.CountsHistory, Log: log,
	}
	return p
}

// IsLeader reports whether this instance currently holds the leader lock.
func (p *Poller) IsLeader() bool { return p.leader.Load() }

// defaultLoopFactory runs the real per-target polling loop until ctx is cancelled.
func (p *Poller) defaultLoopFactory(ctx context.Context, t awsx.Target) {
	newLoop(p, t).Run(ctx)
}
