package coordinator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// Retention deletes rows, so a holder that no longer owns the coordinator lease must not run it.
func TestCoordinator_RetentionDoesNothingWithoutTheLease(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	stoppedAt := time.Now().Add(-8 * 24 * time.Hour)
	if _, err := storetest.Seed(st).UpsertTasks(ctx, []model.Task{{
		Cluster: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"},
		ARN:     "arn:t/old", ServiceName: "api", LastStatus: "STOPPED", DesiredStatus: "STOPPED", StoppedAt: &stoppedAt,
	}}); err != nil {
		t.Fatal(err)
	}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: 50 * time.Millisecond,
		Retention: store.Retention{StoppedTasks: 7 * 24 * time.Hour}}
	// A lease nobody holds: the row exists but names no owner, as after a takeover.
	if err := st.EnsureCoordinatorLease(ctx); err != nil {
		t.Fatal(err)
	}
	holdCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.Hold(holdCtx, store.Lease{Key: store.LeaseKey{Kind: store.LeaseCoordinator}, Epoch: 1})
	}()
	time.Sleep(700 * time.Millisecond) // a dozen retention ticks
	cancel()
	wg.Wait()

	var n int
	if err := st.Pool.QueryRow(ctx, `select count(*) from tasks`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tasks = %d, %v; retention ran without the coordinator lease", n, err)
	}
}

// Agent conversations are pruned by the configured retention, through RunRetention under the coordinator lease.
func TestRetention_PrunesOldAgentConversations(t *testing.T) {
	cfg := config.Config{Retention: config.RetentionConfig{AgentConversations: 24 * time.Hour}}
	r := coordinator.RetentionFrom(cfg)
	if r.AgentConversations != 24*time.Hour {
		t.Fatalf("RetentionFrom AgentConversations = %v, want 24h", r.AgentConversations)
	}
	st := storetest.Open(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateAgentConversation(ctx, u.ID, "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update agent_conversations set updated_at = now() - interval '2 days' where id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	res, err := st.RunRetention(ctx, time.Now(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_conversations"] != 1 {
		t.Fatalf("result = %v, want agent_conversations 1", res)
	}
}

// Agent runs and investigations are pruned by the configured retention, through RunRetention under the coordinator lease.
func TestRetention_PrunesOldInvestigations(t *testing.T) {
	cfg := config.Config{Retention: config.RetentionConfig{AgentRuns: 24 * time.Hour}}
	r := coordinator.RetentionFrom(cfg)
	if r.AgentRuns != 24*time.Hour {
		t.Fatalf("RetentionFrom AgentRuns = %v, want 24h", r.AgentRuns)
	}
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `insert into agent_investigations (subject_kind, subject_key, account_id, alert_kind, alert_title, alert_text, alert_opened_at, requested_at, state, finished_at)
		values ('service', 'service/a/r/c/s', 'a', 'k', 't', 'x', now(), now() - interval '9 days', 'failed', now() - interval '8 days')`); err != nil {
		t.Fatal(err)
	}
	res, err := st.RunRetention(ctx, time.Now(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_investigations"] != 1 {
		t.Fatalf("result = %v, want agent_investigations 1", res)
	}
}
