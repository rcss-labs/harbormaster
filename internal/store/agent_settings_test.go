package store_test

import (
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
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}
