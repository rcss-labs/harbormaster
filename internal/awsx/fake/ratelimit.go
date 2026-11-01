package fake

import (
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// plan 06: per-target rate limiting, call counting and in-flight tracking.

type tokenBucket struct {
	refillPerSecond float64
	burst           float64
	tokens          float64
	last            time.Time
}

type flight struct{ current, max int }

// SetRateLimit gives every API of the target a shared token bucket that starts
// full: burst calls pass at once, then refillPerSecond calls per second. A
// call without a token fails with ThrottlingException. refillPerSecond <= 0
// removes the limit.
func (f *Fake) SetRateLimit(target awsx.Target, refillPerSecond float64, burst int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if refillPerSecond <= 0 {
		delete(f.limits, target)
		return
	}
	b := float64(max(burst, 1))
	f.limits[target] = &tokenBucket{refillPerSecond: refillPerSecond, burst: b, tokens: b, last: time.Now()}
}

// takeRateToken spends a token of the target's bucket, if it has a limit.
func (f *Fake) takeRateToken(t awsx.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.limits[t]
	if !ok {
		return nil
	}
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.refillPerSecond)
	b.last = now
	if b.tokens < 1 {
		return throttlingError()
	}
	b.tokens--
	return nil
}

// enter counts the call, marks it in flight for the target and waits CallLatency.
// The returned function ends the in-flight mark.
func (f *Fake) enter(t awsx.Target, service, op string) func() {
	f.mu.Lock()
	f.calls[op]++
	f.callCounts[strings.Join([]string{t.AccountID, t.Region, service, op}, "|")]++
	fl := f.inFlight[t]
	if fl == nil {
		fl = &flight{}
		f.inFlight[t] = fl
	}
	fl.current++
	fl.max = max(fl.max, fl.current)
	latency := f.CallLatency
	f.mu.Unlock()
	if latency > 0 {
		time.Sleep(latency)
	}
	return func() {
		f.mu.Lock()
		fl.current--
		f.mu.Unlock()
	}
}

// CallCounts returns a copy of the call counters keyed
// "account|region|service|operation" (throttled calls count). See begin in
// fake.go for the service names.
func (f *Fake) CallCounts() map[string]uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]uint64, len(f.callCounts))
	for k, v := range f.callCounts {
		out[k] = v
	}
	return out
}

// ResetCallCounts clears CallCounts and restarts MaxInFlight from the calls in flight now.
func (f *Fake) ResetCallCounts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCounts = map[string]uint64{}
	for _, fl := range f.inFlight {
		fl.max = fl.current
	}
}

// MaxInFlight is the highest number of concurrent calls observed for the target.
func (f *Fake) MaxInFlight(target awsx.Target) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl := f.inFlight[target]; fl != nil {
		return fl.max
	}
	return 0
}
