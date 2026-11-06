package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func TestScalingLoop_WritesTargetPoliciesScheduledActivities(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	h := newObserveHarness(t, target)
	loop := &ScalingLoop{Store: h.store, Clients: h.fake, Every: 60 * time.Second, Now: func() time.Time { return loopNow }}
	ctx := context.Background()

	if err := loop.Run(ctx, target); err != nil {
		t.Fatalf("Run: %v", err)
	}

	payments, err := h.store.ScalingForService(ctx, serviceKey(target, "core-services", "payments-api"))
	if err != nil {
		t.Fatal(err)
	}
	if !payments.HasTarget || payments.Min != 2 || payments.Max != 6 {
		t.Fatalf("payments scaling = has %v min %d max %d, want true 2 6", payments.HasTarget, payments.Min, payments.Max)
	}
	if len(payments.Policies) != 1 {
		t.Fatalf("policies = %d, want 1", len(payments.Policies))
	}
	policy := payments.Policies[0]
	if policy.Type != "TargetTrackingScaling" || policy.Metric != "ECSServiceAverageCPUUtilization" ||
		policy.TargetValue != 60 || policy.ScaleOutCooldown != 60 || policy.ScaleInCooldown != 300 {
		t.Fatalf("policy = %+v", policy)
	}
	if len(payments.Scheduled) != 2 {
		t.Fatalf("scheduled actions = %d, want 2", len(payments.Scheduled))
	}
	if len(payments.Activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(payments.Activities))
	}

	email, err := h.store.ScalingForService(ctx, serviceKey(target, "background-jobs", "email-sender"))
	if err != nil {
		t.Fatal(err)
	}
	if email.HasTarget {
		t.Fatal("email-sender has a scalable target, want none")
	}
}

func TestCollectServiceScaling_ReadsOneServiceLive(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	h := newObserveHarness(t, target)
	api, err := h.fake.AutoScaling(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}

	got, err := CollectServiceScaling(context.Background(), api, serviceKey(target, "core-services", "payments-api"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasTarget || got.Min != 2 || got.Max != 6 || len(got.Policies) != 1 || len(got.Scheduled) != 2 || len(got.Activities) != 1 {
		t.Fatalf("payments = %+v, want target 2-6 with 1 policy, 2 scheduled actions, 1 activity", got)
	}
	if got.Key.Service != "payments-api" || got.PolledAt.IsZero() {
		t.Fatalf("key/polled_at = %v / %v", got.Key, got.PolledAt)
	}

	none, err := CollectServiceScaling(context.Background(), api, serviceKey(target, "background-jobs", "email-sender"))
	if err != nil {
		t.Fatal(err)
	}
	if none.HasTarget || len(none.Policies) != 0 || len(none.Scheduled) != 0 {
		t.Fatalf("email-sender = %+v, want no target", none)
	}
}

func TestCollectServiceScalingConfigPartial(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	h := newObserveHarness(t, target)
	api, err := h.fake.AutoScaling(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey(target, "core-services", "payments-api")

	got, parts := CollectServiceScalingConfigPartial(context.Background(), api, key)
	if len(parts) != 0 || !got.HasTarget || len(got.Policies) != 1 || len(got.Scheduled) != 2 {
		t.Fatalf("healthy read: parts %+v scaling %+v", parts, got)
	}

	scheduledErr := errors.New("DescribeScheduledActions: UnsupportedOperation")
	h.fake.FailAlways("DescribeScheduledActions", scheduledErr)
	got, parts = CollectServiceScalingConfigPartial(context.Background(), api, key)
	if len(parts) != 1 || parts[0].Part != "scheduled_actions" || !errors.Is(parts[0].Err, scheduledErr) {
		t.Fatalf("parts = %+v, want one scheduled_actions error", parts)
	}
	if !got.HasTarget || got.Min != 2 || got.Max != 6 || len(got.Policies) != 1 || len(got.Scheduled) != 0 {
		t.Fatalf("readable parts must survive: %+v", got)
	}

	// the strict reader still fails as a whole, which is what the scaling poll tier reports
	if _, err := CollectServiceScalingConfig(context.Background(), api, key); err == nil {
		t.Fatal("CollectServiceScalingConfig must keep failing when a part fails")
	}
}

func TestScalingLoop_StoresReadablePartsWhenScheduledActionsFail(t *testing.T) {
	target := awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	key := serviceKey(target, "core-services", "payments-api")
	scheduledErr := errors.New("DescribeScheduledActions: UnsupportedOperation")
	ctx := context.Background()

	t.Run("never read before: target and policies stored, scheduled actions flagged unreadable", func(t *testing.T) {
		h := newObserveHarness(t, target)
		loop := &ScalingLoop{Store: h.store, Clients: h.fake, Every: 60 * time.Second, Now: func() time.Time { return loopNow }}
		if _, err := h.store.Pool.Exec(ctx, "delete from scaling"); err != nil {
			t.Fatal(err)
		}
		h.fake.FailAlways("DescribeScheduledActions", scheduledErr)

		err := loop.Run(ctx, target)
		if !errors.Is(err, scheduledErr) {
			t.Fatalf("Run error = %v, want the scheduled actions failure", err)
		}
		got, err := h.store.ScalingForService(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if !got.HasTarget || got.Min != 2 || got.Max != 6 || len(got.Policies) != 1 || len(got.Scheduled) != 0 {
			t.Fatalf("scaling = %+v, want target 2-6, 1 policy, no scheduled actions", got)
		}
		if len(got.Unreadable) != 1 || got.Unreadable[0] != PartScheduledActions {
			t.Fatalf("unreadable = %v, want only %s", got.Unreadable, PartScheduledActions)
		}
	})

	t.Run("known scheduled actions are kept, not wiped", func(t *testing.T) {
		h := newObserveHarness(t, target)
		loop := &ScalingLoop{Store: h.store, Clients: h.fake, Every: 60 * time.Second, Now: func() time.Time { return loopNow }}
		if err := loop.Run(ctx, target); err != nil {
			t.Fatal(err)
		}
		h.fake.FailAlways("DescribeScheduledActions", scheduledErr)
		if err := loop.Run(ctx, target); err == nil {
			t.Fatal("Run must report the failed part")
		}
		got, err := h.store.ScalingForService(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Scheduled) != 2 || len(got.Unreadable) != 1 {
			t.Fatalf("scheduled %d unreadable %v, want the 2 known actions kept and flagged", len(got.Scheduled), got.Unreadable)
		}

		h.fake.FailAlways("DescribeScheduledActions", nil)
		if err := loop.Run(ctx, target); err != nil {
			t.Fatal(err)
		}
		got, _ = h.store.ScalingForService(ctx, key)
		if len(got.Unreadable) != 0 {
			t.Fatalf("unreadable = %v after recovery, want none", got.Unreadable)
		}
	})
}
