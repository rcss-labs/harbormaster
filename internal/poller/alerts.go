package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// alertEventLimit is how many recent service events feed the cannot_place alert.
const alertEventLimit = 50

// AlertsLoop is the 5 s tier that derives each service's status run and the target's alerts
// from the stored snapshots. It makes no AWS calls.
type AlertsLoop struct {
	Store *store.Store
	Every time.Duration
	Now   func() time.Time

	// consecutive counts polls in the current status per service. It lives in memory so an unchanged status
	// writes nothing; the stored run keeps only the status and when it began. After a restart or failover the
	// count starts again, which delays the degraded alert by a few polls at most.
	mu          sync.Mutex
	consecutive map[model.ServiceKey]consecutivePolls
}

type consecutivePolls struct {
	status model.Status
	polls  int
}

func (l *AlertsLoop) Name() string            { return tierAlerts }
func (l *AlertsLoop) Interval() time.Duration { return l.Every }

// Run recomputes the target's status runs and replaces its alerts. Alerts are written for every
// service regardless of tag visibility; the API filters when it presents them.
func (l *AlertsLoop) Run(ctx context.Context, t awsx.Target) error {
	filter := store.TargetFilter{AccountID: t.AccountID, Region: t.Region}
	services, err := l.Store.ListServices(ctx, filter, "", store.Visibility{Admin: true})
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	taskRows, _, err := l.Store.ListTasks(ctx, store.TaskQuery{TargetFilter: filter, Vis: store.Visibility{Admin: true}, Filter: "all"})
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	tasksByService := map[model.ServiceKey][]model.Task{}
	for _, row := range taskRows {
		key := model.ServiceKey{ClusterKey: row.Cluster, Service: row.ServiceName}
		tasksByService[key] = append(tasksByService[key], row.Task)
	}
	unhealthy, err := l.Store.UnhealthyTargets(ctx, filter)
	if err != nil {
		return err
	}
	runs, err := l.Store.StatusRuns(ctx, t)
	if err != nil {
		return err
	}
	// Two batched reads instead of two per service.
	eventsByService, err := l.Store.RecentServiceEventsPerService(ctx, filter, alertEventLimit)
	if err != nil {
		return fmt.Errorf("service events: %w", err)
	}
	targetHealthByService, err := l.Store.TargetHealthByService(ctx, filter, "")
	if err != nil {
		return err
	}

	now := observeNow(l.Now)
	var alerts []model.Alert
	var changed []store.StatusRun // only new or changed runs are written
	l.mu.Lock()
	defer l.mu.Unlock()
	counts := make(map[model.ServiceKey]consecutivePolls, len(services))
	for _, row := range services {
		svc := row.Service
		tasks := tasksByService[svc.Key]
		var running []model.Task
		for _, task := range tasks {
			if task.IsRunning() {
				running = append(running, task)
			}
		}
		current := status.Service(status.Inputs{Service: svc, RunningTasks: running, TargetUnhealthy: unhealthy[svc.Key]})
		polls := 1
		if seen, ok := l.consecutive[svc.Key]; ok && seen.status == current {
			polls = seen.polls + 1
		}
		counts[svc.Key] = consecutivePolls{status: current, polls: polls}
		if previous, ok := runs[svc.Key]; !ok || previous.Status != current {
			changed = append(changed, store.StatusRun{Key: svc.Key, Status: current, Since: now, Polls: 1})
		}

		degradedPolls := 0
		if current == model.StatusDegraded {
			degradedPolls = polls
		}
		alerts = append(alerts, status.ServiceAlerts(status.AlertInput{
			Service: svc, Deployments: svc.Deployments, Tasks: tasks, Events: eventsByService[svc.Key],
			TargetHealth: targetHealthByService[svc.Key], DegradedPolls: degradedPolls, Now: now,
		})...)
	}

	accountAlerts, err := l.accountAlerts(ctx, t, now)
	if err != nil {
		return err
	}
	alerts = append(alerts, accountAlerts...)

	alerts, err = l.keepStandingTimes(ctx, t, alerts, now)
	if err != nil {
		return err
	}
	if err := l.Store.ReplaceAlertsAndStatusRuns(ctx, t, alerts, changed); err != nil {
		return err
	}
	l.consecutive = counts
	return nil
}

// accountAlerts derives account_unreachable alerts from the target's poll loops.
func (l *AlertsLoop) accountAlerts(ctx context.Context, t awsx.Target, now time.Time) ([]model.Alert, error) {
	loops, err := l.Store.ListPollLoops(ctx)
	if err != nil {
		return nil, fmt.Errorf("list poll loops: %w", err)
	}
	var states []status.LoopState
	for _, loop := range loops {
		if loop.Target != t {
			continue
		}
		states = append(states, status.LoopState{
			AccountID: loop.Target.AccountID, Region: loop.Target.Region, Failing: loop.Failing(),
			LastOKAt: loop.LastOKAt, UpdatedAt: loop.UpdatedAt, LastError: loop.LastError,
		})
	}
	accounts, err := l.Store.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	names := make(map[string]string, len(accounts))
	for _, account := range accounts {
		names[account.AccountID] = account.Name
	}
	return status.AccountAlerts(states, names, now), nil
}

// keepStandingTimes stops alerts that are stamped with the poll time from changing on every cycle: when an
// alert stamped "now" is otherwise identical to the stored one (same severity, title, text and tab), it keeps
// the stored time, i.e. the moment it first appeared. That lets the store skip the write.
func (l *AlertsLoop) keepStandingTimes(ctx context.Context, t awsx.Target, alerts []model.Alert, now time.Time) ([]model.Alert, error) {
	stored, err := l.Store.ListAlerts(ctx, store.AlertFilter{AccountID: t.AccountID, Region: t.Region})
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	type identity struct{ cluster, service, kind string }
	byIdentity := make(map[identity]model.Alert, len(stored))
	for _, alert := range stored {
		byIdentity[identity{alert.Key.Cluster, alert.Key.Service, alert.Kind}] = alert
	}
	for i, alert := range alerts {
		if !alert.At.Equal(now) {
			continue
		}
		previous, ok := byIdentity[identity{alert.Key.Cluster, alert.Key.Service, alert.Kind}]
		if ok && previous.Severity == alert.Severity && previous.Title == alert.Title && previous.Text == alert.Text && previous.Tab == alert.Tab {
			alerts[i].At = previous.At
		}
	}
	return alerts, nil
}
