package timeline

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// IncidentGap is the longest pause between failures that still belongs to the same burst.
const IncidentGap = time.Hour

// TriggerLookback is how far before a burst's first failure a change can still be named as its trigger.
const TriggerLookback = 24 * time.Hour

// Burst is a run of failures with no gap longer than IncidentGap.
type Burst struct {
	First, Last    time.Time
	Count          int
	TopReason      string
	TopReasonCount int
}

// DetectBurst returns the newest burst of failures, so an old incident is never blamed on old changes.
// Failures must be newest first. It returns nil when there are none.
func DetectBurst(failures []store.FailureMark) *Burst {
	if len(failures) == 0 {
		return nil
	}
	burst := &Burst{Last: failures[0].At}
	reasons := map[string]int{}
	previous := failures[0].At
	for _, failure := range failures {
		if previous.Sub(failure.At) > IncidentGap {
			break
		}
		previous = failure.At
		burst.First = failure.At
		burst.Count++
		if failure.Reason != "" {
			reasons[failure.Reason]++
		}
	}
	for reason, count := range reasons {
		// Ties resolve by name so the answer does not flicker between polls.
		if count > burst.TopReasonCount || (count == burst.TopReasonCount && reason < burst.TopReason) {
			burst.TopReason, burst.TopReasonCount = reason, count
		}
	}
	return burst
}

// Incident is a burst with the change most likely behind it.
type Incident struct {
	Burst
	// Trigger is nil when no eligible change falls in the lookback.
	Trigger *Event
	// RollbackRevision is set only when rolling back to it can be offered.
	RollbackRevision *int
	// RolledBack is the newest rollback change after the trigger that returned to the revision before it.
	RolledBack *Event
}

// BuildIncident names the last eligible change before the burst's first failure as its likely trigger.
// Events must be sorted by time ascending, as BuildEvents returns them.
func BuildIncident(b *Burst, events []Event, deployedRevision int) *Incident {
	incident := &Incident{Burst: *b}
	earliest := b.First.Add(-TriggerLookback)
	for i := range events {
		event := events[i]
		if event.Lane == LaneChange && event.Affecting && !event.At.Before(earliest) && event.At.Before(b.First) {
			incident.Trigger = &events[i]
		}
	}
	trigger := incident.Trigger
	if trigger == nil {
		return incident
	}
	if trigger.FromRevision != nil {
		for i := range events {
			event := events[i]
			if event.Lane == LaneChange && event.Op == "rollback" && event.At.After(trigger.At) &&
				event.ToRevision != nil && *event.ToRevision == *trigger.FromRevision {
				incident.RolledBack = &events[i]
			}
		}
	}
	if incident.RolledBack == nil && trigger.Op == "deploy" && trigger.FromRevision != nil &&
		trigger.ToRevision != nil && *trigger.ToRevision == deployedRevision {
		from := *trigger.FromRevision
		incident.RollbackRevision = &from
	}
	return incident
}
