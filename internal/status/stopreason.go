package status

import (
	"strings"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// StopReasonGroups lists the stop-reason groups in chart order.
var StopReasonGroups = []string{
	"Out of memory (exit 137)",
	"Failed load balancer health checks",
	"Stopped by a user",
	"Scaled in",
	"Failed to start",
	"Container exited with an error",
	"Replaced by a deployment",
}

// StopReasonGroup buckets a stopped task by why it stopped. Checks run in order and the first match wins.
func StopReasonGroup(task model.Task) string {
	reason := strings.ToLower(task.StoppedReason)
	switch {
	case strings.Contains(reason, "outofmemory") || hasContainerExit(task, 137):
		return StopReasonGroups[0]
	case strings.Contains(reason, "health checks"):
		return StopReasonGroups[1]
	case strings.Contains(reason, "via harbormaster") || task.StopCode == "UserInitiated":
		return StopReasonGroups[2]
	case strings.Contains(reason, "desired count"):
		return StopReasonGroups[3]
	case task.StopCode == "TaskFailedToStart":
		return StopReasonGroups[4]
	case task.StopCode == "EssentialContainerExited" && hasNonZeroExit(task):
		return StopReasonGroups[5]
	default:
		return StopReasonGroups[6]
	}
}

func hasContainerExit(task model.Task, code int) bool {
	for _, container := range task.Containers {
		if container.ExitCode != nil && *container.ExitCode == code {
			return true
		}
	}
	return false
}

func hasNonZeroExit(task model.Task) bool {
	exitCode := task.ExitCode()
	return exitCode != nil && *exitCode != 0
}
