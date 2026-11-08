package api

import (
	"errors"
	"net/http"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
)

// DeploymentConfigurationDTO is the service's deployment configuration, also used in change-set requests.
type DeploymentConfigurationDTO struct {
	CircuitBreaker        CircuitBreakerDTO `json:"circuit_breaker"`
	MinimumHealthyPercent int               `json:"minimum_healthy_percent"`
	MaximumPercent        int               `json:"maximum_percent"`
}

// DeployTargetGroupDTO is a target group behind the service; ResourceLabel is "" for non-ALB load balancers.
type DeployTargetGroupDTO struct {
	ARN           string `json:"arn"`
	Name          string `json:"name"`
	ResourceLabel string `json:"resource_label"`
}

// DeployPolicyDTO is a scaling policy. Only editable policies can be changed from the Deploy tab.
type DeployPolicyDTO struct {
	Name             string  `json:"name"`
	Type             string  `json:"type"`
	Metric           string  `json:"metric"`
	MetricRaw        string  `json:"metric_raw"`
	TargetValue      float64 `json:"target_value"`
	ScaleInCooldown  int     `json:"scale_in_cooldown"`
	ScaleOutCooldown int     `json:"scale_out_cooldown"`
	DisableScaleIn   bool    `json:"disable_scale_in"`
	ResourceLabel    string  `json:"resource_label"`
	Editable         bool    `json:"editable"`
}

// DeployScheduledActionDTO is a scheduled scaling action; a capacity is null when the action leaves it alone.
type DeployScheduledActionDTO struct {
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	Timezone    string `json:"timezone"`
	MinCapacity *int   `json:"min_capacity"`
	MaxCapacity *int   `json:"max_capacity"`
}

// DeployScalingDTO is the service's Application Auto Scaling configuration.
type DeployScalingDTO struct {
	HasTarget        bool                       `json:"has_target"`
	Min              int                        `json:"min"`
	Max              int                        `json:"max"`
	Policies         []DeployPolicyDTO          `json:"policies"`
	ScheduledActions []DeployScheduledActionDTO `json:"scheduled_actions"`
}

// DeploySettingsResponse is GET /api/services/{id}/deploy-settings. Version goes back as base_version on the change set.
type DeploySettingsResponse struct {
	Version                       string                     `json:"version"`
	DeployedRevision              int                        `json:"deployed_revision"`
	Family                        string                     `json:"family"`
	Controller                    ControllerRefDTO           `json:"controller"`
	DesiredCount                  int                        `json:"desired_count"`
	RunningCount                  int                        `json:"running_count"`
	MaxDesired                    int                        `json:"max_desired"`
	EnableExecuteCommand          bool                       `json:"enable_execute_command"`
	TaskRoleARN                   string                     `json:"task_role_arn"`
	DeploymentConfiguration       DeploymentConfigurationDTO `json:"deployment_configuration"`
	HealthCheckGracePeriodSeconds *int                       `json:"health_check_grace_period_seconds"`
	HasLoadBalancer               bool                       `json:"has_load_balancer"`
	TargetGroups                  []DeployTargetGroupDTO     `json:"target_groups"`
	Scaling                       DeployScalingDTO           `json:"scaling"`
	Can                           ChangeCanDTO               `json:"can"`
	Partial                       []PartialErrorDTO          `json:"partial"`
}

// changeCan is what subject may do to a service with the given effective tag.
func changeCan(subject authz.Subject, tagID authz.TagID) ChangeCanDTO {
	return ChangeCanDTO{
		Edit: authz.Can(subject, authz.Edit, tagID), Deploy: authz.Can(subject, authz.Deploy, tagID),
		Operate: authz.Can(subject, authz.Operate, tagID), Admin: subject.Admin,
	}
}

