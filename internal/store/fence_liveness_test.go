package store_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// insertEvent writes one service_events row through the fenced transaction.
func insertEvent(exec func(sql string, args ...any) error, id string) error {
	return exec(`insert into service_events (account_id, region, cluster_name, service_name, event_id, created_at,
		   message, content_hash, raw, polled_at)
		 values ($1, $2, 'core', 'api', $3, now(), 'm', 'h', '{}', now())`, swAccount, swRegion, id)
}

// A frozen or partitioned owner leaves a write transaction open, holding the fence lock. The server must end that
// transaction by itself (idle_in_transaction_session_timeout), or no takeover could ever get the row.
func TestSliceWriter_FrozenOwnerCannotBlockATakeoverForever(t *testing.T) {
	defer store.SetFenceTimeoutsForTest(time.Second, 30*time.Second)()
	s := storetest.Open(t)
	ctx := context.Background()
	_, w := swHold(t, s)
	k := sliceKey(swAccount, swRegion)

	frozen, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unfreeze := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unfreeze) // a failed assertion must not leave the transaction open under the schema drop
	result := make(chan error, 1)
	go func() {
		result <- w.TxForTest(ctx, func(exec func(string, ...any) error) error {
			if err := insertEvent(exec, "frozen-1"); err != nil {
				return err
			}
			close(frozen)
			<-release // the process is frozen here
			return nil
		})
	}()
	<-frozen
	expireLease(t, s, k)

	start := time.Now()
	l, ok, err := s.ClaimLease(ctx, k, uuid.New(), leaseTTL)
	took := time.Since(start)
	if err != nil || !ok {
		t.Fatalf("takeover behind a frozen owner = ok %v, err %v after %v; want it to succeed once the server ends the open transaction", ok, err, took)
	}
	if took > 2500*time.Millisecond {
		t.Fatalf("takeover took %v, want about the 1 s idle timeout", took)
	}
	unfreeze()
	if err := <-result; err == nil {
		t.Fatal("the frozen owner's write committed after its session was ended")
	}
	if n := swCount(t, s, "service_events"); n != 0 {
		t.Fatalf("service_events = %d, want the frozen owner's insert rolled back", n)
	}
	if l.Epoch != 2 {
		t.Fatalf("epoch = %d, want 2 (one change of owner)", l.Epoch)
	}
}

// Old-owner writes that overlap one another never leave a moment without a key-share holder, which can starve a
// takeover's FOR UPDATE for ever. Once the lease has expired by the database clock, no new write of the old epoch may
// start, so the overlap ends and the takeover gets through; and nothing of the old epoch commits after it.
func TestSliceWriter_OverlappingOldOwnerWritesCannotStarveATakeover(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, w := swHold(t, s)
	k := sliceKey(swAccount, swRegion)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var committed atomic.Int64
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 25 * time.Millisecond) // stagger so the transactions overlap
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				err := w.TxForTest(ctx, func(exec func(string, ...any) error) error {
					if err := insertEvent(exec, fmt.Sprintf("w%d-%d", i, n)); err != nil {
						return err
					}
					time.Sleep(200 * time.Millisecond)
					return nil
				})
				if err != nil {
					return // fenced out: the lease expired or was taken over
				}
				committed.Add(1)
			}
		}()
	}
	time.Sleep(400 * time.Millisecond)
	expireLease(t, s, k)

	start := time.Now()
	var claimed bool
	for time.Since(start) < 6*time.Second {
		_, ok, err := s.ClaimLease(ctx, k, uuid.New(), leaseTTL)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			claimed = true
			break
		}
	}
	if !claimed {
		close(stop)
		wg.Wait()
		t.Fatalf("no takeover in %v: overlapping old-owner writes (%d committed) starved it", time.Since(start).Round(time.Millisecond), committed.Load())
	}
	if took := time.Since(start); took > 5*time.Second {
		close(stop)
		wg.Wait()
		t.Fatalf("the takeover took %v behind %d overlapping old-owner writes, want it bounded (a few seconds)", took.Round(time.Millisecond), committed.Load())
	}
	atClaim := swCount(t, s, "service_events")
	time.Sleep(700 * time.Millisecond) // the old owner keeps trying
	close(stop)
	wg.Wait()
	if after := swCount(t, s, "service_events"); after != atClaim {
		t.Fatalf("service_events grew from %d to %d after the takeover: an old-epoch write committed", atClaim, after)
	}
}
