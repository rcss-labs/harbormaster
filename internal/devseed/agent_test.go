package devseed_test

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/devseed"
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
