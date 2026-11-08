package scanner

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var sbomDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type syftDocument struct {
	Artifacts []json.RawMessage `json:"artifacts"`
	Source    struct {
		Target struct {
			ManifestDigest string `json:"manifestDigest"`
		} `json:"target"`
	} `json:"source"`
}

func parseSyftDocument(sbom []byte) (syftDocument, error) {
	var doc syftDocument
	if err := json.Unmarshal(sbom, &doc); err != nil {
		return doc, errors.New("syft output is not valid JSON")
	}
	return doc, nil
}

// CountSBOMPackages is the number of artifacts in a syft-json document.
// Grype's report has no package count, so the SBOM is the source.
func CountSBOMPackages(sbom []byte) (int, error) {
	doc, err := parseSyftDocument(sbom)
	if err != nil {
		return 0, err
	}
	return len(doc.Artifacts), nil
}

// sbomManifestDigest is the manifest digest Syft resolved the source to,
// or "" when it is absent or malformed.
func sbomManifestDigest(sbom []byte) string {
	doc, err := parseSyftDocument(sbom)
	if err != nil || !sbomDigestPattern.MatchString(doc.Source.Target.ManifestDigest) {
		return ""
	}
	return doc.Source.Target.ManifestDigest
}

// parseToolVersion reads the "Version: x.y.z" line of `syft version` or `grype version`.
func parseToolVersion(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Version:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
