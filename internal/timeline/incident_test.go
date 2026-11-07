package timeline_test

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/timeline"
)

func failureAt(at time.Time, reason string) store.FailureMark {
	return store.FailureMark{At: at, Reason: reason}
}

func changeEvent(id string, at time.Time, op string, affecting bool, from, to *int) timeline.Event {
	return timeline.Event{ID: id, Lane: timeline.LaneChange, At: at, Op: op, Affecting: affecting, FromRevision: from, ToRevision: to}
}

func TestDetectIncident_NewestBurstOnly(t *testing.T) {
	burst := timeline.DetectBurst([]store.FailureMark{
		failureAt(baseTime.Add(-30*time.Minute), ""),
		failureAt(baseTime.Add(-40*time.Minute), ""),
		failureAt(baseTime.Add(-5*24*time.Hour), ""),
	})
	if burst == nil {
		t.Fatal("burst is nil")
	}
	if !burst.First.Equal(baseTime.Add(-40*time.Minute)) || !burst.Last.Equal(baseTime.Add(-30*time.Minute)) || burst.Count != 2 {
		t.Fatalf("burst = first %v last %v count %d", burst.First, burst.Last, burst.Count)
	}
	if timeline.DetectBurst(nil) != nil {
		t.Fatal("no failures must give no burst")
	}
}

func TestDetectIncident_GapBoundary(t *testing.T) {
	exact := timeline.DetectBurst([]store.FailureMark{
		failureAt(baseTime, ""), failureAt(baseTime.Add(-time.Hour), ""), failureAt(baseTime.Add(-2*time.Hour), ""),
	})
	if exact.Count != 3 {
		t.Fatalf("gaps of exactly 1 h: count = %d, want 3", exact.Count)
	}
	split := timeline.DetectBurst([]store.FailureMark{
		failureAt(baseTime, ""), failureAt(baseTime.Add(-time.Hour-time.Second), ""),
	})
	if split.Count != 1 {
		t.Fatalf("gap of 1 h 1 s: count = %d, want 1", split.Count)
	}
}

func TestDetectIncident_TopReason(t *testing.T) {
	oom := "Out of memory (exit 137)"
	burst := timeline.DetectBurst([]store.FailureMark{
		failureAt(baseTime, ""), failureAt(baseTime.Add(-time.Minute), ""), failureAt(baseTime.Add(-2*time.Minute), ""),
		failureAt(baseTime.Add(-3*time.Minute), oom), failureAt(baseTime.Add(-4*time.Minute), oom),
		failureAt(baseTime.Add(-5*time.Minute), oom), failureAt(baseTime.Add(-6*time.Minute), "Essential container exited"),
	})
	if burst.TopReason != oom || burst.TopReasonCount != 3 {
		t.Fatalf("top reason = %q x%d, want %q x3", burst.TopReason, burst.TopReasonCount, oom)
	}
	onlyEmpty := timeline.DetectBurst([]store.FailureMark{failureAt(baseTime, ""), failureAt(baseTime.Add(-time.Minute), "")})
	if onlyEmpty.TopReason != "" || onlyEmpty.TopReasonCount != 0 {
		t.Fatalf("empty reasons counted: %q x%d", onlyEmpty.TopReason, onlyEmpty.TopReasonCount)
	}
}

func TestBuildIncident_LastEligibleChangeBeforeFirstFailure(t *testing.T) {
	burst := &timeline.Burst{First: baseTime.Add(-49 * time.Minute), Last: baseTime.Add(-40 * time.Minute), Count: 3}
	events := []timeline.Event{
		changeEvent("op:1", baseTime.Add(-60*time.Minute), "deploy", true, ptr(4), ptr(5)),
		changeEvent("op:2", baseTime.Add(-52*time.Minute), "deploy", true, ptr(5), ptr(6)),
		changeEvent("op:3", baseTime.Add(-20*time.Minute), "scale", true, nil, nil),
	}
	incident := timeline.BuildIncident(burst, events, 6)
	if incident.Trigger == nil || incident.Trigger.ID != "op:2" {
		t.Fatalf("trigger = %+v, want op:2", incident.Trigger)
	}
}

