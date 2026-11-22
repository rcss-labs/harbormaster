package store_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestTokenLogScanUsage_UpsertAndDayBoundary(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

	if b, n, err := st.TokenLogScanUsage(ctx, u.ID, day); err != nil || b != 0 || n != 0 {
		t.Fatalf("empty = %d, %d, %v; want zeros", b, n, err)
	}
	if err := st.AddTokenLogScanUsage(ctx, u.ID, day.Add(10*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTokenLogScanUsage(ctx, u.ID, day.Add(11*time.Hour), 50); err != nil {
		t.Fatal(err)
	}
	if b, n, err := st.TokenLogScanUsage(ctx, u.ID, day.Add(2*time.Hour)); err != nil || b != 150 || n != 2 {
		t.Fatalf("same day = %d, %d, %v; want 150, 2", b, n, err)
	}

	late := time.Date(2026, 11, 20, 23, 59, 0, 0, time.UTC)
	early := time.Date(2026, 11, 21, 0, 1, 0, 0, time.UTC)
	if err := st.AddTokenLogScanUsage(ctx, u.ID, late, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTokenLogScanUsage(ctx, u.ID, early, 9); err != nil {
		t.Fatal(err)
	}
	if b, n, _ := st.TokenLogScanUsage(ctx, u.ID, day); b != 157 || n != 3 {
		t.Errorf("20th = %d, %d; want 157, 3", b, n)
	}
	if b, n, _ := st.TokenLogScanUsage(ctx, u.ID, early); b != 9 || n != 1 {
		t.Errorf("21st = %d, %d; want 9, 1", b, n)
	}
	// A zone offset does not move the UTC day.
	zone := time.FixedZone("x", 5*3600)
	if b, _, _ := st.TokenLogScanUsage(ctx, u.ID, time.Date(2026, 11, 21, 3, 0, 0, 0, zone)); b != 157 {
		t.Errorf("local 03:00+05 should be UTC the 20th, got %d", b)
	}
	if n, err := st.DeleteTokenLogScanUsageBefore(ctx, early); err != nil || n != 1 {
		t.Errorf("delete before = %d, %v; want 1", n, err)
	}
}

func TestReserveTokenLogScan_CapsAtRemainingAndPerSearch(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	day := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	got, err := st.ReserveTokenLogScan(ctx, u.ID, day, 100, 30)
	if err != nil || got != 30 {
		t.Fatalf("first = %d, %v; want 30 (per-search cap)", got, err)
	}
	if got, _ = st.ReserveTokenLogScan(ctx, u.ID, day, 100, 0); got != 70 {
		t.Fatalf("second = %d, want the remaining 70 (no per-search cap)", got)
	}
	if got, _ = st.ReserveTokenLogScan(ctx, u.ID, day, 100, 30); got != 0 {
		t.Fatalf("third = %d, want 0 once the day is used up", got)
	}
	if b, n, _ := st.TokenLogScanUsage(ctx, u.ID, day); b != 100 || n != 2 {
		t.Errorf("usage = %d, %d; want 100 bytes and 2 searches (a refused reserve is not a search)", b, n)
	}
}

func TestReserveTokenLogScan_ConcurrentReservesNeverExceedTheDay(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	day := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var total atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := st.ReserveTokenLogScan(ctx, u.ID, day, 100, 30)
			if err != nil {
				t.Error(err)
			}
			total.Add(got)
		}()
	}
	wg.Wait()
	if total.Load() != 100 {
		t.Errorf("reserved in total = %d, want exactly the day's 100", total.Load())
	}
}

func TestSettleTokenLogScan_ReplacesTheReservationWithActual(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	day := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	if _, err := st.ReserveTokenLogScan(ctx, u.ID, day, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleTokenLogScan(ctx, u.ID, day, 30, 12); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := st.TokenLogScanUsage(ctx, u.ID, day); b != 12 {
		t.Errorf("after refund = %d, want 12", b)
	}
	// Actual above the reservation is charged in full: the charge is never below what was scanned.
	if _, err := st.ReserveTokenLogScan(ctx, u.ID, day, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := st.SettleTokenLogScan(ctx, u.ID, day, 30, 45); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := st.TokenLogScanUsage(ctx, u.ID, day); b != 57 {
		t.Errorf("after overrun = %d, want 57", b)
	}
}
