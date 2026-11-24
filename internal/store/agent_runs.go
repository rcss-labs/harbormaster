package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBudgetExhausted is returned when a run's reservation would take the month's spend over the budget.
var ErrBudgetExhausted = errors.New("agent monthly budget exhausted")

// ErrAgentRunNotRunning is returned when usage or a settle arrives for a run that is no longer running, typically
// because the stale sweep failed it. Late figures are still recorded on a failed run (never lowered).
var ErrAgentRunNotRunning = errors.New("agent run is not running")

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

// AgentUsage is a run's cumulative figures.
type AgentUsage struct{ InputTokens, OutputTokens, CostMicroUSD int64 }

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
	least(coalesce(sum(cost_micro_usd::numeric) filter (where state <> 'running'), 0), 9223372036854775807::numeric)::bigint,
	least(coalesce(sum(greatest(reserved_micro_usd, cost_micro_usd)::numeric) filter (where state = 'running'), 0), 9223372036854775807::numeric)::bigint
	from agent_runs where started_at >= $1 and started_at < $2`

func utcMonthBounds(t time.Time) (start, end time.Time) {
	t = t.UTC()
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// retryDeadlock runs fn, and once more if Postgres picked it as a deadlock victim. Reserving locks the settings
// row, then run rows and the conversation; deleting a conversation locks it, then the runs that reference it.
func retryDeadlock(fn func() error) error {
	err := fn()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		return fn()
	}
	return err
}

// ReserveAgentRun inserts the run as running if its reservation fits the month's budget, else returns
// ErrBudgetExhausted. A budget of 0 refuses everything, a zero reservation included. The settings row lock
// serialises concurrent reservations. Stale runs are failed first and that is committed even on refusal, so a
// refused caller does not roll back the sweep. A ConversationID must belong to UserID, else ErrNotFound.
func (s *Store) ReserveAgentRun(ctx context.Context, run AgentRun, staleAfter time.Duration, now time.Time) error {
	if run.ReservedMicroUSD < 0 {
		return fmt.Errorf("reserve agent run: negative reservation %d", run.ReservedMicroUSD)
	}
	return retryDeadlock(func() error { return s.reserveAgentRun(ctx, run, staleAfter, now) })
}

func (s *Store) reserveAgentRun(ctx context.Context, run AgentRun, staleAfter time.Duration, now time.Time) error {
	if run.ConversationID != nil {
		if run.UserID == nil {
			return ErrNotFound
		}
		if _, err := s.GetAgentConversation(ctx, *run.UserID, *run.ConversationID); err != nil {
			return err
		}
	}
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
		where `+staleAgentRunSQL, now, now.Add(-staleAfter)); err != nil {
		return fmt.Errorf("reserve agent run: settle stale runs: %w", err)
	}
	start, end := utcMonthBounds(now)
	var settled, reserved int64
	if err := tx.QueryRow(ctx, spendSQL, start, end).Scan(&settled, &reserved); err != nil {
		return fmt.Errorf("reserve agent run: spend: %w", err)
	}
	if !fitsBudget(budget, settled, reserved, run.ReservedMicroUSD) {
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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && run.ConversationID != nil {
			return ErrNotFound // the conversation was deleted after the ownership check
		}
		return fmt.Errorf("reserve agent run: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("reserve agent run: %w", err)
	}
	return nil
}

// fitsBudget reports whether settled+reserved+request stays within budget, without overflowing int64. All inputs
// are non-negative. A budget of 0 fits nothing.
func fitsBudget(budget, settled, reserved, request int64) bool {
	if budget <= 0 || settled > budget {
		return false
	}
	room := budget - settled
	if reserved > room {
		return false
	}
	return request <= room-reserved
}

