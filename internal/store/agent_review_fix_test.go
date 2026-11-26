package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// runTogether warms the pool with n connections, then releases n goroutines on one channel, so their statements
// really overlap.
func runTogether(t *testing.T, s *store.Store, n int, fn func(i int)) {
	t.Helper()
	ctx := context.Background()
	var warm sync.WaitGroup
	hold := make(chan struct{})
	for i := 0; i < n; i++ {
		warm.Add(1)
		go func() {
			defer warm.Done()
			conn, err := s.Pool.Acquire(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			<-hold
			conn.Release()
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(hold)
	warm.Wait()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	wg.Wait()
}

// Two coordinators, a restart and a retry can all send the same due list, in different orders.
func TestRecordDue_ConcurrentCallersCreateOne(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	var reviews []store.Review
	for i := 0; i < 20; i++ {
		reviews = append(reviews, oldReview(t, s, fmt.Sprintf("c%02d", i), route.ID))
	}
	var created atomic.Int64
	var failed atomic.Int64
	runTogether(t, s, 10, func(g int) {
		due := make([]store.DueReview, 0, len(reviews))
		for i := range reviews {
			idx := (i*7 + g*3) % len(reviews)
			if g%2 == 1 {
				idx = len(reviews) - 1 - idx
			}
			due = append(due, dueFor(reviews[idx], "d:2026-11-24", revT0))
		}
		n, err := s.RecordDueReviewRuns(ctx, due, revNow)
		created.Add(int64(n))
		if err != nil {
			failed.Add(1)
			t.Error(err)
		}
	})
	if created.Load() != 20 || invCount(t, s, "agent_review_runs") != 20 || failed.Load() != 0 {
		t.Fatalf("created %d, rows %d, failed callers %d; want 20, 20, 0", created.Load(), invCount(t, s, "agent_review_runs"), failed.Load())
	}
}

func TestManual_ConcurrentRequestsCreateOne(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	var ok, conflict atomic.Int64
	runTogether(t, s, 10, func(int) {
		_, err := s.RequestManualReviewRun(ctx, rv.ID, "x", false, revNow)
		switch {
		case err == nil:
			ok.Add(1)
		case errors.Is(err, store.ErrConflict):
			conflict.Add(1)
		default:
			t.Error(err)
		}
	})
	if ok.Load() != 1 || conflict.Load() != 9 || invCount(t, s, "agent_review_runs") != 1 {
		t.Fatalf("ok %d, conflict %d, rows %d; want 1, 9, 1", ok.Load(), conflict.Load(), invCount(t, s, "agent_review_runs"))
	}
}

// Claimers that overlap must still end with one live lease. Each goroutine claims several times over many due rows.
func TestClaimReview_TwoClaimersTakeOne(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	var at []time.Time
	for i := 0; i < 20; i++ {
		at = append(at, revNow.Add(-time.Duration(30-i)*time.Minute))
	}
	seedReviewRuns(t, s, at...)
	var won atomic.Int64
	runTogether(t, s, 10, func(int) {
		for k := 0; k < 5; k++ {
			if _, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, revNow.Add(-15*time.Minute)); err == nil {
				won.Add(1)
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		}
	})
	if n := invCount(t, s, "agent_review_runs where state = 'running'"); won.Load() != 1 || n != 1 {
		t.Fatalf("won %d, running rows %d; want 1, 1", won.Load(), n)
	}
}

func TestReview_LimitHoldsAgainstConcurrentCreators(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	for i := 0; i < store.MaxAgentReviews-1; i++ {
		mustReview(t, s, newReview(fmt.Sprintf("seed %d", i), route.ID))
	}
	var ok, limit atomic.Int64
	runTogether(t, s, 12, func(i int) {
		_, err := s.CreateReview(ctx, newReview(fmt.Sprintf("racer %d", i), route.ID), 0)
		switch {
		case err == nil:
			ok.Add(1)
		case errors.Is(err, store.ErrReviewLimit):
			limit.Add(1)
		default:
			t.Error(err)
		}
	})
	if n := invCount(t, s, "agent_reviews"); ok.Load() != 1 || limit.Load() != 11 || n != store.MaxAgentReviews {
		t.Fatalf("ok %d, limit %d, reviews %d; want 1, 11, 20", ok.Load(), limit.Load(), n)
	}
}

// A due entry read at version 1 must wait for an edit that is still in flight, then see version 2 and record nothing.
func TestRecordDue_WaitsForAnInFlightUpdate(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `update agent_reviews set version = version + 1, enabled = false where id = $1`, rv.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "d:2026-11-24", revT0)}, revNow)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("RecordDueReviewRuns returned %+v while the review was being edited", r)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || r.n != 0 || invCount(t, s, "agent_review_runs") != 0 {
		t.Fatalf("after the edit committed: %+v, rows %d; want nothing recorded", r, invCount(t, s, "agent_review_runs"))
	}
}

