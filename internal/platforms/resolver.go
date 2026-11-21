// Package platforms resolves which CPU architectures the images of x86 Fargate services support, on the poller leader. It reads
// image manifests from ECR through the access role of each account and from three public registries anonymously, and
// stores only the platform strings it parsed; the Cost API reads those tables and never calls a registry.
package platforms

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/registry"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// PublicReader reads manifests of public registries: the registry client behind the address guard, or a fake.
type PublicReader interface {
	Digest(ctx context.Context, ref registry.Ref) (string, error)
	Manifest(ctx context.Context, ref registry.Ref) ([]byte, string, error)
}

// PublicHosts are the only public registries ever contacted. Every other host is registry_not_checked, whatever the
// address guard would have allowed.
var PublicHosts = []string{"registry-1.docker.io", "public.ecr.aws", "ghcr.io"}

// Per-cycle limits.
const (
	batchSize      = 200
	inFlight       = 4
	refTimeout     = 30 * time.Second
	failureBackoff = time.Hour
	failureWindow  = 24 * time.Hour
	jitterFraction = 10 // percent
)

// Media types of an image index.
const (
	mediaOCIIndex   = "application/vnd.oci.image.index.v1+json"
	mediaDockerList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Resolver keeps image_refs and image_manifests current.
type Resolver struct {
	Store   *store.Store
	Clients awsx.Clients
	// Public reads the public registries; nil leaves them unchecked.
	Public PublicReader
	// Interval is how often a cycle runs; Recheck is how often a known image is resolved again.
	Interval, Recheck time.Duration
	Now               func() time.Time
	// Idle reports whether polling is idle; a cycle then does nothing.
	Idle func() bool
	Log  *slog.Logger
	// OnPublicRead counts a public registry read by host and outcome. Optional.
	OnPublicRead func(host, outcome string)
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Run cycles until ctx ends. Only the poller leader runs it.
func (r *Resolver) Run(ctx context.Context) {
	for {
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.log().Error("image platforms: cycle", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.Interval):
		}
	}
}

// RunOnce tracks new images and resolves the ones that are due. It does nothing while polling is idle. One image's
// failure never stops the cycle.
func (r *Resolver) RunOnce(ctx context.Context) error {
	if r.Idle != nil && r.Idle() {
		return nil
	}
	now := r.now()
	if _, err := r.Store.TrackX86FargateImages(ctx, now); err != nil {
		return err
	}
	due, err := r.Store.DueImageRefs(ctx, now, batchSize)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, inFlight)
	for _, ref := range due {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			refCtx, cancel := context.WithTimeout(ctx, refTimeout)
			defer cancel()
			r.resolve(refCtx, ref, now)
		}()
	}
	wg.Wait()
	return nil
}

// outcome is how one check ended.
type outcome struct {
	status, detail     string
	registry, repo     string
	digest             string
	rateLimited, final bool
}

// resolve checks one image and stores the result. The stored checked_at is when the current status began, so a run of
// failures is told apart from a new image and falls back to the normal interval after a day.
func (r *Resolver) resolve(ctx context.Context, ref store.ImageRef, now time.Time) {
	out := r.check(ctx, ref, now)
	checked := now
	if ref.CheckedAt != nil && ref.Status == out.status && out.status != "ok" {
		checked = *ref.CheckedAt
	}
	next := now.Add(r.Recheck + time.Duration(rand.Int64N(int64(r.Recheck)/jitterFraction+1)))
	if out.status != "ok" && out.status != "registry_not_checked" && out.status != "invalid" {
		if out.rateLimited || now.Sub(checked) < failureWindow {
			next = now.Add(failureBackoff + time.Duration(rand.Int64N(int64(failureBackoff)/jitterFraction+1))) // spread, so a registry outage does not return as one burst
		} else {
			next = now.Add(r.Recheck)
		}
	}
	result := store.ImageRef{Image: ref.Image, Registry: out.registry, Repository: out.repo, Digest: out.digest, Status: out.status, Detail: out.detail, CheckedAt: &checked}
	if err := r.Store.SaveImageRefResult(ctx, result, next); err != nil && ctx.Err() == nil {
		r.log().Error("image platforms: save result", "err", err)
	}
}

func (r *Resolver) check(ctx context.Context, ref store.ImageRef, now time.Time) outcome {
	parsed, err := registry.ParseRef(ref.Image)
	if err != nil {
		return outcome{status: "invalid"}
	}
	if scanRef := scan.ParseImageRef(ref.Image); scanRef.Kind == scan.KindECR {
		return r.checkECR(ctx, ref, parsed, scanRef, now)
	}
	return r.checkPublic(ctx, ref, parsed, now)
}

