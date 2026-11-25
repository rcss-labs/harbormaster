package devseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// WithAgentDemo seeds the AI agent demo: sam may open the chat, the chat and investigator roles use the agent's fake
// model, the monthly allowance is 5 USD, and the agent identity may View and read Logs of every demo tag. With the
// notification demo on too, one webhook route to the in-process receiver has the automatic investigation switched on. Pass it only when the agent is enabled (HM_AGENT_URL).
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
	if err := seedInvestigator(ctx, st, o, samID); err != nil {
		return err
	}
	_, err = st.UpdateAgentSettings(ctx, current.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.MonthlyBudgetMicroUSD = demoAgentBudgetMicroUSD
		old.RoleModels["chat"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
		old.RoleModels["investigator"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
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

const investigatorRouteName = "Investigated alerts to the fake receiver"

// seedInvestigator gives the agent identity View and Logs on every tag and, when the notification demo is on, adds the
// one route whose alerts are investigated. The route is scoped to one failing service so the catch-all route keeps its
// meaning for the others. Both steps may repeat after a crash: the permissions are replaced, the route is found by name.
func seedInvestigator(ctx context.Context, st *store.Store, o options, samID int64) error {
	if _, err := st.EnsureAgentIdentity(ctx); err != nil {
		return fmt.Errorf("seed agent identity: %w", err)
	}
	tags, err := st.ListTags(ctx)
	if err != nil {
		return err
	}
	perms := make(map[authz.TagID]authz.ActionSet, len(tags))
	for _, tag := range tags {
		perms[tag.ID] = authz.ActionSet(authz.View) | authz.ActionSet(authz.Logs)
	}
	if err := st.SetAgentIdentityPermissions(ctx, perms, samID); err != nil {
		return fmt.Errorf("seed agent identity permissions: %w", err)
	}
	if !o.notifyDemo || o.notifyBox == nil {
		return nil
	}
	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.Name == investigatorRouteName {
			return nil
		}
	}
	secret, err := json.Marshal(notify.Secret{URL: o.notifyBaseURL + "/fake-notify/webhook/investigator"})
	if err != nil {
		return err
	}
	sealed, err := o.notifyBox.Seal(secret)
	if err != nil {
		return err
	}
	allKinds := make([]string, len(notify.Kinds))
	for i, k := range notify.Kinds {
		allKinds[i] = k.Kind
	}
	sandbox := accountIDByName(fake.MockupEstate(time.Now), "sandbox-admin")
	_, err = st.CreateNotificationRoute(ctx, store.NotificationRoute{
		Name: investigatorRouteName, Enabled: true, Destination: notify.DestWebhook, Investigate: true,
		Config:      store.NotificationConfig{URLHost: hostOf(o.notifyBaseURL)},
		SecretEnc:   sealed,
		MinSeverity: "warning", Kinds: allKinds, QuietTimezone: "Europe/Berlin", NotifyResolved: true,
		Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorService, AccountID: sandbox, Region: "us-east-1", Cluster: "background-jobs", Service: "image-resizer"}},
	}, samID)
	if err != nil {
		return fmt.Errorf("seed investigator route: %w", err)
	}
	return nil
}
