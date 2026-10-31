package poller

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Tier names, in the order runOnce executes them.
const (
	tierClusters     = "clusters"
	tierServiceList  = "serviceList"
	tierServices     = "services"
	tierRunningTasks = "runningTasks"
	tierStoppedTasks = "stoppedTasks"
	tierFullTasks    = "fullTasks"
	tierTargetHealth = "target_health"
	tierScaling      = "scaling"
	tierAlerts       = "alerts"
)

// minTickInterval keeps a misconfigured zero interval from spinning.
const minTickInterval = 10 * time.Millisecond

// tier is the polling cadence of one resource kind. base is the configured interval;
// cur is the interval in force (raised while backing off from throttling). The tier's own
// goroutine mutates it; mu lets Loop.throttled read it from elsewhere.
type tier struct {
	name      string
	base, cur time.Duration
	clean     int // consecutive successful cycles since the last throttle or halving

	mu sync.Mutex
	// Status of the latest cycles, recorded by Loop.recordCycle.
	failures  int // consecutive non-throttle failures
	lastErr   string
	lastOK    *time.Time
	reported  bool // the services tier has reported its health at least once
	wasFailed bool
}

func newTier(name string, base time.Duration) *tier {
	return &tier{name: name, base: base, cur: base}
}

// nextDelay is cur plus a uniform random offset in [-jitter, +jitter].
func (t *tier) nextDelay(jitter time.Duration) time.Duration {
	t.mu.Lock()
	delay := t.cur
	t.mu.Unlock()
	if jitter > 0 {
		delay += time.Duration(rand.Int64N(int64(2*jitter)+1)) - jitter
	}
	return max(delay, minTickInterval)
}
