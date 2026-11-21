package poller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// runnerSpy stands in for the notification dispatcher: it counts starts and stops.
type runnerSpy struct{ started, stopped atomic.Int32 }

func (r *runnerSpy) Run(ctx context.Context) {
	r.started.Add(1)
	<-ctx.Done()
	r.stopped.Add(1)
}

func TestSupervisor_RunsNotifyOnlyWhileLeading(t *testing.T) {
	st := storetest.Open(t)
	lock := randomLockKey()

	leaderSpy, followerSpy := &runnerSpy{}, &runnerSpy{}
	leader := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, Notify: leaderSpy}, lock)
	leader.loopFactory = newLoopRecorder().factory
	startRun(t, leader)
	waitFor(t, 3*time.Second, "the first poller to lead", leader.IsLeader)
	waitFor(t, 3*time.Second, "the leader to start the dispatcher", func() bool { return leaderSpy.started.Load() == 1 })

	follower := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, Notify: followerSpy}, lock)
	follower.loopFactory = newLoopRecorder().factory
	startRun(t, follower)
	time.Sleep(300 * time.Millisecond)
	if follower.IsLeader() || followerSpy.started.Load() != 0 {
		t.Fatalf("the follower started the dispatcher (leader=%v, starts=%d)", follower.IsLeader(), followerSpy.started.Load())
	}
}

func TestConfigFrom_MapsNotificationLogRetention(t *testing.T) {
	cfg := ConfigFrom(config.Config{Retention: config.RetentionConfig{NotificationLog: 30 * 24 * time.Hour}})
	if cfg.Retention != (store.Retention{NotificationLog: 30 * 24 * time.Hour}) {
		t.Fatalf("Retention = %+v, want NotificationLog carried over", cfg.Retention)
	}
}
