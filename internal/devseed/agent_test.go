package devseed_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestDevseed_AgentDemo_SeedsChatAccessAndSettingsOnce(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	sam, err := st.GetUserByUsername(ctx, "sam")
	if err != nil || !sam.AIChat {
		t.Fatalf("sam ai_chat = %v, %v; want true", sam.AIChat, err)
	}
	dana, err := st.GetUserByUsername(ctx, "dana")
	if err != nil || dana.AIChat {
		t.Fatalf("dana ai_chat = %v, %v; want false", dana.AIChat, err)
	}
	got, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := store.AgentModelRef{Provider: "fake", Model: "fake-1"}
	if got.MonthlyBudgetMicroUSD != 5_000_000 || got.RoleModels["chat"] != want {
		t.Fatalf("settings = %+v; want a 5 USD budget and chat on fake/fake-1", got)
	}

	// An admin's later change survives a restart.
	if _, err := st.UpdateAgentSettings(ctx, got.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.MonthlyBudgetMicroUSD = 9_000_000
		return old, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserAIChat(ctx, sam.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	again, err := st.GetAgentSettings(ctx)
	if err != nil || again.MonthlyBudgetMicroUSD != 9_000_000 {
		t.Fatalf("settings after restart = %+v, %v; want the admin's 9 USD kept", again, err)
	}
	if sam, err = st.GetUserByUsername(ctx, "sam"); err != nil || sam.AIChat {
		t.Fatalf("sam ai_chat after restart = %v, %v; want the admin's revoke kept", sam.AIChat, err)
	}
}

func TestDevseed_AgentDemo_NothingSeededWithoutTheOption(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	if err := devseed.Apply(ctx, st, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	sam, err := st.GetUserByUsername(ctx, "sam")
	if err != nil || sam.AIChat {
		t.Fatalf("sam ai_chat = %v, %v; want false", sam.AIChat, err)
	}
	got, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RoleModels) != 0 {
		t.Fatalf("role models = %+v; want none", got.RoleModels)
	}
}

func TestDevseed_AgentDemo_AdminClearingTheModelSurvivesARestart(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seeded, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateAgentSettings(ctx, seeded.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		old.RoleModels = map[string]store.AgentModelRef{}
		return old, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	got, err := st.GetAgentSettings(ctx)
	if err != nil || len(got.RoleModels) != 0 {
		t.Fatalf("role models after restart = %+v, %v; want the admin's empty choice kept", got.RoleModels, err)
	}
}

// A crash after chat access was granted but before the settings were saved leaves the settings untouched; the next
// start finishes the seed.
func TestDevseed_AgentDemo_FinishesAfterACrashBetweenTheTwoWrites(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)

	if err := devseed.Apply(ctx, st, true); err != nil {
		t.Fatalf("apply: %v", err)
	}
	sam, err := st.GetUserByUsername(ctx, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserAIChat(ctx, sam.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("apply after the crash: %v", err)
	}
	got, err := st.GetAgentSettings(ctx)
	if err != nil || got.MonthlyBudgetMicroUSD != 5_000_000 || got.RoleModels["chat"].Model != "fake-1" {
		t.Fatalf("settings = %+v, %v; want the seed finished", got, err)
	}
}

func TestDevseed_AgentDemo_SeedsTheInvestigator(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	opts := []devseed.Option{devseed.WithAgentDemo(), devseed.WithNotifyDemo(box, "http://127.0.0.1:8480")}
	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("apply: %v", err)
	}

	identity, err := st.GetAgentIdentity(ctx)
	if err != nil {
		t.Fatalf("agent identity: %v", err)
	}
	perms, err := st.PermissionsOf(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTags(ctx)
	if err != nil || len(tags) == 0 {
		t.Fatalf("tags = %v, %v", tags, err)
	}
	for _, tag := range tags {
		if got, want := perms[tag.ID], authz.ActionSet(authz.View)|authz.ActionSet(authz.Logs); got != want {
			t.Errorf("identity actions on %s = %v, want View and Logs only", tag.Name, got)
		}
	}
	settings, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := settings.RoleModels["investigator"], (store.AgentModelRef{Provider: "fake", Model: "fake-1"}); got != want {
		t.Errorf("investigator model = %+v, want %+v", got, want)
	}

	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found []store.NotificationRoute
	for _, route := range routes {
		if route.Investigate {
			found = append(found, route)
		}
	}
	if len(found) != 1 || found[0].Destination != notify.DestWebhook || !found[0].Enabled || len(found[0].Scope) != 1 || found[0].Fallback {
		t.Fatalf("routes with the switch on = %+v, want one enabled, scoped webhook route", found)
	}

	// A restart changes nothing and adds no second route.
	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	again, err := st.ListNotificationRoutes(ctx)
	if err != nil || len(again) != len(routes) {
		t.Fatalf("routes after restart = %d, %v; want %d", len(again), err, len(routes))
	}
}

// The e2e suite runs the demo without notification routes: the identity and the model are seeded, a route is not.
func TestDevseed_AgentDemo_NoInvestigatorRouteWithoutTheNotifyDemo(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := st.GetAgentIdentity(ctx); err != nil {
		t.Fatalf("agent identity: %v", err)
	}
	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v, %v; want none", routes, err)
	}
}

func TestDevseed_NoAgentIdentityWithoutTheAgentDemo(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, st, true, devseed.WithNotifyDemo(box, "http://127.0.0.1:8480")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := st.GetAgentIdentity(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent identity without the agent: %v, want none", err)
	}
	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Investigate {
			t.Errorf("route %q has the switch on without the agent", route.Name)
		}
	}
}

func reviewReceiverOptions(t *testing.T) []devseed.Option {
	t.Helper()
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	return []devseed.Option{devseed.WithAgentDemo(), devseed.WithReviewReceiver(box, "http://127.0.0.1:8480")}
}

// The demo starts with a weekly cost review over everything, on a route to the in-process receiver, and one finished
// result with two pieces of evidence, so the reviews page is not empty. A restart adds nothing.
func TestDevseed_ReviewDemo_SeedsAWeeklyCostReviewAndAFinishedRun(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	opts := reviewReceiverOptions(t)
	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("apply: %v", err)
	}
	settings, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := settings.RoleModels["reviewer"], (store.AgentModelRef{Provider: "fake", Model: "fake-1"}); got != want {
		t.Errorf("reviewer model = %+v, want %+v", got, want)
	}
	reviews, err := st.ListReviews(ctx)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("reviews = %+v, %v; want one", reviews, err)
	}
	review := reviews[0]
	if review.Name != "Weekly cost" || review.Type != "cost" || !review.Enabled || review.Cadence != "weekly" || len(review.Scope) != 0 || len(review.RouteIDs) != 1 {
		t.Fatalf("review = %+v, want an enabled weekly cost review over everything with one route", review)
	}
	route, err := st.GetNotificationRoute(ctx, review.RouteIDs[0])
	if err != nil || route.Destination != notify.DestWebhook || !route.Enabled || route.Fallback || route.Investigate {
		t.Fatalf("route = %+v, %v; want an enabled webhook route that is not a fallback", route, err)
	}
	runs, err := st.ListReviewRuns(ctx, store.ReviewRunFilter{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v; want one", runs, err)
	}
	run := runs[0]
	if run.State != store.InvestigationDone || run.Summary == "" || run.ReviewID == nil || *run.ReviewID != review.ID || run.FinishedAt == nil {
		t.Fatalf("run = %+v, want a finished result of the review with a summary", run)
	}
	evidence, err := st.ListReviewRunEvidence(ctx, run.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence = %+v, %v; want two items", evidence, err)
	}

	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	again, err := st.ListReviewRuns(ctx, store.ReviewRunFilter{Limit: 10})
	if err != nil || len(again) != 1 {
		t.Fatalf("runs after a restart = %d, %v; want 1", len(again), err)
	}
	if reviews, err = st.ListReviews(ctx); err != nil || len(reviews) != 1 {
		t.Fatalf("reviews after a restart = %d, %v; want 1", len(reviews), err)
	}
}

