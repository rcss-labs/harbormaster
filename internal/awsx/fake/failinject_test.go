package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/smithy-go"
)

func TestFake_FailInjection(t *testing.T) {
	t.Setenv("HM_FAKE_FAIL", "scheduled_actions, target_health")
	f := newFake()
	api := scalingAPI(t, f)
	scheduled := &applicationautoscaling.DescribeScheduledActionsInput{ServiceNamespace: astypes.ServiceNamespaceEcs}

	// persistent: fails on every call, not just the next one
	for i := 0; i < 3; i++ {
		_, err := api.DescribeScheduledActions(context.Background(), scheduled)
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "UnsupportedOperation" {
			t.Fatalf("call %d: err = %v, want an UnsupportedOperation API error", i, err)
		}
	}
	// other operations are unaffected
	if _, err := api.DescribeScalingPolicies(context.Background(), &applicationautoscaling.DescribeScalingPoliciesInput{ServiceNamespace: astypes.ServiceNamespaceEcs}); err != nil {
		t.Fatalf("DescribeScalingPolicies: %v", err)
	}
	elb, err := f.ELB(context.Background(), homelabUSEast1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := elb.DescribeTargetHealth(context.Background(), &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String("arn")}); err == nil {
		t.Fatal("DescribeTargetHealth succeeded with target_health injected")
	}
}

func TestFake_FailInjectionScalingTierAndDefault(t *testing.T) {
	t.Setenv("HM_FAKE_FAIL", "scaling_tier")
	scheduled := &applicationautoscaling.DescribeScheduledActionsInput{ServiceNamespace: astypes.ServiceNamespaceEcs}
	if _, err := scalingAPI(t, newFake()).DescribeScheduledActions(context.Background(), scheduled); err == nil {
		t.Fatal("scaling_tier did not fail DescribeScheduledActions")
	}
	t.Setenv("HM_FAKE_FAIL", "")
	if _, err := scalingAPI(t, newFake()).DescribeScheduledActions(context.Background(), scheduled); err != nil {
		t.Fatalf("unset HM_FAKE_FAIL must not inject failures: %v", err)
	}
}
