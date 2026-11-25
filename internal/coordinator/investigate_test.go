package coordinator_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/investigate"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// investigationRequest records a request about a service that does not exist, which the job ends without calling anyone.
func investigationRequest(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var id int64
	if err := st.Pool.QueryRow(context.Background(), `insert into agent_investigations
		(subject_kind, subject_key, account_id, region, cluster_name, service_name, alert_kind, alert_title, alert_text, alert_opened_at)
		values ('service', 'service/482109375512/us-east-1/payments-prod/checkout-api', '482109375512', 'us-east-1', 'payments-prod', 'checkout-api',
		'service_failing', 't', 't', now()) returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func investigationState(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	v, err := st.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v.State
}

func investigator(st *store.Store, every time.Duration) *investigate.Runner {
	return &investigate.Runner{Store: st, Concurrent: 2, PerHour: 20, MaxWait: 15 * time.Minute, Interval: every, Log: quiet()}
}

func TestCoordinator_InvestigateKickWakesTheJob(t *testing.T) {
	st := storetest.Open(t)
	bus := signal.NewMemoryBus()
	rec := &kicked{}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), Bus: bus, OnKick: rec.add, RetentionEvery: time.Hour, Investigate: investigator(st, time.Hour)}
	m, _ := startManager(t, st, r)
	waitFor(t, 5*time.Second, "the runner to coordinate", m.HoldsCoordinator)
	first := investigationRequest(t, st)
	publishUntil(t, bus, "investigate", func() bool { return rec.has("investigate") && investigationState(t, st, first) == "skipped" })
}

func TestCoordinator_JobStopsWithTheLease(t *testing.T) {
	st := storetest.Open(t)
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour, Investigate: investigator(st, 20*time.Millisecond)}
	_, stop := startManager(t, st, r)
	first := investigationRequest(t, st)
	waitFor(t, 10*time.Second, "the job to run", func() bool { return investigationState(t, st, first) == "skipped" })
	if r.Investigate.Guard == nil {
		t.Error("the job runs without a guard")
	}
	stop()
	if r.Investigate.Guard != nil {
		t.Error("the stopped holder left its guard set")
	}
	second := investigationRequest(t, st)
	time.Sleep(300 * time.Millisecond)
	if s := investigationState(t, st, second); s != "requested" {
		t.Errorf("after the lease was given back: %s", s)
	}

	// A holder that cannot confirm the lease touches nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	r.Hold(ctx, store.Lease{Key: store.LeaseKey{Kind: store.LeaseCoordinator}, Epoch: 999})
	if s := investigationState(t, st, second); s != "requested" {
		t.Errorf("without the lease: %s", s)
	}
}