// Without the receiver option (no session key) the model is seeded and no review is.
func TestDevseed_ReviewDemo_NoReviewWithoutTheReceiver(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	if err := devseed.Apply(ctx, st, true, devseed.WithAgentDemo()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	settings, err := st.GetAgentSettings(ctx)
	if err != nil || settings.RoleModels["reviewer"].Model != "fake-1" {
		t.Fatalf("settings = %+v, %v; want the reviewer model", settings, err)
	}
	if reviews, err := st.ListReviews(ctx); err != nil || len(reviews) != 0 {
		t.Fatalf("reviews = %+v, %v; want none", reviews, err)
	}
}

// With the agent off nothing about reviews is seeded, even when a box is offered.
func TestDevseed_ReviewDemo_NothingWithTheAgentOff(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, st, true, devseed.WithReviewReceiver(box, "http://127.0.0.1:8480")); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if reviews, err := st.ListReviews(ctx); err != nil || len(reviews) != 0 {
		t.Fatalf("reviews = %+v, %v; want none", reviews, err)
	}
	if routes, err := st.ListNotificationRoutes(ctx); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v, %v; want none", routes, err)
	}
}

func customReviewOptions(t *testing.T) []devseed.Option {
	t.Helper()
	return append(reviewReceiverOptions(t), devseed.WithCustomReviewDemo())
}