// --- one live lease ---

// The released older row must wait behind an expired running younger row.
func TestClaimReview_StaleRunningRowGoesBeforeAReleasedOlderRow(t *testing.T) {
	s := storetest.Open(t)
	ids := seedReviewRuns(t, s, revNow.Add(-10*time.Minute), revNow.Add(-5*time.Minute))
	// The younger row is running with an expired lease; the older one is requested.
	invExec(t, s, `update agent_review_runs set state = 'running', attempts = 1, claim_seq = 1, claimed_until = $2 where id = $1`, ids[1], revNow.Add(-time.Minute))
	got := claimReview(t, s, revNow)
	if got.ID != ids[1] || got.Attempts != 2 {
		t.Fatalf("claimed %+v, want the stale running row %d on its second attempt", got, ids[1])
	}
	if n := invCount(t, s, "agent_review_runs where state = 'running'"); n != 1 {
		t.Fatalf("%d running rows, want 1", n)
	}
	if r := reviewRunState(t, s, ids[0]); r.State != "requested" {
		t.Fatalf("older row = %+v, want it still waiting", r)
	}
}

// A renew that holds the row lock must be waited for: the claimer must not skip past the row and take another.
func TestClaimReview_WaitsForARenewInFlight(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-10*time.Minute), revNow.Add(-5*time.Minute))
	held := claimReview(t, s, revNow.Add(-8*time.Minute)) // lease ended at -7m
	if held.ID != ids[0] {
		t.Fatalf("claimed %d", held.ID)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `update agent_review_runs set claimed_until = $2 where id = $1`, held.ID, revNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		run store.ReviewRun
		err error
	}
	done := make(chan result, 1)
	go func() {
		run, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, revNow.Add(-15*time.Minute))
		done <- result{run, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("claim returned %+v while a renew held the running row", r)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; !errors.Is(r.err, store.ErrNotFound) {
		t.Fatalf("claim after the renew = %+v, want ErrNotFound (the lease is live again)", r)
	}
	if n := invCount(t, s, "agent_review_runs where state = 'running'"); n != 1 {
		t.Fatalf("%d running rows, want 1", n)
	}
}

// --- retention keeps the newest scheduled run of each review ---

func TestRetention_KeepsTheNewestScheduledRunOfEachReview(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := oldReview(t, s, "monthly", route.ID)
	other := oldReview(t, s, "other", route.ID)
	now := time.Now()
	insert := func(reviewID int64, key string, manual bool, requested time.Time) int64 {
		var id int64
		if err := s.Pool.QueryRow(ctx, `insert into agent_review_runs (review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, state, requested_at, finished_at, summary)
			values ($1, 'n', 'cost', $2, 'l', $3, $4, 'done', $3, $3, 's') returning id`, reviewID, key, requested, manual).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	older := insert(rv.ID, "m:2026-09", false, now.Add(-60*24*time.Hour))
	newest := insert(rv.ID, "m:2026-10", false, now.Add(-30*24*time.Hour))
	manualNewer := insert(rv.ID, "x:"+uuid.NewString(), true, now.Add(-20*24*time.Hour))
	otherOnly := insert(other.ID, "d:2026-01-01", false, now.Add(-100*24*time.Hour))
	orphan := insert(rv.ID, "m:2026-08", false, now.Add(-90*24*time.Hour))
	invExec(t, s, `update agent_review_runs set review_id = null where id = $1`, orphan)
	res, err := s.RunRetention(ctx, now, store.Retention{AgentRuns: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_review_runs"] != 3 || reviewRunExists(t, s, older) || reviewRunExists(t, s, orphan) || !reviewRunExists(t, s, newest) || !reviewRunExists(t, s, otherOnly) || reviewRunExists(t, s, manualNewer) {
		t.Fatalf("result = %v; want the older scheduled run, the manual run and the review-less run gone", res)
	}
	latest, _ := s.LatestReviewRuns(ctx)
	if len(latest) != 2 || latest[rv.ID].ID != newest || latest[other.ID].ID != otherOnly {
		t.Fatalf("latest = %+v", latest)
	}
	// The period it already ran is not recorded again.
	if n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "m:2026-10", now.Add(-30*24*time.Hour))}, now); err != nil || n != 0 {
		t.Fatalf("re-recorded %d, %v", n, err)
	}
}

