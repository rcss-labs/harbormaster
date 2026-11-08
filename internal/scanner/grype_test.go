package scanner

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

func TestParseGrypeOutput_MapsFields(t *testing.T) {
	findings, err := ParseGrypeOutput(testdata(t, "grype_edge_nginx.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(findings))
	}
	first := findings[0]
	if first.VulnID != "CVE-0000-0001" || first.Severity != scan.SeverityCritical ||
		first.PackageName != "openssl" || first.PackageVersion != "1.1.1" || first.PackageType != "deb" ||
		first.Location != "/var/lib/dpkg/status" || first.FixedVersion != "1.2.3-1" || first.FixState != "fixed" ||
		first.DataSource != "https://security-tracker.debian.org/tracker/CVE-0000-0001" ||
		first.URL != "https://example.com/CVE-0000-0001" {
		t.Errorf("first finding = %+v", first)
	}
	if first.CVSS == nil || *first.CVSS != 9.8 {
		t.Errorf("cvss = %v, want 9.8", first.CVSS)
	}
	second := findings[1]
	if second.CVSS != nil || second.FixedVersion != "" || second.Location != "" || second.URL != "" || second.FixState != "not-fixed" {
		t.Errorf("second finding = %+v, want empty optional fields", second)
	}
}

func TestParseGrypeOutput_RejectsInvalidJSON(t *testing.T) {
	if _, err := ParseGrypeOutput([]byte("not json")); err == nil {
		t.Error("invalid JSON must be an error")
	}
}

func TestCountSBOMPackages_CountsArtifacts(t *testing.T) {
	n, err := CountSBOMPackages(testdata(t, "syft_minimal.json"))
	if err != nil || n != 3 {
		t.Errorf("count = %d, err = %v, want 3", n, err)
	}
}
