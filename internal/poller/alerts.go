package poller

import (
	"context"
	"fmt"
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

	now := observeNow(l.Now)
	var alerts []model.Alert
	updated := make([]store.StatusRun, 0, len(services))
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
		run := store.StatusRun{Key: svc.Key, Status: current, Since: now, Polls: 1}
		if previous, ok := runs[svc.Key]; ok && previous.Status == current {
			run.Since, run.Polls = previous.Since, previous.Polls+1
		}
		updated = append(updated, run)

		events, err := l.Store.ServiceEvents(ctx, svc.Key, alertEventLimit, 0)
		if err != nil {
			return fmt.Errorf("service events: %w", err)
		}
		targetHealth, err := l.Store.TargetHealthForService(ctx, svc.Key)
		if err != nil {
			return err
		}
		degradedPolls := 0
		if current == model.StatusDegraded {
			degradedPolls = run.Polls
		}
		alerts = append(alerts, status.ServiceAlerts(status.AlertInput{
			Service: svc, Deployments: svc.Deployments, Tasks: tasks, Events: events,
			TargetHealth: targetHealth, DegradedPolls: degradedPolls, Now: now,
		})...)
	}

	accountAlerts, err := l.accountAlerts(ctx, t, now)
	if err != nil {
		return err
	}
	alerts = append(alerts, accountAlerts...)

	if err := l.Store.ReplaceAlerts(ctx, t, alerts); err != nil {
		return err
	}
	return l.Store.UpsertStatusRuns(ctx, updated)
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
