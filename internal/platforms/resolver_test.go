package platforms_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/registry"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	homelab        = "482109375512"
	resizerImage   = homelab + ".dkr.ecr.us-east-1.amazonaws.com/image-resizer:0.4.2"
	catalogImage   = homelab + ".dkr.ecr.eu-west-1.amazonaws.com/catalog-api:4.1.0"
	otelImage      = "public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"
	recheck        = 24 * time.Hour
	registryCalled = "the registry reader was called"
)

type env struct {
	t        *testing.T
	st       *store.Store
	fk       *fake.Fake
	reg      *fake.Registry
	counting *countingReader
	r        *platforms.Resolver
	now      time.Time
	idle     bool
}

// countingReader counts the calls the resolver makes to the public registry reader.
type countingReader struct {
	inner     platforms.PublicReader
	digests   atomic.Int32
	manifests atomic.Int32
	failWith  error
}

func (c *countingReader) Digest(ctx context.Context, ref registry.Ref) (string, error) {
	c.digests.Add(1)
	if c.failWith != nil {
		return "", c.failWith
	}
	return c.inner.Digest(ctx, ref)
}

func (c *countingReader) Manifest(ctx context.Context, ref registry.Ref) ([]byte, string, error) {
	c.manifests.Add(1)
	if c.failWith != nil {
		return nil, "", c.failWith
	}
	return c.inner.Manifest(ctx, ref)
}