// checkECR resolves a private ECR image through the access role of the account its reference names. A single manifest is
// stored without further calls; an index needs BatchGetImage unless its digest is already stored.
func (r *Resolver) checkECR(ctx context.Context, ref store.ImageRef, parsed registry.Ref, scanRef scan.Ref, now time.Time) outcome {
	out := outcome{registry: parsed.Host, repo: parsed.Name}
	account := scanRef.AccountID
	connected, err := r.Store.GetAccountByAccountID(ctx, account)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !connected.Enabled) {
		out.status, out.detail = "not_connected", account
		return out
	}
	if err != nil {
		out.status = "error"
		return out
	}
	client, err := r.Clients.ECR(ctx, awsx.Target{AccountID: account, Region: scanRef.Region})
	if errors.Is(err, awsx.ErrAccountUnknown) {
		out.status, out.detail = "not_connected", account
		return out
	}
	if err != nil {
		out.status = "error"
		return out
	}

	identifier := ecrtypes.ImageIdentifier{}
	switch {
	case scanRef.Digest != "":
		identifier.ImageDigest = aws.String(scanRef.Digest)
	case scanRef.Tag != "":
		identifier.ImageTag = aws.String(scanRef.Tag)
	default: // ECS pulls "latest" for a reference without a tag
		identifier.ImageTag = aws.String("latest")
	}
	described, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(scanRef.Repository), ImageIds: []ecrtypes.ImageIdentifier{identifier}})
	var imageMissing *ecrtypes.ImageNotFoundException
	var repositoryMissing *ecrtypes.RepositoryNotFoundException
	switch {
	case errors.As(err, &imageMissing), errors.As(err, &repositoryMissing):
		out.status = "not_found"
		return out
	case awsx.IsAccessDenied(err):
		out.status, out.detail = "no_permission", account
		return out
	case err != nil:
		out.status = "error"
		return out
	case len(described.ImageDetails) == 0:
		out.status = "not_found"
		return out
	}
	detail := described.ImageDetails[0]
	digest, mediaType := aws.ToString(detail.ImageDigest), aws.ToString(detail.ImageManifestMediaType)
	out.digest = digest

	known, err := r.Store.HasImageManifest(ctx, parsed.Host, parsed.Name, digest)
	if err != nil {
		out.status = "error"
		return out
	}
	if known {
		out.status = "ok"
		return out
	}
	if mediaType != mediaOCIIndex && mediaType != mediaDockerList {
		// A single-architecture image on an x86 task is an amd64 image: no manifest call and no config read.
		return r.store(ctx, out, parsed, digest, mediaType, nil, false, now)
	}
	got, err := client.BatchGetImage(ctx, &ecr.BatchGetImageInput{
		RepositoryName: aws.String(scanRef.Repository), ImageIds: []ecrtypes.ImageIdentifier{{ImageDigest: aws.String(digest)}},
		AcceptedMediaTypes: []string{mediaOCIIndex, mediaDockerList},
	})
	switch {
	case awsx.IsAccessDenied(err):
		out.status, out.detail = "no_permission", account
		return out
	case err != nil, len(got.Images) != 1:
		out.status = "error"
		return out
	}
	body := []byte(aws.ToString(got.Images[0].ImageManifest))
	if sha256Digest(body) != digest {
		out.status = "error"
		return out
	}
	list, isIndex, err := registry.Platforms(body, aws.ToString(got.Images[0].ImageManifestMediaType))
	if err != nil || !isIndex {
		out.status = "error"
		return out
	}
	return r.store(ctx, out, parsed, digest, aws.ToString(got.Images[0].ImageManifestMediaType), list, true, now)
}

// checkPublic reads one of the three allow-listed public registries. A host outside the list, or any host while the public
// registries are off, is never contacted.
func (r *Resolver) checkPublic(ctx context.Context, ref store.ImageRef, parsed registry.Ref, now time.Time) outcome {
	out := outcome{registry: parsed.Host, repo: parsed.Name}
	if r.Public == nil || !slices.Contains(PublicHosts, parsed.Host) {
		out.status = "registry_not_checked"
		return out
	}
	digest, err := r.Public.Digest(ctx, parsed)
	if err != nil {
		return r.publicFailure(out, parsed.Host, err)
	}
	if digest != "" {
		out.digest = digest
		if known, err := r.Store.HasImageManifest(ctx, parsed.Host, parsed.Name, digest); err == nil && known {
			r.count(parsed.Host, "ok")
			out.status = "ok"
			return out
		}
	}
	body, mediaType, err := r.Public.Manifest(ctx, parsed)
	if err != nil {
		return r.publicFailure(out, parsed.Host, err)
	}
	actual := sha256Digest(body)
	// A digest the registry reported, or the reference itself when it is one, must match the body that was read.
	if (digest != "" && digest != actual) || (parsed.Reference[:min(len(parsed.Reference), 7)] == "sha256:" && parsed.Reference != actual) {
		r.count(parsed.Host, "error")
		out.status = "error"
		return out
	}
	list, isIndex, err := registry.Platforms(body, mediaType)
	if err != nil {
		r.count(parsed.Host, "error")
		out.status = "error"
		return out
	}
	r.count(parsed.Host, "ok")
	out.digest = actual
	return r.store(ctx, out, parsed, actual, mediaType, list, isIndex, now)
}

func (r *Resolver) publicFailure(out outcome, host string, err error) outcome {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		out.status = "not_found"
		r.count(host, "not_found")
	case errors.Is(err, registry.ErrDenied):
		out.status = "denied"
		r.count(host, "denied")
	case errors.Is(err, registry.ErrRateLimited):
		out.status, out.rateLimited = "error", true
		r.count(host, "rate_limited")
	case errors.Is(err, registry.ErrBlocked):
		out.status = "error"
		r.count(host, "blocked")
	default:
		out.status = "error"
		r.count(host, "error")
	}
	return out
}

func (r *Resolver) count(host, outcome string) {
	if r.OnPublicRead != nil {
		r.OnPublicRead(host, outcome)
	}
}

// store saves the manifest of a digest once and marks the check ok.
func (r *Resolver) store(ctx context.Context, out outcome, parsed registry.Ref, digest, mediaType string, platforms []string, isIndex bool, now time.Time) outcome {
	if err := r.Store.SaveImageManifest(ctx, parsed.Host, parsed.Name, digest, mediaType, isIndex, platforms, now); err != nil {
		out.status = "error"
		return out
	}
	out.digest, out.status = digest, "ok"
	return out
}

func sha256Digest(body []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(body)) }
