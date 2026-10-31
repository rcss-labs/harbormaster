package status

import (
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

var alertNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func alertKey() model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111", Region: "eu-west-1", Cluster: "prod"}, Service: "api"}
}

func intp(v int) *int { return &v }

func ago(d time.Duration) *time.Time {
	t := alertNow.Add(-d)
	return &t
}

func stoppedTask(exit int, reason string, age time.Duration) model.Task {
	return model.Task{
		LastStatus:    "STOPPED",
		StoppedAt:     ago(age),
		StoppedReason: reason,
		Containers:    []model.Container{{Name: "app", ExitCode: intp(exit)}},
	}
}

func baseService(desired, running int) model.Service {
	return model.Service{Key: alertKey(), Desired: desired, Running: running}
}

func alertByKind(alerts []model.Alert, kind string) *model.Alert {
	for i := range alerts {
		if alerts[i].Kind == kind {
			return &alerts[i]
		}
	}
	return nil
}

func TestServiceAlerts_Table(t *testing.T) {
	tests := []struct {
		name     string
		in       AlertInput
		kind     string
		severity model.AlertSeverity
		title    string
		text     string
		tab      string
		at       time.Time
	}{
		{
			name:     "failing with last stopped task",
			in:       AlertInput{Service: baseService(2, 0), Tasks: []model.Task{stoppedTask(1, "old", 3*time.Hour), stoppedTask(137, "OutOfMemory", time.Hour)}},
			kind:     "service_failing",
			severity: model.AlertDanger,
			title:    "api: 0 of 2 tasks running",
			text:     "Last task stopped: OutOfMemory (exit 137)",
			tab:      "tasks",
			at:       alertNow.Add(-time.Hour),
		},
		{
			name:     "failing without stopped tasks",
			in:       AlertInput{Service: baseService(2, 0)},
			kind:     "service_failing",
			severity: model.AlertDanger,
			title:    "api: 0 of 2 tasks running",
			text:     "Tasks fail to start.",
			tab:      "tasks",
			at:       alertNow,
		},
		{
			name:     "degraded",
			in:       AlertInput{Service: baseService(4, 3), DegradedPolls: 3},
			kind:     "service_degraded",
			severity: model.AlertWarning,
			title:    "api: 3 of 4 tasks running",
			text:     "ECS is starting replacement tasks.",
			tab:      "overview",
			at:       alertNow,
		},
		{
			name: "restart loop",
			in: AlertInput{Service: baseService(2, 2), Tasks: []model.Task{
				stoppedTask(137, "x", 5*time.Hour), stoppedTask(1, "x", 2*time.Hour), stoppedTask(2, "x", 3*time.Hour)}},
			kind:     "restart_loop",
			severity: model.AlertWarning,
			title:    "api: restart loop",
			text:     "3 tasks stopped with an error in the last 24 h.",
			tab:      "tasks",
			at:       alertNow.Add(-2 * time.Hour),
		},
		{
			name:     "unhealthy single task",
			in:       AlertInput{Service: baseService(2, 2), Tasks: []model.Task{{LastStatus: "RUNNING", HealthStatus: "UNHEALTHY"}}},
			kind:     "unhealthy_task",
			severity: model.AlertWarning,
			title:    "api: 1 unhealthy task",
			text:     "Container health check fails.",
			tab:      "tasks",
			at:       alertNow,
		},
		{
			name: "unhealthy two targets",
			in: AlertInput{Service: baseService(2, 2), TargetHealth: []model.TargetHealth{{Unhealthy: 2, Targets: []model.Target{
				{State: "healthy"}, {State: "unhealthy", Description: "Health checks failed with these codes: [503]"}, {State: "unhealthy", Description: "other"}}}}},
			kind:     "unhealthy_task",
			severity: model.AlertWarning,
			title:    "api: 2 unhealthy tasks",
			text:     "Target group health checks fail: Health checks failed with these codes: [503]",
			tab:      "tasks",
			at:       alertNow,
		},
		{
			name: "deployment failed",
			in: AlertInput{Service: baseService(2, 2), Deployments: []model.Deployment{
				{Status: "PRIMARY", RolloutState: "FAILED", RolloutStateReason: "tasks failed to start", CreatedAt: alertNow.Add(-2 * time.Hour), UpdatedAt: alertNow.Add(-time.Hour)}}},
			kind:     "deployment_failed",
			severity: model.AlertDanger,
			title:    "api: deployment failed",
			text:     "tasks failed to start",
			tab:      "overview",
			at:       alertNow.Add(-time.Hour),
		},
		{
			name: "rollout in progress",
			in: AlertInput{Service: func() model.Service {
				s := baseService(3, 1)
				s.Deployments = []model.Deployment{{Status: "PRIMARY", RolloutState: "IN_PROGRESS", TaskDefinitionARN: "arn:aws:ecs:eu-west-1:111:task-definition/api:42", Running: 1, Desired: 3}}
				return s
			}()},
			kind:     "rollout_in_progress",
			severity: model.AlertInfo,
			title:    "api: rolling out revision 42",
			text:     "1 of 3 new tasks running.",
			tab:      "overview",
			at:       alertNow,
		},
		{
			name: "cannot place",
			in: AlertInput{Service: baseService(2, 2), Events: []model.ServiceEvent{
				{CreatedAt: alertNow.Add(-5 * time.Minute), Message: "(service api) was unable to place a task because no container instance met requirements."}}},
			kind:     "cannot_place",
			severity: model.AlertDanger,
			title:    "api: cannot place tasks",
			text:     "(service api) was unable to place a task because no container instance met requirements.",
			tab:      "overview",
			at:       alertNow.Add(-5 * time.Minute),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = alertNow
			alerts := ServiceAlerts(tc.in)
			got := alertByKind(alerts, tc.kind)
			if got == nil {
				t.Fatalf("no %s alert in %+v", tc.kind, alerts)
			}
			if got.Severity != tc.severity || got.Title != tc.title || got.Text != tc.text || got.Tab != tc.tab || !got.At.Equal(tc.at) {
				t.Errorf("got %+v", *got)
			}
			if got.Key != alertKey() {
				t.Errorf("key = %+v", got.Key)
			}
			count := 0
			for _, a := range alerts {
				if a.Kind == tc.kind {
					count++
				}
			}
			if count != 1 {
				t.Errorf("%d alerts of kind %s", count, tc.kind)
			}
		})
	}
}

