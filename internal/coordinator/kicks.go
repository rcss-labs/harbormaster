package coordinator

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/rcss-labs/harbormaster/internal/signal"
)

// kickJobs maps the payloads web tasks publish to the wake-up of the matching job. A job that is off has no entry.
func (r *Runner) kickJobs() map[string]func() {
	jobs := map[string]func(){}
	if r.Scan != nil {
		jobs["scan"] = r.Scan.Kick
	}
	if r.OffHours != nil {
		jobs["offhours"] = r.OffHours.Kick
	}
	if r.Notify != nil {
		jobs["notify"] = r.Notify.Kick
	}
	if r.Investigate != nil {
		jobs["investigate"] = r.Investigate.Kick
	}
	if r.Review != nil {
		jobs["review"] = r.Review.Kick
	}
	return jobs
}

// listenForKicks wakes a job when a web task asks for it, until ctx ends. A message about a job this runner does not run
// is ignored. After the subscription was re-established every job is woken once, because kicks may have been missed in
// between (the jobs would still run at their intervals; this only keeps a request from waiting for one).
func (r *Runner) listenForKicks(ctx context.Context) {
	jobs := r.kickJobs()
	kick := func(name string) {
		wake, ok := jobs[name]
		if !ok {
			return
		}
		wake()
		if r.OnKick != nil {
			r.OnKick(name)
		}
	}
	onGap := func() {
		for _, name := range slices.Sorted(maps.Keys(jobs)) {
			kick(name)
		}
	}
	for ctx.Err() == nil {
		// The store's client reconnects by itself; this loop only covers a Subscribe that gave up.
		if err := r.Bus.Subscribe(ctx, signal.Kick, kick, onGap); err != nil && ctx.Err() == nil {
			r.log().Debug("coordinator: kick subscription ended", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
