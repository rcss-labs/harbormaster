package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// RegisterInput is a draft edited from BaseRevision, the revision the editor opened.
type RegisterInput struct {
	BaseRevision int
	Definition   Definition
	// Deployed is the deployed definition the caller already read. Register uses it only when it
	// is the live PRIMARY revision; otherwise it reads the definition again.
	Deployed *Deployed
}

// Registered describes the new task definition revision.
type Registered struct {
	Revision              int
	ARN                   string
	ImageTag, CPU, Memory string
	// Warning is set when AWS accepted the revision but Harbormaster could not record it.
	Warning string
}

// StaleBaseError means the service moved to another revision while the draft was edited.
type StaleBaseError struct{ BaseRevision, DeployedRevision int }

func (e *StaleBaseError) Error() string {
	return fmt.Sprintf("The service moved from :%d to :%d while you were editing", e.BaseRevision, e.DeployedRevision)
}

// Register validates the draft against the deployed revision and registers it as a new
// revision of the same family, copying the deployed revision's tags. The service is
// re-read from AWS first: if its PRIMARY revision is not BaseRevision, nothing is registered.
func (s *Service) Register(ctx context.Context, subj authz.Subject, key model.ServiceKey, in RegisterInput) (Registered, error) {
	ecsAPI, live, err := s.liveService(ctx, key)
	if err != nil {
		return Registered{}, err
	}
	deployed, err := s.deployedForRegister(ctx, key, live, in.Deployed)
	if err != nil {
		return Registered{}, err
	}
	if deployed.Revision != in.BaseRevision {
		return Registered{}, &StaleBaseError{BaseRevision: in.BaseRevision, DeployedRevision: deployed.Revision}
	}
	input, err := Prepare(deployed.Definition, in.Definition, subj.Admin, key.AccountID, deployed.Tags)
	if err != nil {
		return Registered{}, err
	}
	out, err := ecsAPI.RegisterTaskDefinition(ctx, input)
	if err != nil {
		return Registered{}, wrapAWS(err)
	}
	if out.TaskDefinition == nil {
		return Registered{}, errors.New("RegisterTaskDefinition returned no task definition")
	}
	// AWS has accepted the revision. From here no failure may become an error: the user would
	// retry and register a duplicate. Evict the cache first so the revision is listed even when
	// recording fails, then record, and report a failure as a warning.
	td := *out.TaskDefinition
	s.revisionCache().delete(revisionCacheKey(key, aws.ToString(td.Family)))
	registered, summaryErr := summarizeRegistered(key, td)
	recordErr := s.recordRegistered(context.WithoutCancel(ctx), subj, key, td, registered)
	if err := errors.Join(summaryErr, recordErr); err != nil {
		slog.ErrorContext(ctx, "registered task definition but recording it failed",
			"task_definition_arn", registered.ARN, "error", err)
		registered.Warning = fmt.Sprintf("Registered :%d, but Harbormaster could not record it; the Revisions table may show it as registered by Terraform.", registered.Revision)
	}
	return registered, nil
}

// summarizeRegistered describes td for the response. On failure it still returns the identifying fields.
func summarizeRegistered(key model.ServiceKey, td ecstypes.TaskDefinition) (Registered, error) {
	registered := Registered{
		Revision: int(td.Revision), ARN: aws.ToString(td.TaskDefinitionArn),
		CPU: aws.ToString(td.Cpu), Memory: aws.ToString(td.Memory),
	}
	definition, err := ToRegisterReady(td)
	if err != nil {
		return registered, err
	}
	container, _ := MainContainer(definition, key.Service)
	registered.ImageTag = ImageTag(definition, container)
	return registered, nil
}

// deployedForRegister reuses the caller's Deployed when it describes the live PRIMARY revision,
// and builds it from the live service otherwise (the caller's copy came from a possibly stale snapshot).
func (s *Service) deployedForRegister(ctx context.Context, key model.ServiceKey, live ecstypes.Service, provided *Deployed) (Deployed, error) {
	if primary, ok := PrimaryDeployment(live); ok && provided != nil && provided.ARN != "" && provided.ARN == aws.ToString(primary.TaskDefinition) {
		reused := *provided
		reused.Tags = cloneTags(provided.Tags)
		return reused, nil
	}
	return s.deployedFrom(ctx, key, live)
}

// recordRegistered stores the new definition, who registered it, and the operation log entry, atomically.
func (s *Service) recordRegistered(ctx context.Context, subj authz.Subject, key model.ServiceKey, td ecstypes.TaskDefinition, registered Registered) error {
	now := s.now()
	family := aws.ToString(td.Family)
	return s.inTx(ctx, func(tx store.Tx) error {
		if err := tx.UpsertTaskDefinition(ctx, taskDefinitionRef(key, td, now)); err != nil {
			return err
		}
		if err := tx.RecordRevision(ctx, registered.ARN, subj.UserID, now); err != nil {
			return err
		}
		return tx.AppendOperation(ctx, store.Operation{
			AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
			ActorUserID: &subj.UserID, Source: "harbormaster",
			Text: fmt.Sprintf("Registered task definition %s:%d (image %s, %s CPU / %s MiB).",
				family, registered.Revision, registered.ImageTag, registered.CPU, registered.Memory),
			Details: map[string]any{"revision": registered.Revision, "task_definition_arn": registered.ARN},
		})
	})
}
