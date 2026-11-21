package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// spotSince is the start of the 7-day window the Spot facts read.
func spotSince() time.Time { return time.Now().UTC().AddDate(0, 0, -7) }

// stoppedPaymentsTask is a STOPPED task of payments that ended at ago before now.
func stoppedPaymentsTask(id, stopCode, reason string, exit int, ago time.Duration) model.Task {
	stoppedAt := time.Now().UTC().Add(-ago)
	return model.Task{
		Cluster: clusterKey("core"), ARN: readTaskARN("core", id), ServiceName: "payments", LastStatus: "STOPPED",
		StopCode: stopCode, StoppedReason: reason, Containers: []model.Container{{Name: "app", ExitCode: &exit}},
		StoppedAt: &stoppedAt, PolledAt: stoppedAt, CreatedAt: stoppedAt.Add(-time.Hour),
	}
}

func (fx reliabilityFixture) logOperation(t *testing.T, key model.ServiceKey, source, op string, ago time.Duration) {
	t.Helper()
	details, _ := json.Marshal(map[string]any{"op": op})
	if _, err := fx.s.Pool.Exec(context.Background(),
		`insert into operation_log (ts, account_id, region, cluster_name, service_name, source, text, details)
		 values ($1, $2, $3, $4, $5, $6, 'x', $7::jsonb)`,
		time.Now().UTC().Add(-ago), key.AccountID, key.Region, key.Cluster, key.Service, source, string(details)); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsCostServices_SpotFacts(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	day := 24 * time.Hour

	// Terminal sessions opened through Harbormaster count when they are inside the window.
	fx.logOperation(t, fx.payments, "harbormaster", "terminal", 2*day)
	fx.logOperation(t, fx.payments, "harbormaster", "terminal", 9*day)
	fx.logOperation(t, fx.payments, "harbormaster", "deploy", day)
	fx.logOperation(t, fx.payments, "ecs", "terminal", day)
	fx.logOperation(t, fx.ledger, "harbormaster", "terminal", day)

	// Deployment and scale-in stops: one killed after the stop timeout (137), one clean. Everything else is not
	// evidence about SIGTERM handling.
	const (
		deploying = "Scaling activity initiated by (deployment ecs-svc/0911899770729461037)"
		scaleIn   = "Scaling activity initiated by (desired count change)"
	)
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{
		stoppedPaymentsTask("killed", "ServiceSchedulerInitiated", deploying, 137, time.Hour),
		stoppedPaymentsTask("clean", "ServiceSchedulerInitiated", scaleIn, 143, 2*time.Hour),
		stoppedPaymentsTask("oom", "EssentialContainerExited", "OutOfMemoryError: container killed due to memory usage", 137, 3*time.Hour),
		stoppedPaymentsTask("user", "UserInitiated", "Task stopped by sam via Harbormaster", 143, 4*time.Hour),
		stoppedPaymentsTask("old", "ServiceSchedulerInitiated", deploying, 137, 10*day),
	}); err != nil {
		t.Fatal(err)
	}

	// The service's strategy, controller and size; the cluster's providers; a stop timeout on two containers.
	const tdARN = "arn:aws:ecs:eu-west-1:123456789012:task-definition/payments:7"
	tdRaw, _ := json.Marshal(map[string]any{"ContainerDefinitions": []map[string]any{
		{"Name": "app", "StopTimeout": 30}, {"Name": "sidecar", "StopTimeout": 90}, {"Name": "plain"}}})
	if err := fx.s.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{
		AccountID: fx.payments.AccountID, Region: fx.payments.Region, ARN: tdARN, Family: "payments", Revision: 7,
		CPU: "512", Memory: "1024", RequiresCompatibilities: []string{"FARGATE"}, Raw: tdRaw,
	}); err != nil {
		t.Fatal(err)
	}
	serviceRaw := json.RawMessage(`{"CapacityProviderStrategy":[{"CapacityProvider":"FARGATE","Base":1,"Weight":0},{"CapacityProvider":"FARGATE_SPOT","Base":0,"Weight":1}]}`)
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 3, Running: 3, TaskDefinitionARN: tdARN, DeploymentController: "ECS", Raw: serviceRaw, PolledAt: time.Now().UTC()},
		{Key: fx.ledger, Desired: 1, Running: 1, LaunchType: "FARGATE", DeploymentController: "CODE_DEPLOY", PolledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.UpsertClusters(ctx, []model.Cluster{
		{Key: clusterKey("core"), Status: "ACTIVE", CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"}, PolledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	facts := costFacts(t, fx, fx.adminVis)
	got := facts["payments"]
	if got.Desired != 3 || got.Controller != "ECS" {
		t.Errorf("desired %d controller %q, want 3 and ECS", got.Desired, got.Controller)
	}
	if len(got.StrategyProviders) != 2 || got.StrategyProviders[0] != "FARGATE" || got.StrategyProviders[1] != "FARGATE_SPOT" {
		t.Errorf("strategy providers = %v, want [FARGATE FARGATE_SPOT] from the service's stored raw JSON", got.StrategyProviders)
	}
	if len(got.ClusterProviders) != 2 {
		t.Errorf("cluster providers = %v, want the cluster's two", got.ClusterProviders)
	}
	if got.TerminalSessions7d != 1 {
		t.Errorf("terminal sessions = %d, want 1 (the 9-day-old, non-terminal and ECS rows do not count)", got.TerminalSessions7d)
	}
	if got.ExpectedStops7d != 2 || got.KilledStops7d != 1 {
		t.Errorf("stops = %d expected, %d killed; want 2 and 1 (an OOM stop, a user stop and an old stop are not counted)", got.ExpectedStops7d, got.KilledStops7d)
	}
	if got.StopTimeoutSeconds == nil || *got.StopTimeoutSeconds != 90 {
		t.Errorf("stop timeout = %v, want the highest, 90", got.StopTimeoutSeconds)
	}
	if got.NonProduction {
		t.Error("payments is under the Prod tag, which nobody marked non-production")
	}

	ledger := facts["ledger"]
	if ledger.LaunchType != "FARGATE" || ledger.Controller != "CODE_DEPLOY" || len(ledger.StrategyProviders) != 0 || ledger.StopTimeoutSeconds != nil {
		t.Errorf("ledger = %+v, want a launch type service on CodeDeploy with no strategy and no stop timeout", ledger)
	}
	if ledger.TerminalSessions7d != 1 || ledger.ExpectedStops7d != 0 {
		t.Errorf("ledger terminal %d expected stops %d, want 1 and 0 (the stops belong to payments)", ledger.TerminalSessions7d, ledger.ExpectedStops7d)
	}
	if !facts["batch"].NonProduction {
		t.Error("batch falls back to the built-in Dev tag, which starts non-production")
	}
}

func TestAnalyticsCostServices_SpotFactsRespectVisibility(t *testing.T) {
	fx := newReliabilityFixture(t)
	fx.logOperation(t, fx.batch, "harbormaster", "terminal", time.Hour)
	got := costFacts(t, fx, fx.prodOnlyVi)
	if _, seen := got["batch"]; seen {
		t.Fatalf("a Prod-only viewer received %v, want nothing from the Dev service batch", got["batch"])
	}
	if got["payments"].TerminalSessions7d != 0 {
		t.Errorf("payments terminal sessions = %d, want 0", got["payments"].TerminalSessions7d)
	}
}
