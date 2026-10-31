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
