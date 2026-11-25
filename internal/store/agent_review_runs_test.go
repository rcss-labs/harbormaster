package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var (
	revNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	revT0  = revNow.Add(-6 * time.Hour) // the occurrence the due scan found
)

func dueFor(r store.Review, key string, at time.Time) store.DueReview {
	return store.DueReview{ReviewID: r.ID, Version: r.Version, PeriodKey: key, PeriodLabel: "label " + key, ScheduledFor: at}
}

// oldReview creates a review whose schedule changed long before the test clock.
func oldReview(t *testing.T, s *store.Store, name string, routeID int64) store.Review {
	t.Helper()
	r := mustReview(t, s, newReview(name, routeID))
	longAgo(t, s, r.ID)
	got, err := s.GetReview(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// seedReviewRuns records one requested run per new review, requested at the given times, and returns the run ids
// oldest first.
func seedReviewRuns(t *testing.T, s *store.Store, at ...time.Time) []int64 {
	t.Helper()
	route := reviewRoute(t, s, "seed-route")
	var ids []int64
	for i, when := range at {
		rv := oldReview(t, s, fmt.Sprintf("seed %d", i), route.ID)
		if n, err := s.RecordDueReviewRuns(context.Background(), []store.DueReview{dueFor(rv, "d:2026-11-24", revT0)}, when); err != nil || n != 1 {
			t.Fatalf("record = %d, %v", n, err)
		}
		var id int64
		if err := s.Pool.QueryRow(context.Background(), `select id from agent_review_runs where review_id = $1`, rv.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func claimReview(t *testing.T, s *store.Store, at time.Time) store.ReviewRun {
	t.Helper()
	run, c, err := s.ClaimReviewRun(context.Background(), at, time.Minute, at.Add(-15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c != run.Claim() || c.RunID != run.ID {
		t.Fatalf("returned claim %+v differs from the row's %+v", c, run.Claim())
	}
	return run
}

func reviewRunState(t *testing.T, s *store.Store, id int64) store.ReviewRun {
	t.Helper()
	r, err := s.GetReviewRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// --- due runs ---

func TestRecordDue_OneRowPerReviewAndPeriod(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	b.Type = "security"
	b, err := s.UpdateReview(ctx, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	longAgo(t, s, b.ID)
	n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{
		dueFor(a, "d:2026-11-24", revT0), dueFor(a, "d:2026-11-24", revT0), dueFor(a, "d:2026-11-25", revT0.Add(time.Hour)), dueFor(b, "d:2026-11-24", revT0),
	}, revNow)
	if err != nil || n != 3 {
		t.Fatalf("created = %d, %v; want 3 (a twice on one period, once on another, b once)", n, err)
	}
	if got := invCount(t, s, "agent_review_runs"); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}
	var name, typ, state, key, label string
	var manual bool
	var scheduled, requested time.Time
	var attempts int
	if err := s.Pool.QueryRow(ctx, `select review_name, review_type, state, period_key, period_label, manual, scheduled_for, requested_at, attempts
		from agent_review_runs where review_id = $1`, b.ID).Scan(&name, &typ, &state, &key, &label, &manual, &scheduled, &requested, &attempts); err != nil {
		t.Fatal(err)
	}
	if name != "b" || typ != "security" || state != "requested" || key != "d:2026-11-24" || label != "label d:2026-11-24" || manual || !scheduled.Equal(revT0) || !requested.Equal(revNow) || attempts != 0 {
		t.Fatalf("row = %s %s %s %s %q %v %v %v %d", name, typ, state, key, label, manual, scheduled, requested, attempts)
	}
}

func TestRecordDue_IdempotentAcrossTicks(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	due := []store.DueReview{dueFor(rv, "d:2026-11-24", revT0)}
	if n, err := s.RecordDueReviewRuns(ctx, due, revNow); err != nil || n != 1 {
		t.Fatalf("first = %d, %v", n, err)
	}
	// The row has moved on; a later tick must still not make a second one.
	invExec(t, s, `update agent_review_runs set state = 'failed', reason = 'error', finished_at = $1`, revNow)
	for i := 1; i <= 3; i++ {
		if n, err := s.RecordDueReviewRuns(ctx, due, revNow.Add(time.Duration(i)*time.Minute)); err != nil || n != 0 {
			t.Fatalf("tick %d = %d, %v; want 0", i, n, err)
		}
	}
	if n, err := s.RecordDueReviewRuns(ctx, nil, revNow); err != nil || n != 0 {
		t.Fatalf("empty list = %d, %v", n, err)
	}
	if invCount(t, s, "agent_review_runs") != 1 {
		t.Fatal("want one row")
	}
}

// The scan sends every due review again at each tick: a period that is recorded must not take a run id each time.
func TestRecordDue_TicksTakeNoRunIDs(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	due := []store.DueReview{dueFor(a, "d:2026-11-24", revT0), dueFor(b, "d:2026-11-24", revT0)}
	record := func(want int, due ...store.DueReview) {
		t.Helper()
		if n, err := s.RecordDueReviewRuns(ctx, due, revNow); err != nil || n != want {
			t.Fatalf("created = %d, %v, want %d", n, err, want)
		}
	}
	lastID := func() (id int64) {
		t.Helper()
		if err := s.Pool.QueryRow(ctx, `select last_value from agent_review_runs_id_seq`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	record(2, due...)
	before := lastID()
	for range 100 {
		record(0, due...)
	}
	if after := lastID(); after != before {
		t.Fatalf("100 ticks moved the run ids from %d to %d", before, after)
	}
	// The same period of another review and another period of the same one are still recorded.
	c := oldReview(t, s, "c", route.ID)
	record(2, dueFor(c, "d:2026-11-24", revT0), dueFor(a, "d:2026-11-25", revT0.Add(time.Hour)))
	if after := lastID(); after != before+2 {
		t.Fatalf("two new runs moved the run ids from %d to %d", before, after)
	}
}

func TestRecordDue_IgnoresADisabledOrChangedReview(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	disabled, changed := oldReview(t, s, "disabled", route.ID), oldReview(t, s, "changed", route.ID)
	disabled.Enabled = false
	disabled, err := s.UpdateReview(ctx, disabled, 0)
	if err != nil {
		t.Fatal(err)
	}
	staleEntry := dueFor(changed, "d:2026-11-24", revT0) // built from version 1
	changed.Name = "changed again"
	if _, err := s.UpdateReview(ctx, changed, 0); err != nil {
		t.Fatal(err)
	}
	ghost := store.DueReview{ReviewID: 987654, Version: 1, PeriodKey: "d:2026-11-24", PeriodLabel: "x", ScheduledFor: revT0}
	n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(disabled, "d:2026-11-24", revT0), staleEntry, ghost}, revNow)
	if err != nil || n != 0 || invCount(t, s, "agent_review_runs") != 0 {
		t.Fatalf("created %d, %v, rows %d; want nothing", n, err, invCount(t, s, "agent_review_runs"))
	}
}

func TestRecordDue_IgnoresAnOccurrenceBeforeTheScheduleChanged(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	changed := revT0.Add(time.Hour)
	invExec(t, s, `update agent_reviews set schedule_changed_at = $2 where id = $1`, rv.ID, changed)
	rv, _ = s.GetReview(ctx, rv.ID)
	for name, at := range map[string]time.Time{"before": changed.Add(-time.Minute), "at the change": changed} {
		if n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "d:2026-11-24", at)}, revNow); err != nil || n != 0 {
			t.Fatalf("%s: created %d, %v; want 0", name, n, err)
		}
	}
	if n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "d:2026-11-25", changed.Add(time.Second))}, revNow); err != nil || n != 1 {
		t.Fatalf("after the change: created %d, %v; want 1", n, err)
	}
}

func TestRecordDue_MissedIsRecordedAsSkippedWithoutARun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	missed := dueFor(rv, "d:2026-11-24", revT0)
	missed.Missed = true
	if n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{missed}, revNow); err != nil || n != 1 {
		t.Fatalf("created %d, %v", n, err)
	}
	run, err := s.GetReviewRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "skipped" || run.Reason != "missed" || run.FinishedAt == nil || !run.FinishedAt.Equal(revNow) || run.Attempts != 0 || run.RunID != nil || run.StartedAt != nil {
		t.Fatalf("run = %+v", run)
	}
	// The period is spent: a later, on-time entry for it makes nothing, and nothing claims the skipped row.
	if n, _ := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "d:2026-11-24", revT0)}, revNow.Add(time.Minute)); n != 0 {
		t.Fatalf("created %d for a period that was already recorded as missed", n)
	}
	if _, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim = %v, want ErrNotFound", err)
	}
}

