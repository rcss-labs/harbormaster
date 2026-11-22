package poller

// This file is test support only: production code never calls anything here. The helpers fill a store from a fake
// estate for tests in other packages (api, platforms, awsx, deploy, devseed, rollup). They stay in package poller
// because they need the unexported loop, and the poller's own tests call them too, so a separate package would
// force exporting loop internals and create an import cycle.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// PollOnce (test support only) runs every tier once, in order, for one target. Tests use it to fill the store from a fake estate.
// It claims the target's slice lease under a throwaway owner and releases it afterwards, so its writes are fenced like
// the real loops' and no unfenced way to write exists; it fails when a live owner holds the slice.
func PollOnce(ctx context.Context, st *store.Store, clients awsx.Clients, cfg Config, t awsx.Target) error {
	w, release, err := claimOnce(ctx, st, t)
	if err != nil {
		return err
	}
	defer release()
	return newLoop(New(st, clients, cfg, slog.Default()), t, w).runOnce(ctx)
}

// claimOnce takes the slice's lease for one throwaway owner and returns its writer and a release function.
func claimOnce(ctx context.Context, st *store.Store, t awsx.Target) (*store.SliceWriter, func(), error) {
	// A claim behind a stuck writer must not hang the caller: give up a little after the store's own deadline.
	claimCtx, cancel := context.WithTimeout(ctx, store.ClaimDeadline+2*time.Second)
	defer cancel()
	if err := st.EnsureSliceLease(claimCtx, t); err != nil {
		return nil, nil, err
	}
	// Nothing renews this lease, and the fence also checks expiry, so a helper that runs past the TTL would get
	// ErrLeaseLost halfway through. Ten minutes outlasts any one-shot poll; release returns it as soon as the helper ends.
	l, ok, err := st.ClaimLeaseOnce(claimCtx, store.LeaseKey{Kind: store.LeaseSlice, AccountID: t.AccountID, Region: t.Region}, uuid.New(), 10*time.Minute)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("poll once: slice %s/%s is held by a live owner", t.AccountID, t.Region)
	}
	w, err := st.SliceWriter(l)
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = st.ReleaseLease(rctx, l)
	}
	return w, release, nil
}

// PollCountsOnce is PollOnce followed by one counts-history tick: the sample the counts loop takes every few
// minutes, which also folds the cluster's running capacity into today's cluster_capacity_daily row.
func PollCountsOnce(ctx context.Context, st *store.Store, clients awsx.Clients, cfg Config, t awsx.Target) error {
	w, release, err := claimOnce(ctx, st, t)
	if err != nil {
		return err
	}
	defer release()
	l := newLoop(New(st, clients, cfg, slog.Default()), t, w)
	err = l.runOnce(ctx)
	l.recordCounts(ctx)
	return err
}
