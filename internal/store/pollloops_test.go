package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestPolledAt_MinOverTargets(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	oldest := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	middle := oldest.Add(time.Hour)
	newest := oldest.Add(2 * time.Hour)
	loops := []store.PollLoop{
		{Target: awsx.Target{AccountID: "482109375512", Region: "us-east-1"}, Tier: "services", LastOKAt: &middle, Interval: time.Minute, BaseInterval: time.Minute},
		{Target: awsx.Target{AccountID: "482109375512", Region: "eu-west-1"}, Tier: "services", LastOKAt: &newest, Interval: time.Minute, BaseInterval: time.Minute},
		{Target: awsx.Target{AccountID: "123456789012", Region: "us-east-1"}, Tier: "services", LastOKAt: &oldest, Interval: time.Minute, BaseInterval: time.Minute},
		{Target: awsx.Target{AccountID: "482109375512", Region: "us-east-1"}, Tier: "tasks", LastOKAt: &oldest, Interval: time.Minute, BaseInterval: time.Minute},
	}
	for _, loop := range loops {
		if err := s.UpsertPollLoop(ctx, loop); err != nil {
			t.Fatalf("upsert %s/%s/%s: %v", loop.Target.AccountID, loop.Target.Region, loop.Tier, err)
		}
	}

	got, err := s.PolledAt(ctx, store.TargetFilter{AccountID: "482109375512"})
	if err != nil || !got.Equal(middle) {
		t.Fatalf("PolledAt(account) = %v, %v; want %v (tasks tier ignored)", got, err, middle)
	}
	got, err = s.PolledAt(ctx, store.TargetFilter{})
	if err != nil || !got.Equal(oldest) {
		t.Fatalf("PolledAt(all) = %v, %v; want %v", got, err, oldest)
	}
	got, err = s.PolledAt(ctx, store.TargetFilter{AccountID: "999999999999"})
	if err != nil || !got.IsZero() {
		t.Fatalf("PolledAt(no rows) = %v, %v; want zero time", got, err)
	}
}
