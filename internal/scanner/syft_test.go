package scanner

import "testing"

// Syft 9+ JSON (Syft 1.x) puts the image under source.metadata; source.target is only the pre-v9 shape.
const syftV16Source = `{"artifacts":[],"source":{"type":"image","metadata":{
	"manifestDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111",
	"repoDigests":["123456789012.dkr.ecr.eu-west-1.amazonaws.com/app@sha256:2222222222222222222222222222222222222222222222222222222222222222"]}}}`

func TestSBOMDigests_ReadTheCurrentSyftSchema(t *testing.T) {
	sbom := []byte(syftV16Source)
	if got := sbomManifestDigest(sbom); got != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("manifest digest = %q, want the one under source.metadata", got)
	}
	if !sbomNamesDigest(sbom, "sha256:2222222222222222222222222222222222222222222222222222222222222222") {
		t.Fatal("the repo digest under source.metadata does not name the image")
	}
	if sbomNamesDigest(sbom, "sha256:3333333333333333333333333333333333333333333333333333333333333333") {
		t.Fatal("an unrelated digest names the image")
	}
}

func TestSBOMDigests_StillReadThePreV9Schema(t *testing.T) {
	sbom := []byte(`{"source":{"target":{"manifestDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}}}`)
	if got := sbomManifestDigest(sbom); got != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("manifest digest = %q, want the one under source.target", got)
	}
}
