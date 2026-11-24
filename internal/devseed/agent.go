package devseed

import (
	"context"
	"errors"
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// WithAgentDemo seeds the AI agent demo: sam may open the chat, the chat role uses the agent's fake model, and the
// monthly allowance is 5 USD. Pass it only when the agent is enabled (HM_AGENT_URL).
func WithAgentDemo() Option {
	return func(o *options) { o.agentDemo = true }
}

const (
	demoAgentBudgetMicroUSD = 5_000_000
	// untouchedSettingsVersion is the version the migration gives the settings row; every save raises it.
	untouchedSettingsVersion = 1
)

// seedAgent runs until the settings are first saved. The settings row's version is the marker: it survives an admin
// clearing the model, needs no row that audit retention could prune, and a restart after the seed never undoes what an
// admin changed. Chat access is granted first and the settings save is the commit point, so a crash between the two
// leaves the version untouched and the next start finishes the job.
func seedAgent(ctx context.Context, st *store.Store, o options, samID int64) error {
	if !o.agentDemo {
		return nil
	}
	current, err := st.GetAgentSettings(ctx)
	if err != nil {
		return err
	}
	if current.Version != untouchedSettingsVersion {
		return nil
	}
	if err := st.SetUserAIChat(ctx, samID, true); err != nil {
		return fmt.Errorf("seed agent chat access: %w", err)
	}
	_, err = st.UpdateAgentSettings(ctx, current.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.MonthlyBudgetMicroUSD = demoAgentBudgetMicroUSD
		old.RoleModels["chat"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
		return old, nil
	})
	if errors.Is(err, store.ErrAgentSettingsChanged) {
		return nil // an admin saved first
	}
	if err != nil {
		return fmt.Errorf("seed agent settings: %w", err)
	}
	return nil
}
