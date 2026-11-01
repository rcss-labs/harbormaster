package status

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	alertWindow           = 24 * time.Hour
	cannotPlaceWindow     = 15 * time.Minute
	unreachableAfter      = time.Minute
	restartLoopThreshold  = 3
	degradedPollThreshold = 2 // alert when DegradedPolls is greater than this
)

// AlertInput is everything ServiceAlerts needs for one service.
type AlertInput struct {
	Service       model.Service
	Deployments   []model.Deployment // falls back to Service.Deployments when empty
	Tasks         []model.Task       // running and stopped (7-day history)
	Events        []model.ServiceEvent
	TargetHealth  []model.TargetHealth
	DegradedPolls int // consecutive polls in Degraded
	Now           time.Time
}

// LoopState is the slice of a poll loop's state that account alerts need.
// It mirrors store.PollLoop (which this package cannot import).
type LoopState struct {
	AccountID string
	Region    string
	Failing   bool
	LastOKAt  *time.Time
	UpdatedAt time.Time
	LastError string
}

// ServiceAlerts derives the §6.12 alerts for one service: at most one per kind.
func ServiceAlerts(in AlertInput) []model.Alert {
	svc := in.Service
	if len(in.Deployments) > 0 {
		svc.Deployments = in.Deployments
	}
	name := svc.Key.Service
	now := in.Now
	var running []model.Task
	for _, task := range in.Tasks {
		if task.IsRunning() {
			running = append(running, task)
		}
	}
	targetUnhealthy := 0
	for _, th := range in.TargetHealth {
		targetUnhealthy += th.Unhealthy
	}
	current := Service(Inputs{Service: svc, RunningTasks: running, TargetUnhealthy: targetUnhealthy > 0})

	var alerts []model.Alert
	add := func(kind string, sev model.AlertSeverity, title, text string, at time.Time, tab string) {
		alerts = append(alerts, model.Alert{Key: svc.Key, Kind: kind, Severity: sev, Title: title, Text: text, At: at, Tab: tab})
	}

	switch current {
	case model.StatusFailing:
		text, at := "Tasks fail to start.", now
		if task := newestStoppedWithExit(in.Tasks, now); task != nil {
			text = fmt.Sprintf("Last task stopped: %s (exit %d)", task.StoppedReason, *taskExitCode(*task, name))
			at = *task.StoppedAt
		}
		add("service_failing", model.AlertDanger, fmt.Sprintf("%s: 0 of %d tasks running", name, svc.Desired), text, at, "tasks")
	case model.StatusDegraded:
		if in.DegradedPolls > degradedPollThreshold {
			add("service_degraded", model.AlertWarning, fmt.Sprintf("%s: %d of %d tasks running", name, svc.Running, svc.Desired),
				"ECS is starting replacement tasks.", now, "overview")
		}
	}

	var errored int
	var newestErr time.Time
	for _, task := range in.Tasks {
		if task.StoppedAt == nil || now.Sub(*task.StoppedAt) > alertWindow {
			continue
		}
		if code := task.ExitCode(); code != nil && *code != 0 {
			errored++
			if task.StoppedAt.After(newestErr) {
				newestErr = *task.StoppedAt
			}
		}
	}
	if errored >= restartLoopThreshold {
		add("restart_loop", model.AlertWarning, name+": restart loop",
			fmt.Sprintf("%d tasks stopped with an error in the last 24 h.", errored), newestErr, "tasks")
	}

	unhealthyTasks := 0
	for _, task := range running {
		if task.HealthStatus == "UNHEALTHY" {
			unhealthyTasks++
		}
	}
	if unhealthyTasks > 0 || targetUnhealthy > 0 {
		n := unhealthyTasks
		if targetUnhealthy > n {
			n = targetUnhealthy
		}
		title := fmt.Sprintf("%s: %d unhealthy task", name, n)
		if n > 1 {
			title += "s"
		}
		text := "Container health check fails."
		if desc, ok := firstUnhealthyTarget(in.TargetHealth); ok {
			text = "Target group health checks fail: " + desc
		}
		add("unhealthy_task", model.AlertWarning, title, text, now, "tasks")
	}

	if newest := newestDeployment(svc.Deployments); newest != nil &&
		newest.RolloutState == "FAILED" && now.Sub(newest.UpdatedAt) <= alertWindow {
		add("deployment_failed", model.AlertDanger, name+": deployment failed", newest.RolloutStateReason, newest.UpdatedAt, "overview")
	}

	if current == model.StatusDeploying {
		for _, dep := range svc.Deployments {
			if dep.RolloutState != "IN_PROGRESS" {
				continue
			}
			add("rollout_in_progress", model.AlertInfo, fmt.Sprintf("%s: rolling out revision %d", name, deploymentRevision(dep)),
				fmt.Sprintf("%d of %d new tasks running.", dep.Running, svc.Desired), dep.CreatedAt, "overview")
			break
		}
	}

	var placeEvent *model.ServiceEvent
	for i := range in.Events {
		ev := &in.Events[i]
		if now.Sub(ev.CreatedAt) <= cannotPlaceWindow && strings.Contains(ev.Message, "unable to place a task") &&
			(placeEvent == nil || ev.CreatedAt.After(placeEvent.CreatedAt)) {
			placeEvent = ev
		}
	}
	if placeEvent != nil {
		add("cannot_place", model.AlertDanger, name+": cannot place tasks", placeEvent.Message, placeEvent.CreatedAt, "overview")
	}
	return alerts
}

