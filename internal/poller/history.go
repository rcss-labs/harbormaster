package poller

import (
	"context"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// recordCycle stores one tier cycle's outcome in poll_loops (one row per tier per cycle) and,
// when the services tier flips between ok and failing, updates the account's health.
// A throttle backs the tier off but is not a failure; any other error counts toward Failing.
func (l *Loop) recordCycle(ctx context.Context, tr *tier, err error) {
	now := time.Now().UTC()
	tr.mu.Lock()
	switch {
	case err == nil:
		tr.failures, tr.lastErr, tr.lastOK = 0, "", &now
	case errorsThrottled(err):
		tr.lastErr = err.Error()
	default:
		tr.failures++
		tr.lastErr = err.Error()
	}
	row := store.PollLoop{
		Target: l.target, Tier: tr.name, LastOKAt: tr.lastOK, LastError: tr.lastErr,
		BaseInterval: tr.base, Failures: tr.failures, UpdatedAt: now,
	}
	failing := store.PollLoop{Failures: tr.failures}.Failing()
	flipped := tr.name == tierServices && (!tr.reported || failing != tr.wasFailed)
	if tr.name == tierServices {
		tr.reported, tr.wasFailed = true, failing
	}
	tr.mu.Unlock()

	switch {
	case err == nil:
		tr.onSuccess()
	case errorsThrottled(err):
		tr.onThrottle()
	}
	tr.mu.Lock()
	row.Interval = tr.cur
	tr.mu.Unlock()

	if dbErr := l.p.store.UpsertPollLoop(ctx, row); dbErr != nil && ctx.Err() == nil {
		l.log.Warn("poller: poll loop status", "tier", tr.name, "err", dbErr)
	}
	if flipped {
		var okAt *time.Time
		lastError := ""
		if !failing {
			okAt = row.LastOKAt
		} else {
			lastError = row.LastError
		}
		if dbErr := l.p.store.SetAccountHealth(ctx, l.target.AccountID, okAt, lastError); dbErr != nil && ctx.Err() == nil {
			l.log.Warn("poller: account health", "err", dbErr)
		}
	}
}

// recordCounts samples every service of this loop's target into the counts history.
func (l *Loop) recordCounts(ctx context.Context) {
	l.mu.Lock()
	services := make([]model.Service, 0, len(l.latest))
	for _, s := range l.latest {
		services = append(services, s)
	}
	l.mu.Unlock()
	if err := l.p.store.InsertCountsHistory(ctx, time.Now().UTC(), services); err != nil && ctx.Err() == nil {
		l.log.Warn("poller: counts history", "err", err)
	}
}

// recordDeployments inserts every deployment of the services not seen before by this loop.
// Outcomes and finish times are never written here; plan 02 owns them. The database's
// ON CONFLICT is the backstop when the in-memory set is lost (restart, failover).
func (l *Loop) recordDeployments(ctx context.Context, services []model.Service) {
	for _, s := range services {
		for _, d := range s.Deployments {
			l.mu.Lock()
			seen := l.seenDeployments[d.ID]
			l.mu.Unlock()
			if seen {
				continue
			}
			_, err := l.p.store.InsertDeploymentIfNew(ctx, store.DeploymentRecord{
				Key: s.Key, DeploymentID: d.ID, TaskDefinitionARN: d.TaskDefinitionARN,
				Revision: d.Revision, Kind: "deploy", StartedAt: d.CreatedAt,
			})
			if err != nil {
				if ctx.Err() == nil {
					l.log.Warn("poller: deployment history", "deployment", d.ID, "err", err)
				}
				continue
			}
			l.mu.Lock()
			l.seenDeployments[d.ID] = true
			l.mu.Unlock()
		}
	}
}
