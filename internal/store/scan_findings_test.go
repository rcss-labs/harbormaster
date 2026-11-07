package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func finding(vulnID, pkg, severity string) scan.Finding {
	return scan.Finding{VulnID: vulnID, Severity: severity, PackageName: pkg, PackageVersion: "1.0", PackageType: "deb"}
}

func scannedResult(dbBuiltAt time.Time) store.ScanImageResult {
	packages := 12
	return store.ScanImageResult{Status: "scanned", Platform: "linux/amd64", PlatformDigest: "sha256:plat",
		SyftVersion: "1.0", ScannerImageDigest: "sha256:scanner", Packages: &packages, MatchedDBBuiltAt: &dbBuiltAt}
}

func findingsByVuln(t *testing.T, s *store.Store, digest string) map[string]store.ScanFinding {
	t.Helper()
	rows, err := s.ScanFindings(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	byVuln := map[string]store.ScanFinding{}
	for _, row := range rows {
		byVuln[row.VulnID] = row
	}
	return byVuln
}

func TestRecordScanResult_KeepsFirstSeenAcrossRuns(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	t0 := fx.now.Add(-2 * time.Hour)
	t1 := fx.now.Add(-time.Hour)
	seedScanImages(t, s, t0, "sha256:a")
	if _, err := s.Pool.Exec(ctx, `update scan_images set attempts = 2, defers = 1 where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordScanResult(ctx, "sha256:a", 1, scannedResult(t0), []scan.Finding{
		finding("CVE-0000-0001", "libc", "critical"), finding("CVE-0000-0002", "zlib", "low")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScanResult(ctx, "sha256:a", 2, scannedResult(t1), []scan.Finding{
		finding("CVE-0000-0001", "libc", "critical"), finding("CVE-0000-0003", "curl", "high")}, t1); err != nil {
		t.Fatal(err)
	}

	got := findingsByVuln(t, s, "sha256:a")
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2 (the one missing from the second run is dropped)", len(got))
	}
	kept, added := got["CVE-0000-0001"], got["CVE-0000-0003"]
	if !kept.FirstSeenAt.Equal(t0) || !kept.LastSeenAt.Equal(t1) || kept.RunID != 2 {
		t.Fatalf("kept finding first=%v last=%v run=%d, want %v %v 2", kept.FirstSeenAt, kept.LastSeenAt, kept.RunID, t0, t1)
	}
	if !added.FirstSeenAt.Equal(t1) {
		t.Fatalf("new finding first_seen_at = %v, want %v", added.FirstSeenAt, t1)
	}
	img := mustGetScanImage(t, s, "sha256:a")
	if img.Status != "scanned" || img.Attempts != 0 || img.Packages == nil || *img.Packages != 12 ||
		img.MatchedDBBuiltAt == nil || !img.MatchedDBBuiltAt.Equal(t1) || img.LastMatchedRunID == nil || *img.LastMatchedRunID != 2 {
		t.Fatalf("image after scan: %+v", img)
	}
	var defers int
	if err := s.Pool.QueryRow(ctx, `select defers from scan_images where digest = 'sha256:a'`).Scan(&defers); err != nil || defers != 0 {
		t.Fatalf("defers = %d err=%v, want 0", defers, err)
	}
}

func TestRecordScanResult_FailureKeepsOldFindings(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	seedScanImages(t, s, fx.now, "sha256:a")
	if err := s.RecordScanResult(ctx, "sha256:a", 1, scannedResult(fx.now), []scan.Finding{finding("CVE-0000-0001", "libc", "critical")}, fx.now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_images set defers = 2 where digest = 'sha256:a'`); err != nil {
		t.Fatal(err)
	}

	failure := store.ScanImageResult{Status: "failed", FailureCode: "too_large", FailureDetail: "image is over the limit"}
	if err := s.RecordScanResult(ctx, "sha256:a", 2, failure, nil, fx.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	img := mustGetScanImage(t, s, "sha256:a")
	if img.Status != "failed" || img.FailureCode != "too_large" || img.FailureDetail != "image is over the limit" {
		t.Fatalf("image status=%q code=%q detail=%q", img.Status, img.FailureCode, img.FailureDetail)
	}
	if got := findingsByVuln(t, s, "sha256:a"); len(got) != 1 {
		t.Fatalf("findings after a failed run = %d, want the 1 earlier finding", len(got))
	}
	var defers int
	if err := s.Pool.QueryRow(ctx, `select defers from scan_images where digest = 'sha256:a'`).Scan(&defers); err != nil || defers != 0 {
		t.Fatalf("defers = %d err=%v, want 0", defers, err)
	}
}

func TestRecordScanResult_MissingImageWritesNoFindings(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	err := s.RecordScanResult(ctx, "sha256:gone", 1, scannedResult(fx.now), []scan.Finding{finding("CVE-0000-0001", "libc", "critical")}, fx.now)
	if err != nil {
		t.Fatalf("RecordScanResult for a missing image: %v, want nil", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from scan_findings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("findings = %d err=%v, want 0", n, err)
	}
}

// openFindingFixture seeds a digest with one critical and one high finding.
func openFindingFixture(t *testing.T, fx readFixture, digest string) {
	t.Helper()
	seedScanImages(t, fx.s, fx.now, digest)
	if err := fx.s.RecordScanResult(context.Background(), digest, 1, scannedResult(fx.now), []scan.Finding{
		finding("CVE-0000-0001", "libc", "critical"), finding("CVE-0000-0002", "zlib", "high")}, fx.now); err != nil {
		t.Fatal(err)
	}
}

func acceptCritical(t *testing.T, s *store.Store, digest string, expires time.Time, services ...model.ServiceKey) []int64 {
	t.Helper()
	ids, err := s.CreateScanAcceptances(context.Background(), store.ScanAcceptance{Digest: digest, VulnID: "CVE-0000-0001",
		PackageName: "libc", PackageVersion: "1.0", Reason: "not reachable", ExpiresAt: expires}, services)
	if err != nil || len(ids) != len(services) {
		t.Fatalf("CreateScanAcceptances: ids=%v err=%v, want %d ids", ids, err, len(services))
	}
	return ids
}

func TestOpenFindingCounts_ExpiredAcceptanceCounts(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	svc := serviceKey("core-services", "payments-api")
	for _, digest := range []string{"sha256:expired", "sha256:live", "sha256:revoked"} {
		openFindingFixture(t, fx, digest)
	}
	acceptCritical(t, s, "sha256:expired", fx.now.Add(-time.Minute), svc)
	acceptCritical(t, s, "sha256:live", fx.now.Add(24*time.Hour), svc)
	revokedIDs := acceptCritical(t, s, "sha256:revoked", fx.now.Add(24*time.Hour), svc)
	if _, err := s.RevokeScanAcceptance(ctx, revokedIDs[0], 0, fx.now); err != nil {
		t.Fatal(err)
	}

	counts, accepted, err := s.OpenFindingCounts(ctx, []string{"sha256:expired", "sha256:live", "sha256:revoked"}, &svc, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	for digest, want := range map[string]struct{ critical, accepted int }{
		"sha256:expired": {1, 0}, "sha256:live": {0, 1}, "sha256:revoked": {1, 0}} {
		if counts[digest].Critical != want.critical || counts[digest].High != 1 || accepted[digest] != want.accepted {
			t.Errorf("%s: critical=%d high=%d accepted=%d, want critical=%d high=1 accepted=%d",
				digest, counts[digest].Critical, counts[digest].High, accepted[digest], want.critical, want.accepted)
		}
	}
}

func TestOpenFindingCounts_AcceptanceIsPerService(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	a, b := serviceKey("core-services", "payments-api"), serviceKey("core-services", "notifications")
	openFindingFixture(t, fx, "sha256:a")
	for _, key := range []model.ServiceKey{a, b} {
		if _, err := s.AddScanRequest(ctx, store.ScanRequest{Digest: "sha256:a", Key: key, Container: "app", Image: "repo:v1",
			Source: "manual", RequestedAt: fx.now}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string, scope *model.ServiceKey, wantCritical, wantAccepted int) {
		t.Helper()
		counts, accepted, err := s.OpenFindingCounts(ctx, []string{"sha256:a"}, scope, fx.now)
		if err != nil {
			t.Fatal(err)
		}
		if counts["sha256:a"].Critical != wantCritical || accepted["sha256:a"] != wantAccepted {
			t.Errorf("%s: critical=%d accepted=%d, want %d and %d", label, counts["sha256:a"].Critical, accepted["sha256:a"], wantCritical, wantAccepted)
		}
	}

	acceptCritical(t, s, "sha256:a", fx.now.Add(time.Hour), a)
	check("scope A", &a, 0, 1)
	check("scope B", &b, 1, 0)
	check("no scope, only A accepted", nil, 1, 0)

	acceptCritical(t, s, "sha256:a", fx.now.Add(time.Hour), b)
	check("no scope, A and B accepted", nil, 0, 1)
}
