package poller

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// scalingActivityLimit is how many recent scaling activities are stored per scaled service.
const scalingActivityLimit = 10

// ScalingLoop is the 60 s tier that stores each service's Application Auto Scaling state.
type ScalingLoop struct {
	Store   *store.Store
	Clients awsx.Clients
	Every   time.Duration
	Now     func() time.Time
}

func (l *ScalingLoop) Name() string            { return tierScaling }
func (l *ScalingLoop) Interval() time.Duration { return l.Every }

// Run stores the scaling state of every service of the target. Services without a scalable
// target are stored with HasTarget false.
func (l *ScalingLoop) Run(ctx context.Context, t awsx.Target) error {
	services, err := l.Store.ListServices(ctx, store.TargetFilter{AccountID: t.AccountID, Region: t.Region}, "", store.Visibility{Admin: true})
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	api, err := l.Clients.AutoScaling(ctx, t)
	if err != nil {
		return err
	}
	targets, err := describeScalableTargets(ctx, api, "")
	if err != nil {
		return err
	}
	policies, err := describeScalingPolicies(ctx, api, "")
	if err != nil {
		return err
	}
	scheduled, err := describeScheduledActions(ctx, api, "")
	if err != nil {
		return err
	}

	now := observeNow(l.Now)
	rows := make([]model.Scaling, 0, len(services))
	for _, svc := range services {
		resourceID := scalingResourceID(svc.Key)
		row := model.Scaling{Key: svc.Key, Policies: policies[resourceID], Scheduled: scheduled[resourceID], PolledAt: now}
		if target, ok := targets[resourceID]; ok {
			row.HasTarget = true
			row.Min = int(aws.ToInt32(target.MinCapacity))
			row.Max = int(aws.ToInt32(target.MaxCapacity))
			activities, err := describeScalingActivities(ctx, api, resourceID)
			if err != nil {
				return err
			}
			row.Activities = activities
		}
		rows = append(rows, row)
	}
	return l.Store.UpsertScaling(ctx, rows)
}

// scalingResourceID is the Application Auto Scaling resource id of an ECS service.
func scalingResourceID(key model.ServiceKey) string {
	return "service/" + key.Cluster + "/" + key.Service
}

// The describe helpers list every service of the account and region, or only resourceID when it is not empty.
func describeScalableTargets(ctx context.Context, api awsx.AutoScalingAPI, resourceID string) (map[string]astypes.ScalableTarget, error) {
	targets := map[string]astypes.ScalableTarget{}
	in := &applicationautoscaling.DescribeScalableTargetsInput{ServiceNamespace: astypes.ServiceNamespaceEcs}
	if resourceID != "" {
		in.ResourceIds = []string{resourceID}
	}
	for {
		out, err := api.DescribeScalableTargets(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("describe scalable targets: %w", err)
		}
		for _, target := range out.ScalableTargets {
			targets[aws.ToString(target.ResourceId)] = target
		}
		if out.NextToken == nil {
			return targets, nil
		}
		in.NextToken = out.NextToken
	}
}

func describeScalingPolicies(ctx context.Context, api awsx.AutoScalingAPI, resourceID string) (map[string][]model.ScalingPolicy, error) {
	policies := map[string][]model.ScalingPolicy{}
	in := &applicationautoscaling.DescribeScalingPoliciesInput{ServiceNamespace: astypes.ServiceNamespaceEcs}
	if resourceID != "" {
		in.ResourceId = aws.String(resourceID)
	}
	for {
		out, err := api.DescribeScalingPolicies(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("describe scaling policies: %w", err)
		}
		for _, policy := range out.ScalingPolicies {
			resourceID := aws.ToString(policy.ResourceId)
			policies[resourceID] = append(policies[resourceID], normalizeScalingPolicy(policy))
		}
		if out.NextToken == nil {
			return policies, nil
		}
		in.NextToken = out.NextToken
	}
}

// normalizeScalingPolicy reduces a policy to the metric, target and cooldowns the UI shows. A step
// scaling policy has one cooldown, which applies to both directions.
func normalizeScalingPolicy(policy astypes.ScalingPolicy) model.ScalingPolicy {
	row := model.ScalingPolicy{Name: aws.ToString(policy.PolicyName), Type: string(policy.PolicyType), Raw: rawJSON(policy)}
	if tracking := policy.TargetTrackingScalingPolicyConfiguration; tracking != nil {
		row.TargetValue = aws.ToFloat64(tracking.TargetValue)
		row.ScaleOutCooldown = int(aws.ToInt32(tracking.ScaleOutCooldown))
		row.ScaleInCooldown = int(aws.ToInt32(tracking.ScaleInCooldown))
		if predefined := tracking.PredefinedMetricSpecification; predefined != nil {
			row.Metric = string(predefined.PredefinedMetricType)
		} else if custom := tracking.CustomizedMetricSpecification; custom != nil {
			row.Metric = aws.ToString(custom.MetricName)
		}
	}
	if step := policy.StepScalingPolicyConfiguration; step != nil {
		row.ScaleOutCooldown = int(aws.ToInt32(step.Cooldown))
		row.ScaleInCooldown = row.ScaleOutCooldown
	}
	return row
}

