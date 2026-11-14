package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

var idleNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// newIdlePoller builds a poller without a database: activity is whatever the test says.
func newIdlePoller(t *testing.T, activity func(context.Context, time.Time) (time.Time, bool, error)) *Poller {
	t.Helper()
	cfg := Config{IdleAfter: 10 * time.Minute, IdleInterval: 5 * time.Minute, Telemetry: telemetry.New()}
	p := New(nil, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.now = func() time.Time { return idleNow }
	p.sessionActivity = activity
	return p
}

func activityAt(last time.Time, ok bool, err error) func(context.Context, time.Time) (time.Time, bool, error) {
	return func(context.Context, time.Time) (time.Time, bool, error) { return last, ok, err }
}

func TestCheckIdle_Transitions(t *testing.T) {
	tests := []struct {
		name     string
		last     time.Time
		ok       bool
		err      error
		wantIdle bool
	}{
		{"recent activity is active", idleNow.Add(-time.Minute), true, nil, false},
		{"exactly at threshold is idle", idleNow.Add(-10 * time.Minute), true, nil, true},
		{"old activity is idle", idleNow.Add(-time.Hour), true, nil, true},
		{"no sessions is idle", time.Time{}, false, nil, true},
		{"database error fails toward active", time.Time{}, false, errors.New("db down"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newIdlePoller(t, activityAt(tc.last, tc.ok, tc.err))
			p.checkIdle(context.Background())
			if got := p.idle.isIdle(); got != tc.wantIdle {
				t.Errorf("idle = %v, want %v", got, tc.wantIdle)
			}
			if got := p.cfg.Telemetry.Snapshot().Idle; got != tc.wantIdle {
				t.Errorf("telemetry idle = %v, want %v", got, tc.wantIdle)
			}
		})
	}
}

func TestCheckIdle_ErrorWhileIdleReturnsToActive(t *testing.T) {
	p := newIdlePoller(t, activityAt(time.Time{}, false, nil))
	p.checkIdle(context.Background())
	if !p.idle.isIdle() {
		t.Fatal("setup: want idle")
	}
	p.sessionActivity = activityAt(time.Time{}, false, errors.New("db down"))
	p.checkIdle(context.Background())
	if p.idle.isIdle() {
		t.Error("still idle after a database error, want active")
	}
}

func TestTierDelay_IdleFloorsAtIdleInterval(t *testing.T) {
	p := newIdlePoller(t, activityAt(time.Time{}, false, nil))
	p.cfg.Jitter = 0
	l := newLoop(p, loopTarget)
	fast := newTier("fast", 10*time.Second)
	slow := newTier("slow", 20*time.Minute)
	backedOff := newTier("backedOff", 10*time.Second)
	backedOff.cur = 8 * time.Minute // throttle backoff above the idle interval

	if d, _ := l.tierDelay(fast); d != 10*time.Second {
		t.Errorf("active fast delay = %v, want 10s", d)
	}
	p.checkIdle(context.Background()) // no sessions: idle
	if d, wake := l.tierDelay(fast); d != 5*time.Minute || wake == nil {
		t.Errorf("idle fast delay = %v wake=%v, want 5m and a wake channel", d, wake)
	}
	if d, wake := l.tierDelay(slow); d != 20*time.Minute || wake != nil {
		t.Errorf("idle slow delay = %v wake=%v, want 20m and no wake channel", d, wake)
	}
	if d, wake := l.tierDelay(backedOff); d != 8*time.Minute || wake != nil {
		t.Errorf("idle backed-off delay = %v wake=%v, want 8m and no wake channel", d, wake)
	}
}

func TestTickTier_WakesPromptlyWhenActivityReturns(t *testing.T) {
	p := newIdlePoller(t, activityAt(time.Time{}, false, nil))
	p.cfg.Jitter = 0
	p.cfg.StartStagger = 0
	l := newLoop(p, loopTarget)
	p.checkIdle(context.Background()) // idle

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	due := make(chan bool, 1)
	go func() { due <- l.waitNextRun(ctx, newTier(tierServices, 10*time.Millisecond)) }()

	select {
	case <-due:
		t.Fatal("tier became due while idle (5m floor)")
	case <-time.After(150 * time.Millisecond):
	}

	p.sessionActivity = activityAt(idleNow, true, nil)
	p.checkIdle(context.Background()) // idle -> active
	select {
	case ok := <-due:
		if !ok {
			t.Error("waitNextRun = false after wake, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tier did not wake after idle -> active")
	}
}

func TestRunIdleMonitor_ChecksImmediatelyAndResetsOnExit(t *testing.T) {
	p := newIdlePoller(t, activityAt(time.Time{}, false, nil))
	p.idleCheckEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.runIdleMonitor(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !p.idle.isIdle() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.idle.isIdle() {
		t.Fatal("monitor never marked idle")
	}
	cancel()
	<-done
	if p.idle.isIdle() {
		t.Error("idle state not reset on exit (leadership lost)")
	}
}
