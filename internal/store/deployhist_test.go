package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestInsertDeploymentIfNew_SecondInsertIsNoop(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	record := store.DeploymentRecord{
		Key:               key,
		DeploymentID:      "ecs-svc/1001",
		TaskDefinitionARN: "arn:aws:ecs:us-east-1:482109375512:task-definition/notifications:7",
		Revision:          7,
		Kind:              "deploy",
		StartedAt:         time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC),
	}

	inserted, err := s.InsertDeploymentIfNew(ctx, record)
	if err != nil || !inserted {
		t.Fatalf("first insert = %v, %v; want true, nil", inserted, err)
	}
	inserted, err = s.InsertDeploymentIfNew(ctx, record)
	if err != nil || inserted {
		t.Fatalf("second insert = %v, %v; want false, nil", inserted, err)
	}

	deployments, err := s.ListDeployments(ctx, key, time.Time{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(deployments) != 1 {
		t.Fatalf("deployments = %d, want 1", len(deployments))
	}
}

func TestDeploymentsPerDay_BucketsByKindAndOutcome(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := serviceKey("core-services", "notifications")
	day1 := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)

	records := []store.DeploymentRecord{
		{Key: key, DeploymentID: "d1", Kind: "deploy", StartedAt: day1.Add(9 * time.Hour), Outcome: "completed"},
		{Key: key, DeploymentID: "d2", Kind: "deploy", StartedAt: day1.Add(15 * time.Hour), Outcome: "failed"},
		{Key: key, DeploymentID: "d3", Kind: "restart", StartedAt: day1.Add(16 * time.Hour), Outcome: "failed"},
		{Key: key, DeploymentID: "d4", Kind: "restart", StartedAt: day2.Add(10 * time.Hour), Outcome: "completed"},
	}
	for _, r := range records {
		if _, err := s.InsertDeploymentIfNew(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.DeploymentID, err)
		}
	}

	days, err := s.DeploymentsPerDay(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, day1)
	if err != nil {
		t.Fatalf("per day: %v", err)
	}
	want := []store.DeployDay{
		{Day: day1, Deploy: 2, Restart: 1, Failed: 2},
		{Day: day2, Deploy: 0, Restart: 1, Failed: 0},
	}
	if len(days) != len(want) {
		t.Fatalf("days = %+v, want %+v", days, want)
	}
	for i := range want {
		if !days[i].Day.Equal(want[i].Day) || days[i].Deploy != want[i].Deploy ||
			days[i].Restart != want[i].Restart || days[i].Failed != want[i].Failed {
			t.Fatalf("day %d = %+v, want %+v", i, days[i], want[i])
		}
	}
}
