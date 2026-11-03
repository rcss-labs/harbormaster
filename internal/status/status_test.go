package status

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func healthyTask() model.Task {
	return model.Task{HealthStatus: "HEALTHY"}
}

func unhealthyTask() model.Task {
	return model.Task{HealthStatus: "UNHEALTHY"}
}

func TestService_Table(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want model.Status
	}{
		{
			name: "rollout in progress wins over failing",
			in: Inputs{
				Service: model.Service{Desired: 2, Running: 0, Deployments: []model.Deployment{
					{Status: "PRIMARY", RolloutState: "IN_PROGRESS"},
				}},
			},
			want: model.StatusDeploying,
		},
		{
			name: "desired zero is scaled to zero even with tasks running",
			in: Inputs{
				Service:      model.Service{Desired: 0, Running: 1},
				RunningTasks: []model.Task{healthyTask()},
			},
			want: model.StatusScaledToZero,
		},
		{
			name: "desired without running is failing",
			in: Inputs{
				Service: model.Service{Desired: 2, Running: 0},
			},
			want: model.StatusFailing,
		},
		{
			name: "some running but fewer than desired is degraded",
			in: Inputs{
				Service:      model.Service{Desired: 2, Running: 1},
				RunningTasks: []model.Task{healthyTask()},
			},
			want: model.StatusDegraded,
		},
		{
			name: "all running with one unhealthy task is unhealthy task",
			in: Inputs{
				Service:      model.Service{Desired: 2, Running: 2},
				RunningTasks: []model.Task{healthyTask(), unhealthyTask()},
			},
			want: model.StatusUnhealthyTask,
		},
		{
			name: "unhealthy target is unhealthy task",
			in: Inputs{
				Service:         model.Service{Desired: 2, Running: 2},
				RunningTasks:    []model.Task{healthyTask(), healthyTask()},
				TargetUnhealthy: true,
			},
			want: model.StatusUnhealthyTask,
		},
		{
			name: "all healthy and at desired is steady",
			in: Inputs{
				Service:      model.Service{Desired: 2, Running: 2},
				RunningTasks: []model.Task{healthyTask(), healthyTask()},
			},
			want: model.StatusSteady,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Service(tc.in); got != tc.want {
				t.Fatalf("Service() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCluster_Table(t *testing.T) {
	cases := []struct {
		name     string
		statuses []model.Status
		want     model.ClusterStatus
	}{
		{"failing service needs attention", []model.Status{model.StatusSteady, model.StatusFailing}, model.ClusterNeedsAttention},
		{"failing beats degraded", []model.Status{model.StatusDegraded, model.StatusFailing}, model.ClusterNeedsAttention},
		{"degraded service is degraded", []model.Status{model.StatusSteady, model.StatusDegraded}, model.ClusterDegraded},
		{"unhealthy task is degraded", []model.Status{model.StatusUnhealthyTask}, model.ClusterDegraded},
		{"deploying service is deploying", []model.Status{model.StatusSteady, model.StatusDeploying}, model.ClusterDeploying},
		{"scaled to zero and steady are healthy", []model.Status{model.StatusScaledToZero, model.StatusSteady}, model.ClusterHealthy},
		{"empty cluster is healthy", nil, model.ClusterHealthy},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Cluster(tc.statuses); got != tc.want {
				t.Fatalf("Cluster(%v) = %q, want %q", tc.statuses, got, tc.want)
			}
		})
	}
}

func exitCode(code int) *int { return &code }

func TestStopReasonGroup_Table(t *testing.T) {
	cases := []struct {
		name string
		task model.Task
		want string
	}{
		{
			name: "out of memory reason",
			task: model.Task{StoppedReason: "OutOfMemoryError: Container killed due to memory usage"},
			want: "Out of memory (exit 137)",
		},
		{
			name: "container exit 137 without reason text",
			task: model.Task{Containers: []model.Container{{Name: "app", ExitCode: exitCode(137)}}},
			want: "Out of memory (exit 137)",
		},
		{
			name: "load balancer health check failure",
			task: model.Task{StoppedReason: "Task failed ELB health checks in (target-group notifications-tg)"},
			want: "Failed load balancer health checks",
		},
		{
			name: "stopped by a user via harbormaster",
			task: model.Task{StoppedReason: "Task stopped by pavel via Harbormaster"},
			want: "Stopped by a user",
		},
		{
			name: "user initiated stop code",
			task: model.Task{StopCode: "UserInitiated"},
			want: "Stopped by a user",
		},
		{
			name: "scaled in by desired count change",
			task: model.Task{StoppedReason: "Scaling activity initiated by (desired count change)"},
			want: "Scaled in",
		},
		{
			name: "task failed to start",
			task: model.Task{StopCode: "TaskFailedToStart"},
			want: "Failed to start",
		},
		{
			name: "essential container exited with non-zero code",
			task: model.Task{StopCode: "EssentialContainerExited", Containers: []model.Container{{Name: "app", ExitCode: exitCode(1)}}},
			want: "Container exited with an error",
		},
		{
			name: "spot interruption",
			task: model.Task{StopCode: "SpotInterruption", StoppedReason: "Your Spot Task was interrupted."},
			want: "Spot interruption",
		},
		{
			name: "termination notice",
			task: model.Task{StopCode: "TerminationNotice"},
			want: "Spot interruption",
		},
		{
			name: "oom wins over spot",
			task: model.Task{StopCode: "SpotInterruption", Containers: []model.Container{{Name: "app", ExitCode: exitCode(137)}}},
			want: "Out of memory (exit 137)",
		},
		{
			name: "replaced by a deployment",
			task: model.Task{StoppedReason: "Scaling activity initiated by (deployment ecs-svc/1234)"},
			want: "Replaced by a deployment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StopReasonGroup(tc.task); got != tc.want {
				t.Fatalf("StopReasonGroup() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStopReasonGroups_StableOrder(t *testing.T) {
	want := []string{
		"Out of memory (exit 137)",
		"Failed load balancer health checks",
		"Stopped by a user",
		"Scaled in",
		"Failed to start",
		"Container exited with an error",
		"Spot interruption",
		"Replaced by a deployment",
	}
	if len(StopReasonGroups) != len(want) {
		t.Fatalf("StopReasonGroups has %d entries, want %d", len(StopReasonGroups), len(want))
	}
	for index := range want {
		if StopReasonGroups[index] != want[index] {
			t.Errorf("StopReasonGroups[%d] = %q, want %q", index, StopReasonGroups[index], want[index])
		}
	}
}
