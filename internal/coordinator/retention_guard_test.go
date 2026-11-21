package coordinator_test

import (
	"context"
	"sync"
	"testing"
	"time"

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
	if _, err := st.UpsertTasks(ctx, []model.Task{{
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
