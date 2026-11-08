package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const emailSenderResource = "service/background-jobs/email-sender"

func scalingAPI(t *testing.T, f *Fake) awsx.AutoScalingAPI {
	t.Helper()
	api, err := f.AutoScaling(context.Background(), homelabUSEast1)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func registerTarget(t *testing.T, api awsx.AutoScalingAPI, resource string, min, max int32) {
	t.Helper()
	_, err := api.RegisterScalableTarget(context.Background(), &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(resource), MinCapacity: aws.Int32(min), MaxCapacity: aws.Int32(max)})
	if err != nil {
		t.Fatal(err)
	}
}

func cpuPolicyInput(resource, name string, target float64) *applicationautoscaling.PutScalingPolicyInput {
	return &applicationautoscaling.PutScalingPolicyInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(resource), PolicyName: aws.String(name), PolicyType: astypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &astypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue: aws.Float64(target), ScaleInCooldown: aws.Int32(120), ScaleOutCooldown: aws.Int32(30),
			PredefinedMetricSpecification: &astypes.PredefinedMetricSpecification{PredefinedMetricType: astypes.MetricTypeECSServiceAverageCPUUtilization}}}
}

func describePolicies(api awsx.AutoScalingAPI, resource string) []astypes.ScalingPolicy {
	out, _ := api.DescribeScalingPolicies(context.Background(), &applicationautoscaling.DescribeScalingPoliciesInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceId: aws.String(resource)})
	return out.ScalingPolicies
}

func describeActions(api awsx.AutoScalingAPI, resource string) []astypes.ScheduledAction {
	out, _ := api.DescribeScheduledActions(context.Background(), &applicationautoscaling.DescribeScheduledActionsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceId: aws.String(resource)})
	return out.ScheduledActions
}

func TestFakeAutoScaling_RegisterThenDescribe(t *testing.T) {
	ctx := context.Background()
	api := scalingAPI(t, newFake())
	registerTarget(t, api, emailSenderResource, 1, 4)

	if _, err := api.PutScalingPolicy(ctx, cpuPolicyInput(emailSenderResource, "cpu-50", 50)); err != nil {
		t.Fatal(err)
	}
	// Putting the same name again updates in place.
	if _, err := api.PutScalingPolicy(ctx, cpuPolicyInput(emailSenderResource, "cpu-50", 55)); err != nil {
		t.Fatal(err)
	}
	_, err := api.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), ScheduledActionName: aws.String("night"),
		Schedule: aws.String("cron(0 22 * * ? *)"), Timezone: aws.String("Europe/Berlin"),
		ScalableTargetAction: &astypes.ScalableTargetAction{MinCapacity: aws.Int32(1), MaxCapacity: aws.Int32(2)}})
	if err != nil {
		t.Fatal(err)
	}

	targets, err := api.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceIds: []string{emailSenderResource}})
	if err != nil || len(targets.ScalableTargets) != 1 || aws.ToInt32(targets.ScalableTargets[0].MinCapacity) != 1 || aws.ToInt32(targets.ScalableTargets[0].MaxCapacity) != 4 {
		t.Fatalf("targets = %+v, err %v; want 1..4", targets, err)
	}
	policies := describePolicies(api, emailSenderResource)
	if len(policies) != 1 {
		t.Fatalf("policies = %+v; want exactly one (upsert)", policies)
	}
	cfg := policies[0].TargetTrackingScalingPolicyConfiguration
	if aws.ToString(policies[0].PolicyName) != "cpu-50" || aws.ToFloat64(cfg.TargetValue) != 55 ||
		aws.ToInt32(cfg.ScaleInCooldown) != 120 || aws.ToInt32(cfg.ScaleOutCooldown) != 30 ||
		cfg.PredefinedMetricSpecification.PredefinedMetricType != astypes.MetricTypeECSServiceAverageCPUUtilization {
		t.Fatalf("policy = %+v", policies[0])
	}
	actions := describeActions(api, emailSenderResource)
	if len(actions) != 1 {
		t.Fatalf("actions = %+v", actions)
	}
	a := actions[0]
	if aws.ToString(a.ScheduledActionName) != "night" || aws.ToString(a.Schedule) != "cron(0 22 * * ? *)" || aws.ToString(a.Timezone) != "Europe/Berlin" ||
		aws.ToInt32(a.ScalableTargetAction.MinCapacity) != 1 || aws.ToInt32(a.ScalableTargetAction.MaxCapacity) != 2 {
		t.Fatalf("action = %+v", a)
	}

	if _, err := api.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), PolicyName: aws.String("cpu-50")}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), ScheduledActionName: aws.String("night")}); err != nil {
		t.Fatal(err)
	}
	if p, a := describePolicies(api, emailSenderResource), describeActions(api, emailSenderResource); len(p) != 0 || len(a) != 0 {
		t.Fatalf("after delete: policies %+v actions %+v", p, a)
	}
}

