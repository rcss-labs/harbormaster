package store

import (
	"context"
	"fmt"
)

// AgentModelRef names the provider and model a role uses.
type AgentModelRef struct{ Provider, Model string }

// AgentSettings is the admin-set agent configuration. A zero budget refuses every run.
type AgentSettings struct {
	MonthlyBudgetMicroUSD int64
	UnredactedProviders   []string
	RoleModels            map[string]AgentModelRef
}

// GetAgentSettings returns the settings; the row is seeded by the migration.
func (s *Store) GetAgentSettings(ctx context.Context) (AgentSettings, error) {
	out := AgentSettings{RoleModels: map[string]AgentModelRef{}}
	if err := s.Pool.QueryRow(ctx, `select monthly_budget_micro_usd, unredacted_providers from agent_settings`).
		Scan(&out.MonthlyBudgetMicroUSD, &out.UnredactedProviders); err != nil {
		return AgentSettings{}, fmt.Errorf("get agent settings: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `select role, provider, model from agent_role_models`)
	if err != nil {
		return AgentSettings{}, fmt.Errorf("get agent role models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var ref AgentModelRef
		if err := rows.Scan(&role, &ref.Provider, &ref.Model); err != nil {
			return AgentSettings{}, fmt.Errorf("scan agent role model: %w", err)
		}
		out.RoleModels[role] = ref
	}
	return out, rows.Err()
}

// PutAgentSettings replaces the settings, role models included, in one transaction. It takes the same row lock
// as ReserveAgentRun, so a budget change never interleaves with a reservation.
func (s *Store) PutAgentSettings(ctx context.Context, in AgentSettings) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("put agent settings: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	providers := in.UnredactedProviders
	if providers == nil {
		providers = []string{}
	}
	if _, err := tx.Exec(ctx, `update agent_settings set monthly_budget_micro_usd = $1, unredacted_providers = $2`,
		in.MonthlyBudgetMicroUSD, providers); err != nil {
		return fmt.Errorf("put agent settings: %w", err)
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
	return tx.Commit(ctx)
}
