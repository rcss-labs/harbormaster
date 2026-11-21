package lease

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// Guard is checked right before an external side effect that cannot carry a fencing token (a notification send, a scan
// launch, an off-hours change). It returns nil while the caller still holds the lease with time left, store.ErrLeaseLost
// when it does not, and another error when the database could not be asked; callers treat every error as "do not act".
// A nil Guard means "always allowed".
type Guard func(ctx context.Context) error

// GuardFor builds the guard of one held lease: still the owner at the same epoch, with at least minRemaining left.
// It cannot fence (the effect happens outside the database); it keeps a paused holder from acting on stale decisions.
func GuardFor(st *store.Store, l store.Lease, minRemaining time.Duration) Guard {
	return func(ctx context.Context) error { return st.CheckLease(ctx, l, minRemaining) }
}
