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

func TestSetPreDeployScan_ConcurrentTogglesReportOneChange(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	key := serviceKey("core-services", "payments-api")

	const callers = 16
	start := make(chan struct{})
	changes := make(chan bool, callers)
	for range callers {
		go func() {
			<-start
			changed, err := s.SetPreDeployScan(ctx, key, true, 0, fx.now)
			if err != nil {
				t.Errorf("SetPreDeployScan: %v", err)
			}
			changes <- changed
		}()
	}
	close(start)
	reported := 0
	for range callers {
		if <-changes {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("%d concurrent callers reported a change, want exactly 1", reported)
	}
}

func TestSetPreDeployScan_OffWithoutRowWritesNothing(t *testing.T) {
	fx := newReadFixture(t)
	s, ctx := fx.s, context.Background()
	key := serviceKey("core-services", "payments-api")

	if changed, err := s.SetPreDeployScan(ctx, key, false, 0, fx.now); err != nil || changed {
		t.Fatalf("off without a row: changed=%v err=%v, want false", changed, err)
	}
	var rows int
	if err := s.Pool.QueryRow(ctx, `select count(*) from service_scan_settings`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d err=%v, want none", rows, err)
	}
}
