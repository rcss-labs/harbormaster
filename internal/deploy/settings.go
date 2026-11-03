package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
)

// DeploySettings is the live service and scaling configuration the Deploy tab edits.
type DeploySettings struct {
	Version string

	Family           string
	DeployedRevision int
	Controller       ControllerInfo

	DesiredCount, RunningCount, MaxDesired int
	EnableExecuteCommand                   bool
	TaskRoleARN                            string
	DeploymentConfiguration                DeploymentSettings
	HealthCheckGracePeriodSeconds          *int
	HasLoadBalancer                        bool
	TargetGroups                           []TargetGroupSetting
	Scaling                                ScalingSettings
}

// DeploymentSettings is the service's deployment configuration with ECS defaults filled in.
type DeploymentSettings struct {
	CircuitBreakerEnable, CircuitBreakerRollback bool
	MinimumHealthyPercent, MaximumPercent        int
}

// TargetGroupSetting is a target group behind the service. ResourceLabel is what an ALB request
// count policy needs ("" when the load balancer is not an application load balancer).
type TargetGroupSetting struct{ ARN, Name, ResourceLabel string }

// ScalingSettings is the service's Application Auto Scaling configuration.
type ScalingSettings struct {
	HasTarget        bool
	Min, Max         int
	Policies         []PolicySetting
	ScheduledActions []ScheduledActionSetting
}

// PolicySetting is a scaling policy. Type is "TargetTrackingScaling" or "StepScaling"; Metric is
// "cpu", "memory", "alb" or "other" (MetricRaw is the AWS metric). Only target tracking policies on
// cpu, memory or alb are Editable.
type PolicySetting struct {
	Name, Type, Metric, MetricRaw     string
	TargetValue                       float64
	ScaleInCooldown, ScaleOutCooldown int
	DisableScaleIn                    bool
	ResourceLabel                     string
	Editable                          bool
}

// ScheduledActionSetting is a scheduled scaling action.
type ScheduledActionSetting struct {
	Name, Schedule, Timezone string
	MinCapacity, MaxCapacity *int
}

// DeploySettings reads the settings live from ECS, Application Auto Scaling and ELB.
func (s *Service) DeploySettings(ctx context.Context, key model.ServiceKey) (DeploySettings, error) {
	_, live, err := s.liveService(ctx, key)
	if err != nil {
		return DeploySettings{}, err
	}
	return s.settingsFrom(ctx, key, live)
}

// settingsFrom builds the settings from an already read live service.
func (s *Service) settingsFrom(ctx context.Context, key model.ServiceKey, live ecstypes.Service) (DeploySettings, error) {
	primary, ok := PrimaryDeployment(live)
	if !ok {
		return DeploySettings{}, errors.New("service has no primary deployment")
	}
	family, revision, err := ParseRevisionARN(aws.ToString(primary.TaskDefinition))
	if err != nil {
		return DeploySettings{}, err
	}
	taskRole, err := s.taskRoleOf(ctx, key, aws.ToString(primary.TaskDefinition))
	if err != nil {
		return DeploySettings{}, err
	}
	out := DeploySettings{
		Family: family, DeployedRevision: revision, Controller: Controller(live),
		DesiredCount: int(live.DesiredCount), RunningCount: int(live.RunningCount), MaxDesired: s.maxDesired(),
		EnableExecuteCommand: live.EnableExecuteCommand, TaskRoleARN: taskRole,
		DeploymentConfiguration: deploymentSettingsOf(live), HasLoadBalancer: len(live.LoadBalancers) > 0,
	}
	if live.HealthCheckGracePeriodSeconds != nil {
		grace := int(*live.HealthCheckGracePeriodSeconds)
		out.HealthCheckGracePeriodSeconds = &grace
	}
	if out.TargetGroups, err = s.targetGroupsOf(ctx, key, live); err != nil {
		return DeploySettings{}, err
	}
	autoScaling, err := s.Clients.AutoScaling(ctx, targetOf(key))
	if err != nil {
		return DeploySettings{}, err
	}
	scaling, err := poller.CollectServiceScalingConfig(ctx, autoScaling, key)
	if err != nil {
		return DeploySettings{}, err
	}
	out.Scaling = scalingSettingsOf(scaling)
	out.Version = out.version()
	return out, nil
}

func deploymentSettingsOf(live ecstypes.Service) DeploymentSettings {
	settings := DeploymentSettings{MinimumHealthyPercent: ecsDefaultMinimumHealthyPercent, MaximumPercent: ecsDefaultMaximumPercent}
	config := live.DeploymentConfiguration
	if config == nil {
		return settings
	}
	if config.MinimumHealthyPercent != nil {
		settings.MinimumHealthyPercent = int(*config.MinimumHealthyPercent)
	}
	if config.MaximumPercent != nil {
		settings.MaximumPercent = int(*config.MaximumPercent)
	}
	if breaker := config.DeploymentCircuitBreaker; breaker != nil {
		settings.CircuitBreakerEnable, settings.CircuitBreakerRollback = breaker.Enable, breaker.Rollback
	}
	return settings
}

// taskRoleOf is the task role of a task definition revision, "" when it has none.
func (s *Service) taskRoleOf(ctx context.Context, key model.ServiceKey, arn string) (string, error) {
	ref, err := s.loadTaskDefinition(ctx, key, arn)
	if err != nil {
		return "", err
	}
	var described ecstypes.TaskDefinition
	if err := json.Unmarshal(ref.Raw, &described); err != nil {
		return "", fmt.Errorf("decode task definition %s: %w", arn, err)
	}
	return aws.ToString(described.TaskRoleArn), nil
}

