package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func scanSeen(digest, kind, role string) store.ScanImageSeen {
	return store.ScanImageSeen{
		Digest: digest, ImageRef: "111111111111.dkr.ecr.eu-west-1.amazonaws.com/api", Tag: "v1",
		RegistryKind: kind, RegistryAccountID: "111111111111", RegistryRegion: "eu-west-1",
		Repository: "api", Platform: "linux/amd64", Role: role,
	}
}

func seedScanImages(t *testing.T, s *store.Store, now time.Time, seen ...store.ScanImageSeen) {
	t.Helper()
	if _, err := s.UpsertScanImagesSeen(context.Background(), seen, now); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertScanImagesSeen_NewAreQueuedKnownOnlyTouched(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	t1 := t0.Add(30 * time.Minute)
	seen := []store.ScanImageSeen{scanSeen("sha256:ecr", "ecr", "running"), scanSeen("sha256:hub", "other", "running")}

	fresh, err := s.UpsertScanImagesSeen(ctx, seen, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 2 {
		t.Fatalf("new digests = %v, want both", fresh)
	}
	ecr, err := s.GetScanImage(ctx, "sha256:ecr")
	if err != nil {
		t.Fatal(err)
	}
	if ecr.Status != "queued" || ecr.Role != "running" {
		t.Fatalf("ecr row status=%q role=%q, want queued running", ecr.Status, ecr.Role)
	}
	other, err := s.GetScanImage(ctx, "sha256:hub")
	if err != nil {
		t.Fatal(err)
	}
	if other.Status != "unsupported" || other.FailureCode != "unsupported_registry" {
		t.Fatalf("other row status=%q code=%q, want unsupported unsupported_registry", other.Status, other.FailureCode)
	}

	fresh, err = s.UpsertScanImagesSeen(ctx, seen, t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Fatalf("second call new digests = %v, want none", fresh)
	}
	ecr, _ = s.GetScanImage(ctx, "sha256:ecr")
	if ecr.LastSeenRunningAt == nil || !ecr.LastSeenRunningAt.Equal(t1) {
		t.Fatalf("last_seen_running_at = %v, want %v", ecr.LastSeenRunningAt, t1)
	}
	if !ecr.FirstSeenAt.Equal(t0) {
		t.Fatalf("first_seen_at = %v, want unchanged %v", ecr.FirstSeenAt, t0)
	}
}

func TestUpsertScanImagesSeen_PromotesCandidateAndRevivesAgedOut(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now.Add(-time.Hour),
		scanSeen("sha256:cand", "ecr", "candidate"), scanSeen("sha256:old", "ecr", "running"))
	if _, err := s.Pool.Exec(ctx, `update scan_images set status = 'aged_out', attempts = 2, sbom_key = 'sbom/x.json' where digest = 'sha256:old'`); err != nil {
		t.Fatal(err)
	}

	// A candidate seen as a candidate again stays untouched.
	seedScanImages(t, s, now, scanSeen("sha256:cand", "ecr", "candidate"))
	cand, _ := s.GetScanImage(ctx, "sha256:cand")
	if cand.LastSeenRunningAt != nil {
		t.Fatalf("candidate touched, last_seen_running_at = %v", cand.LastSeenRunningAt)
	}

	seedScanImages(t, s, now, scanSeen("sha256:cand", "ecr", "running"), scanSeen("sha256:old", "ecr", "running"))
	cand, _ = s.GetScanImage(ctx, "sha256:cand")
	if cand.Role != "running" {
		t.Fatalf("candidate role = %q, want running", cand.Role)
	}
	old, _ := s.GetScanImage(ctx, "sha256:old")
	if old.Status != "queued" || old.Attempts != 0 || old.SBOMKey != "" {
		t.Fatalf("aged_out row status=%q attempts=%d sbom=%q, want queued 0 empty", old.Status, old.Attempts, old.SBOMKey)
	}
}

func TestDueScanImages_OrdersRunningBeforeCandidateAndHonoursNextAttempt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now.Add(-3*time.Hour), scanSeen("sha256:cand", "ecr", "candidate"))
	seedScanImages(t, s, now.Add(-2*time.Hour), scanSeen("sha256:run-new", "ecr", "running"))
	seedScanImages(t, s, now.Add(-4*time.Hour), scanSeen("sha256:run-old", "ecr", "running"))
	seedScanImages(t, s, now.Add(-time.Hour), scanSeen("sha256:later", "ecr", "running"), scanSeen("sha256:other", "other", "running"))
	if _, err := s.Pool.Exec(ctx, `update scan_images set next_attempt_at = $1 where digest = 'sha256:later'`, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	due, err := s.DueScanImages(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, img := range due {
		got = append(got, img.Digest)
	}
	want := []string{"sha256:run-old", "sha256:run-new", "sha256:cand"}
	if len(got) != len(want) {
		t.Fatalf("due = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due = %v, want %v", got, want)
		}
	}
	limited, _ := s.DueScanImages(ctx, now, 1)
	if len(limited) != 1 || limited[0].Digest != "sha256:run-old" {
		t.Fatalf("limit 1 = %v, want only run-old", limited)
	}
}

