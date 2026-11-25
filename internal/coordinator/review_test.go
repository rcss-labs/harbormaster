package coordinator_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/review"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// reviewRequest records a run of a review that is gone, which the job ends without calling anyone.
func reviewRequest(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var id int64
	if err := st.Pool.QueryRow(context.Background(), `insert into agent_review_runs
		(review_name, review_type, period_key, period_label, scheduled_for, requested_at)
		values ('Daily spend', 'cost', 'd:2026-11-25', '25 Nov 2026', now(), now()) returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func reviewState(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	v, err := st.GetReviewRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v.State
}

func reviewer(st *store.Store, every time.Duration) *review.Runner {
	return &review.Runner{Store: st, Driver: &agentjob.Driver{Store: st}, Scopes: st, PerDay: 24, MaxLate: 6 * time.Hour,
		InvestigationMaxWait: 15 * time.Minute, Interval: every, Log: quiet()}
}

func TestCoordinator_ReviewKickWakesTheJob(t *testing.T) {
	st := storetest.Open(t)
	bus := signal.NewMemoryBus()
	rec := &kicked{}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), Bus: bus, OnKick: rec.add, RetentionEvery: time.Hour, Review: reviewer(st, time.Hour)}
	m, _ := startManager(t, st, r)
	waitFor(t, 5*time.Second, "the runner to coordinate", m.HoldsCoordinator)
	first := reviewRequest(t, st)
	publishUntil(t, bus, "review", func() bool { return rec.has("review") && reviewState(t, st, first) == "skipped" })
}

func TestCoordinator_ReviewJobStopsWithTheLease(t *testing.T) {
	st := storetest.Open(t)
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour, Review: reviewer(st, 20*time.Millisecond)}
	_, stop := startManager(t, st, r)
	first := reviewRequest(t, st)
	waitFor(t, 10*time.Second, "the job to run", func() bool { return reviewState(t, st, first) == "skipped" })
	if r.Review.Guard == nil {
		t.Error("the job runs without a guard")
	}
	stop()
	if r.Review.Guard != nil {
		t.Error("the stopped holder left its guard set")
	}
	second := reviewRequest(t, st)
	time.Sleep(300 * time.Millisecond)
	if s := reviewState(t, st, second); s != "requested" {
		t.Errorf("after the lease was given back: %s", s)
	}

	// A holder that cannot confirm the lease touches nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	r.Hold(ctx, store.Lease{Key: store.LeaseKey{Kind: store.LeaseCoordinator}, Epoch: 999})
	if s := reviewState(t, st, second); s != "requested" {
		t.Errorf("without the lease: %s", s)
	}
}
