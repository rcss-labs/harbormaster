package deploy_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const outcomeTaskDefinition = "arn:aws:ecs:us-east-1:482109375512:task-definition/image-resizer:7"

var outcomeKey = model.ServiceKey{
	ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"},
	Service:    "image-resizer",
}

type outcomeFixture struct {
	st  *store.Store
	now time.Time
}

// newOutcomeFixture stores a service snapshot with one deployment in the given
// rollout state ("" stores a snapshot with no deployments) and an open history row.
func newOutcomeFixture(t *testing.T, rollout ecstypes.DeploymentRolloutState, reason string, startedAgo time.Duration) *outcomeFixture {
	t.Helper()
	return newAgedOutcomeFixture(t, rollout, reason, startedAgo, time.Minute, true)
}

// newAgedOutcomeFixture also sets how long ago ECS last updated the deployment
// and whether a services row exists at all.
func newAgedOutcomeFixture(t *testing.T, rollout ecstypes.DeploymentRolloutState, reason string, startedAgo, updatedAgo time.Duration, withServiceRow bool) *outcomeFixture {
	t.Helper()
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	svc := ecstypes.Service{ServiceName: aws.String(outcomeKey.Service)}
	if rollout != "" {
		svc.Deployments = []ecstypes.Deployment{{
			Id: aws.String("ecs-svc/1"), RolloutState: rollout, RolloutStateReason: aws.String(reason),
			TaskDefinition: aws.String(outcomeTaskDefinition),
			CreatedAt:      aws.Time(now.Add(-updatedAgo)), UpdatedAt: aws.Time(now.Add(-updatedAgo)),
		}}
	}
	raw, err := json.Marshal(svc)
	if err != nil {
		t.Fatalf("marshal service: %v", err)
	}
	if withServiceRow {
		if _, err := st.Pool.Exec(ctx,
			`insert into services (account_id, region, cluster_name, service_name, content_hash, raw, polled_at)
		 values ($1, $2, $3, $4, 'h', $5, now())`,
			outcomeKey.AccountID, outcomeKey.Region, outcomeKey.Cluster, outcomeKey.Service, raw); err != nil {
			t.Fatalf("insert service: %v", err)
		}
	}
	if _, err := st.InsertDeploymentIfNew(ctx, store.DeploymentRecord{
		Key: outcomeKey, DeploymentID: "ecs-svc/1", TaskDefinitionARN: outcomeTaskDefinition, Revision: 7,
		Kind: "deploy", StartedAt: now.Add(-startedAgo),
	}); err != nil {
		t.Fatalf("insert deployment: %v", err)
	}
	return &outcomeFixture{st: st, now: now}
}

func (f *outcomeFixture) logger() *deploy.OutcomeLogger {
	l := deploy.NewOutcomeLogger(f.st, time.Second)
	l.Now = func() time.Time { return f.now }
	return l
}

func (f *outcomeFixture) outcome(t *testing.T) string {
	t.Helper()
	var outcome *string
	if err := f.st.Pool.QueryRow(context.Background(),
		`select outcome from deployment_history where deployment_id = 'ecs-svc/1'`).Scan(&outcome); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if outcome == nil {
		return ""
	}
	return *outcome
}

func (f *outcomeFixture) operationTexts(t *testing.T) []string {
	t.Helper()
	rows, err := f.st.ListOperations(context.Background(), outcomeKey, true, nil, 50)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	texts := []string{}
	for _, r := range rows {
		texts = append(texts, r.Text)
	}
	return texts
}

func assertOnlyText(t *testing.T, got []string, want string) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("operations = %q, want exactly [%q]", got, want)
	}
}

func TestOutcomeLogger_CompletedWritesOnce(t *testing.T) {
	f := newOutcomeFixture(t, ecstypes.DeploymentRolloutStateCompleted, "", time.Minute)
	ctx := context.Background()

	for pass := 1; pass <= 2; pass++ {
		if err := f.logger().Once(ctx); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if got := f.outcome(t); got != "completed" {
		t.Fatalf("outcome = %q, want completed", got)
	}
	assertOnlyText(t, f.operationTexts(t), "Deployment of image-resizer:7 completed.")

	rows, err := f.st.ListOperations(ctx, outcomeKey, true, nil, 50)
	if err != nil || len(rows) != 1 {
		t.Fatalf("operations = %v, %v; want one row", rows, err)
	}
	if rows[0].Source != "harbormaster" || rows[0].Actor != "" {
		t.Errorf("source = %q, actor = %q; want harbormaster and no actor", rows[0].Source, rows[0].Actor)
	}
	want := map[string]any{"op": "outcome", "deployment_id": "ecs-svc/1", "outcome": "completed", "kind": "deploy", "started_by_user_id": nil}
	if !reflect.DeepEqual(rows[0].Details, want) {
		t.Errorf("details = %v, want %v", rows[0].Details, want)
	}
}

func TestOutcomeLogger_LongSettledDeploymentIsClosedSilently(t *testing.T) {
	f := newAgedOutcomeFixture(t, ecstypes.DeploymentRolloutStateCompleted, "", 30*time.Hour, 30*time.Hour, true)
	if err := f.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.outcome(t); got != "completed" {
		t.Fatalf("outcome = %q, want completed", got)
	}
	if texts := f.operationTexts(t); len(texts) != 0 {
		t.Fatalf("operations = %q, want none for a deployment that settled long ago", texts)
	}
}

func TestOutcomeLogger_MissingServiceRowIsSuperseded(t *testing.T) {
	f := newAgedOutcomeFixture(t, "", "", 3*time.Minute, time.Minute, false)
	if err := f.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.outcome(t); got != "superseded" {
		t.Fatalf("outcome = %q, want superseded", got)
	}
	assertOnlyText(t, f.operationTexts(t), "Deployment of image-resizer:7 was superseded.")
}

func TestOutcomeLogger_FailedIncludesReason(t *testing.T) {
	f := newOutcomeFixture(t, ecstypes.DeploymentRolloutStateFailed,
		"ECS deployment circuit breaker: tasks failed to start.", time.Minute)
	if err := f.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.outcome(t); got != "failed" {
		t.Fatalf("outcome = %q, want failed", got)
	}
	assertOnlyText(t, f.operationTexts(t),
		"Deployment of image-resizer:7 failed: ECS deployment circuit breaker: tasks failed to start.")
}

func TestOutcomeLogger_SupersededAfterTwoMinutes(t *testing.T) {
	old := newOutcomeFixture(t, "", "", 3*time.Minute)
	if err := old.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := old.outcome(t); got != "superseded" {
		t.Fatalf("outcome = %q, want superseded", got)
	}
	assertOnlyText(t, old.operationTexts(t), "Deployment of image-resizer:7 was superseded.")

	recent := newOutcomeFixture(t, "", "", time.Minute)
	if err := recent.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := recent.outcome(t); got != "" {
		t.Fatalf("outcome = %q, want still open", got)
	}
	if texts := recent.operationTexts(t); len(texts) != 0 {
		t.Fatalf("operations = %q, want none", texts)
	}
}

func TestOutcomeLogger_LeavesInProgressOpen(t *testing.T) {
	f := newOutcomeFixture(t, ecstypes.DeploymentRolloutStateInProgress, "", 10*time.Minute)
	if err := f.logger().Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.outcome(t); got != "" {
		t.Fatalf("outcome = %q, want still open", got)
	}
	if texts := f.operationTexts(t); len(texts) != 0 {
		t.Fatalf("operations = %q, want none", texts)
	}
}
