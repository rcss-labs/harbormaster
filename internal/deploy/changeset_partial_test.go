package deploy_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/deploy"
)

// toggleExec is a change set that touches only the ECS Exec flag.
func toggleExec(t *testing.T, h *harness, base deploy.ChangeSet) deploy.ChangeSet {
	t.Helper()
	settings, err := h.svc.DeploySettings(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	base.Service = &deploy.ServiceChange{EnableExecuteCommand: aws.Bool(!settings.EnableExecuteCommand)}
	return base
}

func TestChangeSet_AppliesWhenScheduledActionsUnreadable(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	cs := toggleExec(t, h, baseChangeSet(t, h, paymentsKey))
	got, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	if err != nil {
		t.Fatalf("a change set that leaves scheduled actions alone must apply: %v", err)
	}
	if got.Status != "applied" {
		t.Fatalf("result = %+v", got)
	}
}

// The part can be readable when the client reads and unreadable when it applies, or the reverse
// (throttling comes and goes); neither direction may turn into a version conflict.
func TestChangeSet_AppliesWhenReadabilityChangesBetweenReadAndApply(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: aws.Int(4)}
	h.fake.FailAlways("DescribeScheduledActions", nil)
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs); err != nil {
		t.Fatalf("read while unreadable, apply while readable: %v", err)
	}

	cs = baseChangeSet(t, h, paymentsKey)
	cs.Service = &deploy.ServiceChange{DesiredCount: aws.Int(5)}
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	if _, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs); err != nil {
		t.Fatalf("read while readable, apply while unreadable: %v", err)
	}
}

func TestChangeSet_DetectsConcurrentEditToReadablePartWhileScheduledUnreadable(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	cs := toggleExec(t, h, baseChangeSet(t, h, paymentsKey))

	// Someone else retunes the CPU policy after the client read the settings.
	api, err := h.fake.AutoScaling(t.Context(), awsx.Target{AccountID: homelabAccountID, Region: homelabRegion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutScalingPolicy(t.Context(), cpuPolicyInputFor("cpu-target", 35)); err != nil {
		t.Fatal(err)
	}
	before := writeCalls(h)
	_, err = h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	var stale *deploy.StaleVersionError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StaleVersionError for the concurrent policy edit", err)
	}
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_RefusesScheduledActionEditWhenUnreadable(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{PutScheduledActions: []deploy.ScheduledActionChange{
		{Name: "night", Schedule: "cron(0 2 * * ? *)", MinCapacity: aws.Int(1)}}}
	requireInvalid(t, h, paymentsKey, cs, "Scheduled actions could not be read; retry before changing them.")

	cs.Scaling = &deploy.ScalingChange{DeleteScheduledActions: []string{"anything"}}
	requireInvalid(t, h, paymentsKey, cs, "Scheduled actions could not be read; retry before changing them.")
}

// A client that never saw the scheduled actions cannot edit them, even if they are readable by now.
func TestChangeSet_RefusesBlindScheduledActionEditAfterRecovery(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScheduledActions", errScheduledUnsupported)
	cs := baseChangeSet(t, h, paymentsKey)
	h.fake.FailAlways("DescribeScheduledActions", nil)
	cs.Scaling = &deploy.ScalingChange{PutScheduledActions: []deploy.ScheduledActionChange{
		{Name: "night", Schedule: "cron(0 2 * * ? *)", MinCapacity: aws.Int(1)}}}
	before := writeCalls(h)
	_, err := h.svc.ApplyChangeSet(t.Context(), h.sam, paymentsKey, cs)
	var stale *deploy.StaleVersionError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StaleVersionError", err)
	}
	assertNoNewWrites(t, h, before)
}

func TestChangeSet_RefusesPolicyEditWhenPoliciesUnreadable(t *testing.T) {
	h := newHarness(t)
	h.fake.FailAlways("DescribeScalingPolicies", errScheduledUnsupported)
	cs := baseChangeSet(t, h, paymentsKey)
	cs.Scaling = &deploy.ScalingChange{PutPolicies: []deploy.PolicyChange{cpuPolicy("cpu-target")}}
	requireInvalid(t, h, paymentsKey, cs, "Scaling policies could not be read; retry before changing them.")
}

func cpuPolicyInputFor(name string, target float64) *applicationautoscaling.PutScalingPolicyInput {
	return &applicationautoscaling.PutScalingPolicyInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String("service/core-services/payments-api"), PolicyName: aws.String(name),
		PolicyType: astypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &astypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(target),
			PredefinedMetricSpecification: &astypes.PredefinedMetricSpecification{
				PredefinedMetricType: astypes.MetricTypeECSServiceAverageCPUUtilization},
		},
	}
}
