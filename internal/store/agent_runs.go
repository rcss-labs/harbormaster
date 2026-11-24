package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrBudgetExhausted is returned when a run's reservation would take the month's spend over the budget.
var ErrBudgetExhausted = errors.New("agent monthly budget exhausted")

// AgentRun is one model run. Money is integer micro-dollars. UserID is nil once the user is deleted.
type AgentRun struct {
	ID                        uuid.UUID
	Role                      string
	UserID                    *int64
	ConversationID            *int64
	Provider, Model           string
	State                     string // running | done | failed
	ReservedMicroUSD          int64
	CostMicroUSD              int64
	InputTokens, OutputTokens int64
	Tools                     []string
	StartedAt                 time.Time
	FinishedAt, LastUsageAt   *time.Time
}

// AgentUsageDay is one day and role of finished usage.
type AgentUsageDay struct {
	Day                       time.Time
	Role                      string
	Runs                      int64
	CostMicroUSD              int64
	InputTokens, OutputTokens int64
}

// spendSQL sums a month of runs: finished runs at cost, running ones at the larger of reservation and cost.
const spendSQL = `select
	coalesce(sum(cost_micro_usd) filter (where state <> 'running'), 0),
	coalesce(sum(greatest(reserved_micro_usd, cost_micro_usd)) filter (where state = 'running'), 0)
	from agent_runs where started_at >= $1 and started_at < $2`

func utcMonthBounds(t time.Time) (start, end time.Time) {
	t = t.UTC()
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// ReserveAgentRun inserts the run as running if its reservation fits the month's budget, else returns
// ErrBudgetExhausted. The settings row lock serialises concurrent reservations. Stale runs are failed first and
// that is committed even on refusal, so a crashed task's reservation is never held by a refused caller's rollback.
func (s *Store) ReserveAgentRun(ctx context.Context, run AgentRun, staleAfter time.Duration, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reserve agent run: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var budget int64
	if err := tx.QueryRow(ctx, `select monthly_budget_micro_usd from agent_settings for update`).Scan(&budget); err != nil {
		return fmt.Errorf("reserve agent run: lock settings: %w", err)
	}
	if _, err := tx.Exec(ctx, `update agent_runs set state = 'failed', finished_at = $1
		where state = 'running' and coalesce(last_usage_at, started_at) < $2`, now, now.Add(-staleAfter)); err != nil {
		return fmt.Errorf("reserve agent run: settle stale runs: %w", err)
	}
	start, end := utcMonthBounds(now)
	var settled, reserved int64
	if err := tx.QueryRow(ctx, spendSQL, start, end).Scan(&settled, &reserved); err != nil {
		return fmt.Errorf("reserve agent run: spend: %w", err)
	}
	if settled+reserved+run.ReservedMicroUSD > budget {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("reserve agent run: %w", err)
		}
		return ErrBudgetExhausted
	}
	tools := run.Tools
	if tools == nil {
		tools = []string{}
	}
	if _, err := tx.Exec(ctx, `insert into agent_runs
		(id, role, user_id, conversation_id, provider, model, state, reserved_micro_usd, tools, started_at)
		values ($1, $2, $3, $4, $5, $6, 'running', $7, $8, $9)`,
		run.ID, run.Role, run.UserID, run.ConversationID, run.Provider, run.Model, run.ReservedMicroUSD, tools, now); err != nil {
		return fmt.Errorf("reserve agent run: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("reserve agent run: %w", err)
	}
	return nil
}

// RecordAgentRunUsage overwrites the running run's tokens and cost with cumulative values; they may go down.
// It touches only running rows: a run already failed by the stale sweep keeps what it had.
func (s *Store) RecordAgentRunUsage(ctx context.Context, id uuid.UUID, inputTokens, outputTokens, costMicroUSD int64, now time.Time) error {
	tag, err := s.Pool.Exec(ctx, `update agent_runs set input_tokens = $2, output_tokens = $3, cost_micro_usd = $4, last_usage_at = $5
		where id = $1 and state = 'running'`, id, inputTokens, outputTokens, costMicroUSD, now)
	if err != nil {
		return fmt.Errorf("record agent run usage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.agentRunMissing(ctx, id)
	}
	return nil
}

// SettleAgentRun finishes a running run as done or failed, keeping its last recorded cost; the unused
// reservation is freed because only cost counts once a run is finished. Settling a finished run is a no-op.
func (s *Store) SettleAgentRun(ctx context.Context, id uuid.UUID, state string, tools []string, now time.Time) error {
	if state != "done" && state != "failed" {
		return fmt.Errorf("settle agent run: invalid state %q", state)
	}
	if tools == nil {
		tools = []string{}
	}
	tag, err := s.Pool.Exec(ctx, `update agent_runs set state = $2, tools = $3, finished_at = $4
		where id = $1 and state = 'running'`, id, state, tools, now)
	if err != nil {
		return fmt.Errorf("settle agent run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.agentRunMissing(ctx, id)
	}
	return nil
}

// agentRunMissing returns ErrNotFound when no such run exists; an existing finished run is not an error.
func (s *Store) agentRunMissing(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := s.Pool.QueryRow(ctx, `select exists(select 1 from agent_runs where id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// AgentSpend returns the UTC month's finished cost and the running runs' held amount.
func (s *Store) AgentSpend(ctx context.Context, monthStart time.Time) (settledMicroUSD, reservedMicroUSD int64, err error) {
	start, end := utcMonthBounds(monthStart)
	err = s.Pool.QueryRow(ctx, spendSQL, start, end).Scan(&settledMicroUSD, &reservedMicroUSD)
	if err != nil {
		return 0, 0, fmt.Errorf("agent spend: %w", err)
	}
	return settledMicroUSD, reservedMicroUSD, nil
}

// AgentUsageByDay returns the UTC month's finished usage per day and role.
func (s *Store) AgentUsageByDay(ctx context.Context, monthStart time.Time) ([]AgentUsageDay, error) {
	start, end := utcMonthBounds(monthStart)
	rows, err := s.Pool.Query(ctx, `select (started_at at time zone 'UTC')::date, role, count(*), sum(cost_micro_usd),
		sum(input_tokens), sum(output_tokens)
		from agent_runs where state <> 'running' and started_at >= $1 and started_at < $2
		group by 1, 2 order by 1, 2`, start, end)
	if err != nil {
		return nil, fmt.Errorf("agent usage by day: %w", err)
	}
	defer rows.Close()
	var out []AgentUsageDay
	for rows.Next() {
		var d AgentUsageDay
		if err := rows.Scan(&d.Day, &d.Role, &d.Runs, &d.CostMicroUSD, &d.InputTokens, &d.OutputTokens); err != nil {
			return nil, fmt.Errorf("scan agent usage: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
