package timeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/timeline"
)

var (
	baseTime = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	testKey  = model.ServiceKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "prod", Service: "payments-api"}
)

func ptr[T any](v T) *T { return &v }

func operation(id int64, at time.Time, actor, text string, details map[string]any) store.OperationRow {
	return store.OperationRow{ID: id, TS: at, Source: "harbormaster", Actor: actor, Text: text, Details: details, Key: testKey}
}

func build(in timeline.Input) []timeline.Event {
	in.Key = testKey
	return timeline.BuildEvents(in)
}

func eventByID(t *testing.T, events []timeline.Event, id string) timeline.Event {
	t.Helper()
	for _, event := range events {
		if event.ID == id {
			return event
		}
	}
	t.Fatalf("no event %q in %+v", id, events)
	return timeline.Event{}
}

func TestBuildEvents_GroupsChangeSetRows(t *testing.T) {
	changeSetRows := func(withDeploy bool) []store.OperationRow {
		shared := func(op string, extra map[string]any) map[string]any {
			details := map[string]any{"op": op, "changeset_id": "cs-1", "reason": "raise capacity for launch"}
			for name, value := range extra {
				details[name] = value
			}
			return details
		}
		rows := []store.OperationRow{
			operation(1, baseTime, "sam", "Registered revision 6.", shared("task_definition", nil)),
			operation(3, baseTime.Add(2*time.Second), "sam", "Changed desired count from 1 to 2.",
				shared("scale", map[string]any{"from_desired": float64(1), "to_desired": float64(2)})),
		}
		if withDeploy {
			rows = append(rows, operation(2, baseTime.Add(time.Second), "sam", "Deployed revision 6.",
				shared("deploy", map[string]any{"deployment_id": "ecs-svc/1", "from_revision": float64(5), "to_revision": float64(6)})))
		}
		return rows
	}

	t.Run("with a deploy row", func(t *testing.T) {
		events := build(timeline.Input{Operations: changeSetRows(true)})
		if len(events) != 1 {
			t.Fatalf("events = %+v, want one grouped event", events)
		}
		event := eventByID(t, events, "changeset:cs-1")
		if event.Title != "Change set applied: revision 6" || event.Actor != "sam" || event.Detail != "raise capacity for launch" {
			t.Fatalf("event = %+v", event)
		}
		if event.Lane != "change" || event.Severity != "change" || !event.Affecting || !event.At.Equal(baseTime) {
			t.Fatalf("event = %+v", event)
		}
		if event.FromRevision == nil || *event.FromRevision != 5 || event.ToRevision == nil || *event.ToRevision != 6 {
			t.Fatalf("revisions = %v -> %v, want 5 -> 6", event.FromRevision, event.ToRevision)
		}
	})
	t.Run("without a deploy row", func(t *testing.T) {
		events := build(timeline.Input{Operations: changeSetRows(false)})
		event := eventByID(t, events, "changeset:cs-1")
		if event.Title != "Change set applied" || event.ToRevision != nil {
			t.Fatalf("event = %+v", event)
		}
	})
}

func TestBuildEvents_RegisterOnlyIsNotAffecting(t *testing.T) {
	events := build(timeline.Input{Operations: []store.OperationRow{
		operation(1, baseTime, "sam", "Registered revision 6.", map[string]any{"op": "task_definition"}),
	}})
	event := eventByID(t, events, "op:1")
	if event.Lane != "change" || event.Affecting {
		t.Fatalf("event = %+v, want a change-lane event that is not affecting", event)
	}
}

func TestBuildEvents_AffectingOperations(t *testing.T) {
	affecting := map[string]bool{
		"deploy": true, "rollback": true, "restart": true, "scale": true, "rollout_settings": true,
		"exec": true, "scaling": true, "task_definition": false, "stop_task": false,
	}
	for op, want := range affecting {
		events := build(timeline.Input{Operations: []store.OperationRow{operation(1, baseTime, "sam", "text", map[string]any{"op": op})}})
		if got := eventByID(t, events, "op:1"); got.Affecting != want || got.Op != op {
			t.Errorf("op %q: Affecting = %v Op = %q, want %v", op, got.Affecting, got.Op, want)
		}
	}
}

