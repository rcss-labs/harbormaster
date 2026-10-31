package poller

import (
	"math/rand/v2"
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
)

// minTickInterval keeps a misconfigured zero interval from spinning.
const minTickInterval = 10 * time.Millisecond

// tier is the polling cadence of one resource kind. base is the configured interval;
// cur is the interval in force (Task 15 raises it while backing off from throttling).
type tier struct {
	name      string
	base, cur time.Duration
}

func newTier(name string, base time.Duration) *tier {
	return &tier{name: name, base: base, cur: base}
}

// nextDelay is cur plus a uniform random offset in [-jitter, +jitter].
func (t *tier) nextDelay(jitter time.Duration) time.Duration {
	delay := t.cur
	if jitter > 0 {
		delay += time.Duration(rand.Int64N(int64(2*jitter)+1)) - jitter
	}
	return max(delay, minTickInterval)
}
