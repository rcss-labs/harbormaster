package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/investigate"
	"github.com/rcss-labs/harbormaster/internal/notify"
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

// The dispatcher is connected to the investigator only when the investigator exists; HM_AGENT_INVESTIGATIONS_PER_HOUR=0
// turns the recording and the diagnosis messages off.
func TestWireInvestigations(t *testing.T) {
	cfg := config.Config{AgentInvestigationsPerHour: 20, AgentInvestigationWindow: 7 * time.Minute}
	runner := &investigate.Runner{}

	off := &notify.Dispatcher{}
	wireInvestigations(off, cfg, nil)
	if off.Investigate || off.InvestigationWindow != 0 || off.OnInvestigationRequested != nil {
		t.Errorf("with no investigator: Investigate %v, window %v, hook set %v; want nothing set",
			off.Investigate, off.InvestigationWindow, off.OnInvestigationRequested != nil)
	}

	on := &notify.Dispatcher{}
	wireInvestigations(on, cfg, runner)
	if !on.Investigate || on.InvestigationWindow != 7*time.Minute || on.OnInvestigationRequested == nil {
		t.Errorf("with an investigator: Investigate %v, window %v, hook set %v; want all set",
			on.Investigate, on.InvestigationWindow, on.OnInvestigationRequested != nil)
	}
	if on.OnInvestigationRequested != nil {
		on.OnInvestigationRequested() // the non-blocking kick: must return at once, twice in a row
		on.OnInvestigationRequested()
	}

	cfg.AgentInvestigationsPerHour = 0
	zero := &notify.Dispatcher{}
	wireInvestigations(zero, cfg, runner)
	if zero.Investigate {
		t.Error("PER_HOUR=0 left the investigation step on")
	}
}
