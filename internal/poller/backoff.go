package poller

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	maxBackoffInterval = 60 * time.Second
	// cleanCyclesToRecover is how many clean cycles halve a backed-off interval.
	cleanCyclesToRecover = 5
)

// onThrottle doubles the interval (capped at 60 s) and restarts the clean-cycle count.
func (t *tier) onThrottle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = min(t.cur*2, maxBackoffInterval)
	t.clean = 0
}

// onSuccess counts a clean cycle; every fifth one while backed off halves the interval toward base.
func (t *tier) onSuccess() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clean++
	if t.clean >= cleanCyclesToRecover && t.cur > t.base {
		t.cur = max(t.base, t.cur/2)
		t.clean = 0
	}
}

// throttled reports whether any tier is polling slower than its base interval.
func (l *Loop) throttled() bool {
	for _, d := range l.defs {
		d.tier.mu.Lock()
		backedOff := d.tier.cur > d.tier.base
		d.tier.mu.Unlock()
		if backedOff {
			return true
		}
	}
	return false
}

// errorsThrottled reports whether err, or any error joined into it, is an AWS throttle.
// errors.As alone is not enough: it stops at the first API error in a join.
func errorsThrottled(err error) bool {
	if err == nil {
		return false
	}
	if awsx.IsThrottle(err) {
		return true
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			if errorsThrottled(inner) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return errorsThrottled(e.Unwrap())
	}
	return false
}