// newEnv builds the seeded estate in a test database, polls it once so services and task definitions are stored, and
// returns a resolver over the fake AWS and the fake public registry.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.Open(t)
	ctx := t.Context()
	e := &env{t: t, st: st, now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)}
	if err := devseed.ForceApply(ctx, st); err != nil {
		t.Fatal(err)
	}
	e.fk = fake.New(fake.MockupEstate(func() time.Time { return e.now }))
	for _, account := range e.fk.Estate.Accounts {
		for _, region := range account.Regions {
			if err := poller.PollOnce(ctx, st, e.fk, poller.Config{MaxInFlight: 4}, awsx.Target{AccountID: account.ID, Region: region}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.reg = fake.NewRegistry()
	e.counting = &countingReader{inner: e.reg}
	e.r = &platforms.Resolver{
		Store: st, Clients: e.fk, Public: e.counting, Interval: time.Minute, Recheck: recheck,
		Now: func() time.Time { return e.now }, Idle: func() bool { return e.idle },
	}
	return e
}

func (e *env) cycle() {
	e.t.Helper()
	if err := e.r.RunOnce(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
}

// ref reads the stored state of one image.
func (e *env) ref(image string) store.ImagePlatforms {
	e.t.Helper()
	got, err := e.st.ImagePlatformsFor(e.t.Context(), []string{image})
	if err != nil || len(got) != 1 {
		e.t.Fatalf("image %q = %+v, %v; want it tracked", image, got, err)
	}
	return got[image]
}

func (e *env) nextCheck(image string) time.Time {
	e.t.Helper()
	var next time.Time
	if err := e.st.Pool.QueryRow(e.t.Context(), `select next_check_at from image_refs where image = $1`, image).Scan(&next); err != nil {
		e.t.Fatal(err)
	}
	return next
}

// track adds an image the estate does not use, due now.
func (e *env) track(image string) {
	e.t.Helper()
	if _, err := e.st.Pool.Exec(e.t.Context(),
		`insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at) values ($1, '', '', 'pending', $2, $2)`, image, e.now); err != nil {
		e.t.Fatal(err)
	}
}

func accessDenied() error {
	return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized"}
}

func TestResolver_ECRSingleManifestNeedsNoBatchGetImage(t *testing.T) {
	e := newEnv(t)
	e.cycle()
	got := e.ref(resizerImage)
	if got.Status != "ok" || !got.Known || got.IsIndex || got.Digest == "" {
		t.Fatalf("image = %+v, want ok with a stored single manifest", got)
	}
	if n := e.fk.Calls()["BatchGetImage"]; n != 0 {
		t.Fatalf("BatchGetImage calls = %d, want none: DescribeImages already says it is not an index", n)
	}
}

func TestResolver_ECRIndexStoresPlatforms(t *testing.T) {
	e := newEnv(t)
	e.fk.Estate.SetImageIndex("catalog-api", true)
	e.cycle()
	got := e.ref(catalogImage)
	if got.Status != "ok" || !got.IsIndex || !slices.Contains(got.Platforms, "linux/arm64") || !slices.Contains(got.Platforms, "linux/amd64") {
		t.Fatalf("image = %+v, want an index with amd64 and arm64", got)
	}
	if slices.Contains(got.Platforms, "unknown/unknown") {
		t.Fatalf("platforms = %v, the attestation entry must be left out", got.Platforms)
	}
	calls := e.fk.Calls()["BatchGetImage"]
	if calls != 1 {
		t.Fatalf("BatchGetImage calls = %d, want one for the new index digest", calls)
	}
	e.now = e.now.Add(recheck + 3*time.Hour)
	e.cycle()
	if after := e.fk.Calls()["BatchGetImage"]; after != calls {
		t.Fatalf("BatchGetImage calls = %d after the recheck, want %d: the digest is unchanged and its manifest is stored", after, calls)
	}
	if next := e.nextCheck(catalogImage); next.Before(e.now.Add(recheck)) || next.After(e.now.Add(recheck+recheck/10)) {
		t.Fatalf("next check = %v, want %v plus at most 10 percent", next, e.now.Add(recheck))
	}
}

func TestResolver_TagMovedReadsNewDigest(t *testing.T) {
	e := newEnv(t)
	e.fk.Estate.SetImageIndex("catalog-api", false)
	e.cycle()
	first := e.ref(catalogImage)
	if first.Status != "ok" || slices.Contains(first.Platforms, "linux/arm64") {
		t.Fatalf("first = %+v, want an index without arm64", first)
	}
	e.fk.Estate.SetImageIndex("catalog-api", true) // the tag is pushed again as a multi-arch image
	e.fk.Estate.MoveTag("catalog-api", "4.1.0")
	e.now = e.now.Add(recheck + 3*time.Hour)
	e.cycle()
	second := e.ref(catalogImage)
	if second.Digest == first.Digest || !slices.Contains(second.Platforms, "linux/arm64") {
		t.Fatalf("second = %+v, want a new digest and the new platforms", second)
	}
	if n := e.fk.Calls()["BatchGetImage"]; n != 2 {
		t.Fatalf("BatchGetImage calls = %d, want 2 (one per digest)", n)
	}
}

func TestResolver_AccessDeniedIsNoPermissionWithAccount(t *testing.T) {
	e := newEnv(t)
	e.fk.Estate.SetImageIndex("catalog-api", true)
	e.fk.FailAlways("BatchGetImage", accessDenied())
	e.cycle()
	got := e.ref(catalogImage)
	if got.Status != "no_permission" || got.Detail != homelab {
		t.Fatalf("image = %+v, want no_permission naming the account", got)
	}
	// A single manifest needs no BatchGetImage, so it is unaffected.
	if other := e.ref(resizerImage); other.Status != "ok" {
		t.Fatalf("image-resizer = %+v, want ok", other)
	}
}

func TestResolver_DescribeImagesDeniedIsNoPermission(t *testing.T) {
	e := newEnv(t)
	e.fk.FailAlways("DescribeImages", accessDenied())
	e.cycle()
	if got := e.ref(resizerImage); got.Status != "no_permission" || got.Detail != homelab {
		t.Fatalf("image = %+v, want no_permission", got)
	}
}

func TestResolver_MissingTagIsNotFound(t *testing.T) {
	e := newEnv(t)
	missing := homelab + ".dkr.ecr.us-east-1.amazonaws.com/image-resizer:9.9.9"
	e.track(missing)
	e.cycle()
	if got := e.ref(missing); got.Status != "not_found" {
		t.Fatalf("image = %+v, want not_found", got)
	}
}

func TestResolver_UnconnectedAccountIsNotConnected(t *testing.T) {
	e := newEnv(t)
	other := "999999999999.dkr.ecr.us-east-1.amazonaws.com/app:1"
	e.track(other)
	e.cycle()
	if got := e.ref(other); got.Status != "not_connected" || got.Detail != "999999999999" {
		t.Fatalf("image = %+v, want not_connected naming the account", got)
	}
}

func TestResolver_RefusesHostsOutsideAllowList(t *testing.T) {
	e := newEnv(t)
	images := []string{"quay.io/x/y:1", "10.0.0.5:5000/x:1", "registry.internal/x:1", "localhost/x:1", "169.254.169.254/x:1"}
	for _, image := range images {
		e.track(image)
	}
	e.cycle()
	for _, image := range images {
		if got := e.ref(image); got.Status != "registry_not_checked" {
			t.Errorf("%s = %+v, want registry_not_checked", image, got)
		}
	}
}

func TestResolver_NeverCallsReaderForUnlistedHost(t *testing.T) {
	e := newEnv(t)
	for _, image := range []string{"quay.io/x/y:1", "10.0.0.5:5000/x:1", "registry.internal/x:1"} {
		e.track(image)
	}
	e.cycle()
	for _, host := range e.reg.Hosts() {
		if !slices.Contains(platforms.PublicHosts, host) {
			t.Errorf("%s: the reader was asked about host %q", registryCalled, host)
		}
	}
}

func TestResolver_PublicOffLeavesPublicImagesUnchecked(t *testing.T) {
	e := newEnv(t)
	e.r.Public = nil
	e.cycle()
	if got := e.ref(otelImage); got.Status != "registry_not_checked" {
		t.Fatalf("image = %+v, want registry_not_checked with the public registries off", got)
	}
	if len(e.reg.Hosts()) != 0 {
		t.Fatalf("hosts asked = %v, want none", e.reg.Hosts())
	}
}

func TestResolver_PublicImageIsReadAndStored(t *testing.T) {
	e := newEnv(t)
	e.cycle()
	got := e.ref(otelImage)
	if got.Status != "ok" || !got.Known || !got.IsIndex || !slices.Contains(got.Platforms, "linux/arm64") || got.Registry != "public.ecr.aws" {
		t.Fatalf("image = %+v, want the public multi-arch image stored under its registry", got)
	}
}

func TestResolver_PublicDigestHitSkipsManifest(t *testing.T) {
	e := newEnv(t)
	e.cycle()
	manifests := e.counting.manifests.Load()
	if manifests == 0 {
		t.Fatal("the first cycle never read the manifest")
	}
	e.now = e.now.Add(recheck + 3*time.Hour)
	e.cycle()
	if got := e.counting.manifests.Load(); got != manifests {
		t.Fatalf("manifest reads = %d after the recheck, want %d: the digest was already stored", got, manifests)
	}
	if e.counting.digests.Load() < 2 {
		t.Fatal("the recheck did not ask for the digest")
	}
}

func TestResolver_RateLimitBacksOffOneHour(t *testing.T) {
	e := newEnv(t)
	e.counting.failWith = registry.ErrRateLimited
	e.cycle()
	got := e.ref(otelImage)
	if got.Status != "error" {
		t.Fatalf("image = %+v, want error", got)
	}
	if next := e.nextCheck(otelImage); !next.Equal(e.now.Add(time.Hour)) {
		t.Fatalf("next check = %v, want an hour from now", next)
	}
}

func TestResolver_FailureRetriesHourlyThenFallsBackToRecheck(t *testing.T) {
	e := newEnv(t)
	e.counting.failWith = errors.New("boom")
	e.cycle()
	if next := e.nextCheck(otelImage); !next.Equal(e.now.Add(time.Hour)) {
		t.Fatalf("first failure: next check = %v, want an hour from now", next)
	}
	for i := 0; i < 30; i++ { // a day of failing hourly checks
		e.now = e.now.Add(time.Hour)
		e.cycle()
	}
	// The streak began a day ago, so the hourly retries stopped: the next check is well past another hour.
	if next := e.nextCheck(otelImage); next.Before(e.now.Add(12 * time.Hour)) {
		t.Fatalf("after a day of failures next check = %v (now %v), want the normal recheck interval", next, e.now)
	}
}

func TestResolver_SkipsWhileIdle(t *testing.T) {
	e := newEnv(t)
	e.idle = true
	e.cycle()
	var n int
	if err := e.st.Pool.QueryRow(t.Context(), `select count(*) from image_refs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("image_refs = %d, %v; want nothing tracked while idle", n, err)
	}
	if e.fk.Calls()["DescribeImages"] != 0 {
		t.Fatal("ECR was called while idle")
	}
}

func TestResolver_OneFailureDoesNotStopTheCycle(t *testing.T) {
	e := newEnv(t)
	e.track("999999999999.dkr.ecr.us-east-1.amazonaws.com/app:1")
	e.track("not a valid reference")
	e.fk.FailNext("DescribeImages", errors.New("boom"))
	e.cycle()
	if got := e.ref("not a valid reference"); got.Status != "invalid" {
		t.Fatalf("image = %+v, want invalid", got)
	}
	ok := 0
	for _, image := range []string{resizerImage, catalogImage, otelImage} {
		if e.ref(image).Status == "ok" {
			ok++
		}
	}
	if ok < 2 {
		t.Fatalf("only %d seeded images resolved after one injected failure, want the rest of the cycle to go on", ok)
	}
}
