package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestScaling_UpsertGetNotFound(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "eu-west-1", Cluster: "prod"}, Service: "worker"}

	if _, err := s.ScalingForService(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ScalingForService before poll error = %v, want ErrNotFound", err)
	}

	stepAdjust := 1
	scaling := model.Scaling{
		Key:       key,
		HasTarget: true,
		Min:       2,
		Max:       6,
		Policies: []model.ScalingPolicy{{
			Name: "cpu-target", Type: "TargetTrackingScaling", Metric: "ECSServiceAverageCPUUtilization",
			TargetValue: 60, ScaleOutCooldown: 60, ScaleInCooldown: 300, Raw: json.RawMessage(`{"a":1}`),
		}},
		Scheduled: []model.ScheduledAction{
			{Name: "night", Schedule: "cron(0 1 * * ? *)", Timezone: "UTC", MinCapacity: &stepAdjust, Raw: json.RawMessage(`{}`)},
			{Name: "day", Schedule: "cron(0 7 * * ? *)", Timezone: "UTC", Raw: json.RawMessage(`{}`)},
		},
		Activities: []model.ScalingActivity{{
			ID: "act-1", Description: "scale out", Cause: "cpu high", StatusCode: "Successful",
			StartedAt: time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC),
		}},
		PolledAt: time.Date(2026, 10, 31, 8, 30, 0, 0, time.UTC),
	}
	if err := s.UpsertScaling(ctx, []model.Scaling{scaling}); err != nil {
		t.Fatalf("UpsertScaling: %v", err)
	}

	got, err := s.ScalingForService(ctx, key)
	if err != nil {
		t.Fatalf("ScalingForService: %v", err)
	}
	if got.Min != 2 || got.Max != 6 || !got.HasTarget {
		t.Errorf("capacity = min %d max %d has_target %v; want 2, 6, true", got.Min, got.Max, got.HasTarget)
	}
	if len(got.Policies) != 1 || len(got.Scheduled) != 2 || len(got.Activities) != 1 {
		t.Errorf("counts = %d policies, %d scheduled, %d activities; want 1, 2, 1",
			len(got.Policies), len(got.Scheduled), len(got.Activities))
	}
}

// scalingRangeSource is the capacity lookup plan 02's desired-count warning depends on.
type scalingRangeSource interface {
	Range(ctx context.Context, key model.ServiceKey) (min, max int, ok bool, err error)
}

var _ scalingRangeSource = (*store.Store)(nil)

func TestScaling_Range(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	cluster := model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"}
	withTarget := model.ServiceKey{ClusterKey: cluster, Service: "payments-api"}
	noTarget := model.ServiceKey{ClusterKey: cluster, Service: "batch-worker"}
	unpolled := model.ServiceKey{ClusterKey: cluster, Service: "never-polled"}

	rows := []model.Scaling{
		{Key: withTarget, HasTarget: true, Min: 2, Max: 6, PolledAt: time.Date(2026, 10, 31, 8, 30, 0, 0, time.UTC)},
		{Key: noTarget, HasTarget: false, PolledAt: time.Date(2026, 10, 31, 8, 30, 0, 0, time.UTC)},
	}
	if err := s.UpsertScaling(ctx, rows); err != nil {
		t.Fatalf("UpsertScaling: %v", err)
	}

	minCapacity, maxCapacity, ok, err := s.Range(ctx, withTarget)
	if err != nil || !ok || minCapacity != 2 || maxCapacity != 6 {
		t.Errorf("Range with target = (%d, %d, %v, %v); want (2, 6, true, nil)", minCapacity, maxCapacity, ok, err)
	}
	if _, _, ok, err := s.Range(ctx, noTarget); err != nil || ok {
		t.Errorf("Range without target = ok %v, err %v; want ok false, err nil", ok, err)
	}
	if _, _, ok, err := s.Range(ctx, unpolled); err != nil || ok {
		t.Errorf("Range never polled = ok %v, err %v; want ok false, err nil", ok, err)
	}
}
