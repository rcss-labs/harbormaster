package scan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func resultBody(t *testing.T, digest string, findings []Finding, mutate func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{"digest": digest, "status": "scanned", "findings": findings}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sampleFinding(i int) Finding {
	return Finding{VulnID: fmt.Sprintf("CVE-0000-%04d", i), Severity: "high", PackageName: "pkg", PackageVersion: "1"}
}

func TestParseResult_AcceptsFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/result_ok.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseResult(data, digestA)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("want 2 findings, got %+v", res)
	}
	if res.Findings[0].Severity != SeverityHigh || res.Platform != "linux/amd64" ||
		res.PlatformDigest != digestB || res.SyftVersion != "1.0.0" || res.Packages != 120 || res.Truncated {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.Findings[0].URL != "https://example.com/CVE-0000-0001" {
		t.Errorf("https url should be kept, got %q", res.Findings[0].URL)
	}
}

func TestParseResult_RejectsOversizeAndForeignDigest(t *testing.T) {
	big := append(resultBody(t, digestA, nil, nil), make([]byte, MaxResultBytes)...)
	if _, err := ParseResult(big, digestA); err == nil {
		t.Error("oversize body should error")
	}
	if _, err := ParseResult(resultBody(t, digestB, nil, nil), digestA); err == nil {
		t.Error("foreign digest should error")
	}
	if _, err := ParseResult(resultBody(t, digestA, nil, func(m map[string]any) { m["status"] = "failed" }), digestA); err == nil {
		t.Error("non-scanned status should error")
	}
	if _, err := ParseResult([]byte("{not json"), digestA); err == nil {
		t.Error("bad JSON should error")
	}
}

func TestParseResult_TruncatesAndSanitises(t *testing.T) {
	var many []Finding
	for i := 0; i < MaxFindings+1; i++ {
		many = append(many, sampleFinding(i))
	}
	res, err := ParseResult(resultBody(t, digestA, many, nil), digestA)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != MaxFindings || !res.Truncated {
		t.Errorf("got %d findings truncated=%v, want %d and true", len(res.Findings), res.Truncated, MaxFindings)
	}

	hostile := sampleFinding(1)
	hostile.PackageName = "bad\x00" + strings.Repeat("x", 300)
	crit, bogus, over := sampleFinding(2), sampleFinding(3), sampleFinding(4)
	crit.Severity, bogus.Severity = "Critical", "bogus"
	eleven := 11.0
	over.CVSS = &eleven
	dup := sampleFinding(2)
	res, err = ParseResult(resultBody(t, digestA, []Finding{hostile, crit, bogus, over, dup}, nil), digestA)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 4 {
		t.Fatalf("duplicate should collapse, got %d findings", len(res.Findings))
	}
	if got := res.Findings[0].PackageName; len(got) != MaxStringLen || strings.ContainsRune(got, 0) {
		t.Errorf("package name not sanitised: len %d", len(got))
	}
	if res.Findings[1].Severity != SeverityCritical || res.Findings[2].Severity != SeverityUnknown {
		t.Errorf("severities: %q %q", res.Findings[1].Severity, res.Findings[2].Severity)
	}
	if res.Findings[3].CVSS == nil || *res.Findings[3].CVSS != 10 {
		t.Errorf("cvss not clamped: %v", res.Findings[3].CVSS)
	}
}

func TestParseResult_DropsBadIDsAndURLs(t *testing.T) {
	badID, js, ok := sampleFinding(1), sampleFinding(2), sampleFinding(3)
	badID.VulnID = "CVE 0000 0001"
	js.URL = "javascript:alert(1)"
	ok.URL = "https://example.com/x"
	res, err := ParseResult(resultBody(t, digestA, []Finding{badID, js, ok}, nil), digestA)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 || !res.Truncated {
		t.Fatalf("bad id should be dropped with Truncated: %+v", res)
	}
	if res.Findings[0].URL != "" || res.Findings[1].URL != "https://example.com/x" {
		t.Errorf("urls: %q %q", res.Findings[0].URL, res.Findings[1].URL)
	}

	hostileURLs := []string{
		"https://aws.amazon.com@evil.example/x",
		"https://",
		"https:///evil",
		"https://\\evil.example",
		"https:// evil",
		"ht\ntps://ok.example/",
		"https://ok.example/a\u202eb",
		"http://ok.example/",
	}
	for _, raw := range hostileURLs {
		f := sampleFinding(1)
		f.URL = raw
		res, err := ParseResult(resultBody(t, digestA, []Finding{f}, nil), digestA)
		if err != nil || len(res.Findings) != 1 {
			t.Fatalf("%q: %+v %v", raw, res, err)
		}
		if res.Findings[0].URL != "" {
			t.Errorf("url %q should be emptied, got %q", raw, res.Findings[0].URL)
		}
	}
}

