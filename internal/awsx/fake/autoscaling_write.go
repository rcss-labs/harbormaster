package fake

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The write half of the fake Application Auto Scaling API. Each call mutates
// Estate.ScalingConfig under Mu, so the describe calls and Estate.Advance see
// the change at once. FailNext("PutScalingPolicy", err) and the other
// operation names inject failures; ThrottleAutoScaling throttles them too.

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

func describePolicy(key model.ServiceKey, policy PolicySeed) astypes.ScalingPolicy {
	out := astypes.ScalingPolicy{
		PolicyName:        aws.String(policy.Name),
		PolicyARN:         aws.String("arn:aws:autoscaling:" + key.Region + ":" + key.AccountID + ":scalingPolicy:" + hexOf(hashSeed(key.Service, 5)) + ":resource/ecs/" + resourceID(key) + ":policyName/" + policy.Name),
		PolicyType:        astypes.PolicyTypeTargetTrackingScaling,
		ServiceNamespace:  astypes.ServiceNamespaceEcs,
		ResourceId:        aws.String(resourceID(key)),
		ScalableDimension: scalableDimension,
	}
	if policy.Step {
		out.PolicyType = astypes.PolicyTypeStepScaling
		out.StepScalingPolicyConfiguration = &astypes.StepScalingPolicyConfiguration{AdjustmentType: astypes.AdjustmentTypeChangeInCapacity}
		return out
	}
	out.TargetTrackingScalingPolicyConfiguration = &astypes.TargetTrackingScalingPolicyConfiguration{
		TargetValue: aws.Float64(policy.Target),
		PredefinedMetricSpecification: &astypes.PredefinedMetricSpecification{
			PredefinedMetricType: policy.Metric, ResourceLabel: nonEmptyPtr(policy.ResourceLabel)},
		ScaleOutCooldown: aws.Int32(policy.ScaleOut),
		ScaleInCooldown:  aws.Int32(policy.ScaleIn),
		DisableScaleIn:   aws.Bool(policy.DisableScaleIn),
	}
	return out
}

func validationError(message string) error {
	return &astypes.ValidationException{Message: aws.String(message)}
}

func notFoundError(message string) error {
	return &astypes.ObjectNotFoundException{Message: aws.String(message)}
}

// scalingKey resolves the request to the service key, rejecting anything but
// the ECS desired-count dimension of an existing service. Caller holds Mu.
func (c *autoScalingClient) scalingKey(namespace astypes.ServiceNamespace, dimension astypes.ScalableDimension, resource *string) (model.ServiceKey, error) {
	if namespace != astypes.ServiceNamespaceEcs || dimension != scalableDimension {
		return model.ServiceKey{}, validationError("only the ecs namespace and ecs:service:DesiredCount dimension are supported")
	}
	parts := strings.Split(aws.ToString(resource), "/")
	if len(parts) != 3 || parts[0] != "service" {
		return model.ServiceKey{}, validationError("ResourceId must look like service/<cluster>/<service>")
	}
	key := serviceKeyOf(c.target.AccountID, c.target.Region, parts[1], parts[2])
	for _, svc := range c.f.Estate.Services {
		if svc.AccountID == key.AccountID && svc.Region == key.Region && svc.ClusterName == key.Cluster &&
			aws.ToString(svc.Service.ServiceName) == key.Service {
			return key, nil
		}
	}
	return model.ServiceKey{}, validationError("ECS service doesn't exist: " + aws.ToString(resource))
}

// existingConfig is the scalable target of key, or ObjectNotFoundException.
// Caller holds Mu.
func (c *autoScalingClient) existingConfig(key model.ServiceKey) (*ScalingSeed, error) {
	cfg := c.f.Estate.ScalingConfig[key]
	if cfg == nil {
		return nil, notFoundError("No scalable target registered for service namespace: ecs, resource ID: " + resourceID(key) + ", scalable dimension: ecs:service:DesiredCount")
	}
	return cfg, nil
}

// RegisterScalableTarget creates the target or updates its range; policies and
// scheduled actions of an existing target are kept.
func (c *autoScalingClient) RegisterScalableTarget(_ context.Context, in *applicationautoscaling.RegisterScalableTargetInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "RegisterScalableTarget", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	current := e.ScalingConfig[key]
	var min, max int
	if current != nil {
		min, max = current.Min, current.Max
	} else if in.MinCapacity == nil || in.MaxCapacity == nil {
		return nil, validationError("MinCapacity and MaxCapacity are required when registering a new scalable target")
	}
	if in.MinCapacity != nil {
		min = int(*in.MinCapacity)
	}
	if in.MaxCapacity != nil {
		max = int(*in.MaxCapacity)
	}
	if min < 0 || max < min {
		return nil, validationError("MinCapacity must be at least 0 and not above MaxCapacity")
	}
	if current == nil {
		current = &ScalingSeed{}
		if e.ScalingConfig == nil {
			e.ScalingConfig = map[model.ServiceKey]*ScalingSeed{}
		}
		e.ScalingConfig[key] = current
	}
	current.Min, current.Max = min, max
	return &applicationautoscaling.RegisterScalableTargetOutput{
		ScalableTargetARN: aws.String("arn:aws:application-autoscaling:" + key.Region + ":" + key.AccountID + ":scalable-target/" + hexOf(hashSeed(resourceID(key), 4)))}, nil
}