func TestBuildEvents_DropsOutcomeAndTagRows(t *testing.T) {
	events := build(timeline.Input{Operations: []store.OperationRow{
		operation(1, baseTime, "sam", "Deployment finished.", map[string]any{"op": "outcome", "deployment_id": "d1"}),
		operation(2, baseTime, "sam", "Tags changed.", nil),
		operation(3, baseTime, "sam", "No op key.", map[string]any{"reason": "x"}),
	}})
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestBuildEvents_ExternalDeployment(t *testing.T) {
	deployments := []store.TimelineDeployment{
		{DeploymentRecord: store.DeploymentRecord{DeploymentID: "ecs-svc/2", Revision: 7, StartedAt: baseTime}},
		{DeploymentRecord: store.DeploymentRecord{DeploymentID: "ecs-svc/1", Revision: 6, StartedAt: baseTime.Add(-time.Hour)}},
	}
	t.Run("no matching operation", func(t *testing.T) {
		events := build(timeline.Input{Deployments: deployments})
		newer := eventByID(t, events, "dep:ecs-svc/2")
		if newer.Title != "Deployment to :7 started outside Harbormaster" || newer.Actor != "ECS" || !newer.Affecting || newer.Op != "external_deploy" {
			t.Fatalf("event = %+v", newer)
		}
		if newer.FromRevision == nil || *newer.FromRevision != 6 || newer.ToRevision == nil || *newer.ToRevision != 7 {
			t.Fatalf("revisions = %v -> %v", newer.FromRevision, newer.ToRevision)
		}
		if oldest := eventByID(t, events, "dep:ecs-svc/1"); oldest.FromRevision != nil {
			t.Fatalf("oldest FromRevision = %v, want nil", *oldest.FromRevision)
		}
	})
	t.Run("matching operation", func(t *testing.T) {
		events := build(timeline.Input{
			Deployments: deployments,
			Operations: []store.OperationRow{operation(9, baseTime, "sam", "Deployed revision 7.",
				map[string]any{"op": "deploy", "deployment_id": "ecs-svc/2", "from_revision": float64(6), "to_revision": float64(7)})},
		})
		for _, event := range events {
			if event.ID == "dep:ecs-svc/2" {
				t.Fatalf("duplicate event for an attributed deployment: %+v", event)
			}
		}
		eventByID(t, events, "op:9")
		eventByID(t, events, "dep:ecs-svc/1")
	})
}

func TestBuildEvents_FailedDeployment(t *testing.T) {
	finished := baseTime.Add(10 * time.Minute)
	events := build(timeline.Input{Deployments: []store.TimelineDeployment{{
		DeploymentRecord: store.DeploymentRecord{DeploymentID: "ecs-svc/2", Revision: 7, StartedAt: baseTime, FinishedAt: &finished, Outcome: "failed"},
		FailureReason:    "Tasks failed to start", FailureDetail: "CannotPullContainerError",
	}}})
	failed := eventByID(t, events, "depfail:ecs-svc/2")
	if failed.Title != "Deployment :7 failed" || failed.Severity != "bad" || !failed.At.Equal(finished) || failed.Lane != "change" {
		t.Fatalf("event = %+v", failed)
	}
	if !strings.Contains(failed.Detail, "Tasks failed to start") || !strings.Contains(failed.Detail, "CannotPullContainerError") {
		t.Fatalf("detail = %q", failed.Detail)
	}
	eventByID(t, events, "dep:ecs-svc/2")
}

func TestBuildEvents_StopSeverity(t *testing.T) {
	const arn = "arn:aws:ecs:us-east-1:111111111111:task/prod/a1f3b2c4d5e6f7a8b9c0d1e2f3a4b5c6"
	stoppedAt := baseTime
	tests := map[string]struct {
		stop         store.TimelineStop
		wantSeverity timeline.Severity
		wantDetail   string
	}{
		"error": {
			stop:         store.TimelineStop{TaskARN: arn, StoppedAt: stoppedAt, Kind: "error", StoppedReason: "Essential container in task exited.", Cause: "ignored", ExitCode: ptr(137)},
			wantSeverity: "bad", wantDetail: "Essential container in task exited. Exit code 137.",
		},
		"expected": {
			stop:         store.TimelineStop{TaskARN: arn, StoppedAt: stoppedAt, Kind: "expected", StoppedReason: "raw", Cause: "Replaced by a new deployment."},
			wantSeverity: "info", wantDetail: "Replaced by a new deployment.",
		},
		"normal": {
			stop:         store.TimelineStop{TaskARN: arn, StoppedAt: stoppedAt, StoppedReason: "Scaling activity initiated by deployment", Cause: "Scaled in."},
			wantSeverity: "info", wantDetail: "Scaled in.",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			events := build(timeline.Input{Stops: []store.TimelineStop{test.stop}})
			event := eventByID(t, events, "stop:a1f3b2c4d5e6f7a8b9c0d1e2f3a4b5c6")
			if event.Title != "Task a1f3b2c4 stopped" || event.Lane != "stop" || event.Actor != "ECS" || event.Severity != test.wantSeverity ||
				event.Detail != test.wantDetail || event.TaskID != "a1f3b2c4d5e6f7a8b9c0d1e2f3a4b5c6" || event.Affecting {
				t.Fatalf("event = %+v", event)
			}
		})
	}
}

