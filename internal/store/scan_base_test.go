package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestRecordScanResult_StoresLayersAndScannedBase(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, "sha256:a")

	res := scannedResult(now)
	res.Layers = []string{"sha256:l1", "sha256:l2"}
	res.Base = &scan.BaseLayers{Ref: "node:20", Digest: "sha256:b", Layers: []string{"sha256:l1"}}
	withLayer := finding("CVE-0000-0001", "libc", "high")
	withLayer.LayerID = "sha256:l1"
	if err := s.RecordScanResult(ctx, "sha256:a", 1, res, []scan.Finding{withLayer, finding("CVE-0000-0002", "zlib", "low")}, now); err != nil {
		t.Fatal(err)
	}

	img := mustGetScanImage(t, s, "sha256:a")
	if !reflect.DeepEqual(img.LayerIDs, res.Layers) || img.BaseRef != "node:20" || img.BaseDigest != "sha256:b" ||
		!reflect.DeepEqual(img.BaseLayerIDs, []string{"sha256:l1"}) || img.BaseError != "" {
		t.Fatalf("stored layers/base: %+v", img)
	}
	got := findingsByVuln(t, s, "sha256:a")
	if got["CVE-0000-0001"].LayerID != "sha256:l1" || got["CVE-0000-0002"].LayerID != "" {
		t.Fatalf("finding layer ids: %+v", got)
	}

	// A later scan without a base clears what the earlier one recorded.
	res.Base, res.Layers = nil, nil
	if err := s.RecordScanResult(ctx, "sha256:a", 2, res, nil, now); err != nil {
		t.Fatal(err)
	}
	img = mustGetScanImage(t, s, "sha256:a")
	if img.BaseRef != "" || img.LayerIDs != nil || img.BaseLayerIDs != nil {
		t.Fatalf("base should be cleared: %+v", img)
	}
}

func TestRepoBase_RoundTripSharedByRepositoryAndCleared(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	api := store.RepoKey{AccountID: "111111111111", Region: "eu-west-1", Repository: "api"}
	other := store.RepoKey{AccountID: "111111111111", Region: "eu-west-1", Repository: "worker"}

	if err := s.SetRepoBase(ctx, api, "node:20", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepoBase(ctx, api, "node:22", 0, now); err != nil {
		t.Fatal(err)
	}
	bases, err := s.RepoBases(ctx, []store.RepoKey{api, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != 1 || bases[api] != "node:22" {
		t.Fatalf("bases = %v, want only api -> node:22", bases)
	}
	if err := s.SetRepoBase(ctx, api, "", 0, now); err != nil {
		t.Fatal(err)
	}
	if bases, err = s.RepoBases(ctx, []store.RepoKey{api}); err != nil || len(bases) != 0 {
		t.Fatalf("cleared bases = %v err %v, want none", bases, err)
	}
}
