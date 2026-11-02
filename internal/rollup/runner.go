// Package rollup turns raw polled rows into the small daily analytics tables before retention deletes them.
package rollup

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Result maps a daily table name to the rows written by one RunDue.
type Result map[string]int

const (
	// backfillDays is how many complete days a first run reaches back; it matches the default counts-history
	// retention (168h), after which the source rows are gone.
	backfillDays = 6
	// defaultSampleInterval is the counts-history poll interval, used to turn below-desired samples into minutes.
	defaultSampleInterval = 5 * time.Minute
)

// Runner computes the daily rollups that are due. Only complete UTC days are rolled up, and a day that has no
// source rows is left empty rather than invented.
type Runner struct {
	Store   *store.Store
	Clients awsx.Clients
	Now     func() time.Time
	// UtilEnabled turns on the CloudWatch utilisation rollup (HM_ANALYTICS_UTIL).
	UtilEnabled bool
	// SampleInterval is the counts-history interval (HM_POLL_COUNTS_HISTORY); zero means 5 minutes.
	SampleInterval time.Duration
	Log            *slog.Logger
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) sampleInterval() time.Duration {
	if r.SampleInterval > 0 {
		return r.SampleInterval
	}
	return defaultSampleInterval
}

// RunDue computes every rollup that is due and reports the rows written per table. Failures of one rollup do not
// stop the others; they are joined into the returned error. The caller must run this before retention so the
// source rows still exist.
func (r *Runner) RunDue(ctx context.Context) (Result, error) {
	res := Result{}
	today := r.now().Truncate(24 * time.Hour)
	var errs []error
	if err := r.availability(ctx, today, res); err != nil {
		errs = append(errs, err)
	}
	if r.UtilEnabled {
		if err := r.util(ctx, today.AddDate(0, 0, -1), res); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}
