package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// A write with no rows skips the fence, so a poller that lost its lease could report a read of an object with no rows as
// done. CheckLease is the fence on its own.
func TestSliceWriter_CheckLeaseFencesWithoutWriting(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, old := swHold(t, s)
	if err := old.CheckLease(ctx); err != nil {
		t.Fatalf("CheckLease while holding = %v", err)
	}
	swTakeOver(t, s)
	if err := old.CheckLease(ctx); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("CheckLease after a takeover = %v, want ErrLeaseLost", err)
	}
}
