package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func baseImagePath(digest string) string {
	return "/api/security/images/" + url.PathEscape(digest) + "/base-image"
}

// putBase sets the base and returns the detail the server answers with.
func putBase(t *testing.T, c *apiClient, digest, ref string) SecurityImageDetailDTO {
	t.Helper()
	resp := c.do(http.MethodPut, baseImagePath(digest), SetBaseImageRequest{BaseRef: ref}, nil)
	wantStatus(t, resp, http.StatusOK)
	var out SecurityImageDetailDTO
	decode(t, resp, &out)
	return out
}

func imageDetail(t *testing.T, c *apiClient, digest string) SecurityImageDetailDTO {
	t.Helper()
	resp := c.get("/api/security/images/" + url.PathEscape(digest))
	wantStatus(t, resp, http.StatusOK)
	var out SecurityImageDetailDTO
	decode(t, resp, &out)
	return out
}

func originsByPackage(detail SecurityImageDetailDTO) map[string]string {
	out := map[string]string{}
	for _, f := range detail.Findings {
		out[f.Package] = f.Origin
	}
	return out
}

// scannedPayments scans payments-api:1.18.2, whose fake findings are a go module (app layer) and glibc (base layer).
func scannedPayments(t *testing.T, env *securityEnv) string {
	t.Helper()
	status := mustRequestScan(t, env.sam, securityPaymentsKey.ID(), "payments-api", "1.18.2")
	env.scanAll(t)
	return status.Image.Digest
}

func TestSetBaseImage_ValidatesTheReference(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedCriticalEdgeNginx(t, env)
	for _, ref := range []string{
		"not a ref", "node:", "UPPER/case:1", "ghcr.io/org/img@sha256:abc", "node:20\n", "-bad/ref:1", "host name.io/x:1",
		strings.Repeat("a", 256), "https://node:20",
	} {
		wantStatus(t, env.dana.do(http.MethodPut, baseImagePath(digest), SetBaseImageRequest{BaseRef: ref}, nil), http.StatusUnprocessableEntity)
	}
	if n := countRows(t, env.st, `select count(*) from scan_base_images`); n != 0 {
		t.Fatalf("%d bases stored after invalid refs", n)
	}
	for _, ref := range []string{
		"node:20", "library/node:20-slim", "public.ecr.aws/docker/library/python:3.12",
		"ghcr.io/org/img@sha256:" + strings.Repeat("a", 64), "123456789012.dkr.ecr.eu-west-1.amazonaws.com/base:1",
		"localhost:5000/team/app:2",
	} {
		putBase(t, env.dana, digest, ref)
	}
}

func TestSetBaseImage_VisibilityPermissionsStoreAuditAndQueue(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedCriticalEdgeNginx(t, env)
	body := SetBaseImageRequest{BaseRef: "node:20"}

	wantStatus(t, env.omer.do(http.MethodPut, baseImagePath(digest), body, nil), http.StatusForbidden)
	wantStatus(t, env.addUser(t, "nobody", nil).do(http.MethodPut, baseImagePath(digest), body, nil), http.StatusNotFound)
	if n := countRows(t, env.st, `select count(*) from scan_base_images`); n != 0 {
		t.Fatal("a refused request stored a base")
	}
	if detail := imageDetail(t, env.omer, digest); detail.Can.SetBase || !imageDetail(t, env.dana, digest).Can.SetBase {
		t.Fatal("can.set_base should follow the Deploy permission")
	}

	runs := env.scanRuns(t)
	detail := putBase(t, env.dana, digest, "node:20")
	if detail.BaseImage.Ref != "node:20" || detail.BaseImage.State != "pending" || detail.Status != "queued" {
		t.Fatalf("detail after PUT = base %+v status %q, want pending and queued", detail.BaseImage, detail.Status)
	}
	img, err := env.st.GetScanImage(t.Context(), digest)
	if err != nil {
		t.Fatal(err)
	}
	bases, err := env.st.RepoBases(t.Context(), []store.RepoKey{{AccountID: img.RegistryAccountID, Region: img.RegistryRegion, Repository: img.Repository}})
	if err != nil || len(bases) != 1 {
		t.Fatalf("bases = %v err %v, want the repository's", bases, err)
	}
	entries := auditByAction(t, env.st, "security.base_image")
	if len(entries) != 1 || entries[0].ActorUsername != "dana" || !strings.Contains(entries[0].Summary, "node:20") {
		t.Fatalf("audit = %+v, want one entry by dana naming the base", entries)
	}

	// Already queued: the PUT still succeeds, and the queued scan takes the new base.
	putBase(t, env.dana, digest, "node:22")
	env.scanAll(t)
	if env.scanRuns(t) != runs+1 {
		t.Fatalf("%d runs, want exactly one scan after both changes", env.scanRuns(t)-runs)
	}
	if got := imageDetail(t, env.dana, digest).BaseImage; got.Ref != "node:22" || got.ScannedRef != "node:22" || got.State != "matched" {
		t.Fatalf("base after the scan = %+v, want node:22 matched", got)
	}
}

