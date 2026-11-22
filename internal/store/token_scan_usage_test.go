package store_test

import (
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
