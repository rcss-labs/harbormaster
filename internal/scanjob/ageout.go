package scanjob

import (
	"context"
	"errors"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

const (
	objectRetention = 24 * time.Hour
	ageOutBatch     = 100
)

// ageOut deletes the bucket objects of runs that finished more than a day ago. Results live in the database; no object
// belongs to an image.
func (r *Runner) ageOut(ctx context.Context) error {
	ids, err := r.Store.FinishedScanRunIDsBefore(ctx, r.now().Add(-objectRetention), ageOutBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		digests, err := r.Store.ScanRunDigests(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		keys := []string{scan.JobKey(id), scan.ManifestKey(id)}
		for _, digest := range digests {
			keys = append(keys, scan.ResultKey(id, digest))
		}
		deleted := true
		for _, key := range keys {
			if err := r.Bucket.Delete(ctx, key); err != nil {
				errs = append(errs, err)
				deleted = false
				break
			}
		}
		if !deleted {
			continue
		}
		if err := r.Store.MarkScanRunObjectsDeleted(ctx, id, r.now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
