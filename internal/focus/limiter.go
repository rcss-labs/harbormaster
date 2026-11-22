package focus

import (
	"context"
	"sync"
	"time"
)

// Category is an AWS rate-limit category: calls in one category share a budget.
type Category string

const (
	ECSServiceRead         Category = "ecs_service_read"          // ListServices, DescribeServices, DescribeTaskDefinition
	ECSClusterResourceRead Category = "ecs_cluster_resource_read" // ListTasks, DescribeTasks
	ECSClusterRead         Category = "ecs_cluster_read"          // DescribeClusters
	ELB                    Category = "elb"                       // DescribeTargetGroups, DescribeTargetHealth
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter holds focus calls to a rate per category.
type Limiter struct {
	rate, burst float64
	now         func() time.Time
	// sleep waits d or until ctx ends; tests replace it to advance a fake clock.
	sleep func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	buckets map[Category]*bucket
}

// NewLimiter returns a limiter with one token bucket per category: callsPerSecond refill, burst of twice that.
func NewLimiter(callsPerSecond int, now func() time.Time) *Limiter {
	rate := float64(max(callsPerSecond, 1))
	return &Limiter{rate: rate, burst: 2 * rate, now: now, sleep: sleepContext, buckets: map[Category]*bucket{}}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Wait takes one token of the category, waiting until one is available or ctx ends.
func (l *Limiter) Wait(ctx context.Context, c Category) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait := l.take(c)
		if wait <= 0 {
			return nil
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// take removes a token and returns 0, or returns how long until one is available.
func (l *Limiter) take(c Category) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[c]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[c] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}
