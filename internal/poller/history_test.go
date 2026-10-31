package poller

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestLoop_CountsHistoryEveryInterval(t *testing.T) {
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	cfg := fastConfig()
	cfg.CountsHistory = 30 * time.Millisecond
	l := newFastLoop(t, f, st, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	query := `select count(*) from (select 1 from service_counts_history
	          group by cluster_name, service_name having count(*) < 2) s`
	deadline := time.Now().Add(5 * time.Second)
	for {
		var services, short int
		_ = st.Pool.QueryRow(ctx, `select count(distinct (cluster_name, service_name)) from service_counts_history`).Scan(&services)
		_ = st.Pool.QueryRow(ctx, query).Scan(&short)
		if services == 9 && short == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("services with history = %d (want 9), with fewer than 2 samples = %d", services, short)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHistory_InsertsDeploymentOnce(t *testing.T) {
	h := mockupHarness(t)
	inProgress := `select count(*) from deployment_history
	               where service_name = 'notifications' and revision = 10 and kind = 'deploy' and outcome is null`
	h.runOnce(t)
	if got := h.count(t, inProgress); got != 1 {
		t.Fatalf("in-progress notifications:10 rows = %d, want 1", got)
	}
	total := h.count(t, `select count(*) from deployment_history`)

	h.runOnce(t)
	h.estate.SetRolloutState("notifications", "COMPLETED")
	h.runOnce(t)

	if got := h.count(t, inProgress); got != 1 {
		t.Errorf("notifications:10 rows after COMPLETED = %d, want 1 with NULL outcome", got)
	}
	if got := h.count(t, `select count(*) from deployment_history`); got != total {
		t.Errorf("deployment_history rows = %d, want %d", got, total)
	}
	if got := h.count(t, `select count(*) from deployment_history where finished_at is not null`); got != 0 {
		t.Errorf("finished_at set on %d rows", got)
	}
	if got := h.count(t, `select count(*) from operation_log`); got != 0 {
		t.Errorf("operation_log rows = %d, want 0", got)
	}
}
