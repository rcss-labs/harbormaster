package scanjob

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	maxImportAttempts = 5
	firstRetryDelay   = 5 * time.Minute
	laterRetryDelay   = 30 * time.Minute
)

// retryDelay is the wait before the next attempt of an image that has already used the given number of attempts.
func retryDelay(attemptsUsed int) time.Duration {
	if attemptsUsed < 1 {
		return firstRetryDelay
	}
	return laterRetryDelay
}

// requeue counts a failed attempt for an image still scanning and queues it again after the backoff (the store fails it on
// the third attempt).
func (r *Runner) requeue(ctx context.Context, img store.ScanImage, code, detail string) {
	next := r.now().Add(retryDelay(img.Attempts))
	if err := r.Store.RequeueScanImages(ctx, []string{img.Digest}, next, false, code, detail); err != nil {
		r.log().Error("scan: requeue image", "digest", img.Digest, "err", err)
	}
}

// reconcile imports finished runs, stops runs that ran too long and requeues what no active run covers.
func (r *Runner) reconcile(ctx context.Context) error {
	runs, err := r.Store.ActiveScanRuns(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, run := range runs {
		if err := r.reconcileRun(ctx, run); err != nil {
			r.log().Error("scan: reconcile run", "run", run.ID, "err", err)
			errs = append(errs, err)
		}
	}
	if n, err := r.Store.RequeueScanningWithoutRun(ctx); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		r.log().Warn("scan: requeued images that no active run covers", "images", n)
	}
	return errors.Join(errs...)
}

func (r *Runner) reconcileRun(ctx context.Context, run store.ScanRun) error {
	manifest, err := r.Bucket.Get(ctx, scan.ManifestKey(run.ID), scan.MaxManifestBytes)
	switch {
	case err == nil:
		if err := r.importRun(ctx, run, manifest); err != nil {
			return err
		}
		r.stopTask(ctx, run, "Harbormaster scan run imported")
		return nil
	case errors.Is(err, awsx.ErrObjectTooLarge):
		if err := r.endRun(ctx, run, "failed", scan.CodeTaskFailed, "the scanner's manifest was too large"); err != nil {
			return err
		}
		r.stopTask(ctx, run, "Harbormaster scan manifest too large")
		return nil
	case !errors.Is(err, awsx.ErrObjectNotFound):
		// A 403 for a missing key is normal without s3:ListBucket, so an unreadable manifest is treated as a missing one:
		// the task state and the timeout below still end the run, and the single slot cannot stay wedged.
		r.log().Warn("scan: read manifest", "run", run.ID, "err", err)
	}

	// No manifest yet: the scanner writes it last, so the task is either still working or gone.
	expired := r.now().Sub(run.StartedAt) > r.Cfg.TaskTimeout
	if run.TaskARN == "" {
		if expired {
			return r.endRun(ctx, run, "timed_out", scan.CodeTimeout, "the scanner task was never recorded")
		}
		return nil
	}
	state, err := r.Launcher.DescribeTask(ctx, run.TaskARN)
	switch {
	case errors.Is(err, awsx.ErrTaskNotFound):
		return r.endRun(ctx, run, "failed", scan.CodeTaskMissing, "the scanner task disappeared before it finished")
	case err != nil:
		return err
	case state.LastStatus == "STOPPED":
		detail := "the scanner task stopped without a result"
		if state.ExitCode != nil {
			detail = fmt.Sprintf("%s (exit code %d)", detail, *state.ExitCode)
		}
		if state.StoppedReason != "" {
			detail += ": " + state.StoppedReason
		}
		return r.endRun(ctx, run, "failed", scan.CodeTaskFailed, scan.SanitizeText(detail))
	case expired:
		r.stopTask(ctx, run, "Harbormaster scan task timeout")
		return r.endRun(ctx, run, "timed_out", scan.CodeTimeout, "the scanner task ran longer than the allowed time")
	}
	return nil
}

// stopTask stops the run's task so it cannot keep running once its slot is free; a task ECS no longer knows is fine.
func (r *Runner) stopTask(ctx context.Context, run store.ScanRun, reason string) {
	if run.TaskARN == "" {
		return
	}
	if err := r.Launcher.StopTask(ctx, run.TaskARN, reason); err != nil && !errors.Is(err, awsx.ErrTaskNotFound) {
		r.log().Warn("scan: stop task", "run", run.ID, "err", err)
	}
}

// endRun finishes a run that produced no usable manifest and requeues its images with a counted attempt. A run that timed
// out retries its batch once as large; a large batch that times out again fails.
func (r *Runner) endRun(ctx context.Context, run store.ScanRun, status, code, detail string) error {
	digests, err := r.Store.ScanRunDigests(ctx, run.ID)
	if err != nil {
		return err
	}
	images, err := r.Store.ListScanImages(ctx, digests)
	if err != nil {
		return err
	}
	for _, img := range images {
		if status == "timed_out" {
			r.retryTimedOut(ctx, run, img, detail)
		} else {
			r.requeue(ctx, img, code, detail)
		}
	}
	return r.finishRun(ctx, run.ID, status, store.ScanRunPatch{StopReason: detail, Error: detail, ImagesFailed: len(images)})
}

// finishRun claims the end of a run. When the write fails the run stays active and the next cycle tries again; after
// maxImportAttempts tries it is forced to failed so it cannot hold the slot for good.
func (r *Runner) finishRun(ctx context.Context, id int64, status string, patch store.ScanRunPatch) error {
	_, err := r.Store.FinishScanRun(ctx, id, status, patch)
	if err == nil {
		return nil
	}
	attempts, countErr := r.Store.IncrementImportAttempts(ctx, id)
	if countErr != nil || attempts <= maxImportAttempts {
		return err
	}
	forced := store.ScanRunPatch{Error: "the run's result could not be recorded", ImagesOK: patch.ImagesOK, ImagesFailed: patch.ImagesFailed}
	if _, forceErr := r.Store.FinishScanRun(ctx, id, "failed", forced); forceErr != nil {
		return errors.Join(err, forceErr)
	}
	return err
}
