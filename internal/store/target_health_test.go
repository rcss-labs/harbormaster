package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestTargetHealth_UpsertAndDeleteNotIn(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "prod"}, Service: "api"}
	polled := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tgA := "arn:aws:elasticloadbalancing:us-east-1:482109375512:targetgroup/api-a/1"
	tgB := "arn:aws:elasticloadbalancing:us-east-1:482109375512:targetgroup/api-b/2"
	lb := "arn:aws:elasticloadbalancing:us-east-1:482109375512:loadbalancer/app/api/3"

	rows := []model.TargetHealth{
		{Key: key, TargetGroupARN: tgB, LoadBalancerARN: lb, Healthy: 1, Unhealthy: 1, PolledAt: polled},
		{Key: key, TargetGroupARN: tgA, LoadBalancerARN: lb, Healthy: 2, Unhealthy: 0, PolledAt: polled},
	}
	if err := s.UpsertTargetHealth(ctx, rows); err != nil {
		t.Fatalf("UpsertTargetHealth: %v", err)
	}
	got, err := s.TargetHealthForService(ctx, key)
	if err != nil || len(got) != 2 {
		t.Fatalf("TargetHealthForService = %d rows, %v; want 2", len(got), err)
	}
	if got[0].TargetGroupARN != tgA || got[1].TargetGroupARN != tgB {
		t.Errorf("rows not ordered by target_group_arn: %q, %q", got[0].TargetGroupARN, got[1].TargetGroupARN)
	}

	target := awsx.Target{AccountID: key.AccountID, Region: key.Region}
	if err := s.DeleteTargetHealthNotIn(ctx, target, []string{tgA}); err != nil {
		t.Fatalf("DeleteTargetHealthNotIn: %v", err)
	}
	got, err = s.TargetHealthForService(ctx, key)
	if err != nil || len(got) != 1 || got[0].TargetGroupARN != tgA {
		t.Fatalf("after delete got %d rows, %v; want only %s", len(got), err, tgA)
	}

	rows[1].Healthy = 5
	if err := s.UpsertTargetHealth(ctx, rows[1:]); err != nil {
		t.Fatalf("second UpsertTargetHealth: %v", err)
	}
	got, err = s.TargetHealthForService(ctx, key)
	if err != nil || len(got) != 1 || got[0].Healthy != 5 {
		t.Fatalf("upsert of same key: got %+v, %v; want healthy 5", got, err)
	}
}
