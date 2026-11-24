package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrAgentRunRunning is returned by AuditAgentRun for a run that has not been settled: its audit row would say how
// it ended before that is known.
var ErrAgentRunRunning = errors.New("agent run is still running")

// staleAgentRunSQL is the one rule for a run nobody is working on any more: $2 is now less the stale age.
const staleAgentRunSQL = `state = 'running' and coalesce(last_usage_at, started_at) < $2`

const (
	// agentAuditGrace is how long a finished run is left to write its own audit row.
	agentAuditGrace = time.Minute
	// agentReconcileBatch bounds the audit rows written in one pass; the rest wait for the next.
	agentReconcileBatch = 500
	// A pass looks agentReconcileLooks times, agentReconcileLookAgain apart, for rows that were held.
	agentReconcileLooks     = 4
	agentReconcileLookAgain = 250 * time.Millisecond
)

// AgentRunAudit is the audit row of a run: who, which model, which tools and what it cost, never what was said.
// outcome is done, or a word for how it failed.
func AgentRunAudit(run AgentRun, username, outcome string) AuditEntry {
	tools := run.Tools
	if tools == nil {
		tools = []string{}
	}
	return AuditEntry{
		ActorUserID: run.UserID, ActorUsername: username,
		Action: "agent_run", Target: run.ID.String(), Summary: "AI agent run",
		Details: map[string]any{
			"run_id": run.ID.String(), "role": run.Role, "provider": run.Provider, "model": run.Model, "tools": tools,
			"input_tokens": run.InputTokens, "output_tokens": run.OutputTokens, "cost_micro_usd": run.CostMicroUSD,
			"outcome": outcome,
		},
	}
}

// AuditAgentRun writes a settled run's audit row and marks the run audited, in one transaction. A run that is
// already audited is left alone (nil), so the turn and the reconcile pass may both call it. A running run is
// ErrAgentRunRunning, an unknown one ErrNotFound. The row's outcome never contradicts the run's state: one that does
// is replaced by the state.
func (s *Store) AuditAgentRun(ctx context.Context, runID uuid.UUID, e AuditEntry) error {
	_, err := s.auditAgentRun(ctx, runID, e, "")
	return err
}

// auditAgentRun reports whether it wrote the row; lock is "skip locked" to leave a run that somebody else holds.
func (s *Store) auditAgentRun(ctx context.Context, runID uuid.UUID, e AuditEntry, lock string) (written bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("audit agent run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var state string
	var audited bool
	err = tx.QueryRow(ctx, `select state, audited_at is not null from agent_runs where id = $1 for update `+lock, runID).Scan(&state, &audited)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && lock != "":
		return false, nil
	case errors.Is(err, pgx.ErrNoRows):
		return false, ErrNotFound
	case err != nil:
		return false, fmt.Errorf("audit agent run: %w", err)
	case audited:
		return false, nil
	case state == "running":
		return false, ErrAgentRunRunning
	}
	details := make(map[string]any, len(e.Details))
	for k, v := range e.Details {
		details[k] = v
	}
	if (state == "done") != (details["outcome"] == "done") {
		details["outcome"] = state
	}
	e.Details = details
	if _, err := tx.Exec(ctx, `update agent_runs set audited_at = now() where id = $1`, runID); err != nil {
		return false, fmt.Errorf("audit agent run: %w", err)
	}
	if err := insertAudit(ctx, tx, e); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("audit agent run: %w", err)
	}
	return true, nil
}

