package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

var homelabEUWest1 = awsx.Target{AccountID: homelabID, Region: "eu-west-1"}

func TestFakeTargetHealth_CatalogAPIHasOneUnhealthyTarget(t *testing.T) {
	f := newFake()
	api, err := f.ELB(context.Background(), homelabEUWest1)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := api.DescribeTargetGroups(context.Background(), &elasticloadbalancingv2.DescribeTargetGroupsInput{})
	if err != nil || len(groups.TargetGroups) != 1 || aws.ToString(groups.TargetGroups[0].TargetGroupName) != "catalog-api-tg" {
		t.Fatalf("groups = %+v, err %v; want only catalog-api-tg", groups, err)
	}
	health, err := api.DescribeTargetHealth(context.Background(), &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: groups.TargetGroups[0].TargetGroupArn})
	if err != nil {
		t.Fatal(err)
	}
	var healthy, unhealthy int
	for _, d := range health.TargetHealthDescriptions {
		if d.TargetHealth.State == elbtypes.TargetHealthStateEnumHealthy {
			healthy++
			continue
		}
		unhealthy++
		if d.TargetHealth.Reason != elbtypes.TargetHealthReasonEnumResponseCodeMismatch ||
			aws.ToString(d.TargetHealth.Description) != "Health checks failed with these codes: [503]" {
			t.Fatalf("unhealthy target = %+v", d.TargetHealth)
		}
	}
	if healthy != 1 || unhealthy != 1 {
		t.Fatalf("healthy/unhealthy = %d/%d, want 1/1", healthy, unhealthy)
	}
}

func TestFakeTargetGroups_AddAndRemoveSecondGroup(t *testing.T) {
	f := newFake()
	key := serviceKeyOf(homelabID, "us-east-1", "core-services", "payments-api")
	f.Estate.AddTargetGroup(key, TargetGroup{Healthy: 3})
	api, _ := f.ELB(context.Background(), homelabUSEast1)
	all, err := api.DescribeTargetGroups(context.Background(), &elasticloadbalancingv2.DescribeTargetGroupsInput{})
	// payments-api, web-frontend, auth-service, jenkins-controller + the added group.
	if err != nil || len(all.TargetGroups) != 5 {
		t.Fatalf("groups = %+v (err %v), want 5", all, err)
	}
	second := f.Estate.ExtraTargetGroups[key][0].ARN
	if _, err := api.DescribeTargetHealth(context.Background(), &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(second)}); err != nil {
		t.Fatal(err)
	}
	f.Estate.RemoveTargetGroup(second)
	_, err = api.DescribeTargetHealth(context.Background(), &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(second)})
	var notFound *elbtypes.TargetGroupNotFoundException
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want TargetGroupNotFoundException", err)
	}
}

func TestFakeELB_ThrottleFlag(t *testing.T) {
	f := newFake()
	f.ThrottleELB = 1
	api, _ := f.ELB(context.Background(), homelabUSEast1)
	if _, err := api.DescribeTargetGroups(context.Background(), &elasticloadbalancingv2.DescribeTargetGroupsInput{}); !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want throttle", err)
	}
	if got := f.Calls()["DescribeTargetGroups"]; got != 1 {
		t.Fatalf("calls = %d, want 1 (throttled calls count)", got)
	}
}

func TestFakeAutoScaling_PaymentsAPISetup(t *testing.T) {
	api, err := newFake().AutoScaling(context.Background(), homelabUSEast1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := aws.String("service/core-services/payments-api")
	targets, err := api.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceIds: []string{*id}})
	if err != nil || len(targets.ScalableTargets) != 1 || aws.ToInt32(targets.ScalableTargets[0].MinCapacity) != 2 || aws.ToInt32(targets.ScalableTargets[0].MaxCapacity) != 6 {
		t.Fatalf("targets = %+v, err %v; want payments-api 2..6", targets, err)
	}
	policies, err := api.DescribeScalingPolicies(ctx, &applicationautoscaling.DescribeScalingPoliciesInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceId: id})
	if err != nil || len(policies.ScalingPolicies) != 1 {
		t.Fatalf("policies = %+v, err %v; want 1", policies, err)
	}
	cfg := policies.ScalingPolicies[0].TargetTrackingScalingPolicyConfiguration
	if aws.ToFloat64(cfg.TargetValue) != 60 || aws.ToInt32(cfg.ScaleOutCooldown) != 60 || aws.ToInt32(cfg.ScaleInCooldown) != 300 {
		t.Fatalf("policies = %+v", policies.ScalingPolicies)
	}
	scheduled, err := api.DescribeScheduledActions(ctx, &applicationautoscaling.DescribeScheduledActionsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceId: id})
	if err != nil || len(scheduled.ScheduledActions) != 2 || aws.ToString(scheduled.ScheduledActions[0].Schedule) != "cron(0 7 ? * MON-FRI *)" ||
		aws.ToInt32(scheduled.ScheduledActions[0].ScalableTargetAction.MinCapacity) != 3 || aws.ToInt32(scheduled.ScheduledActions[1].ScalableTargetAction.MinCapacity) != 2 {
		t.Fatalf("scheduled = %+v", scheduled.ScheduledActions)
	}
	activities, err := api.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceId: id})
	if err != nil || len(activities.ScalingActivities) != 1 || aws.ToString(activities.ScalingActivities[0].StatusMessage) != "Successfully set desired count to 3. Change successfully fulfilled by ecs." {
		t.Fatalf("activities = %+v", activities.ScalingActivities)
	}
	other, err := api.DescribeScalableTargets(ctx, &applicationautoscaling.DescribeScalableTargetsInput{ServiceNamespace: astypes.ServiceNamespaceEcs, ResourceIds: []string{"service/background-jobs/email-sender"}})
	if err != nil || len(other.ScalableTargets) != 0 {
		t.Fatalf("email-sender has scalable targets: %+v", other.ScalableTargets)
	}
}
