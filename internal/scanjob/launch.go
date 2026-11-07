package scanjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultBatchSize = 25
	imageTimeout     = 10 * time.Minute
)

func (r *Runner) maxTasks() int {
	if r.Cfg.MaxTasks < 1 {
		return 1
	}
	return r.Cfg.MaxTasks
}

func (r *Runner) batchSize() int {
	if r.Cfg.BatchSize < 1 {
		return defaultBatchSize
	}
	return r.Cfg.BatchSize
}

// launch starts scanner tasks for the images users asked for, as many as the task limit leaves room for. It touches
// nothing when no image is due.
func (r *Runner) launch(ctx context.Context) error {
	active, err := r.Store.ActiveScanRuns(ctx)
	if err != nil {
		return err
	}
	slots := r.maxTasks() - len(active)
	if slots <= 0 {
		return nil
	}
	now := r.now()
	due, err := r.Store.DueScanImages(ctx, now, slots*r.batchSize())
	if err != nil || len(due) == 0 {
		return err
	}

	state, problem, err := r.ensureDB(ctx)
	if err != nil {
		return err
	}
	if band := scan.DBFreshness(state.BuiltAt, now, r.Cfg.DBMaxAge); band == scan.FreshnessNone || band == scan.FreshnessStale {
		return r.holdForDB(ctx, due, state, problem)
	}

	registries, due, err := r.connectedImages(ctx, due)
	if err != nil {
		return err
	}
	var errs []error
	for ; slots > 0 && len(due) > 0; slots-- {
		size := min(r.batchSize(), len(due))
		if err := r.launchBatch(ctx, state, due[:size], registries); err != nil {
			errs = append(errs, err)
		}
		due = due[size:]
	}
	return errors.Join(errs...)
}

// holdForDB handles due images while there is no usable DB: they wait, and any that has waited longer than the DB may be
// old fails, so nothing is scanned days after it was asked for.
func (r *Runner) holdForDB(ctx context.Context, due []store.ScanImage, state store.ScanDBState, problem string) error {
	now := r.now()
	detail := "no vulnerability data is available yet"
	switch {
	case problem != "":
		detail = "vulnerability data is out of date and could not be refreshed: " + problem
	case RefreshInProgress(state, now):
		detail = "vulnerability data is being updated"
	case state.BuiltAt != nil:
		detail = "vulnerability data is older than the allowed age"
	}
	detail = scan.SanitizeText(detail)

	var wait, expired []string
	for _, img := range due {
		if img.QueuedAt != nil && now.Sub(*img.QueuedAt) > r.Cfg.DBMaxAge {
			expired = append(expired, img.Digest)
		} else {
			wait = append(wait, img.Digest)
		}
	}
	var errs []error
	if len(expired) > 0 {
		errs = append(errs, r.Store.FailScanImages(ctx, expired, scan.CodeDBStale, detail))
	}
	if len(wait) > 0 {
		errs = append(errs, r.Store.DeferScanImages(ctx, wait, now.Add(staleRetryDelay), scan.CodeDBStale, detail))
	}
	return errors.Join(errs...)
}

// connectedImages fails the images whose registry account is not connected and returns the scan role details of the
// accounts that are.
func (r *Runner) connectedImages(ctx context.Context, due []store.ScanImage) (map[string]store.AWSAccount, []store.ScanImage, error) {
	accounts := map[string]store.AWSAccount{}
	checked := map[string]bool{}
	var keep []store.ScanImage
	var disconnected []string
	for _, img := range due {
		id := img.RegistryAccountID
		if !checked[id] {
			checked[id] = true
			account, err := r.Store.GetAccountByAccountID(ctx, id)
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				return nil, nil, err
			case account.Enabled:
				accounts[id] = account
			}
		}
		if _, ok := accounts[id]; ok {
			keep = append(keep, img)
		} else {
			disconnected = append(disconnected, img.Digest)
		}
	}
	if len(disconnected) > 0 {
		if err := r.Store.FailScanImages(ctx, disconnected, scan.CodePullDenied, "account not connected"); err != nil {
			return nil, nil, err
		}
	}
	return accounts, keep, nil
}