func TestBaseImage_MatchedGivesOriginPerFinding(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedPayments(t, env)

	before := imageDetail(t, env.sam, digest)
	if before.BaseImage.State != "none" || before.BaseImage.Ref != "" {
		t.Fatalf("base before = %+v, want none", before.BaseImage)
	}
	putBase(t, env.sam, digest, "node:20")
	env.scanAll(t)

	detail := imageDetail(t, env.sam, digest)
	base := detail.BaseImage
	if base.State != "matched" || base.MatchedLayers != 3 || base.TotalLayers != 5 || base.ScannedRef != "node:20" {
		t.Fatalf("base = %+v, want matched 3 of 5", base)
	}
	origins := originsByPackage(detail)
	if origins["glibc"] != "base" || origins["golang.org/x/net"] != "app" {
		t.Fatalf("origins = %v, want glibc base and the go module app", origins)
	}
}

func TestBaseImage_PendingUntilTheRescanRecordsTheNewBase(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedPayments(t, env)
	putBase(t, env.sam, digest, "node:20")
	env.scanAll(t)

	// A different base: pending while the rescan is queued, and no origin is claimed from the old comparison.
	detail := putBase(t, env.sam, digest, "node:22")
	if detail.BaseImage.State != "pending" || detail.BaseImage.ScannedRef != "node:20" {
		t.Fatalf("base = %+v, want pending with the last scanned node:20", detail.BaseImage)
	}
	for _, f := range detail.Findings {
		if f.Origin != "" {
			t.Fatalf("finding %s has origin %q while pending", f.Package, f.Origin)
		}
	}
}

func TestBaseImage_MismatchAndError(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedPayments(t, env)

	putBase(t, env.sam, digest, "node:old")
	env.scanAll(t)
	detail := imageDetail(t, env.sam, digest)
	want := "This image is not built on the current node:old. It may use an older version of it; rebuild the image or set the base by digest."
	if detail.BaseImage.State != "mismatch" || detail.BaseImage.Message != want {
		t.Fatalf("base = %+v, want mismatch with the message", detail.BaseImage)
	}
	for _, f := range detail.Findings {
		if f.Origin != "" {
			t.Fatalf("finding %s has origin %q on a mismatch", f.Package, f.Origin)
		}
	}

	putBase(t, env.sam, digest, "registry.example.com/team/missing:1")
	env.scanAll(t)
	detail = imageDetail(t, env.sam, digest)
	if detail.BaseImage.State != "error" || detail.BaseImage.Message != "Could not read registry.example.com/team/missing:1: manifest unknown" {
		t.Fatalf("base = %+v, want the read error", detail.BaseImage)
	}
}

func TestBaseImage_ClearingGivesNoneAndNoOrigin(t *testing.T) {
	env := newSecurityEnv(t)
	digest := scannedPayments(t, env)
	putBase(t, env.sam, digest, "node:20")
	env.scanAll(t)

	detail := putBase(t, env.sam, digest, "")
	if detail.BaseImage.State != "none" || detail.BaseImage.Ref != "" {
		t.Fatalf("base after clearing = %+v, want none", detail.BaseImage)
	}
	env.scanAll(t)
	detail = imageDetail(t, env.sam, digest)
	if detail.BaseImage.State != "none" {
		t.Fatalf("base after the rescan = %+v, want none", detail.BaseImage)
	}
	for _, f := range detail.Findings {
		if f.Origin != "" {
			t.Fatalf("finding %s has origin %q with no base", f.Package, f.Origin)
		}
	}
	if n := countRows(t, env.st, `select count(*) from scan_base_images`); n != 0 {
		t.Fatalf("%d bases stored after clearing", n)
	}
}

func TestBaseImage_SharedByEveryTagOfTheRepository(t *testing.T) {
	env := newSecurityEnv(t)
	first := scannedCriticalEdgeNginx(t, env)
	second := mustRequestScan(t, env.dana, edgeNginxKey.ID(), "edge-nginx", "1.27.2").Image.Digest
	env.scanAll(t)

	putBase(t, env.dana, first, "node:20")
	if got := imageDetail(t, env.dana, second).BaseImage; got.Ref != "node:20" || got.State != "pending" {
		t.Fatalf("other tag base = %+v, want the repository's base, pending its scan", got)
	}
	if got := securityImage(t, env.dana, second).BaseImage; got.Ref != "node:20" {
		t.Fatalf("list base = %+v, want the repository's base", got)
	}
}