// With the custom switch on the demo also has a disabled custom review, "Idle services", with three lines of
// instructions on the receiver route, and one finished run of it that keeps its copy of the text, a summary and two
// pieces of evidence a real run could cite. The run is not a test run. A restart adds nothing.
func TestDevseed_CustomReviewDemo_SeedsADisabledReviewAndAFinishedRun(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	opts := customReviewOptions(t)
	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("apply: %v", err)
	}
	reviews, err := st.ListReviews(ctx)
	if err != nil || len(reviews) != 2 {
		t.Fatalf("reviews = %+v, %v; want the cost review and the custom one", reviews, err)
	}
	var custom store.Review
	for _, review := range reviews {
		if review.Name == "Idle services" {
			custom = review
		}
	}
	if custom.ID == 0 || custom.Type != "custom" || custom.Enabled || len(custom.RouteIDs) != 1 || len(custom.Scope) != 0 {
		t.Fatalf("custom review = %+v, want a disabled custom review over everything with one route", custom)
	}
	if lines := strings.Split(custom.Instructions, "\n"); len(lines) != 3 {
		t.Fatalf("instructions = %q, want three lines", custom.Instructions)
	}
	runs, err := st.ListReviewRuns(ctx, store.ReviewRunFilter{ReviewID: &custom.ID, Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs of the custom review = %+v, %v; want one", runs, err)
	}
	run := runs[0]
	if run.State != store.InvestigationDone || run.Summary == "" || run.Test || run.ReviewType != "custom" || run.Instructions != custom.Instructions {
		t.Fatalf("run = %+v, want a finished run, not a test run, holding the review's text", run)
	}
	evidence, err := st.ListReviewRunEvidence(ctx, run.ID)
	if err != nil || len(evidence) != 2 {
		t.Fatalf("evidence = %+v, %v; want two items", evidence, err)
	}
	for _, item := range evidence {
		if item.Tool != "list_services" && item.Tool != "get_service" {
			t.Errorf("evidence tool %q, want one a real run can cite", item.Tool)
		}
	}

	if err := devseed.Apply(ctx, st, true, opts...); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if again, err := st.ListReviewRuns(ctx, store.ReviewRunFilter{ReviewID: &custom.ID, Limit: 10}); err != nil || len(again) != 1 {
		t.Fatalf("runs after a restart = %d, %v; want 1", len(again), err)
	}
	if reviews, err = st.ListReviews(ctx); err != nil || len(reviews) != 2 {
		t.Fatalf("reviews after a restart = %d, %v; want 2", len(reviews), err)
	}
}

// Without the option the review demo is the one cost review, as before.
func TestDevseed_CustomReviewDemo_NothingWithoutTheOption(t *testing.T) {
	ctx := t.Context()
	st := storetest.Open(t)
	if err := devseed.Apply(ctx, st, true, reviewReceiverOptions(t)...); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if reviews, err := st.ListReviews(ctx); err != nil || len(reviews) != 1 || reviews[0].Type != "cost" {
		t.Fatalf("reviews = %+v, %v; want only the cost review", reviews, err)
	}
}
