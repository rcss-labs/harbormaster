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
	"github.com/rcss-labs/harbormaster/internal/store"
)

// LockKey is the Postgres advisory lock key held by the leader ("HMPOLL").
const LockKey int64 = 0x484D504F4C4C

const (
	defaultRetryInterval   = 5 * time.Second
	defaultRetentionEvery  = time.Hour
	defaultAccountsRefresh = 30 * time.Second
)

// Intervals are the per-resource polling periods.
type Intervals struct {
	Services, RunningTasks, FullTasks, StoppedTasks, Clusters, ServiceList time.Duration
}

// Config is the poller's runtime configuration.
type Config struct {
	Intervals                                              Intervals
	CountsHistory, Jitter, RetentionEvery, AccountsRefresh time.Duration
	MaxInFlight                                            int
	Retention                                              store.Retention
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
		},
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

	leader atomic.Bool

	// Unexported so tests can isolate themselves from other lanes sharing one Postgres.
	lockKey       int64
	retryInterval time.Duration
	// loopFactory runs one target's polling loop until ctx is cancelled.
	// Task 14 replaces defaultLoopFactory with the real per-target Loop.
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
	p := &Poller{
		store: st, clients: clients, cfg: cfg, log: log,
		lockKey: LockKey, retryInterval: defaultRetryInterval,
	}
	p.loopFactory = p.defaultLoopFactory
	return p
}

// IsLeader reports whether this instance currently holds the leader lock.
func (p *Poller) IsLeader() bool { return p.leader.Load() }

// defaultLoopFactory is a placeholder until Task 14 adds the real per-target Loop: it idles until cancelled.
func (p *Poller) defaultLoopFactory(ctx context.Context, t awsx.Target) {
	<-ctx.Done()
}
