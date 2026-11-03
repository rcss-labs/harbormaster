package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ControllerDTO says which deployment controller owns a service; Banner is "" when Harbormaster can deploy it.
type ControllerDTO struct {
	Type      string `json:"type"`
	Supported bool   `json:"supported"`
	Name      string `json:"name"`
	Banner    string `json:"banner"`
}

// ControllerRefDTO is ControllerDTO without the banner, as the task definition tab shows it.
type ControllerRefDTO struct {
	Type      string `json:"type"`
	Supported bool   `json:"supported"`
	Name      string `json:"name"`
}

func controllerDTO(info deploy.ControllerInfo) ControllerDTO {
	dto := ControllerDTO{Type: info.Type, Supported: info.Supported, Name: info.Name}
	if !info.Supported {
		dto.Banner = deploy.ControllerBanner(info.Name)
	}
	return dto
}

// ChangeCanDTO is what the caller may do to a service's definition and count.
type ChangeCanDTO struct {
	Edit    bool `json:"edit"`
	Deploy  bool `json:"deploy"`
	Operate bool `json:"operate"`
	Admin   bool `json:"admin"`
}

// ResourceTagDTO is one AWS resource tag of the task definition.
type ResourceTagDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TaskDefinitionResponse is GET /api/services/{id}/task-definition.
// Definition is the register-ready JSON object with its keys in display order.
type TaskDefinitionResponse struct {
	Family            string           `json:"family"`
	DeployedRevision  int              `json:"deployed_revision"`
	TaskDefinitionARN string           `json:"task_definition_arn"`
	MainContainer     string           `json:"main_container"`
	Definition        json.RawMessage  `json:"definition"`
	Tags              []ResourceTagDTO `json:"tags"`
	Controller        ControllerRefDTO `json:"controller"`
	Strategy          string           `json:"strategy"`
	Can               ChangeCanDTO     `json:"can"`
	Tag               TagDTO           `json:"tag"`
	PolledAt          time.Time        `json:"polled_at"`
}

// RevisionDTO is one task definition revision of the service's family.
type RevisionDTO struct {
	Revision     int       `json:"revision"`
	ARN          string    `json:"arn"`
	Status       string    `json:"status"`
	ImageTag     string    `json:"image_tag"`
	CPU          string    `json:"cpu"`
	Memory       string    `json:"memory"`
	RegisteredAt time.Time `json:"registered_at"`
	RegisteredBy string    `json:"registered_by"`
}

// RevisionsResponse is GET /api/services/{id}/revisions. RollingOutRevision is null when no rollout runs.
type RevisionsResponse struct {
	Items              []RevisionDTO `json:"items"`
	DeployedRevision   int           `json:"deployed_revision"`
	RollingOutRevision *int          `json:"rolling_out_revision"`
}

// RevisionDefinitionResponse is GET /api/services/{id}/revisions/{revision}.
// Definition is the register-ready JSON object with its keys in display order.
type RevisionDefinitionResponse struct {
	Revision     int             `json:"revision"`
	ARN          string          `json:"arn"`
	Definition   json.RawMessage `json:"definition"`
	RegisteredAt time.Time       `json:"registered_at"`
	RegisteredBy string          `json:"registered_by"`
}

// ImageTagDTO is one tag in the service's ECR repository.
type ImageTagDTO struct {
	Tag      string    `json:"tag"`
	PushedAt time.Time `json:"pushed_at"`
}

// ImageTagsResponse is GET /api/services/{id}/image-tags. Source is "ecr" or "none" (free-text tag field).
type ImageTagsResponse struct {
	Source     string        `json:"source"`
	Repository string        `json:"repository"`
	Current    string        `json:"current"`
	Tags       []ImageTagDTO `json:"tags"`
}

// AutoScalingRangeDTO is the task count range auto scaling keeps the service within.
type AutoScalingRangeDTO struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// DesiredCountResponse is GET /api/services/{id}/desired-count.
type DesiredCountResponse struct {
	Desired     int                  `json:"desired"`
	Running     int                  `json:"running"`
	Max         int                  `json:"max"`
	AutoScaling *AutoScalingRangeDTO `json:"auto_scaling"`
}

