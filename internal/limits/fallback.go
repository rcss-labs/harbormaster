package limits

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

type fallbackConcurrency struct {
	primary, memory Concurrency
	health          kv.Upper
	reg             *telemetry.Registry
}

// WithFallback uses primary while the store is up and memory while it is down, or for any single call where the
// store returns an error, so an outage never blocks or errors a user. A release goes to the backend that granted.
func WithFallback(primary, memory Concurrency, health kv.Upper, reg *telemetry.Registry) Concurrency {
	return &fallbackConcurrency{primary: primary, memory: memory, health: health, reg: reg}
}

func countDecision(reg *telemetry.Registry, limit string, allowed bool, backend string) {
	if reg == nil {
		return
	}
	result := "refused"
	if allowed {
		result = "allowed"
	}
	reg.CountLimitDecision(limit, result, backend)
}

func (f *fallbackConcurrency) Acquire(ctx context.Context, limit string, userID int64, max int) (func(), bool, error) {
	if f.health.Up() {
		release, ok, err := f.primary.Acquire(ctx, limit, userID, max)
		if err == nil {
			countDecision(f.reg, limit, ok, "kv")
			return release, ok, nil
		}
	}
	release, ok, err := f.memory.Acquire(ctx, limit, userID, max)
	if err == nil {
		countDecision(f.reg, limit, ok, "memory")
	}
	return release, ok, err
}

func (f *fallbackConcurrency) Count(ctx context.Context, limit string, userID int64) (int, error) {
	n, err := f.memory.Count(ctx, limit, userID)
	if err != nil {
		return 0, err
	}
	if f.health.Up() {
		if m, err := f.primary.Count(ctx, limit, userID); err == nil {
			n += m
		}
	}
	return n, nil
}

type fallbackRate struct {
	primary, memory Rate
	health          kv.Upper
	reg             *telemetry.Registry
}

// RateWithFallback is WithFallback for Rate. A Reserve counts as an allowed decision: it always grants a slot.
func RateWithFallback(primary, memory Rate, health kv.Upper, reg *telemetry.Registry) Rate {
	return &fallbackRate{primary: primary, memory: memory, health: health, reg: reg}
}

func (f *fallbackRate) Allow(ctx context.Context, name, key string, capacity int, refillPerSecond float64) (bool, error) {
	if f.health.Up() {
		if ok, err := f.primary.Allow(ctx, name, key, capacity, refillPerSecond); err == nil {
			countDecision(f.reg, name, ok, "kv")
			return ok, nil
		}
	}
	ok, err := f.memory.Allow(ctx, name, key, capacity, refillPerSecond)
	if err == nil {
		countDecision(f.reg, name, ok, "memory")
	}
	return ok, err
}

func (f *fallbackRate) Reserve(ctx context.Context, name, key string, every time.Duration) (time.Duration, error) {
	if f.health.Up() {
		if wait, err := f.primary.Reserve(ctx, name, key, every); err == nil {
			countDecision(f.reg, name, true, "kv")
			return wait, nil
		}
	}
	wait, err := f.memory.Reserve(ctx, name, key, every)
	if err == nil {
		countDecision(f.reg, name, true, "memory")
	}
	return wait, err
}