// --- manual runs ---

func TestManual_RefusedWhileOneIsWaitingOrRunning(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	run, err := s.RequestManualReviewRun(ctx, a.ID, "Run by hand", revNow)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Manual || run.State != "requested" || !strings.HasPrefix(run.PeriodKey, "x:") || run.PeriodLabel != "Run by hand" || !run.ScheduledFor.Equal(revNow) ||
		run.ReviewName != "a" || run.ReviewType != "cost" || run.ReviewID == nil || *run.ReviewID != a.ID {
		t.Fatalf("run = %+v", run)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(run.PeriodKey, "x:")); err != nil {
		t.Fatalf("period key %q: %v", run.PeriodKey, err)
	}
	if _, err := s.RequestManualReviewRun(ctx, a.ID, "again", revNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("while waiting = %v, want ErrConflict", err)
	}
	if _, err := s.RequestManualReviewRun(ctx, b.ID, "other review", revNow); err != nil {
		t.Fatalf("another review: %v", err)
	}
	claimReview(t, s, revNow)
	if _, err := s.RequestManualReviewRun(ctx, a.ID, "again", revNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("while running = %v, want ErrConflict", err)
	}
	invExec(t, s, `update agent_review_runs set state = 'done', summary = 's', finished_at = $1, claimed_until = null where review_id = $2`, revNow, a.ID)
	if _, err := s.RequestManualReviewRun(ctx, a.ID, "after", revNow); err != nil {
		t.Fatalf("after it finished: %v", err)
	}
	if _, err := s.RequestManualReviewRun(ctx, 424242, "x", revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown review = %v, want ErrNotFound", err)
	}
}

func TestManual_DisabledReviewCanBeRun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	rv.Enabled = false
	if _, err := s.UpdateReview(ctx, rv, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestManualReviewRun(ctx, rv.ID, "by hand", revNow); err != nil {
		t.Fatalf("manual run of a disabled review: %v", err)
	}
}

// --- claim ---

func insertWaitingInvestigation(t *testing.T, s *store.Store, key, state string, requestedAt time.Time) {
	t.Helper()
	invExec(t, s, `insert into agent_investigations (subject_kind, subject_key, account_id, alert_kind, alert_title, alert_text, alert_opened_at, requested_at, state)
		values ('service', $1, 'a', 'k', 't', 'x', now(), $2, $3)`, key, requestedAt, state)
}

func TestClaimReview_OneAtATime(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-2*time.Minute), revNow.Add(-time.Minute))
	first := claimReview(t, s, revNow)
	if first.ID != ids[0] || first.State != "running" || first.Attempts != 1 || first.ClaimSeq != 1 || first.ClaimedUntil == nil || !first.ClaimedUntil.Equal(revNow.Add(time.Minute)) {
		t.Fatalf("first = %+v, want the oldest run leased for a minute", first)
	}
	if _, _, err := s.ClaimReviewRun(ctx, revNow.Add(30*time.Second), time.Minute, revNow); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second claim while the first is live = %v, want ErrNotFound", err)
	}
	// The lease is over: the stale row goes first, ahead of the younger waiting one.
	again := claimReview(t, s, revNow.Add(2*time.Minute))
	if again.ID != ids[0] || again.Attempts != 2 || again.ClaimSeq != 2 {
		t.Fatalf("after the lease = %+v, want the stale row on its second attempt", again)
	}
}

