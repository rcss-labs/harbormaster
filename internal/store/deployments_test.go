package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAttributeDeployment_InsertsThenUpdatesOnlyAttribution(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	startedAt := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	taskDefinition := "arn:aws:ecs:eu-west-1:111111111111:task-definition/notifications:7"

	if err := s.AttributeDeployment(ctx, store.AttributedDeployment{
		Key: key, DeploymentID: "ecs-svc/1", TaskDefinitionARN: taskDefinition, Revision: 7,
		Kind: "restart", StartedByUserID: 7, StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("attribute: %v", err)
	}

	inserted, err := s.InsertDeploymentIfNew(ctx, store.DeploymentRecord{
		Key: key, DeploymentID: "ecs-svc/1", TaskDefinitionARN: taskDefinition, Revision: 7,
		Kind: "deploy", StartedAt: startedAt,
	})
	if err != nil || inserted {
		t.Fatalf("poller insert = %v, %v; want false, nil", inserted, err)
	}
	assertAttribution(t, s, key, "restart", 7)

	if err := s.AttributeDeployment(ctx, store.AttributedDeployment{
		Key: key, DeploymentID: "ecs-svc/1", TaskDefinitionARN: taskDefinition, Revision: 7,
		Kind: "restart", StartedByUserID: 8, StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("re-attribute: %v", err)
	}
	assertAttribution(t, s, key, "restart", 8)
}

func TestOpenDeployments_JoinsServiceRaw(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	raw := `{"deployments":[]}`
	if _, err := s.Pool.Exec(ctx,
		`insert into services (account_id, region, cluster_name, service_name, content_hash, raw, polled_at)
		 values ($1, $2, $3, $4, 'h', $5, now())`,
		key.AccountID, key.Region, key.Cluster, key.Service, raw); err != nil {
		t.Fatalf("insert service: %v", err)
	}
	startedAt := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	for _, r := range []store.DeploymentRecord{
		{Key: key, DeploymentID: "ecs-svc/open", Kind: "deploy", StartedAt: startedAt},
		{Key: key, DeploymentID: "ecs-svc/done", Kind: "deploy", StartedAt: startedAt.Add(-time.Hour), Outcome: "completed"},
	} {
		if _, err := s.InsertDeploymentIfNew(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.DeploymentID, err)
		}
	}

	open, err := s.OpenDeployments(ctx)
	if err != nil {
		t.Fatalf("open deployments: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open = %d rows, want 1", len(open))
	}
	if open[0].DeploymentID != "ecs-svc/open" || open[0].Key != key {
		t.Fatalf("open[0] = %+v, want ecs-svc/open for %+v", open[0], key)
	}
	// jsonb normalizes whitespace, so compare the decoded documents, not bytes.
	var gotRaw, wantRaw any
	if err := json.Unmarshal(open[0].ServiceRaw, &gotRaw); err != nil {
		t.Fatalf("decode service raw: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &wantRaw); err != nil {
		t.Fatalf("decode want raw: %v", err)
	}
	if !reflect.DeepEqual(gotRaw, wantRaw) {
		t.Fatalf("service raw = %s, want %s", open[0].ServiceRaw, raw)
	}
}

func TestFinishDeployment_ClaimsOnce(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	insertOpenDeployment(t, s, key, "ecs-svc/1")
	finishedAt := time.Date(2026, 10, 30, 9, 5, 0, 0, time.UTC)

	claimed, err := s.FinishDeployment(ctx, key, "ecs-svc/1", "completed", finishedAt)
	if err != nil || !claimed {
		t.Fatalf("first finish = %v, %v; want true, nil", claimed, err)
	}
	claimed, err = s.FinishDeployment(ctx, key, "ecs-svc/1", "failed", finishedAt.Add(time.Minute))
	if err != nil || claimed {
		t.Fatalf("second finish = %v, %v; want false, nil", claimed, err)
	}

	var outcome string
	var storedFinish time.Time
	if err := s.Pool.QueryRow(ctx,
		`select outcome, finished_at from deployment_history where deployment_id = 'ecs-svc/1'`).
		Scan(&outcome, &storedFinish); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if outcome != "completed" || !storedFinish.Equal(finishedAt) {
		t.Fatalf("row = (%s, %s), want (completed, %s)", outcome, storedFinish, finishedAt)
	}
}

func TestFinishDeployment_AcceptsSuperseded(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	insertOpenDeployment(t, s, key, "ecs-svc/1")

	claimed, err := s.FinishDeployment(ctx, key, "ecs-svc/1", "superseded", time.Now())
	if err != nil || !claimed {
		t.Fatalf("finish superseded = %v, %v; want true, nil", claimed, err)
	}
	var outcome string
	if err := s.Pool.QueryRow(ctx,
		`select outcome from deployment_history where deployment_id = 'ecs-svc/1'`).Scan(&outcome); err != nil {
		t.Fatalf("read outcome: %v", err)
	}
	if outcome != "superseded" {
		t.Fatalf("outcome = %q, want superseded", outcome)
	}
}

func insertOpenDeployment(t *testing.T, s *store.Store, key model.ServiceKey, deploymentID string) {
	t.Helper()
	if _, err := s.InsertDeploymentIfNew(context.Background(), store.DeploymentRecord{
		Key: key, DeploymentID: deploymentID, Kind: "deploy", StartedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("insert open deployment: %v", err)
	}
}

func assertAttribution(t *testing.T, s *store.Store, key model.ServiceKey, wantKind string, wantUser int64) {
	t.Helper()
	var kind string
	var userID *int64
	if err := s.Pool.QueryRow(context.Background(),
		`select kind, started_by_user_id from deployment_history
		 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4 and deployment_id = 'ecs-svc/1'`,
		key.AccountID, key.Region, key.Cluster, key.Service).Scan(&kind, &userID); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if kind != wantKind || userID == nil || *userID != wantUser {
		t.Fatalf("attribution = (%s, %v), want (%s, %d)", kind, userID, wantKind, wantUser)
	}
}