func deploySettingsDTO(st deploy.DeploySettings, can ChangeCanDTO) DeploySettingsResponse {
	out := DeploySettingsResponse{
		Version: st.Version, DeployedRevision: st.DeployedRevision, Family: st.Family,
		Controller:   ControllerRefDTO{Type: st.Controller.Type, Supported: st.Controller.Supported, Name: st.Controller.Name},
		DesiredCount: st.DesiredCount, RunningCount: st.RunningCount, MaxDesired: st.MaxDesired,
		EnableExecuteCommand: st.EnableExecuteCommand, TaskRoleARN: st.TaskRoleARN,
		DeploymentConfiguration: DeploymentConfigurationDTO{
			CircuitBreaker:        CircuitBreakerDTO{Enable: st.DeploymentConfiguration.CircuitBreakerEnable, Rollback: st.DeploymentConfiguration.CircuitBreakerRollback},
			MinimumHealthyPercent: st.DeploymentConfiguration.MinimumHealthyPercent, MaximumPercent: st.DeploymentConfiguration.MaximumPercent,
		},
		HealthCheckGracePeriodSeconds: st.HealthCheckGracePeriodSeconds,
		HasLoadBalancer:               st.HasLoadBalancer,
		TargetGroups:                  make([]DeployTargetGroupDTO, 0, len(st.TargetGroups)),
		Partial:                       make([]PartialErrorDTO, 0),
		Scaling: DeployScalingDTO{
			HasTarget: st.Scaling.HasTarget, Min: st.Scaling.Min, Max: st.Scaling.Max,
			Policies:         make([]DeployPolicyDTO, 0, len(st.Scaling.Policies)),
			ScheduledActions: make([]DeployScheduledActionDTO, 0, len(st.Scaling.ScheduledActions)),
		},
		Can: can,
	}
	for _, part := range st.Partial {
		out.Partial = append(out.Partial, PartialErrorDTO{Part: part.Part, Code: "aws_error", Message: partialMessage(part)})
	}
	for _, tg := range st.TargetGroups {
		out.TargetGroups = append(out.TargetGroups, DeployTargetGroupDTO{ARN: tg.ARN, Name: tg.Name, ResourceLabel: tg.ResourceLabel})
	}
	for _, p := range st.Scaling.Policies {
		out.Scaling.Policies = append(out.Scaling.Policies, DeployPolicyDTO{
			Name: p.Name, Type: p.Type, Metric: p.Metric, MetricRaw: p.MetricRaw, TargetValue: p.TargetValue,
			ScaleInCooldown: p.ScaleInCooldown, ScaleOutCooldown: p.ScaleOutCooldown, DisableScaleIn: p.DisableScaleIn,
			ResourceLabel: p.ResourceLabel, Editable: p.Editable,
		})
	}
	for _, a := range st.Scaling.ScheduledActions {
		out.Scaling.ScheduledActions = append(out.Scaling.ScheduledActions, DeployScheduledActionDTO{
			Name: a.Name, Schedule: a.Schedule, Timezone: a.Timezone, MinCapacity: a.MinCapacity, MaxCapacity: a.MaxCapacity,
		})
	}
	return out
}

var partialSubjects = map[string]string{
	"task_role":         "The task role",
	"target_groups":     "Target groups",
	"scaling_target":    "The scalable target",
	"scaling_policies":  "Scaling policies",
	"scheduled_actions": "Scheduled actions",
}

// partialMessage is "<Part> could not be read: <AWS error>".
func partialMessage(part deploy.PartError) string {
	subject, ok := partialSubjects[part.Part]
	if !ok {
		subject = part.Part
	}
	return subject + " could not be read: " + errorText(part.Err)
}

// errorText is the AWS message of err when it has one, else its text.
func errorText(err error) string {
	var awsErr smithy.APIError
	if errors.As(err, &awsErr) {
		return awsErr.ErrorCode() + ": " + awsErr.ErrorMessage()
	}
	return err.Error()
}

func (s *Server) getDeploySettings(w http.ResponseWriter, r *http.Request) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	st, err := s.Deploy.DeploySettings(r.Context(), key)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deploySettingsDTO(st, changeCan(sc.Subject, row.EffectiveTagID)))
}
