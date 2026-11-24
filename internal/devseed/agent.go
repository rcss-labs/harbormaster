package devseed

import (
	"context"
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// WithAgentDemo seeds the AI agent demo: sam may open the chat, the chat role uses the agent's fake model, and the
// monthly allowance is 5 USD. Pass it only when the agent is enabled (HM_AGENT_URL).
func WithAgentDemo() Option {
	return func(o *options) { o.agentDemo = true }
}

const demoAgentBudgetMicroUSD = 5_000_000

// seedAgent runs once: it does nothing when the chat role already has a model, so a restart never undoes what an admin
// changed afterwards.
func seedAgent(ctx context.Context, st *store.Store, o options, samID int64) error {
	if !o.agentDemo {
		return nil
	}
	current, err := st.GetAgentSettings(ctx)
	if err != nil {
		return err
	}
	if _, set := current.RoleModels["chat"]; set {
		return nil
	}
	if _, err := st.UpdateAgentSettings(ctx, current.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.MonthlyBudgetMicroUSD = demoAgentBudgetMicroUSD
		old.RoleModels["chat"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
		return old, nil
	}); err != nil {
		return fmt.Errorf("seed agent settings: %w", err)
	}
	if err := st.SetUserAIChat(ctx, samID, true); err != nil {
		return fmt.Errorf("seed agent chat access: %w", err)
	}
	return nil
}