func TestFakeAutoScaling_PutPolicyWithoutTargetFails(t *testing.T) {
	ctx := context.Background()
	api := scalingAPI(t, newFake())
	var notFound *astypes.ObjectNotFoundException
	if _, err := api.PutScalingPolicy(ctx, cpuPolicyInput(emailSenderResource, "cpu-50", 50)); !errors.As(err, &notFound) {
		t.Fatalf("PutScalingPolicy without target: err = %v, want ObjectNotFoundException", err)
	}
	_, err := api.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), ScheduledActionName: aws.String("x"), Schedule: aws.String("rate(1 hour)"),
		ScalableTargetAction: &astypes.ScalableTargetAction{MinCapacity: aws.Int32(1)}})
	if !errors.As(err, &notFound) {
		t.Fatalf("PutScheduledAction without target: err = %v, want ObjectNotFoundException", err)
	}

	registerTarget(t, api, emailSenderResource, 1, 4)
	if _, err := api.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), PolicyName: aws.String("nope")}); !errors.As(err, &notFound) {
		t.Fatalf("DeleteScalingPolicy unknown: err = %v, want ObjectNotFoundException", err)
	}
	if _, err := api.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(emailSenderResource), ScheduledActionName: aws.String("nope")}); !errors.As(err, &notFound) {
		t.Fatalf("DeleteScheduledAction unknown: err = %v, want ObjectNotFoundException", err)
	}

	alb := cpuPolicyInput(emailSenderResource, "alb", 100)
	alb.TargetTrackingScalingPolicyConfiguration.PredefinedMetricSpecification = &astypes.PredefinedMetricSpecification{
		PredefinedMetricType: astypes.MetricTypeALBRequestCountPerTarget}
	var invalid *astypes.ValidationException
	if _, err := api.PutScalingPolicy(ctx, alb); !errors.As(err, &invalid) {
		t.Fatalf("ALB policy without ResourceLabel: err = %v, want ValidationException", err)
	}
}

func TestFakeAutoScaling_DeregisterDropsPoliciesAndActions(t *testing.T) {
	ctx := context.Background()
	api := scalingAPI(t, newFake())
	const resource = "service/core-services/payments-api"
	deregister := &applicationautoscaling.DeregisterScalableTargetInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(resource)}
	if _, err := api.DeregisterScalableTarget(ctx, deregister); err != nil {
		t.Fatal(err)
	}
	targets, _ := api.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceIds: []string{resource}})
	policies, actions := describePolicies(api, resource), describeActions(api, resource)
	if len(targets.ScalableTargets)+len(policies)+len(actions) != 0 {
		t.Fatalf("after deregister: targets %d policies %d actions %d, want all 0", len(targets.ScalableTargets), len(policies), len(actions))
	}
	var notFound *astypes.ObjectNotFoundException
	if _, err := api.DeregisterScalableTarget(ctx, deregister); !errors.As(err, &notFound) {
		t.Fatalf("second deregister: err = %v, want ObjectNotFoundException", err)
	}
}

func TestFakeAutoScaling_AdvanceClampsDesiredIntoRange(t *testing.T) {
	f := newFake()
	api := scalingAPI(t, f)
	const resource = "service/core-services/payments-api"

	registerTarget(t, api, resource, 5, 6) // desired is 3
	f.Estate.Advance()
	svc := describe(t, f, "core-services", "payments-api")
	if svc.DesiredCount != 5 || primaryOf(svc).DesiredCount != 5 {
		t.Fatalf("desired after raising min = %d (primary %d), want 5", svc.DesiredCount, primaryOf(svc).DesiredCount)
	}

	registerTarget(t, api, resource, 1, 2)
	f.Estate.Advance()
	if got := describe(t, f, "core-services", "payments-api").DesiredCount; got != 2 {
		t.Fatalf("desired after lowering max = %d, want 2", got)
	}
}

func TestFakeRegisterScalableTarget_ClampsDesired(t *testing.T) {
	t.Run("max 0 stops the service", func(t *testing.T) {
		f := newFake()
		registerTarget(t, scalingAPI(t, f), emailSenderResource, 0, 0)
		if got := describe(t, f, "background-jobs", "email-sender").DesiredCount; got != 0 {
			t.Fatalf("desired after max 0 = %d, want 0", got)
		}
	})
	t.Run("min above desired raises it", func(t *testing.T) {
		f := newFake()
		registerTarget(t, scalingAPI(t, f), emailSenderResource, 3, 4)
		svc := describe(t, f, "background-jobs", "email-sender")
		if svc.DesiredCount != 3 || primaryOf(svc).DesiredCount != 3 {
			t.Fatalf("desired after min 3 = %d (primary %d), want 3", svc.DesiredCount, primaryOf(svc).DesiredCount)
		}
	})
}
