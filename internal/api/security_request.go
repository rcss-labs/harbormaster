package api

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// errUnsupported means an image cannot be scanned; Message is shown to the user as is.
type errUnsupported struct{ Message string }

func (e errUnsupported) Error() string { return e.Message }

// errResolve means ECR could not say which digest a tag points to (throttling or any other AWS error). Code is the AWS
// error code.
type errResolve struct{ Code string }

func (e errResolve) Error() string { return "could not resolve the image in ECR: " + e.Code }

// resolvedImage is an ECR image identified by digest. Tag may be empty for a reference by digest.
type resolvedImage struct {
	Ref                                scan.Ref
	Repository, Tag, Digest, MediaType string
	Size                               int64
	Platform                           string
}

// resolveImage finds the digest an image reference points to now, through the access role of the registry's account. Only
// Amazon ECR private images in a connected account can be resolved. The key is the requesting service; the registry
// account is the one named by the image reference.
func (s *Server) resolveImage(ctx context.Context, key model.ServiceKey, imageRef, platform string) (resolvedImage, error) {
	ref := scan.ParseImageRef(imageRef)
	if ref.Kind != scan.KindECR {
		return resolvedImage{}, errUnsupported{"Only Amazon ECR private images are scanned in this version."}
	}
	notConnected := errUnsupported{"Connect AWS account " + ref.AccountID + " to scan this image."}
	account, err := s.Store.GetAccountByAccountID(ctx, ref.AccountID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !account.Enabled) {
		return resolvedImage{}, notConnected
	}
	if err != nil {
		return resolvedImage{}, err
	}
	client, err := s.Clients.ECR(ctx, awsx.Target{AccountID: ref.AccountID, Region: ref.Region})
	if errors.Is(err, awsx.ErrAccountUnknown) {
		return resolvedImage{}, notConnected
	}
	if err != nil {
		return resolvedImage{}, errResolve{awsErrorCode(err)}
	}

	identifier, shown := ecrtypes.ImageIdentifier{}, ""
	switch {
	case ref.Digest != "":
		identifier.ImageDigest, shown = aws.String(ref.Digest), ref.Digest
	case ref.Tag != "":
		identifier.ImageTag, shown = aws.String(ref.Tag), ref.Tag
	default: // ECS pulls "latest" for a reference without a tag
		ref.Tag = "latest"
		identifier.ImageTag, shown = aws.String(ref.Tag), ref.Tag
	}
	out, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(ref.Repository),
		ImageIds:       []ecrtypes.ImageIdentifier{identifier},
	})
	var imageMissing *ecrtypes.ImageNotFoundException
	var repositoryMissing *ecrtypes.RepositoryNotFoundException
	switch {
	case errors.As(err, &imageMissing), errors.As(err, &repositoryMissing):
		return resolvedImage{}, errUnsupported{"Tag " + shown + " is not in ECR."}
	case err != nil:
		return resolvedImage{}, errResolve{awsErrorCode(err)}
	case len(out.ImageDetails) == 0:
		return resolvedImage{}, errUnsupported{"Tag " + shown + " is not in ECR."}
	}
	detail := out.ImageDetails[0]
	return resolvedImage{
		Ref: ref, Repository: ref.Repository, Tag: ref.Tag, Digest: aws.ToString(detail.ImageDigest),
		MediaType: aws.ToString(detail.ImageManifestMediaType), Size: aws.ToInt64(detail.ImageSizeInBytes), Platform: platform,
	}, nil
}

// awsErrorCode is the AWS error code of err, or "unknown" for an error that does not come from AWS.
func awsErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() != "" {
		return apiErr.ErrorCode()
	}
	return "unknown"
}

// ensureMode says why a scan is being ensured, which decides what happens to a finished or failed one.
type ensureMode int

const (
	// ensureManual is the "Scan for vulnerabilities" button.
	ensureManual ensureMode = iota
	// ensureRescan is an explicit Rescan: it scans again even when the result is fresh.
	ensureRescan
	// ensureGate is the pre-deploy gate: it never restarts a failed scan.
	ensureGate
)

// ensureScan records that the request's service container wants the image scanned and makes sure a scan is, or will be,
// available. A new image is queued; a scanned one is reused while the data it was matched against is younger than
// DBMaxAge (D18) and queued again when it is older or when the mode is rescan; a failed one is queued again for the
// button and for rescan, and left alone for the gate; a queued or scanning one is joined. The returned row is current.
func (s *Server) ensureScan(ctx context.Context, img resolvedImage, req store.ScanRequest, mode ensureMode) (store.ScanImage, error) {
	now := s.now()
	row := store.ScanImage{
		Digest: img.Digest, Platform: img.Platform, ImageRef: img.Ref.ImageRef(), Tag: img.Tag,
		RegistryKind: scan.KindECR, RegistryAccountID: img.Ref.AccountID, RegistryRegion: img.Ref.Region,
		Repository: img.Repository, RequestedBy: req.RequestedBy,
	}
	stored, created, err := s.Store.UpsertRequestedScanImage(ctx, row, now)
	if err != nil {
		return store.ScanImage{}, err
	}
	// Every requesting service is recorded, whatever happens to the scan, so each one can see the image.
	req.Digest, req.RequestedAt = img.Digest, now
	if _, err := s.Store.AddScanRequest(ctx, req); err != nil {
		return store.ScanImage{}, err
	}

	requeue := false
	switch {
	case created:
		s.kickScan()
		return stored, nil
	case stored.Status == scan.StatusScanned:
		requeue = mode == ensureRescan || s.matchedDataIsStale(stored, now)
	case stored.Status == scan.StatusFailed:
		requeue = mode != ensureGate
	}
	if !requeue {
		return stored, nil
	}
	if _, err := s.Store.RequeueScanImage(ctx, img.Digest, now); err != nil {
		return store.ScanImage{}, err
	}
	s.kickScan()
	return s.Store.GetScanImage(ctx, img.Digest)
}

// matchedDataIsStale reports whether a scanned image was matched against vulnerability data that is now too old to reuse.
func (s *Server) matchedDataIsStale(img store.ScanImage, now time.Time) bool {
	band := scan.DBFreshness(img.MatchedDBBuiltAt, now, s.Cfg.Scan.DBMaxAge)
	return band == scan.FreshnessStale || band == scan.FreshnessNone
}

func (s *Server) kickScan() {
	if s.Scan != nil {
		s.Scan.Kick()
	}
}
