package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// A poller working from a stale target list runs EnsureSliceLeases while the disconnect transaction is still open. Its
// insert waits on the deleted lease tuple, and when the disconnect commits the row would come back: it must not be
// claimable, or its new epoch would pass the fence and the poller would write the account's rows back.
func TestClaimLease_RefusesASliceRecreatedByAStaleEnsure(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	gone := awsx.Target{AccountID: "111111111111", Region: "us-east-1"}
	ensureAccounts(t, s, gone)
	account, err := s.GetAccountByAccountID(ctx, gone.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSliceLeases(ctx, []awsx.Target{gone}); err != nil {
		t.Fatal(err)
	}

	// The statements DeleteAccount runs, held open between the lease delete and the commit.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `delete from poll_leases where kind = 'slice' and account_id = $1`, gone.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `delete from aws_accounts where id = $1`, account.ID); err != nil {
		t.Fatal(err)
	}
	ensured := make(chan error, 1)
	go func() { ensured <- s.EnsureSliceLeases(ctx, []awsx.Target{gone}) }() // the stale target list
	time.Sleep(300 * time.Millisecond)                                       // let the insert wait on the open disconnect
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ensured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stale ensure did not finish after the disconnect committed")
	}

	l, ok, err := s.ClaimLease(ctx, sliceKey(gone.AccountID, gone.Region), uuid.New(), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("the disconnected account's slice was claimed at epoch %d", l.Epoch)
	}
}
