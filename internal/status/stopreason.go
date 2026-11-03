package status

import (
	"strings"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The stop-reason groups by name; callers compare against these rather than indexing StopReasonGroups.
const (
	StopReasonOutOfMemory        = "Out of memory (exit 137)"
	StopReasonHealthChecks       = "Failed load balancer health checks"
	StopReasonStoppedByUser      = "Stopped by a user"
	StopReasonScaledIn           = "Scaled in"
	StopReasonFailedToStart      = "Failed to start"
	StopReasonContainerExited    = "Container exited with an error"
	StopReasonSpotInterruption   = "Spot interruption"
	StopReasonDeploymentReplaced = "Replaced by a deployment"
)

// StopReasonGroups lists the stop-reason groups in chart order.
var StopReasonGroups = []string{
	StopReasonOutOfMemory,
	StopReasonHealthChecks,
	StopReasonStoppedByUser,
	StopReasonScaledIn,
	StopReasonFailedToStart,
	StopReasonContainerExited,
	StopReasonSpotInterruption,
	StopReasonDeploymentReplaced,
}

// StopReasonGroup buckets a stopped task by why it stopped. Checks run in order and the first match wins.
func StopReasonGroup(task model.Task) string {
	reason := strings.ToLower(task.StoppedReason)
	switch {
	case strings.Contains(reason, "outofmemory") || hasContainerExit(task, 137):
		return StopReasonOutOfMemory
	case strings.Contains(reason, "health checks"):
		return StopReasonHealthChecks
	case strings.Contains(reason, "via harbormaster") || task.StopCode == "UserInitiated":
		return StopReasonStoppedByUser
	case strings.Contains(reason, "desired count"):
		return StopReasonScaledIn
	case task.StopCode == "TaskFailedToStart":
		return StopReasonFailedToStart
	case task.StopCode == "EssentialContainerExited" && hasNonZeroExit(task):
		return StopReasonContainerExited
	case task.StopCode == "SpotInterruption" || task.StopCode == "TerminationNotice":
		return StopReasonSpotInterruption
	default:
		return StopReasonDeploymentReplaced
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
