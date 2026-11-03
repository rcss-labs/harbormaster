package poller

import (
	"context"
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