func TestServiceAlerts_FailingSuppressedWhileDeploying(t *testing.T) {
	svc := baseService(2, 0)
	svc.Deployments = []model.Deployment{{Status: "PRIMARY", RolloutState: "IN_PROGRESS", TaskDefinitionARN: "x:7"}}
	alerts := ServiceAlerts(AlertInput{Service: svc, Now: alertNow})
	if alertByKind(alerts, "service_failing") != nil {
		t.Errorf("failing alert while deploying: %+v", alerts)
	}
	if alertByKind(alerts, "rollout_in_progress") == nil {
		t.Errorf("expected rollout alert")
	}
}

func TestServiceAlerts_DegradedNeedsThreePolls(t *testing.T) {
	for polls, want := range map[int]bool{2: false, 3: true} {
		alerts := ServiceAlerts(AlertInput{Service: baseService(4, 3), DegradedPolls: polls, Now: alertNow})
		if (alertByKind(alerts, "service_degraded") != nil) != want {
			t.Errorf("polls=%d want alert=%v got %+v", polls, want, alerts)
		}
	}
}

func TestServiceAlerts_RestartLoopCountsOnlyNonZeroExitsIn24h(t *testing.T) {
	tasks := []model.Task{
		stoppedTask(137, "x", time.Hour), stoppedTask(137, "x", 2*time.Hour),
		stoppedTask(0, "x", 3*time.Hour), stoppedTask(1, "x", 48*time.Hour),
	}
	in := AlertInput{Service: baseService(2, 2), Tasks: tasks, Now: alertNow}
	if a := alertByKind(ServiceAlerts(in), "restart_loop"); a != nil {
		t.Fatalf("unexpected alert: %+v", *a)
	}
	in.Tasks = append(in.Tasks, stoppedTask(137, "x", 4*time.Hour))
	a := alertByKind(ServiceAlerts(in), "restart_loop")
	if a == nil || !strings.HasPrefix(a.Text, "3 tasks stopped") {
		t.Fatalf("got %+v", a)
	}
}