// RecordAgentRunUsage records cumulative tokens and cost. On a running run it overwrites (figures may go down:
// the loop reports a provisional worst case, then corrects it). On a run the stale sweep already failed it only
// raises them, keeps the state, and returns ErrAgentRunNotRunning so the caller knows. A done run is unchanged
// (also ErrAgentRunNotRunning); an unknown id is ErrNotFound.
func (s *Store) RecordAgentRunUsage(ctx context.Context, id uuid.UUID, inputTokens, outputTokens, costMicroUSD int64, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("record agent run usage: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var state string
	if err := tx.QueryRow(ctx, `select state from agent_runs where id = $1 for update`, id).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("record agent run usage: %w", err)
	}
	switch state {
	case "running":
		_, err = tx.Exec(ctx, `update agent_runs set input_tokens = $2, output_tokens = $3, cost_micro_usd = $4,
			last_usage_at = greatest(last_usage_at, $5) where id = $1`, id, inputTokens, outputTokens, costMicroUSD, now)
	case "failed":
		_, err = tx.Exec(ctx, `update agent_runs set input_tokens = greatest(input_tokens, $2), output_tokens = greatest(output_tokens, $3),
			cost_micro_usd = greatest(cost_micro_usd, $4), last_usage_at = greatest(last_usage_at, $5) where id = $1`,
			id, inputTokens, outputTokens, costMicroUSD, now)
	}
	if err != nil {
		return fmt.Errorf("record agent run usage: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("record agent run usage: %w", err)
	}
	if state != "running" {
		return ErrAgentRunNotRunning
	}
	return nil
}

// SettleAgentRun finishes a running run as done or failed, with its last figures in the same write (usage nil keeps
// those recorded); the unused reservation is freed because only cost counts once a run is finished. The figures
// follow RecordAgentRunUsage: a running run takes them as given, and a run the stale sweep already failed only has
// them raised; it also records the tools, keeps its state, and returns ErrAgentRunNotRunning. A done run is
// unchanged (also ErrAgentRunNotRunning). An unknown id is ErrNotFound.
func (s *Store) SettleAgentRun(ctx context.Context, id uuid.UUID, state string, usage *AgentUsage, tools []string, now time.Time) error {
	if state != "done" && state != "failed" {
		return fmt.Errorf("settle agent run: invalid state %q", state)
	}
	if tools == nil {
		tools = []string{}
	}
	var input, output, cost *int64
	if usage != nil {
		input, output, cost = &usage.InputTokens, &usage.OutputTokens, &usage.CostMicroUSD
	}
	var prior string
	err := s.Pool.QueryRow(ctx, `with prior as (select state from agent_runs where id = $1 for update),
		upd as (update agent_runs set
			state = case when state = 'running' then $2 else state end,
			finished_at = case when state = 'running' then $4 else finished_at end,
			tools = case when state = 'running' or (state = 'failed' and cardinality($3::text[]) > 0) then $3 else tools end,
			input_tokens = case state when 'running' then coalesce($5::bigint, input_tokens) when 'failed' then greatest(input_tokens, $5) else input_tokens end,
			output_tokens = case state when 'running' then coalesce($6::bigint, output_tokens) when 'failed' then greatest(output_tokens, $6) else output_tokens end,
			cost_micro_usd = case state when 'running' then coalesce($7::bigint, cost_micro_usd) when 'failed' then greatest(cost_micro_usd, $7) else cost_micro_usd end
			where id = $1 returning 1)
		select state from prior`, id, state, tools, now, input, output, cost).Scan(&prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("settle agent run: %w", err)
	}
	if prior != "running" {
		return ErrAgentRunNotRunning
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
	rows, err := s.Pool.Query(ctx, `select (started_at at time zone 'UTC')::date, role, count(*),
		least(sum(cost_micro_usd::numeric), 9223372036854775807::numeric)::bigint,
		least(sum(input_tokens::numeric), 9223372036854775807::numeric)::bigint,
		least(sum(output_tokens::numeric), 9223372036854775807::numeric)::bigint
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
