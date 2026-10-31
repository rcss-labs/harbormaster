package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
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

func TestUnhealthyTargets_OnlyServicesWithUnhealthyRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	cluster := model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "prod"}
	otherRegion := model.ClusterKey{AccountID: "482109375512", Region: "eu-west-1", Cluster: "prod"}
	polled := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	sick := model.ServiceKey{ClusterKey: cluster, Service: "sick"}
	mixed := model.ServiceKey{ClusterKey: cluster, Service: "mixed"}
	well := model.ServiceKey{ClusterKey: cluster, Service: "well"}
	elsewhere := model.ServiceKey{ClusterKey: otherRegion, Service: "sick"}
	rows := []model.TargetHealth{
		{Key: sick, TargetGroupARN: "tg-1", Healthy: 0, Unhealthy: 2, PolledAt: polled},
		{Key: mixed, TargetGroupARN: "tg-1", Healthy: 1, Unhealthy: 0, PolledAt: polled},
		{Key: mixed, TargetGroupARN: "tg-2", Healthy: 0, Unhealthy: 1, PolledAt: polled},
		{Key: well, TargetGroupARN: "tg-1", Healthy: 3, Unhealthy: 0, PolledAt: polled},
		{Key: elsewhere, TargetGroupARN: "tg-1", Healthy: 0, Unhealthy: 1, PolledAt: polled},
	}
	if err := s.UpsertTargetHealth(ctx, rows); err != nil {
		t.Fatal(err)
	}

	got, err := s.UnhealthyTargets(ctx, store.TargetFilter{AccountID: cluster.AccountID, Region: cluster.Region})
	if err != nil {
		t.Fatalf("UnhealthyTargets: %v", err)
	}
	if len(got) != 2 || !got[sick] || !got[mixed] || got[well] || got[elsewhere] {
		t.Fatalf("UnhealthyTargets = %v, want only sick and mixed in us-east-1", got)
	}
	all, err := s.UnhealthyTargets(ctx, store.TargetFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("unfiltered UnhealthyTargets = %v, %v; want 3 services", all, err)
	}
}
