package deploy

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
)

// applyScaling runs the Application Auto Scaling writes in a fixed order: register the target (a
// raised minimum scales the service, so this comes after the service step), delete scheduled
// actions, delete policies, put policies, put scheduled actions, and deregister last (which drops
// whatever policies and actions remain). It stops at the first failure and returns what had been
// done before it, as short descriptions for the operations log.
func applyScaling(ctx context.Context, api awsx.AutoScalingAPI, key model.ServiceKey, change ScalingChange, st DeploySettings) (done []string, err error) {
	resourceID := "service/" + key.Cluster + "/" + key.Service
	step := func(description string, call func() error) bool {
		if err = call(); err != nil {
			err = wrapAWS(err)
			return false
		}
		done = append(done, description)
		return true
	}

	if target := change.Target; target != nil {
		if !step(fmt.Sprintf("set the range to %d–%d", target.Min, target.Max), func() error {
			_, err := api.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
				ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
				ResourceId: aws.String(resourceID), MinCapacity: aws.Int32(int32(target.Min)), MaxCapacity: aws.Int32(int32(target.Max)),
			})
			return err
		}) {
			return done, err
		}
	}
	for _, name := range change.DeleteScheduledActions {
		if !step(fmt.Sprintf("deleted scheduled action %s", name), func() error {
			_, err := api.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
				ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
				ResourceId: aws.String(resourceID), ScheduledActionName: aws.String(name)})
			return err
		}) {
			return done, err
		}
	}
	for _, name := range change.DeletePolicies {
		if !step(fmt.Sprintf("deleted policy %s", name), func() error {
			_, err := api.DeleteScalingPolicy(ctx, &applicationautoscaling.DeleteScalingPolicyInput{
				ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
				ResourceId: aws.String(resourceID), PolicyName: aws.String(name)})
			return err
		}) {
			return done, err
		}
	}
	for _, policy := range change.PutPolicies {
		if !step(fmt.Sprintf("set policy %s", policy.Name), func() error {
			_, err := api.PutScalingPolicy(ctx, putPolicyInput(resourceID, policy, st))
			return err
		}) {
			return done, err
		}
	}
	for _, action := range change.PutScheduledActions {
		if !step(fmt.Sprintf("set scheduled action %s", action.Name), func() error {
			_, err := api.PutScheduledAction(ctx, putScheduledActionInput(resourceID, action))
			return err
		}) {
			return done, err
		}
	}
	if change.RemoveTarget {
		step("removed the scalable target", func() error {
			_, err := api.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
				ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
				ResourceId: aws.String(resourceID)})
			return err
		})
	}
	return done, err
}

func putPolicyInput(resourceID string, policy PolicyChange, st DeploySettings) *applicationautoscaling.PutScalingPolicyInput {
	specification := &astypes.PredefinedMetricSpecification{PredefinedMetricType: predefinedMetric(policy.Metric)}
	if policy.Metric == "alb" {
		label, _ := albLabelFor(st, policy.TargetGroupARN)
		specification.ResourceLabel = aws.String(label)
	}
	return &applicationautoscaling.PutScalingPolicyInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(resourceID), PolicyName: aws.String(policy.Name), PolicyType: astypes.PolicyTypeTargetTrackingScaling,
		TargetTrackingScalingPolicyConfiguration: &astypes.TargetTrackingScalingPolicyConfiguration{
			TargetValue:                   aws.Float64(policy.TargetValue),
			PredefinedMetricSpecification: specification,
			ScaleInCooldown:               aws.Int32(int32(policy.ScaleInCooldown)),
			ScaleOutCooldown:              aws.Int32(int32(policy.ScaleOutCooldown)),
			DisableScaleIn:                aws.Bool(policy.DisableScaleIn),
		},
	}
}

func putScheduledActionInput(resourceID string, action ScheduledActionChange) *applicationautoscaling.PutScheduledActionInput {
	capacity := &astypes.ScalableTargetAction{}
	if action.MinCapacity != nil {
		capacity.MinCapacity = aws.Int32(int32(*action.MinCapacity))
	}
	if action.MaxCapacity != nil {
		capacity.MaxCapacity = aws.Int32(int32(*action.MaxCapacity))
	}
	input := &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace: astypes.ServiceNamespaceEcs, ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId: aws.String(resourceID), ScheduledActionName: aws.String(action.Name), Schedule: aws.String(action.Schedule),
		ScalableTargetAction: capacity,
	}
	if action.Timezone != "" {
		input.Timezone = aws.String(action.Timezone)
	}
	return input
}

// refreshStoredScaling re-reads the service's scaling state and stores it, so the Scaling tab shows
// the change without waiting for the poller.
func (s *Service) refreshStoredScaling(ctx context.Context, api awsx.AutoScalingAPI, key model.ServiceKey) error {
	row, err := poller.CollectServiceScaling(ctx, api, key)
	if err != nil {
		return err
	}
	return s.Store.UpsertScaling(ctx, []model.Scaling{row})
}
