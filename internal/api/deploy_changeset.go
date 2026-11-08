package api

import (
	"errors"
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
)

// ChangeSetRequest is the body of POST /api/services/{id}/changeset.
type ChangeSetRequest struct {
	BaseRevision   int                      `json:"base_revision"`
	BaseVersion    string                   `json:"base_version"`
	Reason         string                   `json:"reason"`
	TaskDefinition *ChangeSetTaskDefinition `json:"task_definition"`
	Service        *ChangeSetService        `json:"service"`
	Scaling        *ChangeSetScaling        `json:"scaling"`
	// ScanGate answers the pre-deploy image scan gate; the server re-evaluates the gate on every call.
	ScanGate *ScanGateRequest `json:"scan_gate"`
}

// ChangeSetTaskDefinition carries a new definition to register, or the revision a previous run registered. Exactly one is set.
type ChangeSetTaskDefinition struct {
	Definition map[string]any `json:"definition"`
	Revision   *int           `json:"revision"`
}

// ChangeSetService is the service section; absent fields stay as they are.
type ChangeSetService struct {
	DesiredCount                  *int                        `json:"desired_count"`
	EnableExecuteCommand          *bool                       `json:"enable_execute_command"`
	DeploymentConfiguration       *DeploymentConfigurationDTO `json:"deployment_configuration"`
	HealthCheckGracePeriodSeconds *int                        `json:"health_check_grace_period_seconds"`
	ForceNewDeployment            bool                        `json:"force_new_deployment"`
}

// ChangeSetScaling is the auto scaling section.
type ChangeSetScaling struct {
	Target                 *ChangeSetScalingTarget `json:"target"`
	RemoveTarget           bool                    `json:"remove_target"`
	PutPolicies            []ChangeSetPolicyPut    `json:"put_policies"`
	DeletePolicies         []string                `json:"delete_policies"`
	PutScheduledActions    []ChangeSetScheduledPut `json:"put_scheduled_actions"`
	DeleteScheduledActions []string                `json:"delete_scheduled_actions"`
}

// ChangeSetScalingTarget is the scalable target's capacity range.
type ChangeSetScalingTarget struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// ChangeSetPolicyPut upserts a target tracking policy.
type ChangeSetPolicyPut struct {
	Name             string  `json:"name"`
	Metric           string  `json:"metric"`
	TargetValue      float64 `json:"target_value"`
	ScaleInCooldown  int     `json:"scale_in_cooldown"`
	ScaleOutCooldown int     `json:"scale_out_cooldown"`
	DisableScaleIn   bool    `json:"disable_scale_in"`
	TargetGroupARN   string  `json:"target_group_arn"`
}

// ChangeSetScheduledPut upserts a scheduled action.
type ChangeSetScheduledPut struct {
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	Timezone    string `json:"timezone"`
	MinCapacity *int   `json:"min_capacity"`
	MaxCapacity *int   `json:"max_capacity"`
}

// ChangeSetStepDTO is the outcome of one phase of a change set.
type ChangeSetStepDTO struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ChangeSetResponse is the answer to POST /api/services/{id}/changeset, for "applied", "partial" and "registered_only".
// SettingsVersion is the settings version after the run; a retry sends it as base_version. It is "" when
// it could not be read. Warning is non-null when AWS accepted a change but Harbormaster could not record it.
type ChangeSetResponse struct {
	Status             string             `json:"status"`
	Steps              []ChangeSetStepDTO `json:"steps"`
	SettingsVersion    string             `json:"settings_version"`
	ChangeSetID        string             `json:"changeset_id"`
	RegisteredRevision *int               `json:"registered_revision"`
	DeploymentID       string             `json:"deployment_id"`
	FromRevision       *int               `json:"from_revision"`
	ToRevision         *int               `json:"to_revision"`
	Kind               string             `json:"kind"`
	Warning            *string            `json:"warning"`
}

