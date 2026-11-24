package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/apitoken"
)

// Harbormaster's own client drives the real handler: the two sides agree on the wire.
// agentclient is imported by this test only, so the fence on the shipped binary is unaffected.
func TestAgentClientAgainstTheHandler(t *testing.T) {
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": fake.Demo()}, edit: func(c *Config) {
		c.MaxRunTokens = 150_000
		c.RunTimeout = 4 * time.Minute
	}})
	client, err := agentclient.New(h.server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	catalogue, err := client.Catalogue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Providers) != 1 || catalogue.Providers[0].Provider != "fake" || catalogue.MaxRunTokens != 150_000 || catalogue.RunTimeout != 4*time.Minute {
		t.Fatalf("catalogue = %+v", catalogue)
	}
	model := catalogue.Providers[0].Models[0]
	if model.ID != "fake-1" || model.InputMicroUSDPerMTok != 1_000_000 || model.ContextTokens != 100_000 {
		t.Errorf("model = %+v", model)
	}

	request := agentclient.RunRequest{
		RunID: uuid.New(), Role: "chat", Provider: "fake", Model: "fake-1",
		Messages:        []agentclient.Message{{Role: "user", Text: "How many clusters are there?"}},
		Page:            agentclient.Page{Path: "/clusters"},
		MaxCostMicroUSD: model.MaxRunCostMicroUSD(catalogue.MaxRunTokens),
	}
	var events []agentclient.Event
	if err := client.Run(ctx, h.token, request, func(ev agentclient.Event) { events = append(events, ev) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var text strings.Builder
	var sawTool bool
	var final agentclient.Event
	for _, ev := range events {
		text.WriteString(ev.Delta)
		sawTool = sawTool || (ev.Type == "tool_end" && ev.OK && ev.Tool == "get_overview")
		if ev.Type == "usage" && !ev.Provisional {
			final = ev
		}
	}
	if events[len(events)-1].Type != "done" || !sawTool || !strings.Contains(text.String(), "canned answer") {
		t.Errorf("events = %+v", events)
	}
	if final.InputTokens != 200 || final.OutputTokens != 30 || final.CostMicroUSD != 230 {
		t.Errorf("final usage as the client reads it = %+v", final)
	}

	result, err := client.Test(ctx, h.token, "fake", "fake-1")
	if err != nil || !result.OK {
		t.Errorf("Test = %+v, %v", result, err)
	}

	// Each refusal reaches the client as the error Harbormaster branches on.
	unknown := request
	unknown.Model = "nope"
	if err := client.Run(ctx, h.token, unknown, func(agentclient.Event) {}); !errors.Is(err, agentclient.ErrUnknownModel) {
		t.Errorf("unknown model: err = %v", err)
	}
	if _, err := client.Test(ctx, h.token, "fake", "nope"); !errors.Is(err, agentclient.ErrUnknownModel) {
		t.Errorf("unknown model on Test: err = %v", err)
	}
	other, _, _ := apitoken.Generate(apitoken.KindRun)
	if err := client.Run(ctx, other, request, func(agentclient.Event) {}); !errors.Is(err, agentclient.ErrAgentUnavailable) {
		t.Errorf("a token Harbormaster refuses: err = %v", err)
	}
	bad := request
	bad.MaxCostMicroUSD = 0
	if err := client.Run(ctx, h.token, bad, func(agentclient.Event) {}); !errors.Is(err, agentclient.ErrAgentUnavailable) {
		t.Errorf("an invalid request: err = %v", err)
	}
}

func TestAgentClientSeesBusy(t *testing.T) {
	blocking := newBlockingProvider()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": blocking}, edit: func(c *Config) { c.MaxConcurrentRuns = 1 }})
	client, err := agentclient.New(h.server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	request := agentclient.RunRequest{
		RunID: uuid.New(), Role: "chat", Provider: "fake", Model: "fake-1",
		Messages: []agentclient.Message{{Role: "user", Text: "hi"}}, MaxCostMicroUSD: 1_000_000,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, h.token, request, func(agentclient.Event) {}) }()
	waitFor(t, blocking.entered, "the first run to reach the model")
	if err := client.Run(context.Background(), h.token, request, func(agentclient.Event) {}); !errors.Is(err, agentclient.ErrAgentBusy) {
		t.Errorf("second run: err = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled run: err = %v", err)
	}
	_ = http.StatusOK
}
