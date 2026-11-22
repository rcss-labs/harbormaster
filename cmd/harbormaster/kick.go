package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// kickPublishTimeout bounds one publish: a request that saves a schedule must not wait on a slow store.
const kickPublishTimeout = 250 * time.Millisecond

// kickPublisher wakes the coordinator's jobs from a web task by publishing their names on the kick channel. The
// coordinator that holds the lease listens (internal/coordinator). A publish that fails, or that nobody hears, is only
// logged at debug level: the job still runs at its interval, so the request itself never fails.
type kickPublisher struct {
	bus signal.Bus
	log *slog.Logger
}

func (k *kickPublisher) publish(job string) {
	ctx, cancel := context.WithTimeout(context.Background(), kickPublishTimeout)
	defer cancel()
	if err := k.bus.Publish(ctx, signal.Kick, job); err != nil {
		k.log.Debug("kick not delivered; the job runs at its interval", "job", job, "err", err)
	}
}

// Kick wakes the scan loop; it makes the publisher an api.ScanKicker.
func (k *kickPublisher) Kick()         { k.publish("scan") }
func (k *kickPublisher) KickOffHours() { k.publish("offhours") }
func (k *kickPublisher) KickNotify()   { k.publish("notify") }

// newKickBus is the bus kicks travel on: the shared store while it is up, memory otherwise. Without a store it is memory
// only, which is what a single task (role all) needs, since the web side and the coordinator share the process.
func newKickBus(health *kv.Health) signal.Bus {
	memory := signal.NewMemoryBus()
	if health == nil {
		return memory
	}
	return signal.WithFallback(signal.NewKVBus(health.Client()), memory, health)
}

// buildFocusRegistry is where focused pages are registered: the shared store when one is configured, memory otherwise.
func buildFocusRegistry(health *kv.Health) focus.Registry {
	if health == nil {
		return focus.NewMemoryRegistry(time.Now)
	}
	return focus.NewKVRegistry(health.Client())
}

// buildSnapshotCache is the shared store's side of the snapshot cache, or nil when there is no store or the cache is
// switched off (HM_SNAPSHOT_CACHE=false). Both the poller (tokens) and the web side (models) build it the same way.
func buildSnapshotCache(health *kv.Health, cfg config.Config) snapcache.Cache {
	if health == nil || !cfg.SnapshotCache {
		return nil
	}
	return snapcache.NewKV(health.Client())
}

// buildSnapshotReader is the web side of the snapshot cache, or nil when there is no cache (the handlers then read
// Postgres as they always did).
func buildSnapshotReader(health *kv.Health, cfg config.Config, st *store.Store, log *slog.Logger) *snapcache.Reader {
	cache := buildSnapshotCache(health, cfg)
	if cache == nil {
		return nil
	}
	return &snapcache.Reader{Cache: cache, Store: st, TTL: cfg.SnapshotCacheTTL, Telemetry: telemetry.Default, Log: log, Up: health.Up}
}
