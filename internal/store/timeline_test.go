package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func timelineTask(key model.ServiceKey, arn, stopCode, reason string, exitCode int, stoppedAt time.Time) model.Task {
	return model.Task{Cluster: key.ClusterKey, ARN: arn, ServiceName: key.Service, LastStatus: "STOPPED",
		StopCode: stopCode, StoppedReason: reason, StoppedAt: &stoppedAt, Revision: 7,
		Containers: []model.Container{{Name: "app", ExitCode: &exitCode}}}
}

func TestTimelineFailures_OnlyErrorStopsAndFailedDeployments(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := s.UpsertTasks(ctx, []model.Task{
		timelineTask(key, "arn:t/oom", "EssentialContainerExited", "OutOfMemoryError: Container killed", 137, now.Add(-10*time.Minute)),
		timelineTask(key, "arn:t/spot", "SpotInterruption", "Your Spot Task was interrupted.", 1, now.Add(-20*time.Minute)),
		timelineTask(key, "arn:t/clean", "EssentialContainerExited", "", 0, now.Add(-30*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.DeploymentRecord{
		{Key: key, DeploymentID: "d-ok", Kind: "deploy", StartedAt: now.Add(-50 * time.Minute)},
		{Key: key, DeploymentID: "d-bad", Kind: "deploy", StartedAt: now.Add(-45 * time.Minute)},
	} {
		if _, err := s.InsertDeploymentIfNew(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinishDeployment(ctx, key, "d-ok", "completed", now.Add(-40*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	failure := &store.FailureInfo{Group: "circuit_breaker", Detail: "tasks failed to start"}
	if _, err := s.FinishDeployment(ctx, key, "d-bad", "failed", now.Add(-35*time.Minute), failure); err != nil {
		t.Fatal(err)
	}

	marks, err := s.TimelineFailures(ctx, key, now.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 2 {
		t.Fatalf("marks = %+v, want 2 (error stop and failed deployment)", marks)
	}
	if !marks[0].At.Equal(now.Add(-10*time.Minute)) || marks[0].Reason == "" {
		t.Errorf("newest mark = %+v, want the error stop with a reason", marks[0])
	}
	if !marks[1].At.Equal(now.Add(-35*time.Minute)) || marks[1].Reason != "circuit_breaker" {
		t.Errorf("second mark = %+v, want the failed deployment with reason circuit_breaker", marks[1])
	}
}

func TestTimelineReads_RespectSinceAndOtherServices(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	sibling := serviceKey("core-services", "payments-api")
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-time.Hour)
	if err := s.AppendOperation(ctx, store.Operation{AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster,
		Service: &key.Service, Source: "ecs", Text: "ecs event"}); err != nil {
		t.Fatal(err)
	}

	for _, k := range []model.ServiceKey{key, sibling} {
		name := k.Service
		for label, ts := range map[string]time.Time{"new": now.Add(-5 * time.Minute), "old": now.Add(-3 * time.Hour)} {
			if err := s.AppendOperation(ctx, store.Operation{AccountID: k.AccountID, Region: k.Region, Cluster: k.Cluster,
				Service: &k.Service, Source: "harbormaster", Text: name + " " + label}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `update operation_log set ts = $1 where text = $2`, ts, name+" "+label); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpsertTasks(ctx, []model.Task{
				timelineTask(k, "arn:t/"+name+label, "EssentialContainerExited", "OutOfMemoryError: Container killed", 137, ts),
			}); err != nil {
				t.Fatal(err)
			}
			id := name + "-" + label
			if _, err := s.InsertDeploymentIfNew(ctx, store.DeploymentRecord{Key: k, DeploymentID: id, Kind: "deploy", StartedAt: ts}); err != nil {
				t.Fatal(err)
			}
			failure := &store.FailureInfo{Group: "circuit_breaker", Detail: "x"}
			if _, err := s.FinishDeployment(ctx, k, id, "failed", ts.Add(time.Minute), failure); err != nil {
				t.Fatal(err)
			}
		}
	}

	ops, err := s.TimelineOperations(ctx, key, since, 50)
	if err != nil || len(ops) != 1 || ops[0].Text != "notifications new" {
		t.Errorf("operations = %+v, %v; want only 'notifications new'", ops, err)
	}
	deployments, err := s.TimelineDeployments(ctx, key, since, 50)
	if err != nil || len(deployments) != 1 || deployments[0].DeploymentID != "notifications-new" {
		t.Errorf("deployments = %+v, %v; want only notifications-new", deployments, err)
	}
	stops, err := s.TimelineStops(ctx, key, since, 50)
	if err != nil || len(stops) != 1 || stops[0].TaskARN != "arn:t/notificationsnew" {
		t.Errorf("stops = %+v, %v; want only notificationsnew", stops, err)
	}
	if len(stops) == 1 && (stops[0].Kind != "error" || stops[0].ExitCode == nil || *stops[0].ExitCode != 137 || stops[0].Revision != 7) {
		t.Errorf("stop columns = %+v, want kind error, exit 137, revision 7", stops[0])
	}
	marks, err := s.TimelineFailures(ctx, key, since, 50)
	if err != nil || len(marks) != 2 {
		t.Errorf("failures = %+v, %v; want 2 (one stop, one deployment)", marks, err)
	}
	limited, err := s.TimelineOperations(ctx, key, time.Time{}, 1)
	if err != nil || len(limited) != 1 {
		t.Errorf("limited operations = %+v, %v; want 1 row", limited, err)
	}
}

func TestTimelineDeployments_NamesActorAndFailure(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	now := time.Now().UTC().Truncate(time.Second)
	user, err := s.CreateUser(ctx, "dana", "hash", false, false)
	if err != nil {
		t.Fatal(err)
	}
	attributed := store.DeploymentRecord{Key: key, DeploymentID: "d-attr", Kind: "deploy", Revision: 9,
		StartedAt: now.Add(-20 * time.Minute), StartedByUserID: &user.ID}
	unattributed := store.DeploymentRecord{Key: key, DeploymentID: "d-auto", Kind: "deploy", StartedAt: now.Add(-10 * time.Minute)}
	for _, r := range []store.DeploymentRecord{attributed, unattributed} {
		if _, err := s.InsertDeploymentIfNew(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	failure := &store.FailureInfo{Group: "circuit_breaker", Detail: "tasks failed to start"}
	if _, err := s.FinishDeployment(ctx, key, "d-attr", "failed", now.Add(-15*time.Minute), failure); err != nil {
		t.Fatal(err)
	}

	got, err := s.TimelineDeployments(ctx, key, now.Add(-time.Hour), 50)
	if err != nil || len(got) != 2 {
		t.Fatalf("deployments = %+v, %v; want 2", got, err)
	}
	if got[0].DeploymentID != "d-auto" || got[0].StartedBy != "" {
		t.Errorf("unattributed = %+v, want StartedBy empty", got[0])
	}
	if got[1].StartedBy != "dana" || got[1].FailureReason != "circuit_breaker" ||
		got[1].FailureDetail != "tasks failed to start" || got[1].Outcome != "failed" || got[1].Revision != 9 {
		t.Errorf("attributed = %+v, want dana, circuit_breaker, failed, rev 9", got[1])
	}
}
