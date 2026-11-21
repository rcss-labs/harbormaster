package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/signal"
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
