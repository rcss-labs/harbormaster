package devseed_test

import (
	"errors"
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
