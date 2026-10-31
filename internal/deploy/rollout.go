package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ErrRolloutRunning is returned when a deployment is still IN_PROGRESS.
var ErrRolloutRunning = errors.New("A rollout is already running")

// UnsupportedControllerError means the service is deployed by something other than the ECS rolling controller.
type UnsupportedControllerError struct{ Name string }

func (e *UnsupportedControllerError) Error() string { return ControllerBanner(e.Name) }

// Rollout is a deployment Harbormaster started. Kind is "deploy", "rollback" or "restart".
type Rollout struct {
	DeploymentID             string
	FromRevision, ToRevision int
	Kind                     string
}

// gatedService re-reads the service from AWS and refuses controllers Harbormaster cannot
// deploy and services that are mid-rollout. It returns the PRIMARY revision and ARN.
func (s *Service) gatedService(ctx context.Context, key model.ServiceKey) (api ECSClient, family string, revision int, arn string, err error) {
	api, live, err := s.liveService(ctx, key)
	if err != nil {
		return nil, "", 0, "", err
	}
	if controller := Controller(live); !controller.Supported {
		return nil, "", 0, "", &UnsupportedControllerError{Name: controller.Name}
	}
	if RolloutRunning(live) {
		return nil, "", 0, "", ErrRolloutRunning
	}
	primary, ok := PrimaryDeployment(live)
	if !ok {
		return nil, "", 0, "", errors.New("service has no primary deployment")
	}
	arn = aws.ToString(primary.TaskDefinition)
	family, revision, err = ParseRevisionARN(arn)
	return api, family, revision, arn, err
}

// Deploy points the service at revision of its family. An older revision is a rollback.
func (s *Service) Deploy(ctx context.Context, subj authz.Subject, key model.ServiceKey, revision int) (Rollout, error) {
	if revision <= 0 {
		return Rollout{}, &ValidationError{Message: "Revision must be a positive number"}
	}
	api, family, current, _, err := s.gatedService(ctx, key)
	if err != nil {
		return Rollout{}, err
	}
	if revision == current {
		return Rollout{}, &ValidationError{Message: fmt.Sprintf("Revision :%d is already deployed. Use Restart to replace its tasks.", revision)}
	}
	reference := fmt.Sprintf("%s:%d", family, revision)
	out, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(key.Cluster), Service: aws.String(key.Service), TaskDefinition: aws.String(reference),
	})
	if err != nil {
		return Rollout{}, wrapAWS(err)
	}
	kind, text := "deploy", fmt.Sprintf("Deployed %s.", reference)
	if revision < current {
		kind, text = "rollback", fmt.Sprintf("Rolled back %s.", reference)
	}
	return s.recordRollout(ctx, subj, key, out.Service, kind, "deploy", text, current, revision)
}

// Restart replaces the service's tasks on its current revision (force new deployment).
func (s *Service) Restart(ctx context.Context, subj authz.Subject, key model.ServiceKey) (Rollout, error) {
	api, _, current, _, err := s.gatedService(ctx, key)
	if err != nil {
		return Rollout{}, err
	}
	out, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(key.Cluster), Service: aws.String(key.Service), ForceNewDeployment: true,
	})
	if err != nil {
		return Rollout{}, wrapAWS(err)
	}
	text := fmt.Sprintf("Restarted service (force new deployment) on revision %d.", current)
	return s.recordRollout(ctx, subj, key, out.Service, "restart", "restart", text, current, current)
}

// recordRollout attributes the new PRIMARY deployment to the user and logs the operation.
// kind is the Rollout kind; storedKind is what deployment_history keeps (rollbacks are deploys).
func (s *Service) recordRollout(ctx context.Context, subj authz.Subject, key model.ServiceKey, svc *ecstypes.Service,
	kind, storedKind, text string, from, to int) (Rollout, error) {
	if svc == nil {
		return Rollout{}, errors.New("UpdateService returned no service")
	}
	primary, ok := PrimaryDeployment(*svc)
	if !ok {
		return Rollout{}, errors.New("UpdateService returned no primary deployment")
	}
	rollout := Rollout{DeploymentID: aws.ToString(primary.Id), FromRevision: from, ToRevision: to, Kind: kind}
	now := s.now()
	err := s.Store.AttributeDeployment(ctx, store.AttributedDeployment{
		Key: key, DeploymentID: rollout.DeploymentID, TaskDefinitionARN: aws.ToString(primary.TaskDefinition),
		Revision: to, Kind: storedKind, StartedByUserID: subj.UserID, StartedAt: now,
	})
	if err != nil {
		return Rollout{}, err
	}
	err = s.Store.AppendOperation(ctx, store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
		ActorUserID: &subj.UserID, Source: "harbormaster", Text: text,
		Details: map[string]any{"deployment_id": rollout.DeploymentID, "from_revision": from, "to_revision": to},
	})
	if err != nil {
		return Rollout{}, err
	}
	return rollout, nil
}
