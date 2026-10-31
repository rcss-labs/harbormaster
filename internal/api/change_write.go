package api

import (
	"errors"
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// RegisterRequest is the body of POST /api/services/{id}/revisions.
type RegisterRequest struct {
	BaseRevision int            `json:"base_revision"`
	Definition   map[string]any `json:"definition"`
}

// RegisterResponse describes the newly registered revision.
type RegisterResponse struct {
	Revision  int    `json:"revision"`
	ARN       string `json:"arn"`
	ImageTag  string `json:"image_tag"`
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	CanDeploy bool   `json:"can_deploy"`
	Tag       TagDTO `json:"tag"`
	Strategy  string `json:"strategy"`
}

// DeployRequest is the body of POST /api/services/{id}/deploy.
type DeployRequest struct {
	Revision int `json:"revision"`
}

// RolloutResponse is the answer to deploy and restart. FromRevision is omitted for restarts.
type RolloutResponse struct {
	DeploymentID string `json:"deployment_id"`
	FromRevision *int   `json:"from_revision,omitempty"`
	ToRevision   *int   `json:"to_revision,omitempty"`
	Revision     *int   `json:"revision,omitempty"`
	Kind         string `json:"kind"`
}

// SetDesiredCountRequest is the body of POST /api/services/{id}/desired-count.
type SetDesiredCountRequest struct {
	Desired int `json:"desired"`
}

// SetDesiredCountResponse reports the change; Warning is null when auto scaling will not interfere.
type SetDesiredCountResponse struct {
	Desired  int     `json:"desired"`
	Previous int     `json:"previous"`
	Changed  bool    `json:"changed"`
	Warning  *string `json:"warning"`
}

// StopTaskResponse is the answer to POST /api/tasks/{id}/stop.
type StopTaskResponse struct {
	TaskARN string `json:"task_arn"`
	Reason  string `json:"reason"`
}

// requireAction resolves {serviceId} and demands action on the service's effective tag:
// no View is a 404, View without the action is a 403 naming the missing action.
func (s *Server) requireAction(w http.ResponseWriter, r *http.Request, action authz.Action) (model.ServiceKey, authz.Subject, store.ServiceRow, bool) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return key, authz.Subject{}, row, false
	}
	if !authz.Can(sc.Subject, action, row.EffectiveTagID) {
		s.denyAction(w, r, action, row.EffectiveTagID)
		return key, sc.Subject, row, false
	}
	return key, sc.Subject, row, true
}

// denyAction answers 403 naming the tag the caller lacks the action on.
func (s *Server) denyAction(w http.ResponseWriter, r *http.Request, action authz.Action, tagID authz.TagID) {
	tags, err := s.tagDTOs(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	forbiddenAction(w, action, tags[tagID].Name)
}

// failChange maps deploy errors to their HTTP responses and defers everything else to fail.
func (s *Server) failChange(w http.ResponseWriter, err error) {
	var stale *deploy.StaleBaseError
	var validation *deploy.ValidationError
	var unsupported *deploy.UnsupportedControllerError
	var awsErr *deploy.AWSError
	switch {
	case errors.As(err, &stale):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             map[string]string{"code": "conflict", "message": stale.Error()},
			"deployed_revision": stale.DeployedRevision,
		})
	case errors.Is(err, deploy.ErrRolloutRunning):
		writeError(w, http.StatusConflict, "conflict", deploy.ErrRolloutRunning.Error())
	case errors.As(err, &validation):
		writeError(w, http.StatusUnprocessableEntity, "invalid", validation.Message)
	case errors.As(err, &unsupported):
		writeError(w, http.StatusUnprocessableEntity, "invalid", unsupported.Error())
	case errors.As(err, &awsErr) && awsErr.Validation:
		writeError(w, http.StatusUnprocessableEntity, "invalid", awsErr.Message)
	default:
		s.fail(w, err)
	}
}

func (s *Server) registerRevision(w http.ResponseWriter, r *http.Request) {
	key, subject, row, ok := s.requireAction(w, r, authz.Edit)
	if !ok {
		return
	}
	var body RegisterRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Definition == nil {
		s.fail(w, invalid("definition is required."))
		return
	}
	ctx := r.Context()
	registered, err := s.Deploy.Register(ctx, subject, key, deploy.RegisterInput{BaseRevision: body.BaseRevision, Definition: body.Definition})
	if err != nil {
		s.failChange(w, err)
		return
	}
	deployed, err := s.Deploy.DeployedDefinition(ctx, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, RegisterResponse{
		Revision: registered.Revision, ARN: registered.ARN, ImageTag: registered.ImageTag,
		CPU: registered.CPU, Memory: registered.Memory,
		CanDeploy: authz.Can(subject, authz.Deploy, row.EffectiveTagID) && deployed.Controller.Supported,
		Tag:       inheritedTag(tags, row.EffectiveTagID, row.TagInherited),
		Strategy:  deployed.Strategy,
	})
}

func (s *Server) deployRevision(w http.ResponseWriter, r *http.Request) {
	key, subject, _, ok := s.requireAction(w, r, authz.Deploy)
	if !ok {
		return
	}
	var body DeployRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	rollout, err := s.Deploy.Deploy(r.Context(), subject, key, body.Revision)
	if err != nil {
		s.failChange(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, RolloutResponse{
		DeploymentID: rollout.DeploymentID, FromRevision: &rollout.FromRevision, ToRevision: &rollout.ToRevision, Kind: rollout.Kind,
	})
}

func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	key, subject, _, ok := s.requireAction(w, r, authz.Operate)
	if !ok {
		return
	}
	rollout, err := s.Deploy.Restart(r.Context(), subject, key)
	if err != nil {
		s.failChange(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, RolloutResponse{DeploymentID: rollout.DeploymentID, Revision: &rollout.ToRevision, Kind: rollout.Kind})
}

func (s *Server) setDesiredCount(w http.ResponseWriter, r *http.Request) {
	key, subject, _, ok := s.requireAction(w, r, authz.Operate)
	if !ok {
		return
	}
	var body SetDesiredCountRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	result, err := s.Deploy.SetDesiredCount(r.Context(), subject, key, body.Desired)
	if err != nil {
		s.failChange(w, err)
		return
	}
	out := SetDesiredCountResponse{Desired: result.Desired, Previous: result.Previous, Changed: result.Changed}
	if result.Warning != "" {
		out.Warning = &result.Warning
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request) {
	accountID, region, taskARN, err := model.ParseTaskID(r.PathValue("taskId"))
	if err != nil {
		s.fail(w, errNotFound)
		return
	}
	ctx := r.Context()
	task, err := s.Store.GetTask(ctx, accountID, region, taskARN)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tags(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	subject := auth.SubjectFrom(ctx)
	tagID := authz.EffectiveTaskTag(tags, task.Cluster, task.ServiceName)
	if !authz.Can(subject, authz.View, tagID) {
		s.fail(w, errNotFound)
		return
	}
	if !authz.Can(subject, authz.Operate, tagID) {
		s.denyAction(w, r, authz.Operate, tagID)
		return
	}
	stopped, err := s.Deploy.StopTask(ctx, subject, accountID, region, taskARN)
	if err != nil {
		s.failChange(w, err)
		return
	}
	writeJSON(w, http.StatusOK, StopTaskResponse{TaskARN: stopped.TaskARN, Reason: stopped.Reason})
}
