package status

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func exitCodes(codes ...int) []model.Container {
	var containers []model.Container
	for _, code := range codes {
		containers = append(containers, model.Container{Name: "app", ExitCode: exitCode(code)})
	}
	return containers
}

func TestStopKind_Table(t *testing.T) {
	const (
		spotReason   = "Your Spot Task was interrupted."
		deployReason = "Scaling activity initiated by (deployment ecs-svc/1234)"
		healthReason = "Task failed ELB health checks in (target-group web-tg)"
		oomReason    = "OutOfMemoryError: Container killed due to memory usage"
	)
	cases := []struct {
		name      string
		task      model.Task
		wantKind  string
		wantCause string
	}{
		{"not stopped is never classified", model.Task{LastStatus: "RUNNING", StopCode: "SpotInterruption", Containers: exitCodes(1)}, "", ""},
		{"failed to start", model.Task{LastStatus: "STOPPED", StopCode: "TaskFailedToStart"}, "error", ""},
		{"oom reason", model.Task{LastStatus: "STOPPED", StoppedReason: oomReason}, "error", ""},
		{"container exit 137", model.Task{LastStatus: "STOPPED", Containers: exitCodes(0, 137)}, "error", ""},
		{"health checks", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: healthReason, Containers: exitCodes(0)}, "error", ""},
		{"essential container crashed", model.Task{LastStatus: "STOPPED", StopCode: "EssentialContainerExited", Containers: exitCodes(1)}, "error", ""},
		{"essential container exited cleanly", model.Task{LastStatus: "STOPPED", StopCode: "EssentialContainerExited", Containers: exitCodes(0)}, "", ""},
		{"spot interruption with exit 1", model.Task{LastStatus: "STOPPED", StopCode: "SpotInterruption", StoppedReason: spotReason, Containers: exitCodes(1)}, "expected", "spot"},
		{"spot interruption with exit 0", model.Task{LastStatus: "STOPPED", StopCode: "SpotInterruption", StoppedReason: spotReason, Containers: exitCodes(0)}, "", "spot"},
		{"spot interruption without exit code", model.Task{LastStatus: "STOPPED", StopCode: "SpotInterruption"}, "", "spot"},
		{"termination notice", model.Task{LastStatus: "STOPPED", StopCode: "TerminationNotice", Containers: exitCodes(1)}, "expected", "maintenance"},
		{"user via harbormaster", model.Task{LastStatus: "STOPPED", StoppedReason: "Task stopped by pavel via Harbormaster", Containers: exitCodes(143)}, "expected", "user"},
		{"user initiated stop code", model.Task{LastStatus: "STOPPED", StopCode: "UserInitiated", Containers: exitCodes(0)}, "", "user"},
		{"scale in", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: "Scaling activity initiated by (desired count change)", Containers: exitCodes(1)}, "expected", "scale-in"},
		{"deployment replaced with exit 1", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: deployReason, Containers: exitCodes(1)}, "expected", "deploy"},
		{"deployment replaced with exit 0", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: deployReason, Containers: exitCodes(0)}, "", "deploy"},
		{"scaling activity by an auto scaling policy", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: "Scaling activity initiated by (policy cpu-target-tracking)", Containers: exitCodes(1)}, "expected", "scale-in"},
		{"scaling activity by the service itself, clean exit", model.Task{LastStatus: "STOPPED", StoppedReason: "Scaling activity initiated by (service web)", Containers: exitCodes(0)}, "", "scale-in"},
		{"scaling activity deployment without a stop code", model.Task{LastStatus: "STOPPED", StoppedReason: "Scaling activity initiated by (deployment ecs-svc/9)", Containers: exitCodes(1)}, "expected", "deploy"},
		{"scaling activity loses to health checks", model.Task{LastStatus: "STOPPED", StoppedReason: "Scaling activity initiated by (policy p) health checks", Containers: exitCodes(1)}, "error", ""},
		{"task replaced wording", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: "Task replaced by the service scheduler", Containers: exitCodes(1)}, "expected", "deploy"},
		{"scheduler stop with unrelated reason and non-zero exit", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: "Something else", Containers: exitCodes(2)}, "error", ""},
		{"plain non-zero exit", model.Task{LastStatus: "STOPPED", Containers: exitCodes(2)}, "error", ""},
		{"plain clean stop", model.Task{LastStatus: "STOPPED", Containers: exitCodes(0)}, "", ""},
		// precedence
		{"oom wins over spot", model.Task{LastStatus: "STOPPED", StopCode: "SpotInterruption", Containers: exitCodes(137)}, "error", ""},
		{"oom reason wins over deploy", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: deployReason + " " + oomReason}, "error", ""},
		{"health checks win over deploy", model.Task{LastStatus: "STOPPED", StopCode: "ServiceSchedulerInitiated", StoppedReason: deployReason + " " + healthReason, Containers: exitCodes(143)}, "error", ""},
		{"failed to start wins over user", model.Task{LastStatus: "STOPPED", StopCode: "TaskFailedToStart", StoppedReason: "via Harbormaster"}, "error", ""},
		{"user wins over deploy wording", model.Task{LastStatus: "STOPPED", StopCode: "UserInitiated", StoppedReason: deployReason, Containers: exitCodes(1)}, "expected", "user"},
		{"spot wins over scale-in wording", model.Task{LastStatus: "STOPPED", StopCode: "SpotInterruption", StoppedReason: "desired count", Containers: exitCodes(1)}, "expected", "spot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StopKind(tc.task); got != tc.wantKind {
				t.Errorf("StopKind() = %q, want %q", got, tc.wantKind)
			}
			if got := StopCause(tc.task); got != tc.wantCause {
				t.Errorf("StopCause() = %q, want %q", got, tc.wantCause)
			}
		})
	}
}