func TestParseResult_ClearsBadMetadata(t *testing.T) {
	res, err := ParseResult(resultBody(t, digestA, nil, func(m map[string]any) {
		m["platform_digest"] = "sha256:short"
		m["platform"] = "windows/amd64"
		m["syft_version"] = "1.0\n; rm -rf"
	}), digestA)
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != digestA {
		t.Fatalf("digest should be kept, got %q", res.Digest)
	}
	if res.PlatformDigest != "" || res.Platform != "" || res.SyftVersion != "" {
		t.Errorf("bad metadata should be emptied: %+v", res)
	}
}

func manifestBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"run_id": 42, "kind": "scan", "status": "succeeded",
		"images": []map[string]any{{"digest": digestA, "status": "scanned"}},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseManifest_AcceptsFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/manifest_ok.json")
	if err != nil {
		t.Fatal(err)
	}
	m, warnings, err := ParseManifest(data, 42, []string{digestA, digestB})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 2 {
		t.Fatalf("want 2 images, got %+v", m.Images)
	}
	if len(warnings) != 0 || m.ScannerImageDigest != digestB || m.GrypeVersion != "0.90.0" ||
		m.Images[1].FailureCode != CodePullDenied || m.DBBuiltAt.IsZero() {
		t.Errorf("unexpected manifest %+v warnings %v", m, warnings)
	}
}

func TestParseManifest_DropsImagesNotInJob(t *testing.T) {
	body := manifestBody(t, func(m map[string]any) {
		m["images"] = []map[string]any{{"digest": digestA, "status": "scanned"}, {"digest": digestB, "status": "scanned"}}
	})
	m, warnings, err := ParseManifest(body, 42, []string{digestA})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 1 || len(warnings) != 1 || m.Images[0].Digest != digestA {
		t.Errorf("images %+v warnings %v", m.Images, warnings)
	}
}

func TestParseManifest_RunIDMustMatch(t *testing.T) {
	if _, _, err := ParseManifest(manifestBody(t, nil), 43, []string{digestA}); err == nil {
		t.Error("run id mismatch should error")
	}
	big := append(manifestBody(t, nil), make([]byte, MaxManifestBytes)...)
	if _, _, err := ParseManifest(big, 42, []string{digestA}); err == nil {
		t.Error("oversize manifest should error")
	}
	if _, _, err := ParseManifest([]byte("nope"), 42, nil); err == nil {
		t.Error("bad JSON should error")
	}
}

