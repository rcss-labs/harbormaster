package fake

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// autoScalingClient is the fake Application Auto Scaling API scoped to one
// account and region. It answers from Estate.ScalingConfig for the ECS
// namespace; resource ids look like "service/<cluster>/<service>".
type autoScalingClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.AutoScalingAPI = (*autoScalingClient)(nil)

const scalableDimension = astypes.ScalableDimensionECSServiceDesiredCount

func resourceID(key model.ServiceKey) string { return "service/" + key.Cluster + "/" + key.Service }

// scaledServices returns the in-scope seeds matching the requested namespace
// and resource ids. Caller holds Mu.
func (c *autoScalingClient) scaledServices(namespace astypes.ServiceNamespace, resourceIDs []string) []model.ServiceKey {
	if namespace != astypes.ServiceNamespaceEcs {
		return nil
	}
	var keys []model.ServiceKey
	for key := range c.f.Estate.ScalingConfig {
		if key.AccountID != c.target.AccountID || key.Region != c.target.Region {
			continue
		}
		if len(resourceIDs) > 0 && !hasString(resourceIDs, resourceID(key)) {
			continue
		}
		keys = append(keys, key)
	}
	sortServiceKeys(keys)
	return keys
}

func hasString(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

func (c *autoScalingClient) DescribeScalableTargets(_ context.Context, in *applicationautoscaling.DescribeScalableTargetsInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalableTargetsOutput, error) {
	if err := c.f.beginThrottled("DescribeScalableTargets", &c.f.ThrottleAutoScaling); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := &applicationautoscaling.DescribeScalableTargetsOutput{}
	for _, key := range c.scaledServices(in.ServiceNamespace, in.ResourceIds) {
		cfg := e.ScalingConfig[key]
		out.ScalableTargets = append(out.ScalableTargets, astypes.ScalableTarget{
			ServiceNamespace:  astypes.ServiceNamespaceEcs,
			ResourceId:        aws.String(resourceID(key)),
			ScalableDimension: scalableDimension,
			MinCapacity:       aws.Int32(int32(cfg.Min)),
			MaxCapacity:       aws.Int32(int32(cfg.Max)),
		})
	}
	return out, nil
}

func (c *autoScalingClient) DescribeScalingPolicies(_ context.Context, in *applicationautoscaling.DescribeScalingPoliciesInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingPoliciesOutput, error) {
	if err := c.f.beginThrottled("DescribeScalingPolicies", &c.f.ThrottleAutoScaling); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := &applicationautoscaling.DescribeScalingPoliciesOutput{}
	for _, key := range c.scaledServices(in.ServiceNamespace, singleton(in.ResourceId)) {
		cfg := e.ScalingConfig[key]
		out.ScalingPolicies = append(out.ScalingPolicies, astypes.ScalingPolicy{
			PolicyName:        aws.String("cpu-target"),
			PolicyARN:         aws.String("arn:aws:autoscaling:" + key.Region + ":" + key.AccountID + ":scalingPolicy:" + hexOf(hashSeed(key.Service, 5)) + ":resource/ecs/" + resourceID(key) + ":policyName/cpu-target"),
			PolicyType:        astypes.PolicyTypeTargetTrackingScaling,
			ServiceNamespace:  astypes.ServiceNamespaceEcs,
			ResourceId:        aws.String(resourceID(key)),
			ScalableDimension: scalableDimension,
			TargetTrackingScalingPolicyConfiguration: &astypes.TargetTrackingScalingPolicyConfiguration{
				TargetValue: aws.Float64(float64(cfg.TargetCPU)),
				PredefinedMetricSpecification: &astypes.PredefinedMetricSpecification{
					PredefinedMetricType: astypes.MetricTypeECSServiceAverageCPUUtilization,
				},
				ScaleOutCooldown: aws.Int32(scaleOutCooldownSeconds),
				ScaleInCooldown:  aws.Int32(scaleInCooldownSeconds),
			},
		})
	}
	return out, nil
}

func (c *autoScalingClient) DescribeScheduledActions(_ context.Context, in *applicationautoscaling.DescribeScheduledActionsInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScheduledActionsOutput, error) {
	if err := c.f.beginThrottled("DescribeScheduledActions", &c.f.ThrottleAutoScaling); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := &applicationautoscaling.DescribeScheduledActionsOutput{}
	for _, key := range c.scaledServices(in.ServiceNamespace, singleton(in.ResourceId)) {
		for i, entry := range e.ScalingConfig[key].Scheduled {
			schedule, minCapacity, _ := strings.Cut(entry, "=")
			minValue, _ := strconv.Atoi(minCapacity)
			out.ScheduledActions = append(out.ScheduledActions, astypes.ScheduledAction{
				ScheduledActionName:  aws.String("scheduled-" + itoa(i+1)),
				ScheduledActionARN:   aws.String("arn:aws:autoscaling:" + key.Region + ":" + key.AccountID + ":scheduledAction:" + resourceID(key) + ":scheduledActionName/scheduled-" + itoa(i+1)),
				ServiceNamespace:     astypes.ServiceNamespaceEcs,
				Schedule:             aws.String(schedule),
				ResourceId:           aws.String(resourceID(key)),
				ScalableDimension:    scalableDimension,
				ScalableTargetAction: &astypes.ScalableTargetAction{MinCapacity: aws.Int32(int32(minValue))},
			})
		}
	}
	return out, nil
}

// DescribeScalingActivities returns one activity per scaled service, five
// hours old.
func (c *autoScalingClient) DescribeScalingActivities(_ context.Context, in *applicationautoscaling.DescribeScalingActivitiesInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingActivitiesOutput, error) {
	if err := c.f.beginThrottled("DescribeScalingActivities", &c.f.ThrottleAutoScaling); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := &applicationautoscaling.DescribeScalingActivitiesOutput{}
	started := e.now().Add(-5 * time.Hour)
	for _, key := range c.scaledServices(in.ServiceNamespace, singleton(in.ResourceId)) {
		_, desired := e.runningAndDesired(key)
		out.ScalingActivities = append(out.ScalingActivities, astypes.ScalingActivity{
			ActivityId:        aws.String(hexOf(hashSeed(key.Service, 9))),
			ServiceNamespace:  astypes.ServiceNamespaceEcs,
			ResourceId:        aws.String(resourceID(key)),
			ScalableDimension: scalableDimension,
			Description:       aws.String("Setting desired count to " + itoa(desired) + "."),
			Cause:             aws.String("monitor alarm cpu-target-alarm in state ALARM triggered policy cpu-target"),
			StartTime:         aws.Time(started),
			EndTime:           aws.Time(started.Add(time.Minute)),
			StatusCode:        astypes.ScalingActivityStatusCodeSuccessful,
			StatusMessage:     aws.String("Successfully set desired count to " + itoa(desired) + ". Change successfully fulfilled by ecs."),
		})
	}
	return out, nil
}

func singleton(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	return []string{*s}
}
