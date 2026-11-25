package devseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// WithAgentDemo seeds the AI agent demo: sam may open the chat, the chat and investigator roles use the agent's fake
// model, the monthly allowance is 5 USD, and the agent identity may View and read Logs of every demo tag. With the
// notification demo on too, one webhook route to the in-process receiver has the automatic investigation switched on. Pass it only when the agent is enabled (HM_AGENT_URL).
func WithAgentDemo() Option {
	return func(o *options) { o.agentDemo = true }
}

// WithReviewReceiver lets the agent demo seed one scheduled review and its first result. The review is sent to a
// webhook route, sealed with box, that posts to the in-process receiver under baseURL, so it needs the box (a session
// key) but not the notification demo. Without it the reviewer model is seeded and no review is.
func WithReviewReceiver(box *secretbox.Box, baseURL string) Option {
	return func(o *options) { o.reviewBox, o.reviewBaseURL = box, strings.TrimRight(baseURL, "/") }
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
	if err := seedReviews(ctx, st, o, samID); err != nil {
		return err
	}
	_, err = st.UpdateAgentSettings(ctx, current.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.MonthlyBudgetMicroUSD = demoAgentBudgetMicroUSD
		old.RoleModels["chat"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
		old.RoleModels["investigator"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
		old.RoleModels["reviewer"] = store.AgentModelRef{Provider: "fake", Model: "fake-1"}
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

const (
	reviewRouteName = "Reviews to the fake receiver"
	reviewName      = "Weekly cost"
)

// seedReviews adds the demo review, the route it is sent to and one finished result, so the reviews page has
// something to show on the first start. It does nothing without the receiver option. Every step may repeat after a
// crash: the route and the review are found by name, and a review that has a result already gets no second one.
// The route takes no alert: it wants only account_unreachable, at critical, for one service that is never an account.
func seedReviews(ctx context.Context, st *store.Store, o options, samID int64) error {
	if o.reviewBox == nil {
		return nil
	}
	routeID, err := seedReviewRoute(ctx, st, o, samID)
	if err != nil {
		return err
	}
	reviews, err := st.ListReviews(ctx)
	if err != nil {
		return err
	}
	var review store.Review
	for _, existing := range reviews {
		if existing.Name == reviewName {
			review = existing
		}
	}
	if review.ID == 0 {
		review, err = st.CreateReview(ctx, store.Review{
			Name: reviewName, Type: "cost", Enabled: true, Cadence: "weekly", AtMinute: 8 * 60, Weekday: 1,
			Timezone: "Europe/Berlin", RouteIDs: []int64{routeID},
		}, samID)
		if err != nil {
			return fmt.Errorf("seed review: %w", err)
		}
	}
	return seedReviewResult(ctx, st, review)
}

func seedReviewRoute(ctx context.Context, st *store.Store, o options, samID int64) (int64, error) {
	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil {
		return 0, err
	}
	for _, route := range routes {
		if route.Name == reviewRouteName {
			return route.ID, nil
		}
	}
	secret, err := json.Marshal(notify.Secret{URL: o.reviewBaseURL + "/fake-notify/webhook/reviews"})
	if err != nil {
		return 0, err
	}
	sealed, err := o.reviewBox.Seal(secret)
	if err != nil {
		return 0, err
	}
	sandbox := accountIDByName(fake.MockupEstate(time.Now), "sandbox-admin")
	route, err := st.CreateNotificationRoute(ctx, store.NotificationRoute{
		Name: reviewRouteName, Enabled: true, Destination: notify.DestWebhook,
		Config:      store.NotificationConfig{URLHost: hostOf(o.reviewBaseURL)},
		SecretEnc:   sealed,
		MinSeverity: "critical", Kinds: []string{"account_unreachable"}, QuietTimezone: "Europe/Berlin",
		Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorService, AccountID: sandbox, Region: "us-east-1", Cluster: "background-jobs", Service: "image-resizer"}},
	}, samID)
	if err != nil {
		return 0, fmt.Errorf("seed review route: %w", err)
	}
	return route.ID, nil
}

// seedReviewResult stores one finished run of the review, made the way the job makes one: requested, claimed,
// started, finished.
func seedReviewResult(ctx context.Context, st *store.Store, review store.Review) error {
	existing, err := st.ListReviewRuns(ctx, store.ReviewRunFilter{ReviewID: &review.ID, Limit: 1})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	now := time.Now()
	run, err := st.RequestManualReviewRun(ctx, review.ID, "Example from the demo seed", false, now)
	if err != nil {
		return fmt.Errorf("seed review result: %w", err)
	}
	claimed, claim, err := st.ClaimReviewRun(ctx, now, time.Minute, time.Time{})
	if err != nil || claimed.ID != run.ID {
		return fmt.Errorf("seed review result: claim run %d: %v, %w", run.ID, claimed.ID, err)
	}
	if err := st.StartReviewRun(ctx, claim, store.ReviewStart{
		RunID: uuid.New(), Provider: "fake", Model: "fake-1", ScopeLabel: "Everything the agent can see", Range: "7d", ServicesTotal: 12,
	}, now); err != nil {
		return fmt.Errorf("seed review result: %w", err)
	}
	sandbox := accountIDByName(fake.MockupEstate(time.Now), "sandbox-admin")
	err = st.FinishReviewRun(ctx, claim, store.InvestigationDone, "", &store.InvestigationResult{
		Summary: "This is a canned review from the demo model. It did not judge the services.",
		Evidence: []store.InvestigationEvidence{
			{Ordinal: 1, Tool: "get_cost_analytics", Action: "view", AccountID: sandbox, Region: "us-east-1", ClusterName: "core-services", ServiceName: "payments-api",
				Note: "The first thing the demo model read.", Excerpt: "payments-api: the highest cost of the week"},
			{Ordinal: 2, Tool: "list_services", Action: "view", AccountID: sandbox, Region: "us-east-1", ClusterName: "background-jobs", ServiceName: "image-resizer",
				Note: "A second service the demo model looked at."},
		},
	}, nil, now)
	if err != nil {
		return fmt.Errorf("seed review result: %w", err)
	}
	return nil
}
