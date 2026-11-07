package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func scanRequest(digest string) store.ScanImage {
	return store.ScanImage{
		Digest: digest, ImageRef: "111111111111.dkr.ecr.eu-west-1.amazonaws.com/api", Tag: "v1",
		RegistryKind: "ecr", RegistryAccountID: "111111111111", RegistryRegion: "eu-west-1",
		Repository: "api", Platform: "linux/amd64",
	}
}

func seedScanImages(t *testing.T, s *store.Store, now time.Time, digests ...string) {
	t.Helper()
	for _, digest := range digests {
		if _, _, err := s.UpsertRequestedScanImage(context.Background(), scanRequest(digest), now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpsertRequestedScanImage_NewIsQueuedExistingIsTouchedFailedIsReset(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	t1 := t0.Add(30 * time.Minute)

	img, created, err := s.UpsertRequestedScanImage(ctx, scanRequest("sha256:a"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !created || img.Status != "queued" || !img.LastRequestedAt.Equal(t0) {
		t.Fatalf("first call created=%v status=%q last_requested_at=%v, want true queued %v", created, img.Status, img.LastRequestedAt, t0)
	}

	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanned', scanned_at = $1 where digest = 'sha256:a'`, t0); err != nil {
		t.Fatal(err)
	}
	img, created, err = s.UpsertRequestedScanImage(ctx, scanRequest("sha256:a"), t1)
	if err != nil {
		t.Fatal(err)
	}
	if created || img.Status != "scanned" || !img.LastRequestedAt.Equal(t1) || !img.FirstSeenAt.Equal(t0) {
		t.Fatalf("second call created=%v status=%q last_requested_at=%v first_seen_at=%v, want false scanned %v %v",
			created, img.Status, img.LastRequestedAt, img.FirstSeenAt, t1, t0)
	}

	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'failed', attempts = 3, failure_code = 'task_failed', failure_detail = 'x' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	img, created, err = s.UpsertRequestedScanImage(ctx, scanRequest("sha256:a"), t1.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if created || img.Status != "queued" || img.Attempts != 0 || img.FailureCode != "" || img.NextAttemptAt != nil {
		t.Fatalf("failed row after request created=%v status=%q attempts=%d code=%q next=%v, want false queued 0 empty nil",
			created, img.Status, img.Attempts, img.FailureCode, img.NextAttemptAt)
	}
}

func TestDueScanImages_OrdersByLastRequested(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now.Add(-2*time.Hour), "sha256:middle")
	seedScanImages(t, s, now.Add(-4*time.Hour), "sha256:oldest")
	seedScanImages(t, s, now.Add(-time.Hour), "sha256:later", "sha256:newest")
	if _, err := s.Pool.Exec(ctx, `update scan_images set next_attempt_at = $1 where digest = 'sha256:later'`, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Requesting an old image again moves it to the back of the queue.
	seedScanImages(t, s, now, "sha256:oldest")

	due, err := s.DueScanImages(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, img := range due {
		got = append(got, img.Digest)
	}
	want := []string{"sha256:middle", "sha256:newest", "sha256:oldest"}
	if len(got) != len(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due = %v, want %v", got, want)
		}
	}
	limited, err := s.DueScanImages(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Digest != "sha256:middle" {
		t.Fatalf("limit 1 = %v, want only middle", limited)
	}
}

func TestExpiredScanImages_IgnoresQueuedAndScanning(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	old := now.Add(-100 * 24 * time.Hour)
	seedScanImages(t, s, old, "sha256:scanned", "sha256:failed", "sha256:queued", "sha256:scanning", "sha256:recent")
	for _, sql := range []string{
		`update scan_images set status = 'scanned' where digest = 'sha256:scanned'`,
		`update scan_images set status = 'failed' where digest = 'sha256:failed'`,
		`update scan_images set status = 'scanning' where digest = 'sha256:scanning'`,
		`update scan_images set status = 'scanned', last_requested_at = now() where digest = 'sha256:recent'`,
	} {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ExpiredScanImages(ctx, now.Add(-90*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !contains(got, "sha256:scanned") || !contains(got, "sha256:failed") {
		t.Fatalf("expired = %v, want scanned and failed only", got)
	}
	limited, err := s.ExpiredScanImages(ctx, now.Add(-90*24*time.Hour), 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit 1 = %v err %v, want one", limited, err)
	}
}

func TestRequeueScanImages_FailsAfterThreeAttempts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, "sha256:a", "sha256:b")
	if _, err := s.Pool.Exec(ctx, `update scan_images set attempts = 2, status = 'scanning' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_images set attempts = 0, status = 'scanning' where digest = 'sha256:b'`); err != nil {
		t.Fatal(err)
	}
	next := now.Add(5 * time.Minute)
	if err := s.RequeueScanImages(ctx, []string{"sha256:a", "sha256:b"}, next, true, "task_failed", "exit 1"); err != nil {
		t.Fatal(err)
	}
	a := mustGetScanImage(t, s, "sha256:a")
	if a.Status != "failed" || a.Attempts != 3 || a.FailureCode != "task_failed" || a.FailureDetail != "exit 1" {
		t.Fatalf("third attempt: status=%q attempts=%d code=%q detail=%q", a.Status, a.Attempts, a.FailureCode, a.FailureDetail)
	}
	b := mustGetScanImage(t, s, "sha256:b")
	if b.Status != "queued" || b.Attempts != 1 || !b.Large || b.NextAttemptAt == nil || !b.NextAttemptAt.Equal(next) {
		t.Fatalf("first attempt: status=%q attempts=%d large=%v next=%v", b.Status, b.Attempts, b.Large, b.NextAttemptAt)
	}

	// large=false leaves an existing flag alone.
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanning' where digest = 'sha256:b'`); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueScanImages(ctx, []string{"sha256:b"}, next, false, "task_failed", "again"); err != nil {
		t.Fatal(err)
	}
	b = mustGetScanImage(t, s, "sha256:b")
	if !b.Large || b.Attempts != 2 {
		t.Fatalf("second requeue large=%v attempts=%d, want true 2", b.Large, b.Attempts)
	}
}

func TestDeferScanImages_KeepsAttempts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, "sha256:a")
	if _, err := s.Pool.Exec(ctx, `update scan_images set attempts = 2, large = true, status = 'scanning' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	next := now.Add(time.Hour)
	if err := s.DeferScanImages(ctx, []string{"sha256:a"}, next, "db_stale", "database too old"); err != nil {
		t.Fatal(err)
	}
	a := mustGetScanImage(t, s, "sha256:a")
	if a.Status != "queued" || a.Attempts != 2 || !a.Large || a.FailureCode != "db_stale" || a.NextAttemptAt == nil || !a.NextAttemptAt.Equal(next) {
		t.Fatalf("deferred row status=%q attempts=%d large=%v code=%q next=%v", a.Status, a.Attempts, a.Large, a.FailureCode, a.NextAttemptAt)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func mustGetScanImage(t *testing.T, s *store.Store, digest string) store.ScanImage {
	t.Helper()
	img, err := s.GetScanImage(context.Background(), digest)
	if err != nil {
		t.Fatalf("GetScanImage(%s): %v", digest, err)
	}
	return img
}

func TestRequeueAndMarkScanImages_OnlyMoveRowsInTheExpectedState(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	all := []string{"sha256:a", "sha256:other", "sha256:scanned", "sha256:failed"}
	seedScanImages(t, s, now, all...)
	runID := newScanRun(t, s, "scan", all...)
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanning' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued' where digest = 'sha256:other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanned' where digest = 'sha256:scanned'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'failed' where digest = 'sha256:failed'`); err != nil {
		t.Fatal(err)
	}

	// A repeated import after failover requeues the same digests twice: only the first counts.
	for i := 0; i < 2; i++ {
		if err := s.RequeueScanImages(ctx, all, now, false, "task_failed", "x"); err != nil {
			t.Fatal(err)
		}
	}
	a := mustGetScanImage(t, s, "sha256:a")
	if a.Status != "queued" || a.Attempts != 1 {
		t.Fatalf("requeued twice: status=%q attempts=%d, want queued 1", a.Status, a.Attempts)
	}
	for digest, want := range map[string]string{"sha256:scanned": "scanned", "sha256:failed": "failed"} {
		img := mustGetScanImage(t, s, digest)
		if img.Status != want || img.Attempts != 0 {
			t.Fatalf("%s after requeue status=%q attempts=%d, want %s 0", digest, img.Status, img.Attempts, want)
		}
	}

	// Mark moves queued and scanned (rescan) rows, nothing else.
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkScanImagesScanning(ctx, all, runID); err != nil {
		t.Fatal(err)
	}
	for digest, want := range map[string]string{"sha256:a": "scanning", "sha256:other": "scanning", "sha256:scanned": "scanning", "sha256:failed": "failed"} {
		if got := mustGetScanImage(t, s, digest).Status; got != want {
			t.Fatalf("%s after mark status=%q, want %s", digest, got, want)
		}
	}
}
