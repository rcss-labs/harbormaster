package poller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// loopRecorder is a loop factory that records which targets currently have a running loop.
type loopRecorder struct {
	mu     sync.Mutex
	active map[awsx.Target]int
}

func newLoopRecorder() *loopRecorder { return &loopRecorder{active: map[awsx.Target]int{}} }

func (r *loopRecorder) factory(ctx context.Context, t awsx.Target) {
	r.mu.Lock()
	r.active[t]++
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.active[t]--
	r.mu.Unlock()
}

func (r *loopRecorder) targets() map[awsx.Target]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[awsx.Target]bool{}
	for t, n := range r.active {
		if n > 0 {
			out[t] = true
		}
	}
	return out
}

func TestSupervisor_StartsAndStopsLoopsFromAccounts(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acct, err := st.CreateAccount(ctx, store.AWSAccount{Name: "a", AccountID: "111111111111", RoleARN: "arn:aws:iam::111111111111:role/x", ExternalID: "e", Regions: []string{"eu-west-1", "us-east-1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := newLoopRecorder()
	p := newTestPoller(st, Config{AccountsRefresh: 50 * time.Millisecond, RetentionEvery: time.Hour}, randomLockKey())
	p.loopFactory = rec.factory
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { p.runSupervisor(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, 3*time.Second, "2 loops", func() bool { return len(rec.targets()) == 2 })

	if err := st.UpdateAccount(ctx, acct.ID, "a", []string{"eu-west-1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "1 loop", func() bool {
		ts := rec.targets()
		return len(ts) == 1 && ts[awsx.Target{AccountID: "111111111111", Region: "eu-west-1"}]
	})

	if _, err := st.Pool.Exec(ctx, `update aws_accounts set enabled=false where id=$1`, acct.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "0 loops", func() bool { return len(rec.targets()) == 0 })

	cancel()
	<-done
}

func TestSupervisor_RunsRetention(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	stoppedAt := time.Now().Add(-8 * 24 * time.Hour)
	exit := 137
	if _, err := st.UpsertTasks(ctx, []model.Task{{
		Cluster: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"},
		ARN:     "arn:t/old", ServiceName: "api", LastStatus: "STOPPED", DesiredStatus: "STOPPED",
		StoppedAt: &stoppedAt, Containers: []model.Container{{Name: "app", ExitCode: &exit}},
	}}); err != nil {
		t.Fatal(err)
	}
	p := newTestPoller(st, Config{
		AccountsRefresh: time.Hour, RetentionEvery: 50 * time.Millisecond,
		Retention: store.Retention{StoppedTasks: 7 * 24 * time.Hour},
	}, randomLockKey())
	p.loopFactory = newLoopRecorder().factory
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { p.runSupervisor(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, 3*time.Second, "old task deleted", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from tasks`).Scan(&n) == nil && n == 0
	})
}

func TestSupervisor_RollupRunsBeforeRetention(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	// Three days old: inside the six-day backfill window but past the 24h counts retention, so the same tick
	// that deletes it must roll it up first.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	sampledAt := today.AddDate(0, 0, -3).Add(12 * time.Hour)
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"}, Service: "api"}
	if err := st.InsertCountsHistory(ctx, sampledAt, []model.Service{
		{Key: key, Running: 1, Desired: 2}, {Key: model.ServiceKey{ClusterKey: key.ClusterKey, Service: "web"}, Running: 2, Desired: 2},
	}); err != nil {
		t.Fatal(err)
	}
	p := newTestPoller(st, Config{
		AccountsRefresh: time.Hour, RetentionEvery: 50 * time.Millisecond, CountsHistory: 5 * time.Minute,
		Retention: store.Retention{CountsHistory: 24 * time.Hour},
	}, randomLockKey())
	p.loopFactory = newLoopRecorder().factory
	// The runner normally reads the same retention as the delete; widen only its backfill reach so a row that is
	// already past the 24h retention is still inside the window and the ordering is what the test observes.
	p.rollups.CountsRetention = 7 * 24 * time.Hour
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { p.runSupervisor(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, 3*time.Second, "source rows deleted", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from service_counts_history`).Scan(&n) == nil && n == 0
	})
	var rows, below int
	if err := st.Pool.QueryRow(ctx,
		`select count(*), coalesce(sum(minutes_below), 0) from service_availability_daily where day = $1::date`,
		sampledAt.Format(time.DateOnly)).Scan(&rows, &below); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || below != 5 {
		t.Errorf("daily rows for %s = %d with %d minutes below, want 2 rows and 5 minutes", sampledAt.Format(time.DateOnly), rows, below)
	}
}
