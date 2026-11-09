package scanjob

import (
	"context"
	"errors"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxSkips is how often a scanner may skip an image, or report stale data for it, before the image fails.
const maxSkips = 3

// importRun imports a finished run one image at a time. Everything the scanner wrote is untrusted: only digests of the
// run's own job are taken, and every bucket key is computed here from the run id and digest. Each step is safe to repeat,
// and the run's end is claimed last.
func (r *Runner) importRun(ctx context.Context, run store.ScanRun, manifestData []byte) error {
	digests, err := r.Store.ScanRunDigests(ctx, run.ID)
	if err != nil {
		return err
	}
	manifest, warnings, err := scan.ParseManifest(manifestData, run.ID, digests)
	if err != nil {
		return r.endRun(ctx, run, "failed", scan.CodeTaskFailed, scan.SanitizeText("the scanner's manifest was invalid: "+err.Error()))
	}
	for _, warning := range warnings {
		r.log().Warn("scan: manifest", "run", run.ID, "warning", warning)
	}
	rows, err := r.Store.ListScanImages(ctx, digests)
	if err != nil {
		return err
	}
	images := make(map[string]store.ScanImage, len(rows))
	for _, img := range rows {
		images[img.Digest] = img
	}
	// An image that a newer active run also lists belongs to that run now: this import must not touch it.
	newer, err := r.Store.NewerActiveRunDigests(ctx, run.ID, digests)
	if err != nil {
		return err
	}
	for _, digest := range newer {
		delete(images, digest)
	}

	patch := store.ScanRunPatch{GrypeVersion: manifest.GrypeVersion, SyftVersion: manifest.SyftVersion,
		ScannerImageDigest: manifest.ScannerImageDigest, Error: manifest.Error}
	status := "succeeded"
	if manifest.Status == scan.ManifestDBStale {
		r.deferStaleBatch(ctx, digests, images)
		status, patch.Error = "failed", "the scanner found the vulnerability data too old"
		patch.ImagesFailed = len(digests)
	} else {
		if manifest.Status != scan.ManifestSucceeded {
			status = "failed"
		}
		patch.ImagesOK, patch.ImagesFailed = r.importImages(ctx, run, manifest, digests, images)
	}
	r.recordScannerImage(ctx, manifest.ScannerImageDigest)
	return r.finishRun(ctx, run.ID, status, patch)
}

// deferStaleBatch puts the batch back in the queue because the scanner refused the DB as too old. The leader notes it, so
// the next launch refreshes the DB. An image that keeps coming back this way fails.
func (r *Runner) deferStaleBatch(ctx context.Context, digests []string, images map[string]store.ScanImage) {
	if err := r.noteStale(ctx); err != nil {
		r.log().Error("scan: record stale data", "err", err)
	}
	var scanning []string
	for _, digest := range digests {
		if images[digest].Status == scan.StatusScanning {
			scanning = append(scanning, digest)
		}
	}
	r.bumpAndFail(ctx, scanning, scan.CodeDBStale, "the scanner kept reporting the vulnerability data as too old")
}

func (r *Runner) noteStale(ctx context.Context) error {
	state, err := r.Store.ScanDBState(ctx)
	if err != nil {
		return err
	}
	state.LastRefreshError = staleMarker
	return r.saveDBState(ctx, state)
}

// bumpAndFail defers images due at once, counting the defer; images over the limit fail.
func (r *Runner) bumpAndFail(ctx context.Context, digests []string, code, failDetail string) {
	if len(digests) == 0 {
		return
	}
	over, err := r.Store.BumpScanImageDefers(ctx, digests, r.now(), "", "", maxSkips)
	if err != nil {
		r.log().Error("scan: defer images", "err", err)
		return
	}
	if len(over) == 0 {
		return
	}
	if err := r.Store.FailScanImages(ctx, over, code, failDetail); err != nil {
		r.log().Error("scan: fail images", "err", err)
	}
}

func (r *Runner) recordScannerImage(ctx context.Context, digest string) {
	if digest == "" {
		return
	}
	state, err := r.Store.ScanDBState(ctx)
	if err == nil && state.ScannerImageDigest != digest {
		state.ScannerImageDigest = digest
		err = r.saveDBState(ctx, state)
	}
	if err != nil {
		r.log().Error("scan: record scanner image", "err", err)
	}
}

// importImages applies each digest's outcome and returns how many images were scanned and how many were not.
func (r *Runner) importImages(ctx context.Context, run store.ScanRun, manifest scan.Manifest, digests []string, images map[string]store.ScanImage) (ok, failed int) {
	outcomes := make(map[string]scan.ImageOutcome, len(manifest.Images))
	for _, outcome := range manifest.Images {
		outcomes[outcome.Digest] = outcome
	}
	var skipped []string
	for _, digest := range digests {
		img, known := images[digest]
		if !known {
			continue // handled by a newer run
		}
		// Only an image still scanning is imported. After a repeated import the others already carry their outcome, which
		// is counted so the run's totals stay truthful.
		if img.Status != scan.StatusScanning {
			if img.Status == scan.StatusScanned {
				ok++
			} else {
				failed++
			}
			continue
		}
		outcome, listed := outcomes[digest]
		switch {
		case !listed:
			failed++
			r.requeue(ctx, img, scan.CodeTaskFailed, "the scanner did not report this image")
		case outcome.Status == scan.OutcomeScanned:
			if r.importScanned(ctx, run, manifest, img) {
				ok++
			} else {
				failed++
			}
		case outcome.Status == scan.OutcomeSkipped:
			failed++
			skipped = append(skipped, digest)
		default:
			failed++
			r.handleFailure(ctx, run, img, outcome)
		}
	}
	r.bumpAndFail(ctx, skipped, scan.CodeTaskFailed, "scanner kept skipping this image")
	return ok, failed
}

// handleFailure applies the retry rules to one failed image.
func (r *Runner) handleFailure(ctx context.Context, run store.ScanRun, img store.ScanImage, outcome scan.ImageOutcome) {
	switch outcome.FailureCode {
	case scan.CodePullDenied, scan.CodeImageTooLarge, scan.CodeResultTooLarge:
		r.recordFailure(ctx, run, img, outcome.FailureCode, outcome.FailureDetail)
	case scan.CodeTimeout:
		r.retryTimedOut(ctx, run, img, outcome.FailureDetail)
	case scan.CodeRateLimited, scan.CodeTaskFailed, scan.CodeTaskMissing:
		r.requeue(ctx, img, outcome.FailureCode, outcome.FailureDetail)
	default: // unsupported_registry, db_stale on one image, anything unknown: a failed attempt
		r.requeue(ctx, img, scan.CodeTaskFailed, outcome.FailureDetail)
	}
}

// retryTimedOut queues an image that timed out once more, as large; one that was already large fails.
func (r *Runner) retryTimedOut(ctx context.Context, run store.ScanRun, img store.ScanImage, detail string) {
	if img.Status != scan.StatusScanning {
		return
	}
	if img.Large {
		r.recordFailure(ctx, run, img, scan.CodeTimeout, detail)
		return
	}
	if err := r.Store.RequeueScanImages(ctx, []string{img.Digest}, r.now(), true, scan.CodeTimeout, detail); err != nil {
		r.log().Error("scan: requeue image large", "digest", img.Digest, "err", err)
	}
}

func (r *Runner) recordFailure(ctx context.Context, run store.ScanRun, img store.ScanImage, code, detail string) bool {
	res := store.ScanImageResult{Status: scan.StatusFailed, FailureCode: code, FailureDetail: detail}
	if err := r.Store.RecordScanResult(ctx, img.Digest, run.ID, res, nil, r.now()); err != nil {
		r.log().Error("scan: record failure", "digest", img.Digest, "err", err)
		return false
	}
	return true
}

// importScanned fetches, parses and records one scanned image. The key is computed here. A store or bucket failure is
// tried once more and then recorded as task_failed for the image, so the run still reaches its end.
func (r *Runner) importScanned(ctx context.Context, run store.ScanRun, manifest scan.Manifest, img store.ScanImage) bool {
	body, err := r.getResult(ctx, scan.ResultKey(run.ID, img.Digest))
	switch {
	case errors.Is(err, awsx.ErrObjectTooLarge):
		r.recordFailure(ctx, run, img, scan.CodeResultTooLarge, "the scanner's result was larger than the allowed size")
		return false
	case errors.Is(err, awsx.ErrObjectNotFound):
		r.requeue(ctx, img, scan.CodeTaskFailed, "the scanner reported this image scanned but wrote no result")
		return false
	case err != nil:
		r.recordFailure(ctx, run, img, scan.CodeTaskFailed, "the scan result could not be read")
		return false
	}
	result, err := scan.ParseResult(body, img.Digest)
	if err != nil {
		r.requeue(ctx, img, scan.CodeTaskFailed, scan.SanitizeText("the scanner's result was invalid: "+err.Error()))
		return false
	}

	if err := checkPlatform(img, result); err != nil {
		r.requeue(ctx, img, scan.CodeTaskFailed, scan.SanitizeText("the scanner's result was invalid: "+err.Error()))
		return false
	}

	now := r.now()
	packages := result.Packages
	syftVersion := result.SyftVersion
	if syftVersion == "" {
		syftVersion = manifest.SyftVersion
	}
	recorded := store.ScanImageResult{
		Status: scan.StatusScanned, PlatformDigest: result.PlatformDigest, Platform: result.Platform, SyftVersion: syftVersion,
		ScannerImageDigest: manifest.ScannerImageDigest, ScannedAt: &now, Packages: &packages, Truncated: result.Truncated,
		MatchedDBBuiltAt: run.DBBuiltAt,
		Layers:           result.Layers, Base: result.Base}
	err = r.Store.RecordScanResult(ctx, img.Digest, run.ID, recorded, result.Findings, now)
	if err != nil {
		err = r.Store.RecordScanResult(ctx, img.Digest, run.ID, recorded, result.Findings, now)
	}
	if err != nil {
		r.log().Error("scan: record result", "digest", img.Digest, "err", err)
		r.recordFailure(ctx, run, img, scan.CodeTaskFailed, "the scan result could not be stored")
		return false
	}
	return true
}

// getResult reads one result object, once more after a failure other than "missing" or "too large".
func (r *Runner) getResult(ctx context.Context, key string) ([]byte, error) {
	body, err := r.Bucket.Get(ctx, key, scan.MaxResultBytes)
	if err != nil && !errors.Is(err, awsx.ErrObjectNotFound) && !errors.Is(err, awsx.ErrObjectTooLarge) {
		body, err = r.Bucket.Get(ctx, key, scan.MaxResultBytes)
	}
	return body, err
}

// checkPlatform rejects a result for another platform than the one asked for, and, for a single-manifest image, one whose
// platform digest is not the image's own digest. An image index may name any of its manifests; an unknown media type is
// not checked for that.
func checkPlatform(img store.ScanImage, result scan.Result) error {
	if result.Platform != img.Platform {
		return errors.New("result platform does not match the requested platform")
	}
	if img.ManifestMediaType != "" && !scan.IsImageIndex(img.ManifestMediaType) && result.PlatformDigest != img.Digest {
		return errors.New("result platform digest does not match the image digest")
	}
	return nil
}