func describeScheduledActions(ctx context.Context, api awsx.AutoScalingAPI, resourceID string) (map[string][]model.ScheduledAction, error) {
	actions := map[string][]model.ScheduledAction{}
	in := &applicationautoscaling.DescribeScheduledActionsInput{ServiceNamespace: astypes.ServiceNamespaceEcs}
	if resourceID != "" {
		in.ResourceId = aws.String(resourceID)
	}
	for {
		out, err := api.DescribeScheduledActions(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("describe scheduled actions: %w", err)
		}
		for _, action := range out.ScheduledActions {
			resourceID := aws.ToString(action.ResourceId)
			actions[resourceID] = append(actions[resourceID], normalizeScheduledAction(action))
		}
		if out.NextToken == nil {
			return actions, nil
		}
		in.NextToken = out.NextToken
	}
}

func normalizeScheduledAction(action astypes.ScheduledAction) model.ScheduledAction {
	row := model.ScheduledAction{
		Name:     aws.ToString(action.ScheduledActionName),
		Schedule: aws.ToString(action.Schedule),
		Timezone: aws.ToString(action.Timezone),
		Raw:      rawJSON(action),
	}
	if capacity := action.ScalableTargetAction; capacity != nil {
		if capacity.MinCapacity != nil {
			minimum := int(*capacity.MinCapacity)
			row.MinCapacity = &minimum
		}
		if capacity.MaxCapacity != nil {
			maximum := int(*capacity.MaxCapacity)
			row.MaxCapacity = &maximum
		}
	}
	return row
}

func describeScalingActivities(ctx context.Context, api awsx.AutoScalingAPI, resourceID string) ([]model.ScalingActivity, error) {
	out, err := api.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{
		ServiceNamespace:  astypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: astypes.ScalableDimensionECSServiceDesiredCount,
		MaxResults:        aws.Int32(scalingActivityLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("describe scaling activities: %w", err)
	}
	activities := make([]model.ScalingActivity, 0, len(out.ScalingActivities))
	for _, activity := range out.ScalingActivities {
		row := model.ScalingActivity{
			ID:            aws.ToString(activity.ActivityId),
			Description:   aws.ToString(activity.Description),
			Cause:         aws.ToString(activity.Cause),
			StatusCode:    string(activity.StatusCode),
			StatusMessage: aws.ToString(activity.StatusMessage),
			StartedAt:     aws.ToTime(activity.StartTime),
		}
		if activity.EndTime != nil {
			ended := *activity.EndTime
			row.EndedAt = &ended
		}
		activities = append(activities, row)
	}
	return activities, nil
}

// CollectServiceScalingConfig reads one service's scalable target, policies and scheduled actions
// live, without the activities call.
func CollectServiceScalingConfig(ctx context.Context, api awsx.AutoScalingAPI, key model.ServiceKey) (model.Scaling, error) {
	resourceID := scalingResourceID(key)
	targets, err := describeScalableTargets(ctx, api, resourceID)
	if err != nil {
		return model.Scaling{}, err
	}
	policies, err := describeScalingPolicies(ctx, api, resourceID)
	if err != nil {
		return model.Scaling{}, err
	}
	scheduled, err := describeScheduledActions(ctx, api, resourceID)
	if err != nil {
		return model.Scaling{}, err
	}
	row := model.Scaling{Key: key, Policies: policies[resourceID], Scheduled: scheduled[resourceID], PolledAt: time.Now().UTC()}
	if target, ok := targets[resourceID]; ok {
		row.HasTarget = true
		row.Min = int(aws.ToInt32(target.MinCapacity))
		row.Max = int(aws.ToInt32(target.MaxCapacity))
	}
	return row, nil
}

// Names of the parts of a service's scaling configuration that can fail independently.
const (
	PartScalingTarget    = "scaling_target"
	PartScalingPolicies  = "scaling_policies"
	PartScheduledActions = "scheduled_actions"
)

// PartError is a part of a composite read that failed while the others succeeded.
type PartError struct {
	Part string
	Err  error
}

// CollectServiceScalingConfigPartial is CollectServiceScalingConfig that keeps going when one read
// fails: the part that failed stays at its zero value and is listed in the returned errors. A
// cancelled context is not a part failure; callers check ctx.Err() themselves.
func CollectServiceScalingConfigPartial(ctx context.Context, api awsx.AutoScalingAPI, key model.ServiceKey) (model.Scaling, []PartError) {
	resourceID := scalingResourceID(key)
	row := model.Scaling{Key: key, PolledAt: time.Now().UTC()}
	var failed []PartError
	if targets, err := describeScalableTargets(ctx, api, resourceID); err != nil {
		failed = append(failed, PartError{PartScalingTarget, err})
	} else if target, ok := targets[resourceID]; ok {
		row.HasTarget = true
		row.Min = int(aws.ToInt32(target.MinCapacity))
		row.Max = int(aws.ToInt32(target.MaxCapacity))
	}
	if policies, err := describeScalingPolicies(ctx, api, resourceID); err != nil {
		failed = append(failed, PartError{PartScalingPolicies, err})
	} else {
		row.Policies = policies[resourceID]
	}
	if scheduled, err := describeScheduledActions(ctx, api, resourceID); err != nil {
		failed = append(failed, PartError{PartScheduledActions, err})
	} else {
		row.Scheduled = scheduled[resourceID]
	}
	return row, failed
}

// CollectServiceScaling is CollectServiceScalingConfig plus the recent activities of a scaled
// service: the row the poller would store, for writers that store it right after a change.
func CollectServiceScaling(ctx context.Context, api awsx.AutoScalingAPI, key model.ServiceKey) (model.Scaling, error) {
	row, err := CollectServiceScalingConfig(ctx, api, key)
	if err != nil || !row.HasTarget {
		return row, err
	}
	row.Activities, err = describeScalingActivities(ctx, api, scalingResourceID(key))
	return row, err
}
