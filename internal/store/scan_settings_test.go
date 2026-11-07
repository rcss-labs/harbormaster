package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestPreDeployScan_DefaultOffAndSurvivesServiceRecreation(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	key := serviceKey("core-services", "payments-api")

	if on, err := s.PreDeployScan(ctx, key); err != nil || on {
		t.Fatalf("setting without a row = %v err=%v, want false", on, err)
	}
	if changed, err := s.SetPreDeployScan(ctx, key, false, 0, fx.now); err != nil || changed {
		t.Fatalf("setting off when already off: changed=%v err=%v, want false", changed, err)
	}
	if changed, err := s.SetPreDeployScan(ctx, key, true, 0, fx.now); err != nil || !changed {
		t.Fatalf("setting on: changed=%v err=%v, want true", changed, err)
	}

	if _, err := s.Pool.Exec(ctx, `delete from services where service_name = 'payments-api'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServices(ctx, []model.Service{{Key: key, Desired: 1, PolledAt: fx.now.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if on, err := s.PreDeployScan(ctx, key); err != nil || !on {
		t.Fatalf("setting after the service was recreated = %v err=%v, want true", on, err)
	}
	if changed, err := s.SetPreDeployScan(ctx, key, true, 0, fx.now); err != nil || changed {
		t.Fatalf("setting on when already on: changed=%v err=%v, want false", changed, err)
	}
}