// targetGroupsOf describes the service's target groups. The ALB resource label is
// "app/<lb-name>/<lb-id>/targetgroup/<tg-name>/<tg-id>", built from the load balancer ARN that
// DescribeTargetGroups reports (the service itself only knows the target group ARN).
func (s *Service) targetGroupsOf(ctx context.Context, key model.ServiceKey, live ecstypes.Service) ([]TargetGroupSetting, error) {
	var arns []string
	for _, balancer := range live.LoadBalancers {
		if arn := aws.ToString(balancer.TargetGroupArn); arn != "" {
			arns = append(arns, arn)
		}
	}
	if len(arns) == 0 {
		return nil, nil
	}
	elb, err := s.Clients.ELB(ctx, targetOf(key))
	if err != nil {
		return nil, err
	}
	described, err := elb.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: arns})
	if err != nil {
		return nil, fmt.Errorf("describe target groups: %w", err)
	}
	groups := make([]TargetGroupSetting, 0, len(described.TargetGroups))
	for _, group := range described.TargetGroups {
		setting := TargetGroupSetting{ARN: aws.ToString(group.TargetGroupArn), Name: aws.ToString(group.TargetGroupName)}
		if len(group.LoadBalancerArns) > 0 {
			setting.ResourceLabel = albResourceLabel(group.LoadBalancerArns[0], setting.ARN)
		}
		groups = append(groups, setting)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ARN < groups[j].ARN })
	return groups, nil
}

// albResourceLabel joins the tail of an application load balancer ARN ("loadbalancer/app/x/y")
// and of a target group ARN ("targetgroup/a/b"). It is "" for network and gateway load balancers,
// which have no request count metric.
func albResourceLabel(loadBalancerARN, targetGroupARN string) string {
	_, balancer, ok := strings.Cut(loadBalancerARN, ":loadbalancer/")
	if !ok || !strings.HasPrefix(balancer, "app/") {
		return ""
	}
	_, group, ok := strings.Cut(targetGroupARN, ":targetgroup/")
	if !ok {
		return ""
	}
	return balancer + "/targetgroup/" + group
}

func scalingSettingsOf(scaling model.Scaling) ScalingSettings {
	out := ScalingSettings{HasTarget: scaling.HasTarget, Min: scaling.Min, Max: scaling.Max}
	for _, policy := range scaling.Policies {
		out.Policies = append(out.Policies, policySettingOf(policy))
	}
	for _, action := range scaling.Scheduled {
		out.ScheduledActions = append(out.ScheduledActions, ScheduledActionSetting{
			Name: action.Name, Schedule: action.Schedule, Timezone: action.Timezone,
			MinCapacity: action.MinCapacity, MaxCapacity: action.MaxCapacity})
	}
	sort.Slice(out.Policies, func(i, j int) bool { return out.Policies[i].Name < out.Policies[j].Name })
	sort.Slice(out.ScheduledActions, func(i, j int) bool { return out.ScheduledActions[i].Name < out.ScheduledActions[j].Name })
	return out
}

// policySettingOf reads the fields the model drops (disable scale-in, ALB resource label) from the raw policy.
func policySettingOf(policy model.ScalingPolicy) PolicySetting {
	setting := PolicySetting{
		Name: policy.Name, Type: policy.Type, Metric: "other", MetricRaw: policy.Metric, TargetValue: policy.TargetValue,
		ScaleInCooldown: policy.ScaleInCooldown, ScaleOutCooldown: policy.ScaleOutCooldown,
	}
	var raw astypes.ScalingPolicy
	if err := json.Unmarshal(policy.Raw, &raw); err != nil {
		return setting
	}
	tracking := raw.TargetTrackingScalingPolicyConfiguration
	if tracking == nil {
		return setting
	}
	setting.DisableScaleIn = aws.ToBool(tracking.DisableScaleIn)
	if predefined := tracking.PredefinedMetricSpecification; predefined != nil {
		setting.ResourceLabel = aws.ToString(predefined.ResourceLabel)
		switch predefined.PredefinedMetricType {
		case astypes.MetricTypeECSServiceAverageCPUUtilization:
			setting.Metric = "cpu"
		case astypes.MetricTypeECSServiceAverageMemoryUtilization:
			setting.Metric = "memory"
		case astypes.MetricTypeALBRequestCountPerTarget:
			setting.Metric = "alb"
		}
	}
	setting.Editable = setting.Metric != "other"
	return setting
}

// version hashes the configuration a client edits against. The hash covers ECS Exec,
// deployment configuration, grace period, load balancer presence, target groups and the whole
// scaling configuration. It leaves out what is observation or is guarded elsewhere: desired count
// (autoscaling moves it; last writer wins), running count,
// max desired (server config), task role, family, controller and the deployed revision
// (base_revision guards that one).
func (d DeploySettings) version() string {
	canonical, _ := json.Marshal(struct {
		EnableExecuteCommand    bool
		DeploymentConfiguration DeploymentSettings
		HealthCheckGrace        *int
		HasLoadBalancer         bool
		TargetGroups            []TargetGroupSetting
		Scaling                 ScalingSettings
	}{d.EnableExecuteCommand, d.DeploymentConfiguration, d.HealthCheckGracePeriodSeconds,
		d.HasLoadBalancer, d.TargetGroups, d.Scaling})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
