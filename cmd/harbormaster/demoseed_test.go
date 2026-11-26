package main

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
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

// The seeded weekly review needs the agent on and a session key (the route to the receiver is sealed with it); the
// notification demo is not needed. With the agent off no review is seeded.
func TestDemoSeedOptions_ReviewNeedsTheAgentAndASessionKey(t *testing.T) {
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		agentURL    string
		box         *secretbox.Box
		wantReviews int
	}{
		{"agent off", "", box, 0},
		{"agent on, no session key", "http://127.0.0.1:8484", nil, 0},
		{"agent on, session key", "http://127.0.0.1:8484", box, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := storetest.Open(t)
			cfg := config.Config{AgentURL: tc.agentURL, BaseURL: "http://127.0.0.1:8480"}
			if err := devseed.Apply(ctx, st, false, demoSeedOptions(cfg, tc.box)...); err != nil {
				t.Fatalf("apply: %v", err)
			}
			reviews, err := st.ListReviews(ctx)
			if err != nil || len(reviews) != tc.wantReviews {
				t.Fatalf("reviews = %+v, %v; want %d", reviews, err, tc.wantReviews)
			}
		})
	}
}

// The custom review is seeded only with the agent on, a session key and HM_AGENT_CUSTOM_REVIEWS.
func TestDemoSeedOptions_CustomReviewNeedsItsSwitch(t *testing.T) {
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		agentURL     string
		custom       bool
		wantCustoms  int
		wantComplete int
	}{
		{"switch off", "http://127.0.0.1:8484", false, 0, 1},
		{"switch on, agent off", "", true, 0, 0},
		{"switch on", "http://127.0.0.1:8484", true, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := storetest.Open(t)
			cfg := config.Config{AgentURL: tc.agentURL, AgentCustomReviews: tc.custom, BaseURL: "http://127.0.0.1:8480"}
			if err := devseed.Apply(ctx, st, false, demoSeedOptions(cfg, box)...); err != nil {
				t.Fatalf("apply: %v", err)
			}
			reviews, err := st.ListReviews(ctx)
			if err != nil || len(reviews) != tc.wantComplete {
				t.Fatalf("reviews = %+v, %v; want %d", reviews, err, tc.wantComplete)
			}
			customs := 0
			for _, review := range reviews {
				if review.Type == "custom" {
					customs++
				}
			}
			if customs != tc.wantCustoms {
				t.Fatalf("custom reviews = %d, want %d", customs, tc.wantCustoms)
			}
		})
	}
}
