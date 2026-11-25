package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// reviewRunReasons are the fixed reasons a review run may end with. Checked in Go, so a new reason needs no migration.
var reviewRunReasons = map[string]bool{
	"budget": true, "not_visible": true, "not_configured": true, "rate_limited": true, "missed": true, "disabled": true,
	"agent_unavailable": true, "limit_reached": true, "no_result": true, "interrupted": true, "error": true, "instructions": true,
}

const (
	maxReviewScopeLabelRunes = 300
	maxReviewRangeRunes      = 20
	// reviewRunsLock serialises everything that writes or claims review runs.
	reviewRunsLock = `select pg_advisory_xact_lock(hashtext('agent_review_runs'))`
)

// ReviewRun is one run of a review for one period. ReviewID is nil once the review is deleted; the name and type are
// snapshots. ClaimedUntil is the lease while running and the earliest retry time while requested. RunID is the latest
// agent run and PrevRunID the one before it.
type ReviewRun struct {
	ID                          int64
	ReviewID                    *int64
	ReviewName, ReviewType      string
	PeriodKey, PeriodLabel      string
	ScheduledFor                time.Time
	Manual                      bool
	State, Reason               string
	Attempts                    int
	ClaimSeq                    int64
	ClaimedUntil                *time.Time
	RunID, PrevRunID            *uuid.UUID
	Provider, Model, ScopeLabel string
	// CoveredTags holds every tag the agent identity could View when the run started (P3-R1) or finished (P3-R16).
	CoveredTags           []int64
	ServicesTotal         int
	Range                 string
	Summary               string
	SummaryReplaced       bool
	EvidenceDropped       int
	DeliveryPlannedAt     *time.Time
	RequestedAt           time.Time
	StartedAt, FinishedAt *time.Time
	// Instructions is the copy of the review's text made when the run first started; Test marks a manual run that
	// is stored and never sent.
	Instructions string
	Test         bool
}

// ReviewClaim is the fence of one claim. ClaimReviewRun returns it; every later write by the holder must carry it.
// A row whose attempt or claim_seq differs, or that is not running, gives ErrConflict and nothing is written.
type ReviewClaim struct {
	RunID   int64
	Attempt int
	Seq     int64
}

// Claim returns the fence of the claim that produced r.
func (r ReviewRun) Claim() ReviewClaim {
	return ReviewClaim{RunID: r.ID, Attempt: r.Attempts, Seq: r.ClaimSeq}
}

// ReviewRunFilter pages the list newest first; BeforeAt (when not zero) and BeforeID are the keyset cursor.
type ReviewRunFilter struct {
	ReviewID *int64
	State    string
	BeforeAt time.Time
	BeforeID int64
	Limit    int
}

// DueReview is one occurrence the due scan found: the review at the version the scan read, the period and its
// label, the occurrence time, and whether it is too late to run (recorded as skipped).
type DueReview struct {
	ReviewID               int64
	Version                int
	PeriodKey, PeriodLabel string
	ScheduledFor           time.Time
	Missed                 bool
}

// ReviewStart is what the holder records when the agent run begins.
type ReviewStart struct {
	RunID                              uuid.UUID
	Provider, Model, ScopeLabel, Range string
	CoveredTags                        []int64
	ServicesTotal                      int
	// Instructions is copied to the row by the first start only.
	Instructions string
}

const reviewRunColumns = `id, review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, state, reason,
	attempts, claim_seq, claimed_until, run_id, prev_run_id, provider, model, scope_label, covered_tags, services_total, range,
	summary, summary_replaced, evidence_dropped, delivery_planned_at, requested_at, started_at, finished_at, instructions, test`

func scanReviewRun(row pgx.Row) (ReviewRun, error) {
	var r ReviewRun
	err := row.Scan(&r.ID, &r.ReviewID, &r.ReviewName, &r.ReviewType, &r.PeriodKey, &r.PeriodLabel, &r.ScheduledFor, &r.Manual, &r.State, &r.Reason,
		&r.Attempts, &r.ClaimSeq, &r.ClaimedUntil, &r.RunID, &r.PrevRunID, &r.Provider, &r.Model, &r.ScopeLabel, &r.CoveredTags, &r.ServicesTotal, &r.Range,
		&r.Summary, &r.SummaryReplaced, &r.EvidenceDropped, &r.DeliveryPlannedAt, &r.RequestedAt, &r.StartedAt, &r.FinishedAt, &r.Instructions, &r.Test)
	if r.CoveredTags == nil {
		r.CoveredTags = []int64{}
	}
	return r, err
}

