package scanner

import (
	"slices"
	"strings"
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

func TestParseGrypeOutput_ReadsLayerIDAndDropsMalformedOnes(t *testing.T) {
	findings, err := ParseGrypeOutput(testdata(t, "grype_edge_nginx.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "sha256:" + strings.Repeat("1", 64); findings[0].LayerID != want {
		t.Errorf("layer id = %q, want %q", findings[0].LayerID, want)
	}
	if findings[1].LayerID != "" {
		t.Errorf("a finding without locations has layer id %q", findings[1].LayerID)
	}
	if findings[2].LayerID != "" {
		t.Errorf("a malformed layer id was kept: %q", findings[2].LayerID)
	}
}

func TestSBOMLayers_ReadsDiffIDsBottomFirst(t *testing.T) {
	var want []string
	for _, c := range "1234" {
		want = append(want, "sha256:"+strings.Repeat(string(c), 64))
	}
	if got := sbomLayers(testdata(t, "syft_minimal.json")); !slices.Equal(got, want) {
		t.Errorf("layers = %v, want %v", got, want)
	}
}

func TestSBOMLayers_NoneWhenAbsentOrAnyDigestIsMalformed(t *testing.T) {
	// Dropping one layer would shift the rest and break a prefix comparison, so the whole list goes.
	for name, doc := range map[string]string{
		"absent":    `{"source": {"metadata": {}}}`,
		"malformed": `{"source": {"metadata": {"layers": [{"digest": "sha256:` + strings.Repeat("1", 64) + `"}, {"digest": "sha256:XYZ"}]}}}`,
		"not json":  `nope`,
	} {
		if got := sbomLayers([]byte(doc)); len(got) != 0 {
			t.Errorf("%s: layers = %v, want none", name, got)
		}
	}
}

func TestSBOMLayers_CapsAtMaxLayers(t *testing.T) {
	entries := make([]string, scan.MaxLayers+10)
	for i := range entries {
		entries[i] = `{"digest": "sha256:` + strings.Repeat("a", 64) + `"}`
	}
	doc := `{"source": {"metadata": {"layers": [` + strings.Join(entries, ",") + `]}}}`
	if got := sbomLayers([]byte(doc)); len(got) != scan.MaxLayers {
		t.Errorf("layers = %d, want %d", len(got), scan.MaxLayers)
	}
}
