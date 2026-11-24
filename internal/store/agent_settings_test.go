package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAgentSettings_RoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	got, err := st.GetAgentSettings(ctx)
	if err != nil || got.MonthlyBudgetMicroUSD != 0 || len(got.RoleModels) != 0 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	want := store.AgentSettings{MonthlyBudgetMicroUSD: 5_000_000, UnredactedProviders: []string{"bedrock"},
		RoleModels: map[string]store.AgentModelRef{"chat": {Provider: "fake", Model: "m1"}}}
	if err := st.PutAgentSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	// A second put replaces role models rather than merging.
	want.RoleModels = map[string]store.AgentModelRef{"test": {Provider: "fake", Model: "m2"}}
	if err := st.PutAgentSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetAgentSettings(ctx)
	if err != nil || got.Version != 3 { // the seeded 1, raised by each of the two saves
		t.Fatalf("version = %d, %v; want 3", got.Version, err)
	}
	want.Version = got.Version
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestPutAgentSettings_ErrorsWhenNoSettingsRow(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	if _, err := st.Pool.Exec(ctx, `delete from agent_settings`); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAgentSettings(ctx, store.AgentSettings{MonthlyBudgetMicroUSD: 1}); err == nil {
		t.Fatal("put with no settings row returned nil; the budget would silently stay unset")
	}
}

func TestUpdateAgentSettings_RefusesAStaleVersionAndKeepsTheOldValues(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	first, err := st.GetAgentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := func(budget int64) func(store.AgentSettings) (store.AgentSettings, error) {
		return func(old store.AgentSettings) (store.AgentSettings, error) {
			old.MonthlyBudgetMicroUSD = budget
			return old, nil
		}
	}
	old, err := st.UpdateAgentSettings(ctx, first.Version, set(7))
	if err != nil || old.MonthlyBudgetMicroUSD != 0 {
		t.Fatalf("first update: old %+v, %v", old, err)
	}
	if _, err := st.UpdateAgentSettings(ctx, first.Version, set(9)); !errors.Is(err, store.ErrAgentSettingsChanged) {
		t.Fatalf("stale update err = %v, want ErrAgentSettingsChanged", err)
	}
	// An error from apply aborts the save.
	boom := errors.New("boom")
	if _, err := st.UpdateAgentSettings(ctx, first.Version+1, func(store.AgentSettings) (store.AgentSettings, error) {
		return store.AgentSettings{MonthlyBudgetMicroUSD: 99}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("apply error = %v", err)
	}
	got, _ := st.GetAgentSettings(ctx)
	if got.MonthlyBudgetMicroUSD != 7 || got.Version != first.Version+1 {
		t.Errorf("settings = %+v, want budget 7 at version %d", got, first.Version+1)
	}
}
