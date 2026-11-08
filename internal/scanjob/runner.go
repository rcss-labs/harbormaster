// Package scanjob is the scan loop that runs on the poller leader: it reconciles finished scanner runs, ages out their
// bucket objects and launches the work users asked for, refreshing the vulnerability DB when a scan needs it.
package scanjob

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const defaultInterval = 15 * time.Second

// DBSource is where the vulnerability DB comes from (Anchore, or the fake).
type DBSource interface {
	// FetchLatest returns the upstream latest.json.
	FetchLatest(ctx context.Context) ([]byte, error)
	// OpenArchive streams the archive that latest.json names by its bare file name.
	OpenArchive(ctx context.Context, path string) (io.ReadCloser, error)
}

// Runner launches and reconciles scanner runs. Run it on the poller leader only.
type Runner struct {
	Store    *store.Store
	Launcher awsx.Launcher
	Bucket   awsx.Bucket
	DB       DBSource
	Cfg      config.ScanConfig
	Now      func() time.Time
	Log      *slog.Logger

	kickOnce sync.Once
	kickCh   chan struct{}

	// Set by tests only.
	archiveCap     int64
	refreshTimeout time.Duration
	afterCycle     func()
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) kicks() chan struct{} {
	r.kickOnce.Do(func() { r.kickCh = make(chan struct{}, 1) })
	return r.kickCh
}

// Kick wakes the loop so a request does not wait for the next interval. It never blocks. It wakes only the loop in this
// process: requests are served by any task but Run runs only in the poller leader, so on another task a request waits at
// most HM_SCAN_INTERVAL (15 s by default).
func (r *Runner) Kick() {
	select {
	case r.kicks() <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run runs a cycle at once and then every Cfg.Interval or Kick, until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	interval := r.Cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Cycle(ctx); err != nil && ctx.Err() == nil {
			r.log().Error("scan: cycle", "err", err)
		}
		if r.afterCycle != nil {
			r.afterCycle()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.kicks():
		}
	}
}

// Cycle reconciles finished runs, ages out old bucket objects and launches due work, in that order. A failing step does
// not stop the next one.
func (r *Runner) Cycle(ctx context.Context) error {
	var errs []error
	for _, step := range []func(context.Context) error{r.reconcile, r.ageOut, r.launch} {
		if err := step(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
