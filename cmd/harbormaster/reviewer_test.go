package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// The review job exists, and review messages are sent, only with the agent on and a daily cap above 0.
func TestServe_ReviewJobNeedsTheAgentAndACap(t *testing.T) {
	st := storetest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	on := config.Config{AgentURL: "https://agent.internal:8484", AgentReviewsPerDay: 7, AgentReviewMaxLate: 90 * time.Minute, AgentInvestigationMaxWait: 5 * time.Minute}
	off, zero := on, on
	off.AgentURL, zero.AgentReviewsPerDay = "", 0
	for name, cfg := range map[string]config.Config{"agent off": off, "cap of 0": zero} {
		d := &notify.Dispatcher{}
		r := newReviewer(cfg, st, nil, nil, log)
		wireReviews(d, r)
		if r != nil || d.Reviews {
			t.Errorf("%s: job %+v, dispatcher reviews %v; want neither", name, r, d.Reviews)
		}
	}
	kicked := false
	r := newReviewer(on, st, nil, func() { kicked = true }, log)
	if r == nil || r.PerDay != 7 || r.MaxLate != 90*time.Minute || r.InvestigationMaxWait != 5*time.Minute || r.Store != st || r.Scopes == nil ||
		r.Driver == nil || r.Driver.Store == nil || r.Driver.Auth == nil || r.Driver.Guard != nil || r.Now == nil {
		t.Fatalf("job = %+v", r)
	}
	if r.CustomReviews {
		t.Error("custom reviews run although they are not switched on")
	}
	on.AgentCustomReviews = true
	if custom := newReviewer(on, st, nil, nil, log); !custom.CustomReviews {
		t.Error("custom reviews do not run although they are switched on")
	}
	r.KickNotify()
	d := &notify.Dispatcher{}
	wireReviews(d, r)
	if !kicked || !d.Reviews || d.Investigate {
		t.Errorf("kicked %v, dispatcher reviews %v, investigate %v", kicked, d.Reviews, d.Investigate)
	}
	wireReviews(nil, r) // a task without a dispatcher
}

// The dispatcher sends custom results only when the operator switch is on, the same switch the job reads.
func TestServe_DispatcherGetsTheCustomReviewsSwitch(t *testing.T) {
	st := storetest.Open(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{AgentURL: "https://agent.internal:8484", AgentReviewsPerDay: 7, AgentReviewMaxLate: 90 * time.Minute, AgentInvestigationMaxWait: 5 * time.Minute}
	for _, on := range []bool{false, true} {
		cfg.AgentCustomReviews = on
		d := &notify.Dispatcher{}
		wireReviews(d, newReviewer(cfg, st, nil, nil, log))
		if d.CustomReviews != on || !d.Reviews {
			t.Errorf("switch %v: dispatcher custom %v, reviews %v", on, d.CustomReviews, d.Reviews)
		}
	}
}