// --- cost of the paid attempts ---

func TestStartReview_BusyRetryDropsOutOfTheCost(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-time.Minute))
	busy, a, b := uuid.New(), uuid.New(), uuid.New()
	for id, cost := range map[uuid.UUID]int64{busy: 5000, a: 700, b: 250} {
		invExec(t, s, `insert into agent_runs (id, role, provider, model, state, cost_micro_usd, started_at, finished_at) values ($1, 'reviewer', 'p', 'm', 'done', $2, now(), now())`, id, cost)
	}
	st := func(id uuid.UUID) store.ReviewStart { return store.ReviewStart{RunID: id, Provider: "p", Model: "m"} }
	c1 := claimReview(t, s, revNow) // attempt 1: the paid run
	mustNoErr(t, s.StartReviewRun(ctx, c1.Claim(), st(a), revNow))
	mustNoErr(t, s.ReleaseReviewRun(ctx, c1.Claim(), time.Time{}, true))
	c2 := claimReview(t, s, revNow.Add(time.Minute)) // attempt 2: found the agent busy, given back free
	mustNoErr(t, s.StartReviewRun(ctx, c2.Claim(), st(busy), revNow.Add(time.Minute)))
	mustNoErr(t, s.ReleaseReviewRun(ctx, c2.Claim(), time.Time{}, false))
	c3 := claimReview(t, s, revNow.Add(2*time.Minute)) // attempt 2 again: the second paid run
	mustNoErr(t, s.StartReviewRun(ctx, c3.Claim(), st(b), revNow.Add(2*time.Minute)))
	mustNoErr(t, s.StartReviewRun(ctx, c3.Claim(), st(b), revNow.Add(2*time.Minute))) // a repeated start
	got := reviewRunState(t, s, ids[0])
	if got.RunID == nil || *got.RunID != b || got.PrevRunID == nil || *got.PrevRunID != a {
		t.Fatalf("run %v, prev %v; want %v and %v", got.RunID, got.PrevRunID, b, a)
	}
	if costs, err := s.ReviewRunCosts(ctx, ids); err != nil || costs[ids[0]] != 950 {
		t.Fatalf("cost = %v, %v; want 700 + 250 = 950", costs, err)
	}
}

// --- claim details ---

func TestRenewReview_ReallyExtendsTheLease(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute))
	held := claimReview(t, s, revNow) // lease to +1m
	mustNoErr(t, s.RenewReviewRun(ctx, held.Claim(), revNow.Add(30*time.Second), 5*time.Minute))
	got := reviewRunState(t, s, held.ID)
	if got.ClaimedUntil == nil || !got.ClaimedUntil.Equal(revNow.Add(30*time.Second+5*time.Minute)) || got.Attempts != 1 || got.ClaimSeq != held.ClaimSeq || got.State != "running" {
		t.Fatalf("after renew: %+v", got)
	}
	if _, _, err := s.ClaimReviewRun(ctx, revNow.Add(2*time.Minute), time.Minute, revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim inside the renewed lease = %v, want ErrNotFound", err)
	}
}

func TestClaimReview_RespectsARetryTime(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute))
	held := claimReview(t, s, revNow)
	retry := revNow.Add(5 * time.Minute)
	mustNoErr(t, s.ReleaseReviewRun(ctx, held.Claim(), retry, true))
	if _, _, err := s.ClaimReviewRun(ctx, retry.Add(-time.Second), time.Minute, revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim before the retry time = %v, want ErrNotFound", err)
	}
	again := claimReview(t, s, retry)
	if again.ID != held.ID || again.Attempts != 2 {
		t.Fatalf("claim at the retry time = %+v", again)
	}
}

// A claim that finds only a row with no attempt left fails it and says ErrNotFound; the failure must be kept.
func TestClaimReview_ExhaustedRowIsFailedAndKept(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-time.Minute))
	first := claimReview(t, s, revNow)
	mustNoErr(t, s.ReleaseReviewRun(ctx, first.Claim(), time.Time{}, true))
	second := claimReview(t, s, revNow.Add(time.Minute))
	mustNoErr(t, s.ReleaseReviewRun(ctx, second.Claim(), time.Time{}, true)) // requested again with attempts 2
	if _, _, err := s.ClaimReviewRun(ctx, revNow.Add(2*time.Minute), time.Minute, revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %v, want ErrNotFound", err)
	}
	got := reviewRunState(t, s, ids[0])
	if got.State != "failed" || got.Reason != "interrupted" || got.Attempts != 2 || got.FinishedAt == nil || !got.FinishedAt.Equal(revNow.Add(2*time.Minute)) || got.ClaimedUntil != nil {
		t.Fatalf("row = %+v, want failed as interrupted and committed", got)
	}
}