func TestBuildIncident_SkipsIneligible(t *testing.T) {
	burst := &timeline.Burst{First: baseTime.Add(-30 * time.Minute), Count: 1}
	events := []timeline.Event{
		changeEvent("op:deploy", baseTime.Add(-50*time.Minute), "deploy", true, ptr(5), ptr(6)),
		changeEvent("op:register", baseTime.Add(-40*time.Minute), "task_definition", false, nil, nil),
		{ID: "term:1", Lane: timeline.LaneAccess, At: baseTime.Add(-35 * time.Minute), Affecting: true},
	}
	incident := timeline.BuildIncident(burst, events, 6)
	if incident.Trigger == nil || incident.Trigger.ID != "op:deploy" {
		t.Fatalf("trigger = %+v, want op:deploy", incident.Trigger)
	}
}

func TestBuildIncident_ChangeAtFirstFailureIsNotBefore(t *testing.T) {
	burst := &timeline.Burst{First: baseTime, Count: 1}
	events := []timeline.Event{changeEvent("op:1", baseTime, "deploy", true, ptr(5), ptr(6))}
	if incident := timeline.BuildIncident(burst, events, 6); incident.Trigger != nil {
		t.Fatalf("a change at the first failure became trigger: %+v", incident.Trigger)
	}
}

func TestBuildIncident_LookbackBound(t *testing.T) {
	burst := &timeline.Burst{First: baseTime, Count: 1}
	tooOld := []timeline.Event{changeEvent("op:1", baseTime.Add(-24*time.Hour-time.Second), "deploy", true, ptr(5), ptr(6))}
	incident := timeline.BuildIncident(burst, tooOld, 6)
	if incident.Trigger != nil || incident.RollbackRevision != nil {
		t.Fatalf("change 24 h 1 s before: trigger %+v rollback %v", incident.Trigger, incident.RollbackRevision)
	}
	exact := []timeline.Event{changeEvent("op:1", baseTime.Add(-24*time.Hour), "deploy", true, ptr(5), ptr(6))}
	if incident := timeline.BuildIncident(burst, exact, 6); incident.Trigger == nil {
		t.Fatal("change exactly 24 h before must count")
	}
}

func TestBuildIncident_RollbackOffered(t *testing.T) {
	burst := &timeline.Burst{First: baseTime, Count: 1}
	events := []timeline.Event{changeEvent("op:1", baseTime.Add(-10*time.Minute), "deploy", true, ptr(5), ptr(6))}
	incident := timeline.BuildIncident(burst, events, 6)
	if incident.RollbackRevision == nil || *incident.RollbackRevision != 5 {
		t.Fatalf("rollback revision = %v, want 5", incident.RollbackRevision)
	}
}

func TestBuildIncident_NoRollbackWhenNotApplicable(t *testing.T) {
	burst := &timeline.Burst{First: baseTime, Count: 1}
	at := baseTime.Add(-10 * time.Minute)
	cases := map[string]struct {
		event    timeline.Event
		deployed int
	}{
		"superseded":          {changeEvent("op:1", at, "deploy", true, ptr(5), ptr(6)), 7},
		"no from revision":    {changeEvent("op:1", at, "deploy", true, nil, ptr(6)), 6},
		"scale":               {changeEvent("op:1", at, "scale", true, ptr(5), ptr(6)), 6},
		"restart":             {changeEvent("op:1", at, "restart", true, ptr(5), ptr(6)), 6},
		"trigger is rollback": {changeEvent("op:1", at, "rollback", true, ptr(6), ptr(5)), 5},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			incident := timeline.BuildIncident(burst, []timeline.Event{tc.event}, tc.deployed)
			if incident.Trigger == nil {
				t.Fatal("trigger is nil")
			}
			if incident.RollbackRevision != nil {
				t.Fatalf("rollback revision = %d, want nil", *incident.RollbackRevision)
			}
		})
	}
}

func TestBuildIncident_RolledBackNamed(t *testing.T) {
	burst := &timeline.Burst{First: baseTime.Add(-20 * time.Minute), Count: 1}
	events := []timeline.Event{
		changeEvent("op:deploy", baseTime.Add(-30*time.Minute), "deploy", true, ptr(5), ptr(6)),
		changeEvent("op:rollback", baseTime.Add(-10*time.Minute), "rollback", true, ptr(6), ptr(5)),
	}
	incident := timeline.BuildIncident(burst, events, 6)
	if incident.RolledBack == nil || incident.RolledBack.ID != "op:rollback" {
		t.Fatalf("rolled back = %+v, want op:rollback", incident.RolledBack)
	}
	if incident.RollbackRevision != nil {
		t.Fatalf("rollback revision = %d, want nil", *incident.RollbackRevision)
	}
}
