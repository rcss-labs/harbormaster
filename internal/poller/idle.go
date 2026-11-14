package poller

import (
	"context"
	"sync"
	"time"
)

// defaultIdleCheckEvery is how often the leader re-reads session activity. It bounds how long a
// returning user waits for fresh data: a tier wakes at the next check after activity resumes.
const defaultIdleCheckEvery = 30 * time.Second

// idleState is the poller's idle flag plus a broadcast channel that is closed on every
// idle -> active transition, waking tiers that are sleeping out a stretched idle interval.
type idleState struct {
	mu   sync.Mutex
	idle bool
	wake chan struct{}
}

func newIdleState() *idleState { return &idleState{wake: make(chan struct{})} }

func (s *idleState) isIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle
}

// snapshot returns the flag and the channel that the next idle -> active transition closes.
func (s *idleState) snapshot() (idle bool, wake <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idle, s.wake
}

// set records the state and reports whether it changed. Going active wakes every waiter.
func (s *idleState) set(idle bool) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idle == idle {
		return false
	}
	s.idle = idle
	if !idle {
		close(s.wake)
		s.wake = make(chan struct{})
	}
	return true
}

// idleEnabled reports whether idle mode is configured on.
func (p *Poller) idleEnabled() bool { return p.cfg.IdleAfter > 0 }

// checkIdle reads session activity and updates the idle state. Idle means no unexpired session was
// seen for IdleAfter (or none exists). A database error fails toward active so polling never
// slows down because of a monitoring problem.
func (p *Poller) checkIdle(ctx context.Context) {
	now := p.now()
	last, ok, err := p.sessionActivity(ctx, now)
	idle := false
	switch {
	case err != nil:
		if ctx.Err() == nil {
			p.log.Warn("poller: read session activity, polling at normal speed", "err", err)
		}
	case !ok:
		idle = true
	default:
		idle = now.Sub(last) >= p.cfg.IdleAfter
	}
	p.applyIdle(idle)
}

func (p *Poller) applyIdle(idle bool) {
	if !p.idle.set(idle) {
		return
	}
	p.cfg.Telemetry.SetPollerIdle(idle)
	if idle {
		p.log.Info("poller: idle, slowing AWS polling", "idle_after", p.cfg.IdleAfter, "interval", p.cfg.IdleInterval)
	} else {
		p.log.Info("poller: activity resumed, polling at normal speed")
	}
}

// runIdleMonitor keeps the idle state current until ctx ends, then resets it to active (the
// supervisor, and with it this monitor, runs only on the leader). It does nothing when idle mode is off.
func (p *Poller) runIdleMonitor(ctx context.Context) {
	if !p.idleEnabled() {
		return
	}
	defer p.applyIdle(false)
	p.checkIdle(ctx)
	ticker := time.NewTicker(p.idleCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkIdle(ctx)
		}
	}
}
