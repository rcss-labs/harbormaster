package main

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestDemoSeedOptions_AgentDemoFollowsTheAgentSwitch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		agentURL string
		wantChat bool
	}{
		{"agent off seeds no chat model", "", false},
		{"agent on seeds the fake chat model", "http://127.0.0.1:8484", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := storetest.Open(t)
			cfg := config.Config{AgentURL: tc.agentURL}
			if err := devseed.Apply(ctx, st, false, demoSeedOptions(cfg, nil)...); err != nil {
				t.Fatalf("apply: %v", err)
			}
			settings, err := st.GetAgentSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, has := settings.RoleModels["chat"]; has != tc.wantChat {
				t.Fatalf("chat model seeded = %v, want %v", has, tc.wantChat)
			}
		})
	}
}
