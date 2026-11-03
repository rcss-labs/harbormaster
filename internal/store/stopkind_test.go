package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestUpsertTasks_PersistsStopKindAndCause(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	exit := 1
	at := time.Now().Add(-time.Hour)
	spot := model.Task{Cluster: clusterKey("staging-web"), ARN: "arn:t/spot", LastStatus: "STOPPED", StopCode: "SpotInterruption",
		StoppedReason: "Your Spot Task was interrupted.", StoppedAt: &at, Containers: []model.Container{{Name: "app", ExitCode: &exit}}}
	oom := stoppedTask("staging-web", "arn:t/oom", at)
	clean := model.Task{Cluster: clusterKey("staging-web"), ARN: "arn:t/clean", LastStatus: "STOPPED", StoppedAt: &at}
	running := model.Task{Cluster: clusterKey("staging-web"), ARN: "arn:t/running", LastStatus: "RUNNING"}
	if _, err := s.UpsertTasks(ctx, []model.Task{spot, oom, clean, running}); err != nil {
		t.Fatal(err)
	}
	for arn, want := range map[string][2]string{
		"arn:t/spot": {"expected", "spot"}, "arn:t/oom": {"error", ""}, "arn:t/clean": {"", ""}, "arn:t/running": {"", ""},
	} {
		var kind, cause string
		if err := s.Pool.QueryRow(ctx, `select coalesce(stop_kind, ''), coalesce(stop_cause, '') from tasks where task_arn = $1`, arn).Scan(&kind, &cause); err != nil {
			t.Fatal(err)
		}
		if kind != want[0] || cause != want[1] {
			t.Errorf("%s: stop_kind/stop_cause = %q/%q, want %q/%q", arn, kind, cause, want[0], want[1])
		}
	}
}

// Expected stops (spot interruption, deployment replacement) exit non-zero but are not errors: they stay out
// of the stopped_error and problems filters and are counted as stopped_expected, not stopped_normal.
func TestExpectedStops_NotErrors(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	admin := store.Visibility{Admin: true}
	started := fx.now.Add(-2 * time.Hour)
	stoppedAt := fx.now.Add(-5 * time.Minute)
	stopped := func(id, stopCode, reason string, exitCode int) model.Task {
		return model.Task{Cluster: clusterKey("core-services"), ARN: readTaskARN("core-services", id),
			ServiceName: "payments-api", LastStatus: "STOPPED", StopCode: stopCode, StoppedReason: reason,
			Family: "payments-api-fam", AvailabilityZone: "eu-west-1a", CapacityProvider: "FARGATE_SPOT",
			CreatedAt: started, StartedAt: &started, StoppedAt: &stoppedAt, PolledAt: fx.now,
			Containers: []model.Container{{Name: "app", ExitCode: intPtr(exitCode)}}}
	}
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{
		stopped("x1spot", "SpotInterruption", "Your Spot Task was interrupted.", 1),
		stopped("x2deploy", "ServiceSchedulerInitiated", "Scaling activity initiated by (deployment ecs-svc/1)", 1),
	}); err != nil {
		t.Fatal(err)
	}
	spotARN, deployARN := readTaskARN("core-services", "x1spot"), readTaskARN("core-services", "x2deploy")
	for _, filter := range []string{"stopped_error", "problems"} {
		rows, _, err := fx.s.ListTasks(ctx, store.TaskQuery{Vis: admin, Filter: filter})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Task.ARN == spotARN || row.Task.ARN == deployARN {
				t.Errorf("%s filter includes expected stop %s", filter, row.Task.ARN)
			}
		}
	}
	rows, _, err := fx.s.ListTasks(ctx, store.TaskQuery{Vis: admin, Filter: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]store.TaskRow{}
	for _, row := range rows {
		found[row.Task.ARN] = row
	}
	if row := found[spotARN]; row.StopKind != "expected" || row.StopCause != "spot" {
		t.Errorf("spot row kind/cause = %q/%q, want expected/spot", row.StopKind, row.StopCause)
	}
	if row := found[deployARN]; row.StopKind != "expected" || row.StopCause != "scaling" {
		t.Errorf("deploy row kind/cause = %q/%q, want expected/scaling", row.StopKind, row.StopCause)
	}
	sum, err := fx.s.TaskSummary(ctx, store.TargetFilter{}, admin, fx.now.Add(-72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sum.StoppedError != 2 || sum.StoppedNormal != 1 || sum.StoppedExpected != 2 {
		t.Errorf("summary error/normal/expected = %d/%d/%d, want 2/1/2", sum.StoppedError, sum.StoppedNormal, sum.StoppedExpected)
	}
}