// launchBatch records one run for the batch, writes its job and starts the task. Any failure after the run exists ends
// the run and requeues the batch with a counted attempt.
func (r *Runner) launchBatch(ctx context.Context, state store.ScanDBState, batch []store.ScanImage, accounts map[string]store.AWSAccount) error {
	now := r.now()
	digests := make([]string, len(batch))
	large := false
	for i, img := range batch {
		digests[i] = img.Digest
		large = large || img.Large
	}
	runID, err := r.Store.CreateScanRun(ctx, store.ScanRun{
		Kind: scan.ModeScan, ClusterARN: r.Cfg.ClusterARN, BatchSize: len(batch), Large: large, StartedAt: now,
		DBBuiltAt: state.BuiltAt, DBChecksum: state.Checksum, ScannerImageDigest: state.ScannerImageDigest}, digests)
	if err != nil {
		return err
	}
	fail := func(cause error) error {
		detail := scan.SanitizeText(cause.Error())
		r.requeueAfterLaunchFailure(ctx, batch, detail)
		// The failure is recorded on the run and the images; the cycle itself did its job.
		r.log().Error("scan: launch failed", "run", runID, "err", cause)
		return r.finishRun(ctx, runID, "failed", store.ScanRunPatch{Error: detail, ImagesFailed: len(batch)})
	}

	if err := r.Store.MarkScanImagesScanning(ctx, digests, runID); err != nil {
		return fail(err)
	}
	body, err := json.Marshal(r.buildJob(runID, state, batch, accounts, now))
	if err != nil {
		return fail(err)
	}
	if err := r.Bucket.Put(ctx, scan.JobKey(runID), body); err != nil {
		return fail(fmt.Errorf("write the job: %w", err))
	}
	taskARN, err := r.Launcher.RunTask(ctx, awsx.LaunchSpec{RunID: runID, Mode: scan.ModeScan, Large: large})
	if err != nil {
		return fail(fmt.Errorf("start the scanner task: %w", err))
	}
	if err := r.Store.SetScanRunTask(ctx, runID, taskARN); err != nil {
		// The task runs; reconcile cannot find it without its ARN, so the timeout path will end this run.
		return fmt.Errorf("record task of run %d: %w", runID, err)
	}
	return nil
}

func (r *Runner) requeueAfterLaunchFailure(ctx context.Context, batch []store.ScanImage, detail string) {
	for _, img := range batch {
		r.requeue(ctx, img, scan.CodeTaskFailed, detail)
	}
}

// buildJob lists the batch and the registry credentials of the batch's accounts only.
func (r *Runner) buildJob(runID int64, state store.ScanDBState, batch []store.ScanImage, accounts map[string]store.AWSAccount, now time.Time) scan.Job {
	job := scan.Job{
		RunID: runID, Kind: scan.ModeScan, Deadline: now.Add(r.Cfg.TaskTimeout),
		Limits: scan.JobLimits{
			ImageTimeoutSeconds: int(imageTimeout / time.Second), TaskBudgetSeconds: int(r.Cfg.TaskTimeout / time.Second),
			MaxImageMB: r.Cfg.MaxImageMB, MaxFindings: scan.MaxFindings},
		DB:         scan.JobDB{ArchiveKey: state.ArchiveKey, Checksum: state.Checksum, MaxAgeHours: int(r.Cfg.DBMaxAge / time.Hour)},
		Registries: map[string]scan.JobRegistry{},
	}
	if state.BuiltAt != nil {
		job.DB.BuiltAt = *state.BuiltAt
	}
	for _, img := range batch {
		id := img.RegistryAccountID
		if _, ok := job.Registries[id]; !ok {
			job.Registries[id] = scan.JobRegistry{
				RoleARN:    fmt.Sprintf("arn:aws:iam::%s:role/%s", id, r.Cfg.RoleName),
				ExternalID: accounts[id].ExternalID, Region: img.RegistryRegion}
		}
		job.Images = append(job.Images, scan.JobImage{
			Digest: img.Digest, ImageRef: img.ImageRef, Tag: img.Tag, Platform: img.Platform,
			AccountID: id, Region: img.RegistryRegion, Large: img.Large})
	}
	sort.Slice(job.Images, func(i, j int) bool { return job.Images[i].Digest < job.Images[j].Digest })
	return job
}