func TestClaimReview_WaitsForAWaitingInvestigation(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute))
	insertWaitingInvestigation(t, s, "service/a/r/c/s", "requested", revNow.Add(-5*time.Minute))
	if _, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, revNow.Add(-15*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim with an investigation waiting = %v, want ErrNotFound", err)
	}
	if got := reviewRunState(t, s, 1); got.State != "requested" || got.Attempts != 0 || got.ClaimSeq != 0 {
		t.Fatalf("the row changed: %+v", got)
	}
	invExec(t, s, `update agent_investigations set state = 'done'`)
	claimReview(t, s, revNow)
}

func TestClaimReview_IgnoresAnInvestigationOlderThanTheBound(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute))
	bound := revNow.Add(-15 * time.Minute)
	insertWaitingInvestigation(t, s, "service/a/r/c/old", "requested", bound.Add(-time.Second))
	insertWaitingInvestigation(t, s, "service/a/r/c/at", "requested", bound) // not after the bound
	insertWaitingInvestigation(t, s, "service/a/r/c/running", "running", revNow)
	if _, _, err := s.ClaimReviewRun(ctx, revNow, time.Minute, bound); err != nil {
		t.Fatalf("claim = %v, want the review to go ahead", err)
	}
}

func TestClaimReview_SecondExpiryFailsAsInterrupted(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-3*time.Minute), revNow.Add(-2*time.Minute))
	claimReview(t, s, revNow)
	claimReview(t, s, revNow.Add(2*time.Minute)) // attempt 2 of the first row
	// Its second lease ends with no attempt left: the row fails and the next due row is returned in the same call.
	next := claimReview(t, s, revNow.Add(4*time.Minute))
	if next.ID != ids[1] || next.Attempts != 1 {
		t.Fatalf("next = %+v, want the second row on its first attempt", next)
	}
	failed := reviewRunState(t, s, ids[0])
	if failed.State != "failed" || failed.Reason != "interrupted" || failed.Attempts != 2 || failed.FinishedAt == nil || !failed.FinishedAt.Equal(revNow.Add(4*time.Minute)) || failed.ClaimedUntil != nil {
		t.Fatalf("first row = %+v, want failed as interrupted", failed)
	}
	if _, _, err := s.ClaimReviewRun(ctx, revNow.Add(10*time.Minute), time.Minute, revNow); err != nil {
		t.Fatalf("second row, first expiry: %v", err)
	}
}

// --- start ---

func TestStartReview_KeepsThePreviousRun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute))
	first := claimReview(t, s, revNow)
	a, b := uuid.New(), uuid.New()
	start := store.ReviewStart{RunID: a, Provider: "bedrock", Model: "anthropic.claude-sonnet-5-5-v1:0", ScopeLabel: "2 accounts", Range: "24h", CoveredTags: []int64{4, 9}, ServicesTotal: 31}
	if err := s.StartReviewRun(ctx, first.Claim(), start, revNow); err != nil {
		t.Fatal(err)
	}
	got := reviewRunState(t, s, first.ID)
	if got.RunID == nil || *got.RunID != a || got.PrevRunID != nil || got.StartedAt == nil || !got.StartedAt.Equal(revNow) || got.Provider != "bedrock" ||
		got.ScopeLabel != "2 accounts" || got.Range != "24h" || got.ServicesTotal != 31 || fmt.Sprint(got.CoveredTags) != "[4 9]" {
		t.Fatalf("after start = %+v", got)
	}
	if err := s.ReleaseReviewRun(ctx, first.Claim(), revNow, true); err != nil {
		t.Fatal(err)
	}
	second := claimReview(t, s, revNow.Add(time.Minute))
	start.RunID = b
	start.CoveredTags = nil
	if err := s.StartReviewRun(ctx, second.Claim(), start, revNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = reviewRunState(t, s, first.ID)
	if got.RunID == nil || *got.RunID != b || got.PrevRunID == nil || *got.PrevRunID != a || len(got.CoveredTags) != 0 {
		t.Fatalf("after the second start: run %v, prev %v, tags %v", got.RunID, got.PrevRunID, got.CoveredTags)
	}
}

// --- fence ---

type fenceFixture struct {
	table, evidenceTable string
	seed                 func(t *testing.T, s *store.Store)
	// claim leases the row and returns the claim's parts.
	claim func(t *testing.T, s *store.Store, at time.Time) (id int64, attempt int, seq int64)
	ops   func(s *store.Store, id int64, attempt int, seq int64, now time.Time) map[string]func() error
	// freeRelease gives the attempt back; failNow settles the row as failed.
	freeRelease func(s *store.Store, id int64, attempt int, seq int64) error
	failNow     func(s *store.Store, id int64, attempt int, seq int64, now time.Time) error
}