// visibleService resolves {serviceId} and checks View on its effective tag; anything not visible is a 404.
func (s *Server) visibleService(w http.ResponseWriter, r *http.Request) (model.ServiceKey, scope, store.ServiceRow, bool) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return key, scope{}, store.ServiceRow{}, false
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return key, scope{}, store.ServiceRow{}, false
	}
	row, err := s.Store.GetServiceView(r.Context(), key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return key, scope{}, store.ServiceRow{}, false
	}
	return key, sc, row, true
}

func (s *Server) getTaskDefinition(w http.ResponseWriter, r *http.Request) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	deployed, err := s.Deploy.DeployedDefinition(ctx, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	definition, err := deploy.DefinitionJSON(deployed.Definition)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region})
	if err != nil {
		s.fail(w, err)
		return
	}
	resourceTags := make([]ResourceTagDTO, 0, len(deployed.Tags))
	for _, t := range deployed.Tags {
		resourceTags = append(resourceTags, ResourceTagDTO{Key: aws.ToString(t.Key), Value: aws.ToString(t.Value)})
	}
	tagID := row.EffectiveTagID
	writeJSON(w, http.StatusOK, TaskDefinitionResponse{
		Family: deployed.Family, DeployedRevision: deployed.Revision, TaskDefinitionARN: deployed.ARN,
		MainContainer: deployed.MainContainer, Definition: definition, Tags: resourceTags,
		Controller: ControllerRefDTO{Type: deployed.Controller.Type, Supported: deployed.Controller.Supported, Name: deployed.Controller.Name},
		Strategy:   deployed.Strategy,
		Can: ChangeCanDTO{
			Edit: authz.Can(sc.Subject, authz.Edit, tagID), Deploy: authz.Can(sc.Subject, authz.Deploy, tagID),
			Operate: authz.Can(sc.Subject, authz.Operate, tagID), Admin: sc.Subject.Admin,
		},
		Tag:      inheritedTag(tags, tagID, row.TagInherited),
		PolledAt: polledAt,
	})
}

func (s *Server) getRevisions(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	list, err := s.Deploy.Revisions(r.Context(), key)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := RevisionsResponse{Items: make([]RevisionDTO, 0, len(list.Items)), DeployedRevision: list.DeployedRevision}
	if list.RollingOutRevision != 0 {
		out.RollingOutRevision = &list.RollingOutRevision
	}
	for _, rev := range list.Items {
		out.Items = append(out.Items, RevisionDTO{
			Revision: rev.Revision, ARN: rev.ARN, Status: rev.Status, ImageTag: rev.ImageTag,
			CPU: rev.CPU, Memory: rev.Memory, RegisteredAt: rev.RegisteredAt, RegisteredBy: rev.RegisteredBy,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRevisionDefinition(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil {
		s.fail(w, store.ErrNotFound)
		return
	}
	found, err := s.Deploy.RevisionDefinition(r.Context(), key, revision)
	if err != nil {
		s.fail(w, err)
		return
	}
	definition, err := deploy.DefinitionJSON(found.Definition)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RevisionDefinitionResponse{
		Revision: found.Revision, ARN: found.ARN, Definition: definition,
		RegisteredAt: found.RegisteredAt, RegisteredBy: found.RegisteredBy,
	})
}

func (s *Server) getImageTags(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	info, err := s.Deploy.ImageTags(r.Context(), key)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ImageTagsResponse{Source: info.Source, Repository: info.Repository, Current: info.Current, Tags: make([]ImageTagDTO, 0, len(info.Tags))}
	for _, t := range info.Tags {
		out.Tags = append(out.Tags, ImageTagDTO{Tag: t.Tag, PushedAt: t.PushedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDesiredCount(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	info, err := s.Deploy.DesiredCount(r.Context(), key)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := DesiredCountResponse{Desired: info.Desired, Running: info.Running, Max: info.Max}
	if info.AutoScaling != nil {
		out.AutoScaling = &AutoScalingRangeDTO{Min: info.AutoScaling.Min, Max: info.AutoScaling.Max}
	}
	writeJSON(w, http.StatusOK, out)
}
