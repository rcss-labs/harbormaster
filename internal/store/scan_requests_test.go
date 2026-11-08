package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func addRequest(t *testing.T, s *store.Store, digest string, key model.ServiceKey, container string, at time.Time) {
	t.Helper()
	if id, err := s.AddScanRequest(context.Background(), store.ScanRequest{Digest: digest, Key: key, Container: container,
		Image: "repo:v1", Tag: "v1", Source: "manual", RequestedAt: at}); err != nil || id == 0 {
		t.Fatalf("AddScanRequest: id=%d err=%v", id, err)
	}
}

func TestAddScanRequest_UpsertsPerServiceAndContainer(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	key := serviceKey("core-services", "payments-api")
	seedScanImages(t, s, fx.now, "sha256:a")
	later := fx.now.Add(time.Hour)

	addRequest(t, s, "sha256:a", key, "app", fx.now)
	if _, err := s.AddScanRequest(ctx, store.ScanRequest{Digest: "sha256:a", Key: key, Container: "app", Image: "repo:v2", Tag: "v2",
		Source: "pre_deploy", RequestedAt: later}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScanRequestsForDigests(ctx, []string{"sha256:a"})
	if err != nil {
		t.Fatal(err)
	}
	rows := got["sha256:a"]
	if len(rows) != 1 || !rows[0].RequestedAt.Equal(later) || rows[0].Source != "pre_deploy" || rows[0].Tag != "v2" || rows[0].Image != "repo:v2" {
		t.Fatalf("after the same service and container twice: %+v, want one row with the later values", rows)
	}

	addRequest(t, s, "sha256:a", key, "sidecar", fx.now)
	got, err = s.ScanRequestsForDigests(ctx, []string{"sha256:a"})
	if err != nil || len(got["sha256:a"]) != 2 {
		t.Fatalf("rows with a second container = %d err=%v, want 2", len(got["sha256:a"]), err)
	}
	if got["sha256:a"][0].Container != "app" {
		t.Fatalf("newest first: first container = %q, want app", got["sha256:a"][0].Container)
	}

	latest, err := s.LatestScanRequest(ctx, key, "app", "", "sha256:a")
	if err != nil || latest.Tag != "v2" {
		t.Fatalf("LatestScanRequest by digest: %+v err=%v", latest, err)
	}
	byTag, err := s.LatestScanRequest(ctx, key, "app", "v2", "")
	if err != nil || byTag.Digest != "sha256:a" {
		t.Fatalf("LatestScanRequest by tag: %+v err=%v", byTag, err)
	}
	if _, err := s.LatestScanRequest(ctx, key, "app", "v9", ""); err != store.ErrNotFound {
		t.Fatalf("LatestScanRequest for an unknown tag: err=%v, want ErrNotFound", err)
	}
}

func TestVisibleScanImages_FollowsRequestingServices(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	prodSvc, devSvc := serviceKey("core-services", "payments-api"), serviceKey("background-jobs", "image-resizer")
	seedScanImages(t, s, fx.now, "sha256:both", "sha256:prodonly", "sha256:norequest")
	addRequest(t, s, "sha256:both", prodSvc, "app", fx.now)
	addRequest(t, s, "sha256:both", devSvc, "app", fx.now)
	addRequest(t, s, "sha256:prodonly", prodSvc, "app", fx.now)

	digests := func(vis store.Visibility) []string {
		t.Helper()
		imgs, err := s.VisibleScanImages(ctx, store.TargetFilter{}, vis)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, img := range imgs {
			out = append(out, img.Digest)
		}
		return out
	}
	if got := digests(fx.vis(fx.dev)); !equalStrings(got, []string{"sha256:both"}) {
		t.Errorf("Dev-only viewer sees %v, want [sha256:both]", got)
	}
	if got := digests(store.Visibility{Admin: true}); len(got) != 2 {
		t.Errorf("admin sees %v, want both and prodonly but never the unrequested one", got)
	}
	if got := digests(fx.vis(fx.prod)); len(got) != 2 {
		t.Errorf("Prod viewer sees %v, want sha256:both and sha256:prodonly", got)
	}
}

func TestRequestingServices_Distinct(t *testing.T) {
	fx := newReadFixture(t)
	a, b := serviceKey("core-services", "payments-api"), serviceKey("core-services", "notifications")
	seedScanImages(t, fx.s, fx.now, "sha256:a")
	addRequest(t, fx.s, "sha256:a", a, "app", fx.now)
	addRequest(t, fx.s, "sha256:a", a, "sidecar", fx.now)
	addRequest(t, fx.s, "sha256:a", b, "app", fx.now)

	got, err := fx.s.RequestingServices(context.Background(), "sha256:a")
	if err != nil || len(got) != 2 {
		t.Fatalf("RequestingServices = %v err=%v, want 2 distinct services", got, err)
	}
}
