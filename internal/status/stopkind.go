package status

import (
	"strings"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Stop kinds. A stop is an error when the workload failed, "expected" when the platform or an operator
// ended it (a non-zero exit there is a side effect of the SIGTERM, not a fault) and "" otherwise.
const (
	StopKindError    = "error"
	StopKindExpected = "expected"
)

// Stop causes, the label shown next to an expected stop.
const (
	StopCauseSpot        = "spot"
	StopCauseMaintenance = "maintenance"
	StopCauseDeploy      = "deploy"
	StopCauseScaling     = "scaling" // "Scaling activity initiated by (deployment <id>)"
	StopCauseScaleIn     = "scale-in"
	StopCauseUser        = "user"
)

// StopKind classifies a STOPPED task as "error", "expected" or "" (normal, or not stopped).
// Rules, first match wins: failed to start, out of memory, failed health checks and a crashed
// essential container are errors; a recognised platform or operator cause makes a non-zero exit "expected";
// any other non-zero exit is an error.
func StopKind(task model.Task) string {
	kind, _ := classifyStop(task)
	return kind
}

// StopCause names why an expected stop happened ("spot", "maintenance", "deploy", "scale-in", "user"),
// or "" when there is no such cause or the stop is an error.
func StopCause(task model.Task) string {
	_, cause := classifyStop(task)
	return cause
}

func classifyStop(task model.Task) (kind, cause string) {
	if task.LastStatus != "STOPPED" {
		return "", ""
	}
	reason := strings.ToLower(task.StoppedReason)
	nonZeroExit := hasNonZeroExit(task)
	switch {
	case task.StopCode == "TaskFailedToStart",
		strings.Contains(reason, "outofmemory"), hasContainerExit(task, 137),
		strings.Contains(reason, "health checks"),
		task.StopCode == "EssentialContainerExited" && nonZeroExit:
		return StopKindError, ""
	}
	switch {
	case task.StopCode == "SpotInterruption":
		cause = StopCauseSpot
	case task.StopCode == "TerminationNotice":
		cause = StopCauseMaintenance
	case strings.Contains(reason, "via harbormaster") || task.StopCode == "UserInitiated":
		cause = StopCauseUser
	case strings.Contains(reason, "desired count"):
		cause = StopCauseScaleIn
	case strings.Contains(reason, "scaling activity initiated by (deployment "): // the id varies per deployment
		cause = StopCauseScaling
	case task.StopCode == "ServiceSchedulerInitiated" && (strings.Contains(reason, "deployment") || strings.Contains(reason, "replaced")):
		cause = StopCauseDeploy
	}
	switch {
	case cause != "" && nonZeroExit:
		return StopKindExpected, cause
	case cause != "":
		return "", cause
	case nonZeroExit:
		return StopKindError, ""
	}
	return "", ""
}