func (body ChangeSetRequest) toChangeSet() (deploy.ChangeSet, error) {
	cs := deploy.ChangeSet{BaseRevision: body.BaseRevision, BaseVersion: body.BaseVersion, Reason: body.Reason}
	if td := body.TaskDefinition; td != nil {
		switch {
		case td.Definition != nil && td.Revision != nil:
			return cs, invalid("task_definition takes a definition or a revision, not both.")
		case td.Definition != nil:
			if body.BaseRevision == 0 {
				return cs, invalid("base_revision is required with a definition.")
			}
			cs.TaskDefinition = &deploy.TaskDefinitionChange{Definition: td.Definition}
		case td.Revision != nil && *td.Revision > 0:
			cs.TaskDefinition = &deploy.TaskDefinitionChange{Revision: *td.Revision}
		default:
			return cs, invalid("task_definition needs a definition or a revision.")
		}
	}
	if svc := body.Service; svc != nil {
		change := &deploy.ServiceChange{
			DesiredCount: svc.DesiredCount, EnableExecuteCommand: svc.EnableExecuteCommand,
			HealthCheckGracePeriodSeconds: svc.HealthCheckGracePeriodSeconds, ForceNewDeployment: svc.ForceNewDeployment,
		}
		if dc := svc.DeploymentConfiguration; dc != nil {
			change.DeploymentConfiguration = &deploy.DeploymentConfigChange{
				CircuitBreakerEnable: dc.CircuitBreaker.Enable, CircuitBreakerRollback: dc.CircuitBreaker.Rollback,
				MinimumHealthyPercent: dc.MinimumHealthyPercent, MaximumPercent: dc.MaximumPercent,
			}
		}
		cs.Service = change
	}
	if sc := body.Scaling; sc != nil {
		change := &deploy.ScalingChange{RemoveTarget: sc.RemoveTarget, DeletePolicies: sc.DeletePolicies, DeleteScheduledActions: sc.DeleteScheduledActions}
		if sc.Target != nil {
			change.Target = &deploy.ScalingTargetChange{Min: sc.Target.Min, Max: sc.Target.Max}
		}
		for _, p := range sc.PutPolicies {
			change.PutPolicies = append(change.PutPolicies, deploy.PolicyChange{
				Name: p.Name, Metric: p.Metric, TargetValue: p.TargetValue, ScaleInCooldown: p.ScaleInCooldown,
				ScaleOutCooldown: p.ScaleOutCooldown, DisableScaleIn: p.DisableScaleIn, TargetGroupARN: p.TargetGroupARN,
			})
		}
		for _, a := range sc.PutScheduledActions {
			change.PutScheduledActions = append(change.PutScheduledActions, deploy.ScheduledActionChange{
				Name: a.Name, Schedule: a.Schedule, Timezone: a.Timezone, MinCapacity: a.MinCapacity, MaxCapacity: a.MaxCapacity,
			})
		}
		cs.Scaling = change
	}
	return cs, nil
}

func changeSetResponse(result deploy.ChangeSetResult) ChangeSetResponse {
	out := ChangeSetResponse{
		Status: result.Status, Steps: make([]ChangeSetStepDTO, 0, len(result.Steps)),
		SettingsVersion: result.SettingsVersion, ChangeSetID: result.ChangeSetID,
		Kind: result.Kind, Warning: warningPointer(result.Warning),
	}
	for _, step := range result.Steps {
		out.Steps = append(out.Steps, ChangeSetStepDTO{Name: step.Name, Status: step.Status, Message: step.Message})
	}
	if result.RegisteredRevision != 0 {
		out.RegisteredRevision = &result.RegisteredRevision
	}
	if rollout := result.Rollout; rollout != nil {
		out.DeploymentID = rollout.DeploymentID
		out.FromRevision, out.ToRevision = &rollout.FromRevision, &rollout.ToRevision
	}
	return out
}

func (s *Server) applyChangeSet(w http.ResponseWriter, r *http.Request) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	var body ChangeSetRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	cs, err := body.toChangeSet()
	if err != nil {
		s.fail(w, err)
		return
	}
	// Refuse the whole request before any write when a section needs an action the caller lacks.
	for _, action := range deploy.RequiredActions(cs) {
		if !authz.Can(sc.Subject, action, row.EffectiveTagID) {
			s.denyAction(w, r, action, row.EffectiveTagID)
			return
		}
	}
	// The pre-deploy image gate: after the caller's actions are checked, so a caller without them is refused before any ECR
	// call or queued scan. A deploy by revision is gated only for a revision that never ran; rollbacks and non-image changes pass.
	var gate gateOutcome
	if td := cs.TaskDefinition; td != nil {
		var ok bool
		if td.Definition != nil {
			gate, ok = s.checkScanGate(w, r, key, cs.BaseRevision, td.Definition, body.ScanGate, sc.Subject)
		} else {
			// A deploy by revision is gated only when that revision never ran on this service and an image differs.
			gate, ok = s.checkRevisionGate(w, r, key, td.Revision, body.ScanGate, sc.Subject)
		}
		if !ok {
			return
		}
	}
	result, err := s.Deploy.ApplyChangeSet(r.Context(), sc.Subject, key, cs)
	if err != nil {
		var forbidden *deploy.ForbiddenError
		var stale *deploy.StaleVersionError
		switch {
		case errors.As(err, &forbidden):
			s.denyAction(w, r, forbidden.Action, row.EffectiveTagID)
		case errors.As(err, &stale):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":    map[string]string{"code": "conflict", "message": stale.Error()},
				"settings": deploySettingsDTO(stale.Current, changeCan(sc.Subject, row.EffectiveTagID)),
			})
		default:
			s.failChange(w, err)
		}
		return
	}
	response := changeSetResponse(result)
	if len(gate.Acknowledged) > 0 {
		if err := s.recordDeployAnyway(r, key, gate, result.Status == "registered_only" || (result.Status == "partial" && result.Rollout == nil), sc.Subject); err != nil {
			response.Warning = joinWarning(response.Warning, deployAnywayNotRecorded)
		}
	}
	writeJSON(w, http.StatusOK, response)
}