func fenceFixtures() map[string]fenceFixture {
	ctx := context.Background()
	return map[string]fenceFixture{
		"investigations": {
			table: "agent_investigations", evidenceTable: "agent_investigation_evidence",
			seed: func(t *testing.T, s *store.Store) { invSeed(t, s, []string{"a"}, invNow) },
			claim: func(t *testing.T, s *store.Store, at time.Time) (int64, int, int64) {
				v := claimed(t, s, at)
				return v.ID, v.Attempts, v.ClaimSeq
			},
			ops: func(s *store.Store, id int64, attempt int, seq int64, now time.Time) map[string]func() error {
				return holderOps(s, store.Claim{InvestigationID: id, Attempt: attempt, Seq: seq}, now)
			},
			freeRelease: func(s *store.Store, id int64, attempt int, seq int64) error {
				return s.ReleaseInvestigation(ctx, store.Claim{InvestigationID: id, Attempt: attempt, Seq: seq}, time.Time{}, false)
			},
			failNow: func(s *store.Store, id int64, attempt int, seq int64, now time.Time) error {
				return s.FinishInvestigation(ctx, store.Claim{InvestigationID: id, Attempt: attempt, Seq: seq}, "failed", "error", nil, now)
			},
		},
		"reviews": {
			table: "agent_review_runs", evidenceTable: "agent_review_evidence",
			seed: func(t *testing.T, s *store.Store) { seedReviewRuns(t, s, invNow) },
			claim: func(t *testing.T, s *store.Store, at time.Time) (int64, int, int64) {
				r := claimReview(t, s, at)
				return r.ID, r.Attempts, r.ClaimSeq
			},
			ops: func(s *store.Store, id int64, attempt int, seq int64, now time.Time) map[string]func() error {
				c := store.ReviewClaim{RunID: id, Attempt: attempt, Seq: seq}
				return map[string]func() error{
					"start": func() error {
						return s.StartReviewRun(ctx, c, store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m"}, now)
					},
					"renew":   func() error { return s.RenewReviewRun(ctx, c, now, time.Hour) },
					"release": func() error { return s.ReleaseReviewRun(ctx, c, now, true) },
					"skip":    func() error { return s.SkipReviewRun(ctx, c, "budget", now) },
					"finish": func() error {
						return s.FinishReviewRun(ctx, c, "done", "", &store.InvestigationResult{Summary: "stale", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}, nil, now)
					},
				}
			},
			freeRelease: func(s *store.Store, id int64, attempt int, seq int64) error {
				return s.ReleaseReviewRun(ctx, store.ReviewClaim{RunID: id, Attempt: attempt, Seq: seq}, time.Time{}, false)
			},
			failNow: func(s *store.Store, id int64, attempt int, seq int64, now time.Time) error {
				return s.FinishReviewRun(ctx, store.ReviewClaim{RunID: id, Attempt: attempt, Seq: seq}, "failed", "error", nil, nil, now)
			},
		},
	}
}

// snapshot is the whole row and the number of evidence rows, as text.
func (f fenceFixture) snapshot(t *testing.T, s *store.Store, id int64) string {
	t.Helper()
	var row string
	var evidence int
	if err := s.Pool.QueryRow(context.Background(), `select row_to_json(x)::text, (select count(*) from `+f.evidenceTable+`) from `+f.table+` x where id = $1`, id).Scan(&row, &evidence); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s evidence=%d", row, evidence)
}

// The scenarios of the phase 2 fence tests, run against both tables.
func TestReviewFence_SameScenariosAsInvestigations(t *testing.T) {
	ops := []string{"start", "renew", "release", "skip", "finish"}
	scenarios := map[string]func(t *testing.T, s *store.Store, f fenceFixture) (stale func(op string) error, id int64){
		// A free release gives the attempt back, so the next claim reuses the attempt number; only claim_seq tells them apart.
		"stale attempt after a reused attempt number": func(t *testing.T, s *store.Store, f fenceFixture) (func(string) error, int64) {
			f.seed(t, s)
			id, attempt, seq := f.claim(t, s, invNow)
			if err := f.freeRelease(s, id, attempt, seq); err != nil {
				t.Fatal(err)
			}
			id2, attempt2, seq2 := f.claim(t, s, invNow.Add(time.Minute))
			if id2 != id || attempt2 != attempt || seq2 == seq {
				t.Fatalf("setup: attempts %d/%d, seq %d/%d", attempt, attempt2, seq, seq2)
			}
			return func(op string) error { return f.ops(s, id, attempt, seq, invNow.Add(2*time.Minute))[op]() }, id
		},
		"wrong attempt": func(t *testing.T, s *store.Store, f fenceFixture) (func(string) error, int64) {
			f.seed(t, s)
			id, attempt, seq := f.claim(t, s, invNow)
			return func(op string) error { return f.ops(s, id, attempt+1, seq, invNow)[op]() }, id
		},
		"wrong seq": func(t *testing.T, s *store.Store, f fenceFixture) (func(string) error, int64) {
			f.seed(t, s)
			id, attempt, seq := f.claim(t, s, invNow)
			return func(op string) error { return f.ops(s, id, attempt, seq+1, invNow)[op]() }, id
		},
		"settled row": func(t *testing.T, s *store.Store, f fenceFixture) (func(string) error, int64) {
			f.seed(t, s)
			id, attempt, seq := f.claim(t, s, invNow)
			if err := f.failNow(s, id, attempt, seq, invNow); err != nil {
				t.Fatal(err)
			}
			return func(op string) error { return f.ops(s, id, attempt, seq, invNow)[op]() }, id
		},
	}
	for fixtureName, f := range fenceFixtures() {
		for scenarioName, scenario := range scenarios {
			for _, op := range ops {
				t.Run(fixtureName+"/"+scenarioName+"/"+op, func(t *testing.T) {
					s := storetest.Open(t)
					stale, id := scenario(t, s, f)
					before := f.snapshot(t, s, id)
					if err := stale(op); !errors.Is(err, store.ErrConflict) {
						t.Fatalf("%s = %v, want ErrConflict", op, err)
					}
					if after := f.snapshot(t, s, id); after != before {
						t.Fatalf("row changed:\n before %s\n after  %s", before, after)
					}
				})
			}
		}
	}
}

// --- finish ---

func claimedReview(t *testing.T, s *store.Store) (store.ReviewRun, store.ReviewClaim) {
	t.Helper()
	seedReviewRuns(t, s, invNow)
	run := claimReview(t, s, invNow)
	return run, run.Claim()
}

func reviewSnapshot(t *testing.T, s *store.Store, id int64) string {
	return fenceFixtures()["reviews"].snapshot(t, s, id)
}

func TestFinishReview_EvidenceAndStateAreOneTransaction(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, c := claimedReview(t, s)
	before := reviewSnapshot(t, s, run.ID)
	invExec(t, s, `alter table agent_review_evidence add constraint test_refuse_boom check (tool <> 'boom')`)
	ev := viewEvidence("x")
	ev.Tool = "boom"
	err := s.FinishReviewRun(ctx, c, "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("first"), ev}}, nil, invNow)
	if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvestigationText) {
		t.Fatalf("err = %v, want the database's refusal", err)
	}
	if after := reviewSnapshot(t, s, run.ID); after != before {
		t.Fatalf("row changed:\n before %s\n after  %s", before, after)
	}
}

func TestFinishReview_StoresTheResultAndReplacesEvidence(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, c := claimedReview(t, s)
	res := &store.InvestigationResult{Summary: "Spend is flat.", SummaryReplaced: true, EvidenceDropped: 2,
		Evidence: []store.InvestigationEvidence{viewEvidence("one"), viewEvidence("two")}}
	if err := s.FinishReviewRun(ctx, c, "done", "", res, nil, invNow); err != nil {
		t.Fatal(err)
	}
	got := reviewRunState(t, s, run.ID)
	if got.State != "done" || got.Reason != "" || got.Summary != "Spend is flat." || !got.SummaryReplaced || got.EvidenceDropped != 2 || got.FinishedAt == nil || !got.FinishedAt.Equal(invNow) || got.ClaimedUntil != nil {
		t.Fatalf("run = %+v", got)
	}
	ev, err := s.ListReviewRunEvidence(ctx, run.ID)
	if err != nil || len(ev) != 2 || ev[0].Ordinal != 1 || ev[1].Ordinal != 2 || ev[1].Excerpt != "two" || ev[0].Action != "view" {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
}

func TestFinishReview_RefusesLogsEvidence(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, c := claimedReview(t, s)
	before := reviewSnapshot(t, s, run.ID)
	logs := viewEvidence("x")
	logs.Action = "logs"
	err := s.FinishReviewRun(ctx, c, "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("a"), logs}}, nil, invNow)
	if err == nil || errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want a refusal of logs evidence", err)
	}
	if after := reviewSnapshot(t, s, run.ID); after != before {
		t.Fatalf("row changed:\n before %s\n after  %s", before, after)
	}
	// The schema refuses it too, for a writer that skips the Go check.
	if _, err := s.Pool.Exec(ctx, `insert into agent_review_evidence (review_run_id, ordinal, tool, action, account_id, region, cluster_name, service_name)
		values ($1, 1, 't', 'logs', 'a', 'r', 'c', 's')`, run.ID); err == nil {
		t.Fatal("the schema accepted logs evidence")
	}
}

func TestFinishReview_RefusesAnInconsistentStateOrReason(t *testing.T) {
	good := &store.InvestigationResult{Summary: "s"}
	cases := []struct {
		name          string
		state, reason string
		result        *store.InvestigationResult
	}{
		{"done with a reason", "done", "error", good},
		{"done without a result", "done", "", nil},
		{"done with an empty summary", "done", "", &store.InvestigationResult{Summary: "  "}},
		{"failed without a reason", "failed", "", nil},
		{"failed with an unknown reason", "failed", "because", nil},
		{"failed with a phase 2 only reason", "failed", "expired", nil},
		{"skipped", "skipped", "budget", nil},
		{"requested", "requested", "", nil},
		{"running", "running", "", nil},
		{"empty state", "", "", good},
		{"13 evidence items", "done", "", &store.InvestigationResult{Summary: "s", Evidence: make13()}},
		{"empty service", "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{{Tool: "t", Action: "view", AccountID: "a", Region: "r", ClusterName: "c"}}}},
		{"negative dropped", "done", "", &store.InvestigationResult{Summary: "s", EvidenceDropped: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			run, c := claimedReview(t, s)
			before := reviewSnapshot(t, s, run.ID)
			err := s.FinishReviewRun(context.Background(), c, tc.state, tc.reason, tc.result, nil, invNow)
			if err == nil || errors.Is(err, store.ErrConflict) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if after := reviewSnapshot(t, s, run.ID); after != before {
				t.Fatalf("row changed:\n before %s\n after  %s", before, after)
			}
		})
	}
	s := storetest.Open(t)
	_, c := claimedReview(t, s)
	if err := s.FinishReviewRun(context.Background(), c, "failed", "no_result", nil, nil, invNow); err != nil {
		t.Fatalf("a failed run with a fixed reason: %v", err)
	}
}

func make13() []store.InvestigationEvidence {
	out := make([]store.InvestigationEvidence, 13)
	for i := range out {
		out[i] = viewEvidence("x")
	}
	return out
}

func TestSkipReview_RefusesAnUnknownReason(t *testing.T) {
	s := storetest.Open(t)
	run, c := claimedReview(t, s)
	for _, reason := range []string{"", "because", "expired"} {
		if err := s.SkipReviewRun(context.Background(), c, reason, invNow); err == nil || errors.Is(err, store.ErrConflict) {
			t.Fatalf("reason %q = %v, want a refusal", reason, err)
		}
	}
	if err := s.SkipReviewRun(context.Background(), c, "budget", invNow); err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, run.ID); got.State != "skipped" || got.Reason != "budget" || got.FinishedAt == nil || got.ClaimedUntil != nil {
		t.Fatalf("run = %+v", got)
	}
}

func TestReviewText_RefusedBeforeAnySQL(t *testing.T) {
	long := strings.Repeat("x", 201)
	finish := func(mut func(*store.InvestigationResult)) func(*store.Store, store.ReviewClaim) error {
		return func(s *store.Store, c store.ReviewClaim) error {
			r := &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence("x")}}
			mut(r)
			return s.FinishReviewRun(context.Background(), c, "done", "", r, nil, invNow)
		}
	}
	start := func(mut func(*store.ReviewStart)) func(*store.Store, store.ReviewClaim) error {
		return func(s *store.Store, c store.ReviewClaim) error {
			st := store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", ScopeLabel: "all", Range: "24h"}
			mut(&st)
			return s.StartReviewRun(context.Background(), c, st, invNow)
		}
	}
	cases := map[string]func(*store.Store, store.ReviewClaim) error{
		"provider invalid utf-8":  start(func(st *store.ReviewStart) { st.Provider = "p\xff" }),
		"model has NUL":           start(func(st *store.ReviewStart) { st.Model = "m\x00" }),
		"provider too long":       start(func(st *store.ReviewStart) { st.Provider = long }),
		"scope label too long":    start(func(st *store.ReviewStart) { st.ScopeLabel = strings.Repeat("é", 301) }),
		"scope label NUL":         start(func(st *store.ReviewStart) { st.ScopeLabel = "a\x00" }),
		"range invalid utf-8":     start(func(st *store.ReviewStart) { st.Range = "\xff" }),
		"summary invalid utf-8":   finish(func(r *store.InvestigationResult) { r.Summary = "a\xc3" }),
		"summary NUL":             finish(func(r *store.InvestigationResult) { r.Summary = "a\x00b" }),
		"summary over 1200 runes": finish(func(r *store.InvestigationResult) { r.Summary = strings.Repeat("é", 1201) }),
		"excerpt invalid utf-8":   finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = "ok\xff\xfe" }),
		"excerpt NUL":             finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = "a\x00" }),
		"excerpt too long":        finish(func(r *store.InvestigationResult) { r.Evidence[0].Excerpt = strings.Repeat("x", 1501) }),
		"note invalid utf-8":      finish(func(r *store.InvestigationResult) { r.Evidence[0].Note = "\xff" }),
		"note too long":           finish(func(r *store.InvestigationResult) { r.Evidence[0].Note = strings.Repeat("n", 301) }),
		"tool invalid utf-8":      finish(func(r *store.InvestigationResult) { r.Evidence[0].Tool = "t\xff" }),
		"service name invalid":    finish(func(r *store.InvestigationResult) { r.Evidence[0].ServiceName = "s\xff" }),
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			row, c := claimedReview(t, s)
			before := reviewSnapshot(t, s, row.ID)
			if err := run(s, c); !errors.Is(err, store.ErrInvestigationText) {
				t.Fatalf("err = %v, want ErrInvestigationText", err)
			}
			if after := reviewSnapshot(t, s, row.ID); after != before {
				t.Fatalf("row changed:\n before %s\n after  %s", before, after)
			}
		})
	}
}

