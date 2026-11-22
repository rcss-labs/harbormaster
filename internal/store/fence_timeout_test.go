package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// The fence SELECT waits behind a takeover that holds the lease row FOR UPDATE. The server's statement timeout must cut
// that wait too: a timeout set inside the same statement would not bound it, and a write could hang for as long as the
// takeover's transaction stays open.
func TestSliceWriter_FenceWaitIsBoundedByTheStatementTimeout(t *testing.T) {
	defer store.SetFenceTimeoutsForTest(10*time.Second, 500*time.Millisecond)()
	s := storetest.Open(t)
	ctx := context.Background()
	_, w := swHold(t, s)
	k := sliceKey(swAccount, swRegion)

	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx); conn.Release() })
	var one int
	if err := tx.QueryRow(ctx, `select 1 from poll_leases where kind = $1 and account_id = $2 and region = $3 for update`,
		k.Kind, k.AccountID, k.Region).Scan(&one); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- w.TxForTest(ctx, func(func(string, ...any) error) error { return nil })
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the fence passed although the lease row was locked")
		}
		var pgErr interface{ SQLState() string }
		if !asPgError(err, &pgErr) || pgErr.SQLState() != "57014" {
			t.Fatalf("fence error = %v, want the server's statement timeout (57014)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the fence waited on the locked lease row for 3 s; the statement timeout did not bound it")
	}
}

func asPgError(err error, target *interface{ SQLState() string }) bool {
	for err != nil {
		if e, ok := err.(interface{ SQLState() string }); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