// ReconcileAgentRuns repairs what runs left behind when their turn could not finish: it fails stale running runs
// (ReserveAgentRun's rule), deletes run tokens whose run is not running or does not exist, and writes the missing
// audit row of runs it just failed or that finished over a minute ago. A run that is running and not stale, and its
// token, are never touched. It never waits for a row somebody else holds, so it may run beside a live turn or another
// pass; it looks a few more times, in case it was held for a moment only (a turn's own last attempt is cut off just
// before), and then leaves it to the next pass. Each part runs whether or not the one before it failed. An answer
// that could not be saved is not rebuilt: what was said is stored nowhere else.
func (s *Store) ReconcileAgentRuns(ctx context.Context, staleAfter time.Duration, now time.Time) (AgentReconcileResult, error) {
	var res AgentReconcileResult
	var settled []uuid.UUID
	for looks := 1; ; looks++ {
		ids, settleErr := s.settleStaleAgentRuns(ctx, staleAfter, now)
		settled = append(settled, ids...)
		tokens, tokenErr := s.deleteEndedRunTokens(ctx)
		audited, auditErr := s.auditFinishedAgentRuns(ctx, settled, now)
		res.Settled += int64(len(ids))
		res.TokensDeleted += tokens
		res.Audited += audited
		if err := errors.Join(settleErr, tokenErr, auditErr); err != nil || looks == agentReconcileLooks {
			return res, err
		}
		var left bool
		if err := s.Pool.QueryRow(ctx, `select exists (select 1 from agent_runs where `+staleAgentRunSQL+`)
			or exists (select 1 from api_tokens t where `+endedRunTokenSQL+`)
			or exists (select 1 from agent_runs r where `+unauditedAgentRunSQL+`)`,
			now.Add(-agentAuditGrace), now.Add(-staleAfter), settled).Scan(&left); err != nil {
			return res, fmt.Errorf("reconcile agent runs: look for what is left: %w", err)
		}
		if !left {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(agentReconcileLookAgain):
		}
	}
}

// endedRunTokenSQL is a run token t nobody needs any more.
const endedRunTokenSQL = `t.kind = 'run' and not exists (select 1 from agent_runs r where r.id = t.run_id and r.state = 'running')`

// unauditedAgentRunSQL is a finished run r whose audit row is overdue: $1 is now less the grace, $3 the runs this
// pass settled itself.
const unauditedAgentRunSQL = `r.state <> 'running' and r.audited_at is null and (r.finished_at < $1 or r.id = any($3::uuid[]))`

// AgentReconcileResult counts what one ReconcileAgentRuns pass repaired.
type AgentReconcileResult struct{ Settled, TokensDeleted, Audited int64 }

// settleStaleAgentRuns fails the stale runs nobody holds and returns their ids.
func (s *Store) settleStaleAgentRuns(ctx context.Context, staleAfter time.Duration, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `with stale as materialized (
			select id from agent_runs where `+staleAgentRunSQL+` for update skip locked)
		update agent_runs set state = 'failed', finished_at = $1 where id in (select id from stale) returning id`,
		now, now.Add(-staleAfter))
	if err != nil {
		return nil, fmt.Errorf("reconcile agent runs: settle stale runs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("reconcile agent runs: settle stale runs: %w", err)
	}
	return ids, nil
}

func (s *Store) deleteEndedRunTokens(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `with ended as materialized (
			select t.id from api_tokens t where `+endedRunTokenSQL+` for update skip locked)
		delete from api_tokens where id in (select id from ended)`)
	if err != nil {
		return 0, fmt.Errorf("reconcile agent runs: delete tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// auditFinishedAgentRuns writes the audit rows of justSettled and of runs whose own row is overdue, one transaction
// each, so one that cannot be written does not hold up the rest.
func (s *Store) auditFinishedAgentRuns(ctx context.Context, justSettled []uuid.UUID, now time.Time) (written int64, err error) {
	rows, err := s.Pool.Query(ctx, `select r.id, r.role, r.user_id, r.provider, r.model, r.state, r.cost_micro_usd,
			r.input_tokens, r.output_tokens, r.tools, coalesce(u.username, '')
		from agent_runs r left join users u on u.id = r.user_id
		where `+unauditedAgentRunSQL+` order by r.finished_at limit $2`, now.Add(-agentAuditGrace), agentReconcileBatch, justSettled)
	if err != nil {
		return 0, fmt.Errorf("reconcile agent runs: list unaudited runs: %w", err)
	}
	type unaudited struct {
		run      AgentRun
		username string
	}
	due, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (u unaudited, err error) {
		err = row.Scan(&u.run.ID, &u.run.Role, &u.run.UserID, &u.run.Provider, &u.run.Model, &u.run.State,
			&u.run.CostMicroUSD, &u.run.InputTokens, &u.run.OutputTokens, &u.run.Tools, &u.username)
		return u, err
	})
	if err != nil {
		return 0, fmt.Errorf("reconcile agent runs: list unaudited runs: %w", err)
	}
	var errs []error
	for _, u := range due {
		ok, err := s.auditAgentRun(ctx, u.run.ID, AgentRunAudit(u.run, u.username, u.run.State), "skip locked")
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			written++
		}
		if ctx.Err() != nil {
			break
		}
	}
	return written, errors.Join(errs...)
}