// The shared checks keep the phase 2 behaviour: logs evidence is still allowed there, the limits are the same.
func TestInvestigationChecksUnchangedByTheSharedHelpers(t *testing.T) {
	logs := viewEvidence("x")
	logs.Action = "logs"
	refused := map[string]*store.InvestigationResult{
		"13 items":      {Summary: "s", Evidence: make13()},
		"unknown":       {Summary: "s", Evidence: []store.InvestigationEvidence{func() store.InvestigationEvidence { e := viewEvidence("x"); e.Action = "write"; return e }()}},
		"empty service": {Summary: "s", Evidence: []store.InvestigationEvidence{{Tool: "t", Action: "view", AccountID: "a", Region: "r", ClusterName: "c"}}},
		"big excerpt":   {Summary: "s", Evidence: []store.InvestigationEvidence{viewEvidence(strings.Repeat("x", 1501))}},
	}
	for name, res := range refused {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			invSeed(t, s, []string{"a"}, invNow)
			held := claimed(t, s, invNow)
			if err := s.FinishInvestigation(context.Background(), held.Claim(), "done", "", res, invNow); err == nil || errors.Is(err, store.ErrConflict) {
				t.Fatalf("err = %v, want a refusal", err)
			}
			requireUntouched(t, s, held)
		})
	}
	s := storetest.Open(t)
	invSeed(t, s, []string{"a"}, invNow)
	held := claimed(t, s, invNow)
	if err := s.FinishInvestigation(context.Background(), held.Claim(), "done", "", &store.InvestigationResult{Summary: "s", Evidence: []store.InvestigationEvidence{logs, viewEvidence("y")}}, invNow); err != nil {
		t.Fatalf("an investigation may cite logs: %v", err)
	}
	if ev, err := s.ListInvestigationEvidence(context.Background(), held.ID); err != nil || len(ev) != 2 || ev[0].Action != "logs" {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
}

