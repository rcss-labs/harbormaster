package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// The agent identity exists from the first start with the agent on, and never with it off.
func TestNewInvestigator_OnlyWithTheAgentOn(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{AgentInvestigationsConcurrent: 3, AgentInvestigationsPerHour: 7, AgentInvestigationMaxWait: 5 * time.Minute}
	if r := newInvestigator(ctx, cfg, st, nil, nil, log); r != nil {
		t.Errorf("a job was built with the agent off: %+v", r)
	}
	if _, err := st.GetAgentIdentity(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("identity with the agent off: %v, want none", err)
	}
	cfg.AgentURL = "https://agent.internal:8484"
	r := newInvestigator(ctx, cfg, st, nil, nil, log)
	if r == nil || r.Concurrent != 3 || r.PerHour != 7 || r.MaxWait != 5*time.Minute || r.Auth == nil {
		t.Fatalf("job = %+v", r)
	}
	if _, err := st.GetAgentIdentity(ctx); err != nil {
		t.Errorf("identity with the agent on: %v", err)
	}
}