func TestRequeueScanImages_FailsAfterThreeAttempts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, scanSeen("sha256:a", "ecr", "running"), scanSeen("sha256:b", "ecr", "running"))
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
	a, _ := s.GetScanImage(ctx, "sha256:a")
	if a.Status != "failed" || a.Attempts != 3 || a.FailureCode != "task_failed" || a.FailureDetail != "exit 1" {
		t.Fatalf("third attempt: status=%q attempts=%d code=%q detail=%q", a.Status, a.Attempts, a.FailureCode, a.FailureDetail)
	}
	b, _ := s.GetScanImage(ctx, "sha256:b")
	if b.Status != "queued" || b.Attempts != 1 || !b.Large || b.NextAttemptAt == nil || !b.NextAttemptAt.Equal(next) {
		t.Fatalf("first attempt: status=%q attempts=%d large=%v next=%v", b.Status, b.Attempts, b.Large, b.NextAttemptAt)
	}

	// large=false leaves an existing flag alone.
	if err := s.RequeueScanImages(ctx, []string{"sha256:b"}, next, false, "task_failed", "again"); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetScanImage(ctx, "sha256:b")
	if !b.Large || b.Attempts != 2 {
		t.Fatalf("second requeue large=%v attempts=%d, want true 2", b.Large, b.Attempts)
	}
}

func TestDeferScanImages_KeepsAttempts(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, scanSeen("sha256:a", "ecr", "running"))
	if _, err := s.Pool.Exec(ctx, `update scan_images set attempts = 2, large = true, status = 'scanning' where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}
	next := now.Add(time.Hour)
	if err := s.DeferScanImages(ctx, []string{"sha256:a"}, next, "db_stale", "database too old"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetScanImage(ctx, "sha256:a")
	if a.Status != "queued" || a.Attempts != 2 || !a.Large || a.FailureCode != "db_stale" || a.NextAttemptAt == nil || !a.NextAttemptAt.Equal(next) {
		t.Fatalf("deferred row status=%q attempts=%d large=%v code=%q next=%v", a.Status, a.Attempts, a.Large, a.FailureCode, a.NextAttemptAt)
	}
}

func TestLiveSBOMDigests_OnlyThoseMatchedAgainstOlderData(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	built := time.Now().Truncate(time.Second)
	for _, d := range []string{"sha256:never", "sha256:old", "sha256:current", "sha256:nosbom", "sha256:failed"} {
		seedScanImages(t, s, built, scanSeen(d, "ecr", "running"))
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`update scan_images set status = 'scanned', sbom_key = 'sbom/k' where digest in ('sha256:never', 'sha256:old', 'sha256:current', 'sha256:nosbom')`)
	exec(`update scan_images set sbom_key = null where digest = 'sha256:nosbom'`)
	exec(`update scan_images set status = 'failed', sbom_key = 'sbom/k' where digest = 'sha256:failed'`)
	exec(`update scan_images set matched_db_built_at = $1 where digest = 'sha256:old'`, built.Add(-time.Hour))
	exec(`update scan_images set matched_db_built_at = $1 where digest = 'sha256:current'`, built)

	got, err := s.LiveSBOMDigests(ctx, built, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !(contains(got, "sha256:never") && contains(got, "sha256:old")) {
		t.Fatalf("live SBOM digests = %v, want never and old", got)
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