func collectReviewRuns(rows pgx.Rows) ([]ReviewRun, error) {
	defer rows.Close()
	out := []ReviewRun{}
	for rows.Next() {
		r, err := scanReviewRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordDueReviewRuns is the only writer of scheduled review runs. In one transaction, serialised by an advisory
// lock, it re-reads each entry's review and inserts a row only when the review exists, is enabled, is still at the
// entry's version and its schedule changed before the occurrence. A row already recorded for the review and period
// is left alone, however many callers send it. A missed entry is recorded as skipped (reason missed, finished now),
// the others as requested. created counts the rows inserted.
func (s *Store) RecordDueReviewRuns(ctx context.Context, due []DueReview, now time.Time) (created int, err error) {
	if len(due) == 0 {
		return 0, nil
	}
	for _, d := range due {
		if err := checkInvestigationText("period key", d.PeriodKey, 100, 0); err != nil {
			return 0, err
		}
		if err := checkInvestigationText("period label", d.PeriodLabel, 200, 0); err != nil {
			return 0, err
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("record due review runs: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, reviewRunsLock); err != nil {
		return 0, fmt.Errorf("record due review runs: lock: %w", err)
	}
	for _, d := range due {
		tag, err := tx.Exec(ctx, `insert into agent_review_runs
			(review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, state, reason, requested_at, finished_at)
			select r.id, r.name, r.type, $3, $4, $5, false,
			       case when $6 then 'skipped' else 'requested' end, case when $6 then 'missed' else '' end,
			       $7::timestamptz, case when $6 then $7::timestamptz else null end
			from agent_reviews r
			where r.id = $1 and r.enabled and r.version = $2 and r.schedule_changed_at < $5
			for share of r
			on conflict (review_id, period_key) where review_id is not null do nothing`,
			d.ReviewID, d.Version, d.PeriodKey, d.PeriodLabel, d.ScheduledFor, d.Missed, now)
		if err != nil {
			return 0, fmt.Errorf("record due review runs: %w", err)
		}
		created += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("record due review runs: %w", err)
	}
	return created, nil
}

// RequestManualReviewRun queues a run of a review now, enabled or not. A test run is stored and never sent: it is
// marked planned when made, so no delivery is planned for it. ErrConflict when the review already has a
// requested or running run, ErrNotFound for an unknown review.
func (s *Store) RequestManualReviewRun(ctx context.Context, reviewID int64, periodLabel string, test bool, now time.Time) (ReviewRun, error) {
	if err := checkInvestigationText("period label", periodLabel, 200, 0); err != nil {
		return ReviewRun{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ReviewRun{}, fmt.Errorf("request review run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, reviewRunsLock); err != nil {
		return ReviewRun{}, fmt.Errorf("request review run: lock: %w", err)
	}
	var busy bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from agent_review_runs where review_id = $1 and state in ('requested', 'running'))`, reviewID).Scan(&busy); err != nil {
		return ReviewRun{}, fmt.Errorf("request review run: %w", err)
	}
	if busy {
		return ReviewRun{}, ErrConflict
	}
	run, err := scanReviewRun(tx.QueryRow(ctx, `insert into agent_review_runs
		(review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, requested_at, test, delivery_planned_at)
		select r.id, r.name, r.type, 'x:' || $2::text, $3, $4::timestamptz, true, $4::timestamptz, $5::boolean,
		       case when $5::boolean then $4::timestamptz end
		from agent_reviews r where r.id = $1 for share of r
		returning `+reviewRunColumns, reviewID, uuid.NewString(), periodLabel, now, test))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRun{}, ErrNotFound
	}
	if err != nil {
		return ReviewRun{}, fmt.Errorf("request review run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ReviewRun{}, fmt.Errorf("request review run: %w", err)
	}
	return run, nil
}

// ClaimReviewRun leases the oldest due review run to the caller, as ClaimInvestigation does: one that is requested
// with no retry time or a past one, or running with an expired lease. It returns ErrNotFound when nothing is due,
// while another review run is running with a live lease, and while an investigation requested after
// investigationsWaitingSince is waiting (a review never starts ahead of one). A row with no attempt left is failed
// as interrupted, not returned, and the next due row is tried. Everything runs in one transaction under the lock, after waiting for the rows of running runs, and an expired running
// row goes before a requested one, so there is never a second live lease. The pick skips locked rows: every running row is
// already held here, and a requested row held by a DeleteReview must not be waited for (the two would deadlock).
func (s *Store) ClaimReviewRun(ctx context.Context, now time.Time, lease time.Duration, investigationsWaitingSince time.Time) (ReviewRun, ReviewClaim, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, reviewRunsLock); err != nil {
		return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: lock: %w", err)
	}
	// Wait for any holder still writing its running row (a renew), so the checks below see its lease.
	if _, err := tx.Exec(ctx, `select 1 from agent_review_runs where state = 'running' order by id for update`); err != nil {
		return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: wait for running rows: %w", err)
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from agent_review_runs where state = 'running' and claimed_until > $1)
		or exists (select 1 from agent_investigations where state = 'requested' and requested_at > $2)`, now, investigationsWaitingSince).Scan(&blocked); err != nil {
		return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: %w", err)
	}
	if blocked {
		return ReviewRun{}, ReviewClaim{}, ErrNotFound
	}
	for {
		run, err := scanReviewRun(tx.QueryRow(ctx, `update agent_review_runs set
			state = case when attempts < $3 then 'running' else 'failed' end,
			reason = case when attempts < $3 then reason else 'interrupted' end,
			finished_at = case when attempts < $3 then finished_at else $1::timestamptz end,
			attempts = case when attempts < $3 then attempts + 1 else attempts end,
			claim_seq = case when attempts < $3 then claim_seq + 1 else claim_seq end,
			claimed_until = case when attempts < $3 then $2::timestamptz else null end
			where id = (select id from agent_review_runs
			  where (state = 'requested' and (claimed_until is null or claimed_until <= $1))
			     or (state = 'running' and claimed_until <= $1)
			  order by (state = 'running') desc, requested_at, id limit 1 for update skip locked)
			returning `+reviewRunColumns, now, now.Add(lease), maxInvestigationAttempts))
		if errors.Is(err, pgx.ErrNoRows) {
			// Rows failed on the way stay failed.
			if err := tx.Commit(ctx); err != nil {
				return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: %w", err)
			}
			return ReviewRun{}, ReviewClaim{}, ErrNotFound
		}
		if err != nil {
			return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: %w", err)
		}
		if run.State == InvestigationFailed {
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			return ReviewRun{}, ReviewClaim{}, fmt.Errorf("claim review run: %w", err)
		}
		return run, run.Claim(), nil
	}
}

// fencedReviewRun is the predicate every holder write carries: $1 id, $2 attempt, $3 claim_seq.
const fencedReviewRun = `id = $1 and state = 'running' and attempts = $2 and claim_seq = $3`

// fencedReviewUpdate runs an update whose where clause is fencedReviewRun and maps no rows to ErrConflict.
func (s *Store) fencedReviewUpdate(ctx context.Context, what string, c ReviewClaim, setSQL string, args ...any) error {
	all := append([]any{c.RunID, c.Attempt, c.Seq}, args...)
	tag, err := s.Pool.Exec(ctx, `update agent_review_runs set `+setSQL+` where `+fencedReviewRun, all...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// StartReviewRun records the agent run of the claimed attempt at now, stores no covered tags for a test run (phase 3 readers
// decide by tags and know no test runs, so only admins see it), copies start.Instructions to the row while its
// copy is empty (a later start, by a retry or another holder, leaves the first copy), and keeps the first attempt's run id as
// prev_run_id on the second, so both paid attempts are costed; a run given back free on the first attempt is not kept. ErrConflict when the claim is stale.
func (s *Store) StartReviewRun(ctx context.Context, c ReviewClaim, start ReviewStart, now time.Time) error {
	for _, f := range []struct {
		name, text string
		max        int
	}{{"provider", start.Provider, 200}, {"model", start.Model, 200}, {"scope label", start.ScopeLabel, maxReviewScopeLabelRunes}, {"range", start.Range, maxReviewRangeRunes}, {"instructions", start.Instructions, maxReviewInstructionRunes}} {
		if err := checkInvestigationText(f.name, f.text, f.max, 0); err != nil {
			return err
		}
	}
	tags := start.CoveredTags
	if tags == nil {
		tags = []int64{}
	}
	return s.fencedReviewUpdate(ctx, "start review run", c, `prev_run_id = case when attempts = 2 and run_id is distinct from $4 then coalesce(prev_run_id, run_id)
				when attempts = 2 then prev_run_id end,
		run_id = $4, provider = $5, model = $6,
		scope_label = $7, range = $8, covered_tags = case when test then '{}'::bigint[] else $9::bigint[] end, services_total = $10, started_at = $11,
		instructions = case when instructions = '' then $12 else instructions end`,
		start.RunID, start.Provider, start.Model, start.ScopeLabel, start.Range, tags, start.ServicesTotal, now, start.Instructions)
}

// RenewReviewRun extends the lease of a claim to now + lease. ErrConflict when the claim is stale.
func (s *Store) RenewReviewRun(ctx context.Context, c ReviewClaim, now time.Time, lease time.Duration) error {
	return s.fencedReviewUpdate(ctx, "renew review run", c, `claimed_until = $4`, now.Add(lease))
}

// ReleaseReviewRun hands a claimed attempt back to the queue, to be claimed again from retryAt (zero: at once).
// countAttempt false gives the attempt back; when that returns the row to no attempts started_at is cleared too
// (run_id stays), so the daily count ignores it. ErrConflict when the claim is stale.
func (s *Store) ReleaseReviewRun(ctx context.Context, c ReviewClaim, retryAt time.Time, countAttempt bool) error {
	var retry *time.Time
	if !retryAt.IsZero() {
		retry = &retryAt
	}
	return s.fencedReviewUpdate(ctx, "release review run", c, `state = 'requested', claimed_until = $4,
		started_at = case when $5 or attempts > 1 then started_at else null end,
		attempts = case when $5 then attempts else attempts - 1 end`, retry, countAttempt)
}

// SkipReviewRun ends a running review run without a result, for a claim the caller holds, with a fixed reason.
// ErrConflict when the claim is stale.
func (s *Store) SkipReviewRun(ctx context.Context, c ReviewClaim, reason string, now time.Time) error {
	if !reviewRunReasons[reason] {
		return fmt.Errorf("skip review run: unknown reason %q", reason)
	}
	return s.fencedReviewUpdate(ctx, "skip review run", c, `state = 'skipped', reason = $4, finished_at = $5, claimed_until = null`, reason, now)
}

// FinishReviewRun settles the claimed attempt as done (no reason, a summary) or failed (a fixed reason), writes the
// result and replaces the evidence in one transaction, and clears the lease. A review cites View evidence only.
// alsoCovered joins the covered tags of the start, in the same fenced update (P3-R16); none leaves them as they are. A
// test run has none, as at its start.
// ErrConflict when the claim is stale; a refused result is an error. Nothing is written in either case.
func (s *Store) FinishReviewRun(ctx context.Context, c ReviewClaim, state, reason string, result *InvestigationResult, alsoCovered []int64, now time.Time) error {
	switch state {
	case InvestigationDone:
		if reason != "" {
			return fmt.Errorf("finish review run: a done run has no reason, got %q", reason)
		}
	case InvestigationFailed:
		if !reviewRunReasons[reason] {
			return fmt.Errorf("finish review run: unknown reason %q", reason)
		}
	default:
		return fmt.Errorf("finish review run: cannot finish as %q", state)
	}
	if result == nil {
		if state == InvestigationDone {
			return errors.New("finish review run: a done run needs a result")
		}
		result = &InvestigationResult{}
	}
	if state == InvestigationDone && strings.TrimSpace(result.Summary) == "" {
		return errors.New("finish review run: a done run needs a summary")
	}
	if err := checkInvestigationText("summary", result.Summary, maxInvestigationSummary, 0); err != nil {
		return err
	}
	if err := checkEvidenceItems("finish review run", result.Evidence, result.EvidenceDropped, "view"); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finish review run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	tag, err := tx.Exec(ctx, `update agent_review_runs set state = $4, reason = $5, summary = $6, summary_replaced = $7,
		evidence_dropped = $8, finished_at = $9, claimed_until = null,
		covered_tags = case when test then '{}'::bigint[] else array(select distinct t from unnest(covered_tags || $10::bigint[]) t order by t) end
		where `+fencedReviewRun,
		c.RunID, c.Attempt, c.Seq, state, reason, result.Summary, result.SummaryReplaced, result.EvidenceDropped, now, alsoCovered)
	if err != nil {
		return fmt.Errorf("finish review run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `delete from agent_review_evidence where review_run_id = $1`, c.RunID); err != nil {
		return fmt.Errorf("finish review run: clear evidence: %w", err)
	}
	if err := insertEvidence(ctx, tx, `agent_review_evidence`, `review_run_id`, c.RunID, result.Evidence); err != nil {
		return fmt.Errorf("finish review run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish review run: %w", err)
	}
	return nil
}

// ReviewRunsStartedSince counts review runs whose latest run started at or after since; the daily cap reads it. The
// count and the start that follows are separate statements, so concurrent holders can overshoot the cap by their
// number. A free release clears started_at, so a run that never began is not counted.
func (s *Store) ReviewRunsStartedSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from agent_review_runs where started_at >= $1`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("count started review runs: %w", err)
	}
	return n, nil
}

// GetReviewRun returns one review run, or ErrNotFound.
func (s *Store) GetReviewRun(ctx context.Context, id int64) (ReviewRun, error) {
	r, err := scanReviewRun(s.Pool.QueryRow(ctx, `select `+reviewRunColumns+` from agent_review_runs where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewRun{}, ErrNotFound
	}
	if err != nil {
		return ReviewRun{}, fmt.Errorf("get review run: %w", err)
	}
	return r, nil
}

// ListReviewRunEvidence returns the stored evidence of a review run in order.
func (s *Store) ListReviewRunEvidence(ctx context.Context, id int64) ([]InvestigationEvidence, error) {
	out, err := s.listEvidence(ctx, `agent_review_evidence`, `review_run_id`, id)
	if err != nil {
		return nil, fmt.Errorf("list review run evidence: %w", err)
	}
	return out, nil
}

// ListReviewRuns returns review runs newest first, after the cursor when one is given.
func (s *Store) ListReviewRuns(ctx context.Context, f ReviewRunFilter) ([]ReviewRun, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	var before *time.Time
	if !f.BeforeAt.IsZero() {
		before = &f.BeforeAt
	}
	rows, err := s.Pool.Query(ctx, `select `+reviewRunColumns+` from agent_review_runs
		where ($1::bigint is null or review_id = $1) and ($2 = '' or state = $2)
		  and ($3::timestamptz is null or (requested_at, id) < ($3, $4))
		order by requested_at desc, id desc limit $5`, f.ReviewID, f.State, before, f.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list review runs: %w", err)
	}
	out, err := collectReviewRuns(rows)
	if err != nil {
		return nil, fmt.Errorf("list review runs: %w", err)
	}
	return out, nil
}

// LatestReviewRuns returns the last run of each review that has one, for the settings list.
func (s *Store) LatestReviewRuns(ctx context.Context) (map[int64]ReviewRun, error) {
	rows, err := s.Pool.Query(ctx, `select distinct on (review_id) `+reviewRunColumns+` from agent_review_runs
		where review_id is not null order by review_id, requested_at desc, id desc`)
	if err != nil {
		return nil, fmt.Errorf("latest review runs: %w", err)
	}
	runs, err := collectReviewRuns(rows)
	if err != nil {
		return nil, fmt.Errorf("latest review runs: %w", err)
	}
	out := make(map[int64]ReviewRun, len(runs))
	for _, r := range runs {
		out[*r.ReviewID] = r
	}
	return out, nil
}

// ReviewRunCosts returns, for each given review run, the settled cost in micro-dollars of both its agent runs
// (run_id and prev_run_id). A run whose agent runs retention has deleted is absent from the map.
func (s *Store) ReviewRunCosts(ctx context.Context, ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `select rr.id, sum(a.cost_micro_usd)::bigint
		from agent_review_runs rr join agent_runs a on a.id = rr.run_id or a.id = rr.prev_run_id
		where rr.id = any($1) group by rr.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("review run costs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, cost int64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, fmt.Errorf("review run costs: %w", err)
		}
		out[id] = cost
	}
	return out, rows.Err()
}

// DeleteReviewRunsBefore removes settled (done, failed, skipped) review runs finished before t, and never one that
// finished less than 48 hours before now, whatever t is. A requested or running row is never removed here, and
// the newest scheduled run of each existing review stays. Reviews are not
// touched. Evidence cascades.
func (s *Store) DeleteReviewRunsBefore(ctx context.Context, t, now time.Time) (int64, error) {
	if floor := now.Add(-minInvestigationRetention); t.After(floor) {
		t = floor
	}
	// The newest scheduled run of each existing review stays whatever its age: it is what stops a period that was
	// already run from being recorded again.
	tag, err := s.Pool.Exec(ctx, `delete from agent_review_runs r where r.state in ('done', 'failed', 'skipped') and r.finished_at < $1
		and not (r.review_id is not null and not r.manual and r.id = (select n.id from agent_review_runs n
		  where n.review_id = r.review_id and not n.manual order by n.requested_at desc, n.id desc limit 1))`, t)
	if err != nil {
		return 0, fmt.Errorf("delete review runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
