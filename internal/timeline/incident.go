package timeline

import (
	"strings"
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
	// TriggerUnknown is set when the history read for the burst was cut short, so the true trigger or the
	// burst's real start may be missing and no trigger can be named.
	TriggerUnknown bool
}

// MarkTriggerUnknown drops the trigger, rollback offer and rolled-back event, which cannot be trusted
// when the reads behind them were truncated.
func (i *Incident) MarkTriggerUnknown() {
	i.TriggerUnknown = true
	i.Trigger, i.RollbackRevision, i.RolledBack = nil, nil, nil
}

// BuildIncident names the last eligible change before the burst's first failure as its likely trigger.
// Events must be sorted by time ascending, as BuildEvents returns them. It returns nil for a nil burst.
func BuildIncident(b *Burst, events []Event, deployedRevision int) *Incident {
	if b == nil {
		return nil
	}
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
				event.ToRevision != nil && *event.ToRevision == *trigger.FromRevision && rollbackStands(event, events) {
				incident.RolledBack = &events[i]
			}
		}
	}
	// Any rollout after the trigger means deployedRevision may lag behind what the poller has seen, so no offer.
	if incident.RolledBack == nil && trigger.Op == "deploy" && trigger.FromRevision != nil && trigger.ToRevision != nil &&
		*trigger.FromRevision < *trigger.ToRevision && *trigger.ToRevision == deployedRevision &&
		!rolloutAfter(trigger.At, events) {
		from := *trigger.FromRevision
		incident.RollbackRevision = &from
	}
	return incident
}

func isRollout(event Event) bool {
	if event.Lane != LaneChange {
		return false
	}
	switch event.Op {
	case "deploy", "rollback", "restart", "external_deploy":
		return true
	}
	return false
}

func rolloutAfter(at time.Time, events []Event) bool {
	for _, event := range events {
		if event.At.After(at) && isRollout(event) {
			return true
		}
	}
	return false
}

// rollbackStands reports whether the rollback still explains the service's state: no later rollout moved away
// from its revision and no later deployment of that revision failed.
func rollbackStands(rollback Event, events []Event) bool {
	for _, event := range events {
		if !event.At.After(rollback.At) || event.Lane != LaneChange || event.ToRevision == nil ||
			*event.ToRevision == *rollback.ToRevision {
			continue
		}
		if isRollout(event) {
			return false
		}
	}
	for _, event := range events {
		if event.At.After(rollback.At) && event.Lane == LaneChange && strings.HasPrefix(event.ID, "depfail:") &&
			event.ToRevision != nil && *event.ToRevision == *rollback.ToRevision {
			return false
		}
	}
	return true
}