func TestParseManifest_SanitisesAndWhitelists(t *testing.T) {
	hostile := "bad\x00line\n" + strings.Repeat("y", 400)
	body := manifestBody(t, func(m map[string]any) {
		m["error"] = hostile
		m["scanner_image_digest"] = "not-a-digest"
		m["grype_version"] = "0.1; rm"
		m["db_built_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		m["images"] = []map[string]any{
			{"digest": digestA, "status": "failed", "failure_code": "pull_denied", "failure_detail": hostile},
			{"digest": "sha256:" + strings.Repeat("c", 64), "status": "mystery", "failure_code": "made_up"},
			{"digest": "evil\x00\n" + digestB, "status": "scanned"},
		}
	})
	m, warnings, err := ParseManifest(body, 42, []string{digestA, "sha256:" + strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Images) != 2 {
		t.Fatalf("want 2 images, got %+v", m.Images)
	}
	for _, s := range []string{m.Error, m.Images[0].FailureDetail} {
		if len(s) > MaxStringLen || strings.ContainsAny(s, "\x00\n") || s == "" {
			t.Errorf("not sanitised: %q", s)
		}
	}
	if m.Images[0].FailureCode != CodePullDenied {
		t.Errorf("known code lost: %q", m.Images[0].FailureCode)
	}
	if m.Images[1].Status != OutcomeFailed || m.Images[1].FailureCode != CodeTaskFailed {
		t.Errorf("unknown status/code not whitelisted: %+v", m.Images[1])
	}
	if m.ScannerImageDigest != "" || m.GrypeVersion != "" || !m.DBBuiltAt.IsZero() {
		t.Errorf("bad digest/version/future time not cleared: %+v", m)
	}
	if len(warnings) != 1 || strings.ContainsAny(warnings[0], "\x00\n") {
		t.Errorf("warnings should name the dropped digest sanitised: %q", warnings)
	}

	if _, _, err := ParseManifest(manifestBody(t, func(m map[string]any) { m["status"] = "weird" }), 42, []string{digestA}); err == nil {
		t.Error("unknown manifest status should error")
	}
}

func TestParseUpstreamDB_ValidatesPathAndChecksum(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	body := func(built, path, sum string) []byte {
		return []byte(fmt.Sprintf(`{"built":%q,"path":%q,"checksum":%q}`, built, path, sum))
	}
	sum := digestA
	past := "2026-11-07T01:00:00Z"
	got, err := ParseUpstreamDB(body(past, "vulnerability-db_v6.0.0_2026-11-07T01:00:00Z_1.tar.zst", sum), now)
	if err != nil || got.Path == "" || got.Checksum != sum || got.Built.IsZero() {
		t.Fatalf("real shape should parse: %+v %v", got, err)
	}
	bad := map[string][]byte{
		"slash":    body(past, "v6/db.tar.zst", sum),
		"dotdot":   body(past, "../db.tar.zst", sum),
		"checksum": body(past, "db.tar.zst", "sha256:xyz"),
		"future":   body("2026-11-07T13:00:00Z", "db.tar.zst", sum),
		"oversize": append(body(past, "db.tar.zst", sum), make([]byte, MaxDBLatestBytes)...),
	}
	for name, b := range bad {
		if _, err := ParseUpstreamDB(b, now); err == nil {
			t.Errorf("%s should error", name)
		}
	}
}

func TestParseDBLatest_ValidatesKeyAndChecksum(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	body := func(built, key, sum string) []byte {
		return []byte(fmt.Sprintf(`{"built_at":%q,"archive_key":%q,"checksum":%q}`, built, key, sum))
	}
	past := "2026-11-07T01:00:00Z"
	got, err := ParseDBLatest(body(past, "grype-db/v6/2026-11-07.tar.zst", digestA), now)
	if err != nil || got.ArchiveKey != "grype-db/v6/2026-11-07.tar.zst" || got.Checksum != digestA || got.BuiltAt.IsZero() {
		t.Fatalf("valid latest should parse: %+v %v", got, err)
	}
	bad := map[string][]byte{
		"key outside": body(past, "results/1/db.tar.zst", digestA),
		"nested key":  body(past, "grype-db/v6/a/b.tar.zst", digestA),
		"checksum":    body(past, "grype-db/v6/a.tar.zst", "md5:1"),
		"future":      body("2026-11-07T13:00:00Z", "grype-db/v6/a.tar.zst", digestA),
	}
	for name, b := range bad {
		if _, err := ParseDBLatest(b, now); err == nil {
			t.Errorf("%s should error", name)
		}
	}
}

func TestSeverityCounts_Total(t *testing.T) {
	if got := (SeverityCounts{1, 2, 3, 4, 5, 6}).Total(); got != 21 {
		t.Errorf("Total = %d, want 21", got)
	}
}

func TestParseResult_BoundsMemoryOnManyEmptyFindings(t *testing.T) {
	var body bytes.Buffer
	body.WriteString(`{"digest":"` + digestA + `","status":"scanned","findings":[`)
	for body.Len() < MaxResultBytes-10 {
		body.WriteString("{},")
	}
	body.WriteString("{}]}")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := ParseResult(body.Bytes(), digestA)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Findings) != 0 {
		t.Errorf("want truncated and empty, got %d findings truncated=%v", len(res.Findings), res.Truncated)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 100<<20 {
		t.Errorf("parsing allocated %d MiB, want under 100", allocated>>20)
	}
}

func TestParseResult_RejectsBadPackagesAndFutureSBOM(t *testing.T) {
	for _, packages := range []any{-1, int64(math.MaxInt32) + 1, 1e30} {
		body := resultBody(t, digestA, nil, func(m map[string]any) { m["packages"] = packages })
		if _, err := ParseResult(body, digestA); err == nil {
			t.Errorf("packages %v should be rejected", packages)
		}
	}
	body := resultBody(t, digestA, nil, func(m map[string]any) { m["packages"] = math.MaxInt32 })
	if _, err := ParseResult(body, digestA); err != nil {
		t.Errorf("max int32 packages should be accepted: %v", err)
	}
	body = resultBody(t, digestA, nil, func(m map[string]any) {
		m["sbom_generated_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	})
	if _, err := ParseResult(body, digestA); err == nil {
		t.Error("future sbom_generated_at should be rejected")
	}
}

func TestParseResult_RejectsTrailingBytes(t *testing.T) {
	body := append(resultBody(t, digestA, nil, nil), []byte(` {"x":1}`)...)
	if _, err := ParseResult(body, digestA); err == nil {
		t.Error("trailing JSON should be rejected")
	}
	manifest := append(manifestBody(t, nil), []byte("garbage")...)
	if _, _, err := ParseManifest(manifest, 42, []string{digestA}); err == nil {
		t.Error("trailing bytes after manifest should be rejected")
	}
}

func TestParsers_ErrorsDoNotCarryAttackerText(t *testing.T) {
	huge := strings.Repeat("Z", 1<<20)
	now := time.Now()
	bodies := map[string]func() error{
		"result time": func() error {
			_, err := ParseResult([]byte(`{"digest":"`+digestA+`","status":"scanned","sbom_generated_at":"`+huge+`"}`), digestA)
			return err
		},
		"result digest": func() error {
			_, err := ParseResult([]byte(`{"digest":"`+huge+`","status":"scanned"}`), digestA)
			return err
		},
		"result type": func() error {
			_, err := ParseResult([]byte(`{"digest":"`+digestA+`","status":"scanned","findings":[{"vuln_id":5}]}`), digestA)
			return err
		},
		"manifest status": func() error {
			_, _, err := ParseManifest([]byte(`{"run_id":42,"status":"`+huge+`"}`), 42, nil)
			return err
		},
		"manifest time": func() error {
			_, _, err := ParseManifest([]byte(`{"run_id":42,"status":"failed","db_built_at":"`+huge+`"}`), 42, nil)
			return err
		},
		"latest time": func() error {
			_, err := ParseDBLatest([]byte(`{"built_at":"`+huge+`"}`), now)
			return err
		},
		"upstream path": func() error {
			_, err := ParseUpstreamDB([]byte(`{"built":"2026-01-01T00:00:00Z","path":"`+huge+`","checksum":"`+digestA+`"}`), now)
			return err
		},
	}
	for name, run := range bodies {
		err := run()
		if err == nil {
			t.Errorf("%s: want an error", name)
			continue
		}
		if len(err.Error()) > 200 {
			t.Errorf("%s: error is %d bytes long", name, len(err.Error()))
		}
	}
}

func TestSanitizeText_DropsInvisibleAndBidiCharacters(t *testing.T) {
	// right-to-left override, isolate, zero-width space, line and paragraph separators, a tag character
	in := "a\u202eb\u2066c\u200bd\u2028e\u2029f\U000E0041g"
	if got := SanitizeText(in); got != "abcdefg" {
		t.Errorf("SanitizeText = %q, want %q", got, "abcdefg")
	}
}

func TestParseManifest_WarningsAreCapped(t *testing.T) {
	var images []map[string]any
	for i := 0; i < 500; i++ {
		images = append(images, map[string]any{"digest": fmt.Sprintf("sha256:%064x", i+1000), "status": "scanned"})
	}
	body := manifestBody(t, func(m map[string]any) { m["images"] = images })
	_, warnings, err := ParseManifest(body, 42, []string{digestA})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 21 {
		t.Fatalf("want 20 warnings plus a summary, got %d", len(warnings))
	}
	if warnings[20] != "and 480 more" {
		t.Errorf("summary = %q", warnings[20])
	}
}

func TestParseManifest_KindAndScannedOutcomeWhitelists(t *testing.T) {
	body := manifestBody(t, func(m map[string]any) {
		m["kind"] = "db-refresh"
		m["images"] = []map[string]any{{"digest": digestA, "status": "scanned", "failure_code": "pull_denied", "failure_detail": "stale text"}}
	})
	m, _, err := ParseManifest(body, 42, []string{digestA})
	if err != nil || len(m.Images) != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	if m.Kind != "" {
		t.Errorf("unknown kind should be emptied, got %q", m.Kind)
	}
	if m.Images[0].FailureCode != "" || m.Images[0].FailureDetail != "" {
		t.Errorf("scanned outcome should carry no failure: %+v", m.Images[0])
	}
	m, _, err = ParseManifest(manifestBody(t, func(m map[string]any) { m["kind"] = "rematch" }), 42, nil)
	if err != nil || m.Kind != ModeRematch {
		t.Errorf("rematch kind should be kept: %q %v", m.Kind, err)
	}
}
