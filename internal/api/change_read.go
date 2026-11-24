package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
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
	// EnvironmentRedacted is true when any environment value was replaced for an access token, so a caller does not
	// report "[redacted]" as a real value.
	EnvironmentRedacted bool `json:"environment_redacted,omitempty"`
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
	// EnvironmentRedacted is true when any environment value was replaced for an access token.
	EnvironmentRedacted bool `json:"environment_redacted,omitempty"`
}

// ImageTagDTO is one tag in the service's ECR repository.
type ImageTagDTO struct {
	Tag      string    `json:"tag"`
	PushedAt time.Time `json:"pushed_at"`
}

// ImageTagsResponse is GET /api/services/{id}/image-tags. Source is "ecr" or "none" (free-text tag field).
// Container is the container the tags belong to, Image its reference without the tag.
// Total counts all tags of the repository, Matched those that fit q, Tags holds at most limit
// of them, and Truncated says Matched exceeds what Tags holds.
type ImageTagsResponse struct {
	Source     string        `json:"source"`
	Container  string        `json:"container"`
	Image      string        `json:"image"`
	Repository string        `json:"repository"`
	Current    string        `json:"current"`
	Total      int           `json:"total"`
	Matched    int           `json:"matched"`
	Truncated  bool          `json:"truncated"`
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
	definitionSource, redacted := redactEnvironment(deployed.Definition, s.redactionFor(r))
	definition, err := deploy.DefinitionJSON(definitionSource)
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
		Can:        changeCan(sc.Subject, tagID),
		Tag:        inheritedTag(tags, tagID, row.TagInherited),
		PolledAt:   polledAt,

		EnvironmentRedacted: redacted,
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
	definitionSource, redacted := redactEnvironment(found.Definition, s.redactionFor(r))
	definition, err := deploy.DefinitionJSON(definitionSource)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RevisionDefinitionResponse{
		Revision: found.Revision, ARN: found.ARN, Definition: definition,
		RegisteredAt: found.RegisteredAt, RegisteredBy: found.RegisteredBy,
		EnvironmentRedacted: redacted,
	})
}

func (s *Server) getImageTags(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	query := deploy.ImageTagQuery{Container: r.URL.Query().Get("container"), Search: r.URL.Query().Get("q")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			s.fail(w, invalid("limit must be a number between 1 and 200"))
			return
		}
		query.Limit = limit
	}
	info, err := s.Deploy.ImageTags(r.Context(), key, query)
	if err != nil {
		s.failChange(w, err)
		return
	}
	out := ImageTagsResponse{
		Source: info.Source, Container: info.Container, Image: info.Image, Repository: info.Repository, Current: info.Current,
		Total: info.Total, Matched: info.Matched, Truncated: info.Truncated, Tags: make([]ImageTagDTO, 0, len(info.Tags)),
	}
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

// redactedValue replaces an environment value that must not reach the caller.
const redactedValue = secretshape.Redacted

// envRedaction says which environment values an answer hides. The zero value hides everything, so a mode that
// was never decided fails closed.
type envRedaction int

const (
	redactAll          envRedaction = iota // every value
	redactNone                             // no value
	redactSecretShapes                     // only values whose name or content looks secret
)

// redactionFor decides the mode for this request.
func (s *Server) redactionFor(r *http.Request) envRedaction {
	cred, ok := auth.CredentialFrom(r.Context())
	return redactionMode(cred, ok, s.Cfg.TokenRedactEnv)
}

// redactionMode: a session is never redacted. A personal or OAuth access token is redacted entirely while
// HM_TOKEN_REDACT_ENV is on (off by default, owner decision 9). A run token is redacted entirely when that switch
// is on or its provider was not lifted, and otherwise only for secret-looking values: the operator's switch is
// never weakened by an admin's per-provider lift. No credential or an unknown kind is redacted entirely.
func redactionMode(cred auth.Credential, ok, tokenRedactEnv bool) envRedaction {
	if !ok {
		return redactAll
	}
	switch cred.Kind {
	case auth.CredentialSession:
		return redactNone
	case auth.CredentialToken:
		switch cred.TokenKind {
		case apitoken.KindPAT, apitoken.KindOAuthAccess:
			if tokenRedactEnv {
				return redactAll
			}
			return redactNone
		case apitoken.KindRun:
			if tokenRedactEnv || cred.RedactEnv {
				return redactAll
			}
			return redactSecretShapes
		}
	}
	return redactAll
}

// redactEnvironment returns a copy of the task definition with the environment values replaced by "[redacted]" as
// the mode says, and whether the answer should be flagged as redacted. Names, secrets[].valueFrom and everything
// else are kept. The input is never modified: definitions may be shared with a cache. Any shape this code does not
// expect (another type, another key casing, a non-string value) is replaced rather than passed through.
func redactEnvironment(definition map[string]any, mode envRedaction) (map[string]any, bool) {
	if mode == redactNone {
		return definition, false
	}
	replaced := mode == redactAll
	hide := func() any {
		replaced = true
		return redactedValue
	}
	out := make(map[string]any, len(definition))
	for key, value := range definition {
		if !strings.EqualFold(key, "containerDefinitions") {
			out[key] = value
			continue
		}
		containers, ok := value.([]any)
		if !ok {
			out[key] = hide()
			continue
		}
		redactedContainers := make([]any, len(containers))
		for i, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				redactedContainers[i] = hide()
				continue
			}
			copied := make(map[string]any, len(container))
			for field, fieldValue := range container {
				if !strings.EqualFold(field, "environment") {
					copied[field] = fieldValue
					continue
				}
				copied[field] = redactEntries(fieldValue, mode, hide)
			}
			redactedContainers[i] = copied
		}
		out[key] = redactedContainers
	}
	return out, replaced
}

func redactEntries(raw any, mode envRedaction, hide func() any) any {
	entries, ok := raw.([]any)
	if !ok {
		return hide()
	}
	redacted := make([]any, len(entries))
	for i, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			redacted[i] = hide()
			continue
		}
		name := ""
		for key, value := range entry {
			if strings.EqualFold(key, "name") {
				name, _ = value.(string)
			}
		}
		copied := make(map[string]any, len(entry))
		for key, value := range entry {
			if strings.EqualFold(key, "value") && hidesValue(mode, name, value) {
				copied[key] = hide()
				continue
			}
			copied[key] = value
		}
		redacted[i] = copied
	}
	return redacted
}

// hidesValue reports whether the mode replaces one environment value. In secrets-only mode a value that is not a
// string counts as secret.
func hidesValue(mode envRedaction, name string, value any) bool {
	if mode != redactSecretShapes {
		return true
	}
	text, ok := value.(string)
	return !ok || secretshape.NameLooksSecret(name) || secretshape.ScrubText(text) != text
}
