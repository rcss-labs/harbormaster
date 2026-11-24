package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AgentModelRef names the provider and model a role uses.
type AgentModelRef struct{ Provider, Model string }

// ErrAgentSettingsChanged is returned when a save was made from settings that have since been saved by someone else.
var ErrAgentSettingsChanged = errors.New("agent settings changed since they were read")

// AgentSettings is the admin-set agent configuration. A zero budget refuses every run. Version is raised by every
// save.
type AgentSettings struct {
	Version               int64
	MonthlyBudgetMicroUSD int64
	UnredactedProviders   []string
	RoleModels            map[string]AgentModelRef
}

// GetAgentSettings returns the settings; the row is seeded by the migration.
func (s *Store) GetAgentSettings(ctx context.Context) (AgentSettings, error) {
	// One repeatable-read snapshot, so the budget and role models cannot come from two different saves.
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AgentSettings{}, fmt.Errorf("get agent settings: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	out := AgentSettings{RoleModels: map[string]AgentModelRef{}}
	if err := readAgentSettings(ctx, tx, "", &out); err != nil {
		return AgentSettings{}, err
	}
	return out, nil
}

// readAgentSettings fills out from the settings row and role models; lock is "for update" to take the row lock.
func readAgentSettings(ctx context.Context, tx pgx.Tx, lock string, out *AgentSettings) error {
	if err := tx.QueryRow(ctx, `select monthly_budget_micro_usd, unredacted_providers, version from agent_settings `+lock).
		Scan(&out.MonthlyBudgetMicroUSD, &out.UnredactedProviders, &out.Version); err != nil {
		return fmt.Errorf("get agent settings: %w", err)
	}
	rows, err := tx.Query(ctx, `select role, provider, model from agent_role_models`)
	if err != nil {
		return fmt.Errorf("get agent role models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var ref AgentModelRef
		if err := rows.Scan(&role, &ref.Provider, &ref.Model); err != nil {
			return fmt.Errorf("scan agent role model: %w", err)
		}
		out.RoleModels[role] = ref
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("get agent role models: %w", err)
	}
	return nil
}

// UpdateAgentSettings reads the settings under the row lock ReserveAgentRun takes, returns ErrAgentSettingsChanged
// when their version is not version, and otherwise saves what apply makes of them (an error from apply aborts the
// save and is returned as is) and raises the version. apply runs while the lock is held, so it must not wait on
// the network. It returns the settings as they were.
func (s *Store) UpdateAgentSettings(ctx context.Context, version int64, apply func(old AgentSettings) (AgentSettings, error)) (AgentSettings, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AgentSettings{}, fmt.Errorf("update agent settings: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	old := AgentSettings{RoleModels: map[string]AgentModelRef{}}
	if err := readAgentSettings(ctx, tx, "for update", &old); err != nil {
		return AgentSettings{}, err
	}
	if old.Version != version {
		return old, ErrAgentSettingsChanged
	}
	next, err := apply(old)
	if err != nil {
		return old, err
	}
	if err := writeAgentSettings(ctx, tx, next); err != nil {
		return old, err
	}
	if err := tx.Commit(ctx); err != nil {
		return old, fmt.Errorf("update agent settings: %w", err)
	}
	return old, nil
}

// writeAgentSettings replaces the settings row's values and the role models, and raises the version.
func writeAgentSettings(ctx context.Context, tx pgx.Tx, in AgentSettings) error {
	providers := in.UnredactedProviders
	if providers == nil {
		providers = []string{}
	}
	tag, err := tx.Exec(ctx, `update agent_settings set monthly_budget_micro_usd = $1, unredacted_providers = $2, version = version + 1`,
		in.MonthlyBudgetMicroUSD, providers)
	if err != nil {
		return fmt.Errorf("put agent settings: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("put agent settings: settings row missing")
	}
	if _, err := tx.Exec(ctx, `delete from agent_role_models`); err != nil {
		return fmt.Errorf("put agent role models: %w", err)
	}
	for role, ref := range in.RoleModels {
		if _, err := tx.Exec(ctx, `insert into agent_role_models (role, provider, model) values ($1, $2, $3)`,
			role, ref.Provider, ref.Model); err != nil {
			return fmt.Errorf("put agent role models: %w", err)
		}
	}
	return nil
}