func TestBuildEvents_TerminalSessionJoined(t *testing.T) {
	const arn = "arn:aws:ecs:us-east-1:111111111111:task/prod/abc123"
	open := func(id int64, at time.Time, session string) store.OperationRow {
		return operation(id, at, "sam", "Opened terminal session.", map[string]any{
			"op": "terminal", "task_arn": arn, "container": "app", "command": "/bin/sh", "session_id": session})
	}
	closed := func(id int64, at time.Time, session string) store.OperationRow {
		return operation(id, at, "sam", "Closed terminal session.", map[string]any{
			"op": "terminal_close", "session_id": session, "duration_ms": float64(4 * time.Minute / time.Millisecond), "reason": "client"})
	}

	t.Run("open and close", func(t *testing.T) {
		events := build(timeline.Input{Operations: []store.OperationRow{open(1, baseTime, "s1"), closed(2, baseTime.Add(4*time.Minute), "s1")}})
		if len(events) != 1 {
			t.Fatalf("events = %+v, want one joined event", events)
		}
		event := eventByID(t, events, "term:s1")
		if event.Lane != "access" || event.Actor != "sam" || !event.At.Equal(baseTime) || event.Affecting ||
			event.Detail != "Task abc123, container app, /bin/sh. Lasted 4 minutes." {
			t.Fatalf("event = %+v", event)
		}
	})
	t.Run("open without close", func(t *testing.T) {
		event := eventByID(t, build(timeline.Input{Operations: []store.OperationRow{open(1, baseTime, "s1")}}), "term:s1")
		if event.Detail != "Task abc123, container app, /bin/sh. No close recorded." {
			t.Fatalf("detail = %q", event.Detail)
		}
	})
	t.Run("close without open", func(t *testing.T) {
		closeAt := baseTime.Add(4 * time.Minute)
		event := eventByID(t, build(timeline.Input{Operations: []store.OperationRow{closed(2, closeAt, "s1")}}), "term:s1")
		if event.Title != "Terminal session closed" || !event.At.Equal(closeAt) || event.Lane != "access" || event.Affecting {
			t.Fatalf("event = %+v", event)
		}
	})
}

func TestBuildEvents_AlertsAreActiveNotRaised(t *testing.T) {
	alerts := []model.Alert{
		{Key: testKey, Kind: "crash-loop", Severity: model.AlertDanger, Title: "Crash loop", Text: "Tasks keep stopping.", At: baseTime.Add(-2 * time.Hour)},
		{Key: testKey, Kind: "cpu", Severity: model.AlertWarning, Title: "High CPU", At: baseTime},
		{Key: testKey, Kind: "note", Severity: model.AlertInfo, Title: "Note", At: baseTime},
	}
	events := build(timeline.Input{Alerts: alerts})
	crash := eventByID(t, events, "alert:crash-loop")
	if crash.Title != "Alert active: Crash loop" || crash.Severity != "bad" || crash.Actor != "Harbormaster" || crash.Lane != "alert" || crash.Affecting {
		t.Fatalf("event = %+v", crash)
	}
	if eventByID(t, events, "alert:cpu").Severity != "warn" || eventByID(t, events, "alert:note").Severity != "info" {
		t.Fatalf("severities wrong: %+v", events)
	}

	from := baseTime.Add(-time.Hour)
	inRange := timeline.InRange(events, from)
	clamped := eventByID(t, inRange, "alert:crash-loop")
	if !clamped.At.Equal(from) || !strings.Contains(clamped.Detail, "Tasks keep stopping.") ||
		!strings.Contains(clamped.Detail, baseTime.Add(-2*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("clamped alert = %+v", clamped)
	}
	if kept := eventByID(t, inRange, "alert:cpu"); !kept.At.Equal(baseTime) || kept.Detail != "" {
		t.Fatalf("in-range alert changed: %+v", kept)
	}
}

func TestInRange_FiltersOtherLanesButKeepsOrder(t *testing.T) {
	from := baseTime
	events := build(timeline.Input{
		Operations: []store.OperationRow{
			operation(1, baseTime.Add(-time.Minute), "sam", "Old.", map[string]any{"op": "restart"}),
			operation(2, baseTime, "sam", "Edge.", map[string]any{"op": "restart"}),
			operation(3, baseTime.Add(time.Minute), "sam", "New.", map[string]any{"op": "restart"}),
		},
	})
	if len(events) != 3 || events[0].ID != "op:1" || events[2].ID != "op:3" {
		t.Fatalf("BuildEvents must keep older changes, sorted ascending: %+v", events)
	}
	got := timeline.InRange(events, from)
	if len(got) != 2 || got[0].ID != "op:2" || got[1].ID != "op:3" {
		t.Fatalf("InRange = %+v", got)
	}
}

func TestBuildEvents_DesiredCountDiff(t *testing.T) {
	events := build(timeline.Input{Operations: []store.OperationRow{
		operation(1, baseTime, "sam", "Changed desired count from 1 to 2.",
			map[string]any{"op": "scale", "from_desired": float64(1), "to_desired": float64(2)}),
		operation(2, baseTime.Add(time.Second), "sam", "Changed desired count from 2 to 3.", map[string]any{"op": "scale"}),
		operation(3, baseTime.Add(2*time.Second), "sam", "Changed desired count.",
			map[string]any{"op": "scale", "from_desired": "two", "to_desired": float64(3)}),
	}})
	diff := eventByID(t, events, "op:1").Diff
	if len(diff) != 1 || diff[0] != (timeline.DiffRow{Label: "desired", From: "1", To: "2"}) {
		t.Fatalf("diff = %+v", diff)
	}
	if len(eventByID(t, events, "op:2").Diff) != 0 || len(eventByID(t, events, "op:3").Diff) != 0 {
		t.Fatal("rows without usable from_desired/to_desired must have no diff")
	}
}