// --- reconciler ---

// The reconciler repairs the agent run, not the review run row: that row is reclaimed, then failed, by the claim rules.
func TestReconcile_ReviewerRunLeavesTheReviewRunRowToTheClaimRules(t *testing.T) {
	st, ctx := storetest.Open(t), context.Background()
	setBudget(t, st, 1_000_000)
	identity, err := st.EnsureAgentIdentity(ctx)
	mustNoErr(t, err)
	ids := seedReviewRuns(t, st, agentNow.Add(-time.Minute))
	row := claimReview(t, st, agentNow) // lease to +1m
	agent := agentRun(100)
	agent.Role, agent.UserID = "reviewer", &identity.ID
	mustNoErr(t, st.ReserveAgentRun(ctx, agent, 1000*time.Hour, agentNow))
	mustNoErr(t, st.StartReviewRun(ctx, row.Claim(), store.ReviewStart{RunID: agent.ID, Provider: "p", Model: "m"}, agentNow))
	before := reviewSnapshot(t, st, ids[0])

	now := agentNow.Add(reconcileStale + time.Second)
	if res := reconcile(t, st, now); res.Settled != 1 || res.Audited != 1 {
		t.Fatalf("result = %+v", res)
	}
	if after := reviewSnapshot(t, st, ids[0]); after != before {
		t.Fatalf("the reconciler touched the review run row:\n before %s\n after  %s", before, after)
	}
	again := claimReview(t, st, now) // the lease is long over
	if again.ID != ids[0] || again.Attempts != 2 || again.State != "running" {
		t.Fatalf("reclaimed = %+v", again)
	}
	if _, _, err := st.ClaimReviewRun(ctx, now.Add(time.Hour), time.Minute, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim after the second lease = %v, want ErrNotFound", err)
	}
	if got := reviewRunState(t, st, ids[0]); got.State != "failed" || got.Reason != "interrupted" {
		t.Fatalf("row = %+v, want failed as interrupted", got)
	}
}

// A repeated start must not store the run's own id as the previous one, or its cost would be counted twice.
func TestStartReview_RepeatedStartNeverStoresItsOwnIdAsPrevious(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-time.Minute))
	first := claimReview(t, s, revNow)
	mustNoErr(t, s.ReleaseReviewRun(ctx, first.Claim(), time.Time{}, true)) // attempt 1 never started a run
	second := claimReview(t, s, revNow.Add(time.Minute))
	b := uuid.New()
	for i := 0; i < 2; i++ {
		mustNoErr(t, s.StartReviewRun(ctx, second.Claim(), store.ReviewStart{RunID: b, Provider: "p", Model: "m"}, revNow.Add(time.Minute)))
	}
	if got := reviewRunState(t, s, ids[0]); got.RunID == nil || *got.RunID != b || got.PrevRunID != nil {
		t.Fatalf("run %v, prev %v; want %v and none", got.RunID, got.PrevRunID, b)
	}
}

// The claim fails an exhausted row, then goes on to the next due row. A DeleteReview in flight holds that next row
// (set null of its review_id) and wants the first one: without skip locked the two wait for each other.
func TestClaimReview_DoesNotDeadlockWithADeleteReviewInFlight(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-10*time.Minute), revNow.Add(-5*time.Minute))
	invExec(t, s, `update agent_review_runs set attempts = 2, claim_seq = 2 where id = $1`, ids[0]) // exhausted, due, claimed first
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `update agent_review_runs set review_id = null where id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, revNow.Add(-15*time.Minute))
		done <- err
	}()
	time.Sleep(500 * time.Millisecond) // the claim has failed the first row and reached the second
	_, delErr := tx.Exec(ctx, `update agent_review_runs set review_id = null where id = $1`, ids[0])
	if delErr == nil {
		delErr = tx.Commit(ctx)
	}
	claimErr := <-done
	if delErr != nil || !errors.Is(claimErr, store.ErrNotFound) {
		t.Fatalf("delete side: %v; claim side: %v; want no deadlock and ErrNotFound", delErr, claimErr)
	}
	if got := reviewRunState(t, s, ids[0]); got.State != "failed" || got.Reason != "interrupted" {
		t.Fatalf("first row = %+v, want failed as interrupted", got)
	}
	if got := reviewRunState(t, s, ids[1]); got.State != "requested" {
		t.Fatalf("second row = %+v, want it still waiting", got)
	}
}
