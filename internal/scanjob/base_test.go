package scanjob

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func (fx *fixture) setBase(repository, ref string) {
	fx.t.Helper()
	key := store.RepoKey{AccountID: homelab, Region: "us-east-1", Repository: repository}
	if err := fx.st.SetRepoBase(context.Background(), key, ref, 0, fx.clock.Now()); err != nil {
		fx.t.Fatal(err)
	}
}

func TestRunner_JobCarriesTheRepositoryBaseAndItsECRAccount(t *testing.T) {
	fx := newFixture(t)
	baseRef := staging + ".dkr.ecr.us-east-1.amazonaws.com/base:1"
	fx.setBase(edge, baseRef)
	fx.queue(edge, edgeTag)
	fx.cycle()

	job := fx.job(1)
	if len(job.Images) != 1 || job.Images[0].BaseRef != baseRef {
		t.Fatalf("images = %+v, want the base ref on the image", job.Images)
	}
	base, ok := job.Registries[staging]
	if !ok || base.RoleARN != "arn:aws:iam::"+staging+":role/harbormaster-scan" || base.ExternalID != "ext-stage" || base.Region != "us-east-1" {
		t.Fatalf("registries = %+v, want the base account's scan role added", job.Registries)
	}
	if _, ok := job.Registries[homelab]; !ok {
		t.Fatalf("registries = %+v, want the image account kept", job.Registries)
	}
}

func TestRunner_JobBaseInUnconnectedOrPublicRegistryAddsNoAccount(t *testing.T) {
	for name, ref := range map[string]string{
		"not connected": "999999999999.dkr.ecr.us-east-1.amazonaws.com/base:1",
		"public":        "public.ecr.aws/docker/library/python:3.12",
		"hub":           "node:20",
	} {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t)
			fx.setBase(edge, ref)
			fx.queue(edge, edgeTag)
			fx.cycle()

			job := fx.job(1)
			if job.Images[0].BaseRef != ref {
				t.Fatalf("base ref = %q, want %q", job.Images[0].BaseRef, ref)
			}
			if len(job.Registries) != 1 {
				t.Fatalf("registries = %+v, want only the image account", job.Registries)
			}
		})
	}
}

func TestRunner_ImportStoresLayersAndTheScannedBase(t *testing.T) {
	fx := newFixture(t)
	fx.setBase(edge, "node:20")
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.clock.Advance(time.Minute)
	fx.cycle()

	img := fx.image(digest)
	if img.Status != "scanned" || img.BaseRef != "node:20" || len(img.LayerIDs) == 0 || len(img.BaseLayerIDs) == 0 {
		t.Fatalf("image = %+v, want layers and the scanned base recorded", img)
	}
	if !reflect.DeepEqual(img.BaseLayerIDs, img.LayerIDs[:len(img.BaseLayerIDs)]) {
		t.Fatalf("base layers %v are not a prefix of %v", img.BaseLayerIDs, img.LayerIDs)
	}
	findings, err := fx.st.ScanFindings(context.Background(), digest)
	if err != nil || len(findings) == 0 || findings[0].LayerID == "" {
		t.Fatalf("findings = %+v err %v, want layer ids", findings, err)
	}
}