// AccountAlerts returns one account_unreachable alert per account x region whose
// poll loops are failing and have not succeeded for over a minute. accountNames
// maps AWS account id to display name; the id itself is the fallback.
func AccountAlerts(loops []LoopState, accountNames map[string]string, now time.Time) []model.Alert {
	seen := map[string]bool{}
	var alerts []model.Alert
	for _, loop := range loops {
		if !loop.Failing {
			continue
		}
		since := loop.UpdatedAt
		if loop.LastOKAt != nil {
			since = *loop.LastOKAt
		}
		id := loop.AccountID + "/" + loop.Region
		if now.Sub(since) <= unreachableAfter || seen[id] {
			continue
		}
		seen[id] = true
		display := accountNames[loop.AccountID]
		if display == "" {
			display = loop.AccountID
		}
		alerts = append(alerts, model.Alert{
			Key:      model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: loop.AccountID, Region: loop.Region}},
			Kind:     "account_unreachable",
			Severity: model.AlertDanger,
			Title:    fmt.Sprintf("%s / %s: unreachable", display, loop.Region),
			Text:     loop.LastError,
			At:       since,
		})
	}
	return alerts
}

// SortAlerts orders danger before warning before info, newest first within a severity.
func SortAlerts(alerts []model.Alert) {
	rank := func(sev model.AlertSeverity) int {
		switch sev {
		case model.AlertDanger:
			return 0
		case model.AlertWarning:
			return 1
		case model.AlertInfo:
			return 2
		}
		return 3
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		if ri, rj := rank(alerts[i].Severity), rank(alerts[j].Severity); ri != rj {
			return ri < rj
		}
		return alerts[i].At.After(alerts[j].At)
	})
}

// taskExitCode prefers the container named after the service, else Task.ExitCode.
func taskExitCode(task model.Task, serviceName string) *int {
	for _, container := range task.Containers {
		if container.Name == serviceName && container.ExitCode != nil {
			return container.ExitCode
		}
	}
	return task.ExitCode()
}

func newestStoppedWithExit(tasks []model.Task, now time.Time) *model.Task {
	var best *model.Task
	for i := range tasks {
		task := &tasks[i]
		if task.StoppedAt == nil || now.Sub(*task.StoppedAt) > alertWindow || task.ExitCode() == nil {
			continue
		}
		if best == nil || task.StoppedAt.After(*best.StoppedAt) {
			best = task
		}
	}
	return best
}

func firstUnhealthyTarget(rows []model.TargetHealth) (string, bool) {
	for _, row := range rows {
		for _, target := range row.Targets {
			if target.State == "unhealthy" {
				return target.Description, true
			}
		}
	}
	return "", false
}

func newestDeployment(deps []model.Deployment) *model.Deployment {
	var best *model.Deployment
	for i := range deps {
		if best == nil || deps[i].CreatedAt.After(best.CreatedAt) {
			best = &deps[i]
		}
	}
	return best
}

// deploymentRevision prefers Deployment.Revision, else the ARN's ":N" suffix.
func deploymentRevision(dep model.Deployment) int {
	if dep.Revision > 0 {
		return dep.Revision
	}
	arn := dep.TaskDefinitionARN
	if idx := strings.LastIndex(arn, ":"); idx >= 0 {
		if n, err := strconv.Atoi(arn[idx+1:]); err == nil {
			return n
		}
	}
	return 0
}
