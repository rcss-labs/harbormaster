package scanner

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

var sbomDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type syftDocument struct {
	Artifacts []json.RawMessage `json:"artifacts"`
	Source    struct {
		// Metadata is where Syft 1.x (schema 9+) describes the image; Target is the older shape.
		Metadata struct {
			ManifestDigest string   `json:"manifestDigest"`
			RepoDigests    []string `json:"repoDigests"`
			Layers         []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		} `json:"metadata"`
		Target struct {
			ManifestDigest string   `json:"manifestDigest"`
			RepoDigests    []string `json:"repoDigests"`
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

// imageIdentity is the manifest digest and repo digests Syft resolved the image to, from whichever schema it wrote.
func (doc syftDocument) imageIdentity() (manifestDigest string, repoDigests []string) {
	if doc.Source.Metadata.ManifestDigest != "" || len(doc.Source.Metadata.RepoDigests) > 0 {
		return doc.Source.Metadata.ManifestDigest, doc.Source.Metadata.RepoDigests
	}
	return doc.Source.Target.ManifestDigest, doc.Source.Target.RepoDigests
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
	if err != nil {
		return ""
	}
	manifestDigest, _ := doc.imageIdentity()
	if !sbomDigestPattern.MatchString(manifestDigest) {
		return ""
	}
	return manifestDigest
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

// sbomNamesDigest reports whether Syft's source is the image the job named:
// one of its repo digests ends in @digest, or its manifest digest is digest.
func sbomNamesDigest(sbom []byte, digest string) bool {
	doc, err := parseSyftDocument(sbom)
	if err != nil {
		return false
	}
	manifestDigest, repoDigests := doc.imageIdentity()
	if manifestDigest == digest {
		return true
	}
	for _, repoDigest := range repoDigests {
		if strings.HasSuffix(repoDigest, "@"+digest) {
			return true
		}
	}
	return false
}

// sbomLayers is the image's layer diff IDs, bottom first, from source.metadata.layers[].digest
// (Syft records the diff ID there). One malformed digest voids the list: skipping it would shift
// the layers above it and corrupt a prefix comparison. At most scan.MaxLayers are kept.
func sbomLayers(sbom []byte) []string {
	doc, err := parseSyftDocument(sbom)
	if err != nil {
		return nil
	}
	layers := make([]string, 0, len(doc.Source.Metadata.Layers))
	for _, layer := range doc.Source.Metadata.Layers {
		if !sbomDigestPattern.MatchString(layer.Digest) {
			return nil
		}
		layers = append(layers, layer.Digest)
	}
	if len(layers) > scan.MaxLayers {
		layers = layers[:scan.MaxLayers]
	}
	return layers
}