func TestServiceAlerts_UnhealthyFromTargetHealthOnly(t *testing.T) {
	in := AlertInput{Service: baseService(2, 2), Now: alertNow,
		Tasks:        []model.Task{{LastStatus: "RUNNING", HealthStatus: "HEALTHY"}},
		TargetHealth: []model.TargetHealth{{Unhealthy: 1, Targets: []model.Target{{State: "unhealthy", Description: "timeout"}}}}}
	a := alertByKind(ServiceAlerts(in), "unhealthy_task")
	if a == nil || a.Title != "api: 1 unhealthy task" || a.Text != "Target group health checks fail: timeout" {
		t.Fatalf("got %+v", a)
	}
}

func TestServiceAlerts_DeploymentFailedOlderThan24hIgnored(t *testing.T) {
	in := AlertInput{Service: baseService(2, 2), Now: alertNow, Deployments: []model.Deployment{
		{RolloutState: "FAILED", CreatedAt: alertNow.Add(-50 * time.Hour), UpdatedAt: alertNow.Add(-25 * time.Hour)}}}
	if alertByKind(ServiceAlerts(in), "deployment_failed") != nil {
		t.Error("stale failed deployment alerted")
	}
	// an older failed deployment superseded by a newer completed one is also ignored
	in.Deployments = []model.Deployment{
		{RolloutState: "FAILED", CreatedAt: alertNow.Add(-3 * time.Hour), UpdatedAt: alertNow.Add(-2 * time.Hour)},
		{RolloutState: "COMPLETED", CreatedAt: alertNow.Add(-time.Hour), UpdatedAt: alertNow.Add(-time.Hour)}}
	if alertByKind(ServiceAlerts(in), "deployment_failed") != nil {
		t.Error("superseded failed deployment alerted")
	}
}

func TestAccountAlerts_OnlyAfterOneMinuteAndOncePerTarget(t *testing.T) {
	names := map[string]string{"111": "prod-account"}
	loops := []LoopState{
		{AccountID: "111", Region: "eu-west-1", Failing: true, LastOKAt: ago(30 * time.Second), LastError: "boom"},
		{AccountID: "111", Region: "us-east-1", Failing: true, LastOKAt: ago(2 * time.Minute), LastError: "AccessDenied"},
		{AccountID: "111", Region: "us-east-1", Failing: true, LastOKAt: ago(5 * time.Minute), LastError: "AccessDenied"},
		{AccountID: "222", Region: "eu-west-1", Failing: true, UpdatedAt: *ago(10 * time.Minute), LastError: "expired"},
		{AccountID: "111", Region: "eu-central-1", Failing: false, LastOKAt: ago(time.Hour)},
	}
	alerts := AccountAlerts(loops, names, alertNow)
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts: %+v", len(alerts), alerts)
	}
	first := alertByKind(alerts, "account_unreachable")
	if first == nil || first.Severity != model.AlertDanger || first.Key.Service != "" || first.Key.Cluster != "" {
		t.Fatalf("bad alert %+v", first)
	}
	titles := map[string]string{}
	for _, a := range alerts {
		titles[a.Title] = a.Text
	}
	if titles["prod-account / us-east-1: unreachable"] != "AccessDenied" {
		t.Errorf("titles = %v", titles)
	}
	if titles["222 / eu-west-1: unreachable"] != "expired" {
		t.Errorf("unnamed account fallback: %v", titles)
	}
}

func TestSortAlerts_SeverityThenNewest(t *testing.T) {
	alerts := []model.Alert{
		{Kind: "i", Severity: model.AlertInfo, At: alertNow},
		{Kind: "w-old", Severity: model.AlertWarning, At: alertNow.Add(-time.Hour)},
		{Kind: "d-old", Severity: model.AlertDanger, At: alertNow.Add(-time.Hour)},
		{Kind: "w-new", Severity: model.AlertWarning, At: alertNow},
		{Kind: "d-new", Severity: model.AlertDanger, At: alertNow},
	}
	SortAlerts(alerts)
	var order []string
	for _, a := range alerts {
		order = append(order, a.Kind)
	}
	if got := strings.Join(order, ","); got != "d-new,d-old,w-new,w-old,i" {
		t.Errorf("order = %s", got)
	}
}