// DeregisterScalableTarget removes the target together with its policies and
// scheduled actions, as AWS does.
func (c *autoScalingClient) DeregisterScalableTarget(_ context.Context, in *applicationautoscaling.DeregisterScalableTargetInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeregisterScalableTargetOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "DeregisterScalableTarget", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	if _, err := c.existingConfig(key); err != nil {
		return nil, err
	}
	delete(e.ScalingConfig, key)
	return &applicationautoscaling.DeregisterScalableTargetOutput{}, nil
}

// PutScalingPolicy upserts a policy by name. Target tracking policies need a
// predefined metric; the ALB metric also needs its ResourceLabel.
func (c *autoScalingClient) PutScalingPolicy(_ context.Context, in *applicationautoscaling.PutScalingPolicyInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScalingPolicyOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "PutScalingPolicy", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	cfg, err := c.existingConfig(key)
	if err != nil {
		return nil, err
	}
	policy := PolicySeed{Name: aws.ToString(in.PolicyName)}
	if policy.Name == "" {
		return nil, validationError("PolicyName is required")
	}
	switch in.PolicyType {
	case astypes.PolicyTypeStepScaling:
		policy.Step = true
	case astypes.PolicyTypeTargetTrackingScaling:
		tracking := in.TargetTrackingScalingPolicyConfiguration
		if tracking == nil || tracking.TargetValue == nil || tracking.PredefinedMetricSpecification == nil {
			return nil, validationError("a target tracking policy needs TargetValue and a PredefinedMetricSpecification")
		}
		spec := tracking.PredefinedMetricSpecification
		policy.Metric, policy.Target = spec.PredefinedMetricType, *tracking.TargetValue
		policy.ScaleOut, policy.ScaleIn = aws.ToInt32(tracking.ScaleOutCooldown), aws.ToInt32(tracking.ScaleInCooldown)
		policy.DisableScaleIn = aws.ToBool(tracking.DisableScaleIn)
		policy.ResourceLabel = aws.ToString(spec.ResourceLabel)
		if policy.Metric == astypes.MetricTypeALBRequestCountPerTarget && policy.ResourceLabel == "" {
			return nil, validationError("ResourceLabel is required for the ALBRequestCountPerTarget metric")
		}
	default:
		return nil, validationError("PolicyType must be TargetTrackingScaling or StepScaling")
	}
	replaced := false
	for i := range cfg.Policies {
		if cfg.Policies[i].Name == policy.Name {
			cfg.Policies[i], replaced = policy, true
		}
	}
	if !replaced {
		cfg.Policies = append(cfg.Policies, policy)
	}
	return &applicationautoscaling.PutScalingPolicyOutput{PolicyARN: describePolicy(key, policy).PolicyARN}, nil
}

func (c *autoScalingClient) DeleteScalingPolicy(_ context.Context, in *applicationautoscaling.DeleteScalingPolicyInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScalingPolicyOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "DeleteScalingPolicy", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	cfg, err := c.existingConfig(key)
	if err != nil {
		return nil, err
	}
	for i, policy := range cfg.Policies {
		if policy.Name == aws.ToString(in.PolicyName) {
			cfg.Policies = append(cfg.Policies[:i:i], cfg.Policies[i+1:]...)
			return &applicationautoscaling.DeleteScalingPolicyOutput{}, nil
		}
	}
	return nil, notFoundError("No scaling policy found for resource ID: " + resourceID(key) + ", policy name: " + aws.ToString(in.PolicyName))
}

// PutScheduledAction upserts a scheduled action by name.
func (c *autoScalingClient) PutScheduledAction(_ context.Context, in *applicationautoscaling.PutScheduledActionInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScheduledActionOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "PutScheduledAction", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	cfg, err := c.existingConfig(key)
	if err != nil {
		return nil, err
	}
	if aws.ToString(in.ScheduledActionName) == "" || aws.ToString(in.Schedule) == "" || in.ScalableTargetAction == nil {
		return nil, validationError("ScheduledActionName, Schedule and ScalableTargetAction are required")
	}
	action := ScheduledSeed{Name: *in.ScheduledActionName, Schedule: *in.Schedule, Timezone: aws.ToString(in.Timezone),
		Min: in.ScalableTargetAction.MinCapacity, Max: in.ScalableTargetAction.MaxCapacity}
	replaced := false
	for i := range cfg.Scheduled {
		if cfg.Scheduled[i].Name == action.Name {
			cfg.Scheduled[i], replaced = action, true
		}
	}
	if !replaced {
		cfg.Scheduled = append(cfg.Scheduled, action)
	}
	return &applicationautoscaling.PutScheduledActionOutput{}, nil
}

func (c *autoScalingClient) DeleteScheduledAction(_ context.Context, in *applicationautoscaling.DeleteScheduledActionInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScheduledActionOutput, error) {
	done, err := c.f.beginThrottled(c.target, "application-autoscaling", "DeleteScheduledAction", &c.f.ThrottleAutoScaling)
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	key, err := c.scalingKey(in.ServiceNamespace, in.ScalableDimension, in.ResourceId)
	if err != nil {
		return nil, err
	}
	cfg, err := c.existingConfig(key)
	if err != nil {
		return nil, err
	}
	for i, action := range cfg.Scheduled {
		if action.Name == aws.ToString(in.ScheduledActionName) {
			cfg.Scheduled = append(cfg.Scheduled[:i:i], cfg.Scheduled[i+1:]...)
			return &applicationautoscaling.DeleteScheduledActionOutput{}, nil
		}
	}
	return nil, notFoundError("No scheduled action called " + aws.ToString(in.ScheduledActionName) + " found for resource ID: " + resourceID(key))
}
