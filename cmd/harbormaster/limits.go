package main

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/limits"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// buildConcurrency returns the per-user session limits: per task without a store, and estate-wide through it
// otherwise, falling back to per task while the store is down.
func buildConcurrency(health *kv.Health) limits.Concurrency {
	memory := limits.NewMemoryConcurrency()
	if health == nil {
		return memory
	}
	return limits.WithFallback(limits.NewKVConcurrency(health.Client()), memory, health, telemetry.Default)
}

// buildRates returns the token buckets and call pacers, wired like buildConcurrency.
func buildRates(health *kv.Health) limits.Rate {
	memory := limits.NewMemoryRate(time.Now)
	if health == nil {
		return memory
	}
	return limits.RateWithFallback(limits.NewKVRate(health.Client()), memory, health, telemetry.Default)
}
