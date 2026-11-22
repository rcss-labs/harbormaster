package poller

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// claimOnce's lease is never renewed and the fence checks expiry, so it must outlast any one-shot poll: a helper that
// runs past the lease would fail with ErrLeaseLost halfway through.
func TestClaimOnce_LeaseOutlastsALongPoll(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	w, release, err := claimOnce(ctx, st, loopTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if w == nil {
		t.Fatal("no writer")
	}
	rows, err := st.ListLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Key.Kind == store.LeaseSlice && r.Key.AccountID == loopTarget.AccountID && r.Key.Region == loopTarget.Region {
			if left := time.Until(r.ExpiresAt); left < 5*time.Minute {
				t.Fatalf("one-shot lease has %v left, want more than 5 minutes", left)
			}
			return
		}
	}
	t.Fatal("the claimed lease row is missing")
}
