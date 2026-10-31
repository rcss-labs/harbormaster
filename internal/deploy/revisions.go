package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	revisionsTTL      = 30 * time.Second
	maxRevisions      = 20
	registrarFallback = "terraform"
)

// Revision is one task definition revision of a service's family.
// Status is "deployed", "rolling_out", "not_deployed" or "previous".
type Revision struct {
	Revision              int
	ARN, Status, ImageTag string
	CPU, Memory           string
	RegisteredAt          time.Time
	RegisteredBy          string
}

// RevisionList is the newest revisions of a family, newest first.
// RollingOutRevision is 0 when no rollout is in progress.
type RevisionList struct {
	Items              []Revision
	DeployedRevision   int
	RollingOutRevision int
}

// Revisions lists the newest ACTIVE revisions of the service's task definition family
// (cached 30 s), plus the deployed revision when it is INACTIVE.
func (s *Service) Revisions(ctx context.Context, key model.ServiceKey) (RevisionList, error) {
	svc, primaryRef, err := s.primaryDefinition(ctx, key)
	if err != nil {
		return RevisionList{}, err
	}
	primary, _ := PrimaryDeployment(svc)
	primaryARN := aws.ToString(primary.TaskDefinition)
	family, primaryRevision, err := ParseRevisionARN(primaryARN)
	if err != nil {
		return RevisionList{}, err
	}

	arns, err := s.familyRevisionARNs(ctx, key, family)
	if err != nil {
		return RevisionList{}, err
	}
	// Deployments may reference revisions outside the newest ACTIVE ones (for example INACTIVE).
	for _, deployment := range svc.Deployments {
		status := aws.ToString(deployment.Status)
		deploymentARN := aws.ToString(deployment.TaskDefinition)
		if (status == "PRIMARY" || status == "ACTIVE") && deploymentARN != "" && !containsString(arns, deploymentARN) {
			arns = append(arns, deploymentARN)
		}
	}

	list := RevisionList{}
	deployedRevisions := map[int]bool{}
	if primary.RolloutState == ecstypes.DeploymentRolloutStateInProgress {
		list.RollingOutRevision = primaryRevision
		for _, deployment := range svc.Deployments {
			if aws.ToString(deployment.Status) != "ACTIVE" {
				continue
			}
			_, revision, err := ParseRevisionARN(aws.ToString(deployment.TaskDefinition))
			if err != nil {
				continue
			}
			deployedRevisions[revision] = true
			if revision > list.DeployedRevision {
				list.DeployedRevision = revision
			}
		}
	} else {
		list.DeployedRevision = primaryRevision
		deployedRevisions[primaryRevision] = true
	}

	registrars, err := s.Store.RevisionRegistrars(ctx, arns)
	if err != nil {
		return RevisionList{}, err
	}
	for _, arn := range arns {
		ref := primaryRef
		if arn != primaryARN {
			if ref, err = s.loadTaskDefinition(ctx, key, arn); err != nil {
				return RevisionList{}, err
			}
		}
		revision := ref.Revision
		registeredBy := registrars[arn]
		if registeredBy == "" {
			registeredBy = registrarFallback
		}
		list.Items = append(list.Items, Revision{
			Revision: revision, ARN: arn, ImageTag: ref.ImageTag(key.Service),
			CPU: ref.CPU, Memory: ref.Memory, RegisteredAt: ref.RegisteredAt, RegisteredBy: registeredBy,
			Status: revisionStatus(revision, primaryRevision, list.RollingOutRevision, deployedRevisions),
		})
	}
	sortRevisionsNewestFirst(list.Items)
	return list, nil
}

func revisionStatus(revision, primaryRevision, rollingOut int, deployed map[int]bool) string {
	switch {
	case revision == rollingOut && rollingOut != 0:
		return "rolling_out"
	case deployed[revision]:
		return "deployed"
	case revision > primaryRevision:
		return "not_deployed"
	default:
		return "previous"
	}
}

// familyRevisionARNs returns the ARNs of the newest ACTIVE revisions of exactly this family.
// ListTaskDefinitions matches by prefix, so "api" would also return "api-worker".
func (s *Service) familyRevisionARNs(ctx context.Context, key model.ServiceKey, family string) ([]string, error) {
	cacheKey := revisionCacheKey(key, family)
	if arns, ok := s.revisionCache().get(cacheKey, s.now()); ok {
		return append([]string(nil), arns...), nil
	}
	ecsAPI, err := s.Clients.ECS(ctx, targetOf(key))
	if err != nil {
		return nil, err
	}
	var arns []string
	var next *string
	for len(arns) < maxRevisions {
		out, err := ecsAPI.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(family),
			Status:       ecstypes.TaskDefinitionStatusActive,
			Sort:         ecstypes.SortOrderDesc,
			MaxResults:   aws.Int32(maxRevisions),
			NextToken:    next,
		})
		if err != nil {
			return nil, wrapAWS(err)
		}
		for _, arn := range out.TaskDefinitionArns {
			if arnFamily, _, err := ParseRevisionARN(arn); err == nil && arnFamily == family && len(arns) < maxRevisions {
				arns = append(arns, arn)
			}
		}
		if next = out.NextToken; next == nil {
			break
		}
	}
	s.revisionCache().put(cacheKey, arns, s.now())
	return append([]string(nil), arns...), nil
}

func sortRevisionsNewestFirst(items []Revision) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Revision > items[j-1].Revision; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// primaryDefinition returns the polled service and the stored task definition of its PRIMARY deployment.
func (s *Service) primaryDefinition(ctx context.Context, key model.ServiceKey) (ecstypes.Service, model.TaskDefinitionRef, error) {
	snapshot, err := s.Store.GetService(ctx, key)
	if err != nil {
		return ecstypes.Service{}, model.TaskDefinitionRef{}, fmt.Errorf("get service: %w", err)
	}
	svc, err := ParseService(snapshot.Raw)
	if err != nil {
		return ecstypes.Service{}, model.TaskDefinitionRef{}, err
	}
	primary, ok := PrimaryDeployment(svc)
	if !ok {
		return ecstypes.Service{}, model.TaskDefinitionRef{}, fmt.Errorf("service has no primary deployment")
	}
	ref, err := s.loadTaskDefinition(ctx, key, aws.ToString(primary.TaskDefinition))
	if err != nil {
		return ecstypes.Service{}, model.TaskDefinitionRef{}, err
	}
	return svc, ref, nil
}

func revisionCacheKey(key model.ServiceKey, family string) string {
	return fmt.Sprintf("%s/%s/%s", key.AccountID, key.Region, family)
}
