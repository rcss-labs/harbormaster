package scanjob

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// A coordinator that lost its lease before it recorded a run starts nothing and records nothing.
func TestRunner_GuardBlocksTheRunBeforeItIsRecorded(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.runner.Guard = func(context.Context) error { return store.ErrLeaseLost }

	err := fx.runner.Cycle(context.Background())
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("Cycle = %v, want the guard's ErrLeaseLost", err)
	}
	if fx.runCount() != 0 || len(fx.scanner.Launches()) != 0 {
		t.Fatalf("runs = %d, launches = %d, want none", fx.runCount(), len(fx.scanner.Launches()))
	}
	if got := fx.image(digest).Status; got == "scanning" || got == "failed" {
		t.Fatalf("image status = %q, want it left queued for the next holder", got)
	}
}

// The lease can be lost between recording the run and starting the task (the job upload takes time): nothing is
// launched, the run is closed as failed with the reason, and the images wait again without an attempt counted.
func TestRunner_GuardBlocksRunTaskAfterLeaseLoss(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	var calls atomic.Int32
	fx.runner.Guard = func(context.Context) error {
		if calls.Add(1) == 1 {
			return nil
		}
		return store.ErrLeaseLost
	}

	if err := fx.runner.Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	if got := fx.scanner.Launches(); len(got) != 0 {
		t.Fatalf("launched %d tasks without the lease: %+v", len(got), got)
	}
	run := fx.run(1)
	if run.Status != "failed" || run.Error != "coordinator lease lost" {
		t.Fatalf("run = %+v, want failed with the reason", run)
	}
	img := fx.image(digest)
	if img.Status != "queued" || img.Attempts != 0 {
		t.Fatalf("image = %q with %d attempts, want queued with no attempt counted", img.Status, img.Attempts)
	}

	// The next holder launches it.
	fx.runner.Guard = nil
	fx.clock.Advance(2 * time.Hour)
	fx.cycle()
	if got := len(fx.scanner.Launches()); got != 1 {
		t.Fatalf("launches after the lease moved = %d, want 1", got)
	}
}