// --- lifecycle ---

func TestReviewRunsStartedSince_IgnoresAFreeRelease(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedReviewRuns(t, s, revNow.Add(-time.Minute), revNow.Add(-30*time.Second))
	first := claimReview(t, s, revNow)
	if err := s.StartReviewRun(ctx, first.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m"}, revNow); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReviewRunsStartedSince(ctx, revNow.Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("started = %d, %v; want 1", n, err)
	}
	if n, _ := s.ReviewRunsStartedSince(ctx, revNow.Add(time.Second)); n != 0 {
		t.Fatalf("started after the clock = %d", n)
	}
	if err := s.ReleaseReviewRun(ctx, first.Claim(), time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	got := reviewRunState(t, s, first.ID)
	if n, _ := s.ReviewRunsStartedSince(ctx, revNow.Add(-time.Hour)); n != 0 || got.StartedAt != nil || got.State != "requested" || got.Attempts != 0 || got.RunID == nil {
		t.Fatalf("after a free release: started %d, row %+v; want 0 started, started_at cleared, run id kept", n, got)
	}
	// A counted release keeps it, and so does a free release of a second attempt.
	second := claimReview(t, s, revNow.Add(time.Minute))
	if err := s.StartReviewRun(ctx, second.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m"}, revNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseReviewRun(ctx, second.Claim(), time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ReviewRunsStartedSince(ctx, revNow.Add(-time.Hour)); n != 1 {
		t.Fatalf("after a counted release started = %d, want 1", n)
	}
	third := claimReview(t, s, revNow.Add(2*time.Minute))
	if third.Attempts != 2 {
		t.Fatalf("attempts = %d", third.Attempts)
	}
	if err := s.ReleaseReviewRun(ctx, third.Claim(), time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ReviewRunsStartedSince(ctx, revNow.Add(-time.Hour)); n != 1 {
		t.Fatalf("after a free release of a second attempt started = %d, want 1", n)
	}
}

func TestReviewRunCosts_SumsBothAttempts(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ids := seedReviewRuns(t, s, revNow.Add(-3*time.Minute), revNow.Add(-2*time.Minute), revNow.Add(-time.Minute))
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	for id, cost := range map[uuid.UUID]int64{a: 700, b: 250, c: 40} {
		invExec(t, s, `insert into agent_runs (id, role, provider, model, state, cost_micro_usd, started_at, finished_at) values ($1, 'reviewer', 'p', 'm', 'done', $2, now(), now())`, id, cost)
	}
	invExec(t, s, `update agent_review_runs set run_id = $2, prev_run_id = $3 where id = $1`, ids[0], b, a)
	invExec(t, s, `update agent_review_runs set run_id = $2 where id = $1`, ids[1], c)
	invExec(t, s, `update agent_review_runs set run_id = $2, prev_run_id = $2 where id = $1`, ids[2], uuid.New()) // runs retention deleted
	got, err := s.ReviewRunCosts(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got[ids[0]] != 950 || got[ids[1]] != 40 || got[ids[2]] != 0 {
		t.Fatalf("costs = %v, want 950, 40 and nothing for the deleted one", got)
	}
	if only, _ := s.ReviewRunCosts(ctx, ids[:1]); len(only) != 1 || only[ids[0]] != 950 {
		t.Fatalf("one id = %v", only)
	}
	if none, err := s.ReviewRunCosts(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("no ids = %v, %v", none, err)
	}
}

func TestLatestReviewRuns(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b, none := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID), oldReview(t, s, "none", route.ID)
	for i, key := range []string{"d:2026-11-22", "d:2026-11-23", "d:2026-11-24"} {
		if _, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(a, key, revT0)}, revNow.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(b, "d:2026-11-24", revT0)}, revNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestReviewRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a.ID].PeriodKey != "d:2026-11-24" || got[b.ID].PeriodKey != "d:2026-11-24" {
		t.Fatalf("latest = %+v", got)
	}
	if _, ok := got[none.ID]; ok {
		t.Fatal("a review with no run has an entry")
	}
	list, err := s.ListReviewRuns(ctx, store.ReviewRunFilter{})
	if err != nil || len(list) != 4 || list[0].PeriodKey != "d:2026-11-24" || list[0].ReviewID == nil || *list[0].ReviewID != a.ID {
		t.Fatalf("list = %d rows, %v (first %+v)", len(list), err, list)
	}
	page, _ := s.ListReviewRuns(ctx, store.ReviewRunFilter{Limit: 2})
	rest, _ := s.ListReviewRuns(ctx, store.ReviewRunFilter{BeforeAt: page[1].RequestedAt, BeforeID: page[1].ID})
	if len(page) != 2 || len(rest) != 2 || rest[0].ID == page[1].ID {
		t.Fatalf("pages = %d + %d", len(page), len(rest))
	}
	only, _ := s.ListReviewRuns(ctx, store.ReviewRunFilter{ReviewID: &b.ID})
	if len(only) != 1 || only[0].ReviewName != "b" {
		t.Fatalf("by review = %+v", only)
	}
	if running, _ := s.ListReviewRuns(ctx, store.ReviewRunFilter{State: "running"}); len(running) != 0 {
		t.Fatalf("by state = %+v", running)
	}
	if _, err := s.GetReviewRun(ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
}

// --- retention ---

func insertReviewRunRow(t *testing.T, s *store.Store, key, state string, finished *time.Time) int64 {
	t.Helper()
	var id int64
	if err := s.Pool.QueryRow(context.Background(), `insert into agent_review_runs (review_name, review_type, period_key, period_label, scheduled_for, state, requested_at, finished_at, summary)
		values ($1, 'cost', $1, 'l', now(), $2, now(), $3, '') returning id`, key, state, finished).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func reviewRunExists(t *testing.T, s *store.Store, id int64) bool {
	t.Helper()
	return invCount(t, s, fmt.Sprintf("agent_review_runs where id = %d", id)) == 1
}

func hoursAgo(now time.Time, h int) *time.Time {
	t := now.Add(-time.Duration(h) * time.Hour)
	return &t
}

func TestRetention_DeletesOldReviewRunsWithEvidence(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	now := time.Now()
	old := insertReviewRunRow(t, s, "old", "done", hoursAgo(now, 100))
	oldFailed := insertReviewRunRow(t, s, "old-failed", "failed", hoursAgo(now, 90))
	oldSkipped := insertReviewRunRow(t, s, "old-skipped", "skipped", hoursAgo(now, 80))
	young := insertReviewRunRow(t, s, "young", "failed", hoursAgo(now, 60))
	invExec(t, s, `insert into agent_review_evidence (review_run_id, ordinal, tool, action, account_id, region, cluster_name, service_name) values ($1, 1, 't', 'view', 'a', 'r', 'c', 's')`, old)
	invExec(t, s, `insert into agent_review_evidence (review_run_id, ordinal, tool, action, account_id, region, cluster_name, service_name) values ($1, 1, 't', 'view', 'a', 'r', 'c', 's')`, young)
	res, err := s.RunRetention(ctx, now, store.Retention{AgentRuns: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res["agent_review_runs"] != 3 || reviewRunExists(t, s, old) || reviewRunExists(t, s, oldFailed) || reviewRunExists(t, s, oldSkipped) || !reviewRunExists(t, s, young) {
		t.Fatalf("result = %v; want the three settled runs older than 72h gone", res)
	}
	if invCount(t, s, "agent_review_evidence") != 1 {
		t.Fatal("the old run's evidence should cascade and the young run's stay")
	}
	if n, err := s.DeleteReviewRunsBefore(ctx, now.Add(-72*time.Hour), now); err != nil || n != 0 {
		t.Fatalf("second delete = %d, %v", n, err)
	}
}

func TestRetention_ReviewRunsNeverYoungerThanTheFloor(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now()
	recent := insertReviewRunRow(t, s, "recent", "done", hoursAgo(now, 30))
	past := insertReviewRunRow(t, s, "past", "done", hoursAgo(now, 49))
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !reviewRunExists(t, s, recent) || reviewRunExists(t, s, past) {
		t.Fatal("the floor should keep the 30h-old row and release the 49h-old one")
	}
	// A direct caller gets the same floor.
	if n, err := s.DeleteReviewRunsBefore(context.Background(), now, now); err != nil || n != 0 || !reviewRunExists(t, s, recent) {
		t.Fatalf("direct delete = %d, %v", n, err)
	}
}

func TestRetention_NeverDeletesWaitingOrRunningReviewRuns(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now()
	// Even with a finish time far in the past, an unsettled row stays.
	waiting := insertReviewRunRow(t, s, "waiting", "requested", hoursAgo(now, 1000))
	running := insertReviewRunRow(t, s, "running", "running", hoursAgo(now, 1000))
	if _, err := s.RunRetention(context.Background(), now, store.Retention{AgentRuns: 72 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !reviewRunExists(t, s, waiting) || !reviewRunExists(t, s, running) {
		t.Fatal("a requested or running review run was deleted")
	}
}

func TestRetention_NeverDeletesReviews(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := mustReview(t, s, newReview("ancient", route.ID))
	invExec(t, s, `update agent_reviews set created_at = $2, updated_at = $2, schedule_changed_at = $2 where id = $1`, rv.ID, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := s.RunRetention(ctx, time.Now(), store.Retention{AgentRuns: 72 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetReview(ctx, rv.ID); err != nil {
		t.Fatalf("review after retention: %v", err)
	}
	if invCount(t, s, "agent_review_routes") != 1 {
		t.Fatal("the review's route link went")
	}
}

// --- reconciler ---

func TestReconcile_ReviewerRunIsAuditedAsTheIdentity(t *testing.T) {
	st := storetest.Open(t)
	setBudget(t, st, 1_000_000)
	identity, err := st.EnsureAgentIdentity(t.Context())
	mustNoErr(t, err)
	run := agentRun(100)
	run.Role, run.UserID = "reviewer", &identity.ID
	mustNoErr(t, st.ReserveAgentRun(t.Context(), run, 1000*time.Hour, agentNow))
	tokenFor(t, st, identity.ID, run.ID)
	mustNoErr(t, st.RecordAgentRunUsage(t.Context(), run.ID, 11, 7, 93, agentNow.Add(time.Minute)))

	now := agentNow.Add(time.Minute + reconcileStale + time.Second)
	if res := reconcile(t, st, now); res != (store.AgentReconcileResult{Settled: 1, TokensDeleted: 1, Audited: 1}) {
		t.Fatalf("result = %+v", res)
	}
	rows := agentAuditRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	e, d := rows[0], rows[0].Details
	if e.ActorUserID == nil || *e.ActorUserID != identity.ID || e.ActorUsername != identity.Username || e.Target != run.ID.String() ||
		d["role"] != "reviewer" || d["outcome"] != "failed" || d["cost_micro_usd"] != float64(93) {
		t.Fatalf("audit = %+v", e)
	}
	// The review run row that held this agent run is not touched by the pass: the claim rules settle it after its lease.
	if res := reconcile(t, st, now.Add(time.Hour)); res != (store.AgentReconcileResult{}) {
		t.Fatalf("the second pass repaired something: %+v", res)
	}
}

// The tags a finish adds join those of the start, once each and in order; none adds none, and a lost claim adds none.
func TestFinishReview_AddsCoveredTags(t *testing.T) {
	for name, tc := range map[string]struct {
		start, finish, want []int64
		state, reason       string
	}{
		"more":                   {[]int64{3, 1}, []int64{7, 3, 2, 7}, []int64{1, 2, 3, 7}, "done", ""},
		"fewer at the finish":    {[]int64{1, 3}, []int64{1}, []int64{1, 3}, "done", ""},
		"none given":             {[]int64{1, 3}, nil, []int64{1, 3}, "done", ""},
		"covered nothing at all": {nil, nil, []int64{}, "done", ""},
		"a failure":              {[]int64{1}, nil, []int64{1}, "failed", "error"},
	} {
		t.Run(name, func(t *testing.T) {
			s, ctx := storetest.Open(t), context.Background()
			seedReviewRuns(t, s, revNow)
			run := claimReview(t, s, revNow)
			if err := s.StartReviewRun(ctx, run.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", CoveredTags: tc.start}, revNow); err != nil {
				t.Fatal(err)
			}
			var res *store.InvestigationResult
			if tc.state == "done" {
				res = &store.InvestigationResult{Summary: "s"}
			}
			stale := run.Claim()
			stale.Seq++
			if err := s.FinishReviewRun(ctx, stale, tc.state, tc.reason, res, []int64{99}, revNow); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("stale = %v, want ErrConflict", err)
			}
			if err := s.FinishReviewRun(ctx, run.Claim(), tc.state, tc.reason, res, tc.finish, revNow); err != nil {
				t.Fatal(err)
			}
			if got := reviewRunState(t, s, run.ID).CoveredTags; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("covered = %v, want %v", got, tc.want)
			}
		})
	}
}
