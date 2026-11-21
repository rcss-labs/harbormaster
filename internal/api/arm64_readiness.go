package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// containerImage is one container of a task definition and its image string, as written.
type containerImage struct{ Name, Image string }

// containerImages reads the containers of a task definition's ContainerDefinitions (PascalCase ECS JSON). Entries without an
// image are skipped.
func containerImages(containerDefinitions []byte) []containerImage {
	var containers []struct {
		Name  string `json:"Name"`
		Image string `json:"Image"`
	}
	if json.Unmarshal(containerDefinitions, &containers) != nil {
		return nil
	}
	out := make([]containerImage, 0, len(containers))
	for _, container := range containers {
		if container.Image != "" {
			out = append(out, containerImage{Name: container.Name, Image: container.Image})
		}
	}
	return out
}

// imageFactsOf mirrors what the platform check stored into the evaluator's input.
func imageFactsOf(p store.ImagePlatforms) cost.ImageFacts {
	return cost.ImageFacts{Status: p.Status, Detail: p.Detail, Digest: p.Digest, Known: p.Known, IsIndex: p.IsIndex, Platforms: p.Platforms}
}

// imageChecks judges each container image. An image the check does not track yet is unchecked (pending), never ready.
func imageChecks(containers []containerImage, stored map[string]store.ImagePlatforms) []cost.ImageCheck {
	checks := make([]cost.ImageCheck, 0, len(containers))
	for _, container := range containers {
		facts := cost.ImageFacts{}
		if p, ok := stored[container.Image]; ok {
			facts = imageFactsOf(p)
		}
		checks = append(checks, cost.CheckImage(container.Name, container.Image, facts))
	}
	return checks
}

// imageDTOs is the API form of image verdicts. The account of a no_permission or not_connected image is shown by name.
func imageDTOs(checks []cost.ImageCheck, accounts map[string]string) []ARM64ImageDTO {
	out := make([]ARM64ImageDTO, 0, len(checks))
	for _, check := range checks {
		dto := ARM64ImageDTO{Container: check.Container, Image: check.Image, State: check.State, Reason: check.Reason}
		if check.Detail != "" {
			dto.Account = accounts[check.Detail]
			if dto.Account == "" {
				dto.Account = check.Detail
			}
		}
		out = append(out, dto)
	}
	return out
}

// arm64Section evaluates every visible service for ARM64 from polled rows and the stored image check. It makes one lookup
// for all images, never one per service, and never calls a registry or AWS.
func (s *Server) arm64Section(ctx context.Context, facts []store.CostServiceFact, spend map[model.ServiceKey]*serviceSpend, tags map[authz.TagID]TagDTO, accounts map[string]string, subject authz.Subject) (CostARM64DTO, error) {
	section := CostARM64DTO{CheckEnabled: s.Cfg.ARM64.Check, Services: []CostARM64ServiceDTO{}}
	if !section.CheckEnabled {
		return section, nil
	}
	var all []string
	seen := map[string]bool{}
	for _, fact := range facts {
		if fact.Architecture == "ARM64" || !fact.RequiresFargate {
			continue
		}
		for _, container := range containerImages(fact.ContainerDefinitions) {
			if !seen[container.Image] {
				seen[container.Image] = true
				all = append(all, container.Image)
			}
		}
	}
	stored, err := s.Store.ImagePlatformsFor(ctx, all)
	if err != nil {
		return section, err
	}
	for _, fact := range facts {
		totals := spend[fact.Key]
		if totals == nil {
			totals = &serviceSpend{}
		}
		result := cost.EvaluateARM64(cost.ARM64Input{
			Region: fact.Key.Region, Architecture: fact.Architecture, OSFamily: fact.OSFamily, RequiresFargate: fact.RequiresFargate,
			UnpricedTasks: totals.notFargate, Tasks: totals.taskList,
			Images: imageChecks(containerImages(fact.ContainerDefinitions), stored),
		})
		switch result.Status {
		case cost.ARM64AlreadyARM64:
			section.AlreadyARM64++
			continue
		case cost.ARM64Ready, cost.ARM64Blocked, cost.ARM64Unknown:
		default:
			continue
		}
		saving := result.SavingMonthly
		section.Services = append(section.Services, CostARM64ServiceDTO{
			ServiceID: fact.Key.ID(), Service: fact.Key.Service, Cluster: fact.Key.Cluster, Region: fact.Key.Region,
			Tag: inheritedTag(tags, fact.TagID, fact.TagInherited), Status: string(result.Status),
			Tasks: totals.tasks(), SpotTasks: totals.spot, SavingMonthly: &saving,
			Images: imageDTOs(imageChecks(containerImages(fact.ContainerDefinitions), stored), accounts), AutoScaled: fact.AutoScaled,
			CanEdit: authz.Can(subject, authz.Edit, fact.TagID),
		})
	}
	return section, nil
}

// getARM64Readiness answers GET /api/services/{id}/arm64-readiness for the Deploy tab's banner: what the image check says
// about the images of the service's current task definition. A service the caller cannot see is a 404.
func (s *Server) getARM64Readiness(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	service, err := s.Store.GetService(ctx, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	definition, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, service.TaskDefinitionARN)
	if err != nil {
		s.fail(w, err)
		return
	}
	var described struct {
		ContainerDefinitions json.RawMessage `json:"ContainerDefinitions"`
	}
	_ = json.Unmarshal(definition.Raw, &described)
	containers := containerImages(described.ContainerDefinitions)
	images := make([]string, 0, len(containers))
	for _, container := range containers {
		images = append(images, container.Image)
	}
	stored, err := s.Store.ImagePlatformsFor(ctx, images)
	if err != nil {
		s.fail(w, err)
		return
	}
	accounts, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	checks := imageChecks(containers, stored)
	status, _ := cost.ImagesReadiness(checks)
	var checkedAt *time.Time
	for _, p := range stored {
		if p.CheckedAt != nil && (checkedAt == nil || p.CheckedAt.After(*checkedAt)) {
			checkedAt = p.CheckedAt
		}
	}
	writeJSON(w, http.StatusOK, ARM64ReadinessDTO{Status: string(status), Images: imageDTOs(checks, accounts), CheckedAt: checkedAt})
}
