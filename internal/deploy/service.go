package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// DefaultMaxDesiredCount is the desired count ceiling when none is configured.
const DefaultMaxDesiredCount = 50

// ScalingRangeSource reports the auto scaling range registered for a service.
// Plan 03 wires the auto scaling snapshot; until then Service.Scaling is nil.
type ScalingRangeSource interface {
	Range(ctx context.Context, key model.ServiceKey) (min, max int, ok bool, err error)
}

// Service runs the change-path operations on ECS services: registering task
// definition revisions, deploying, rolling back and restarting.
// Permission checks live in the API layer, not here.
type Service struct {
	Store           *store.Store
	Clients         awsx.Clients
	Scaling         ScalingRangeSource
	MaxDesiredCount int
	Now             func() time.Time
	// InTx runs the post-AWS writes of one action in a single transaction. Nil means
	// Store.InTx; tests replace it to make the recording step fail.
	InTx func(ctx context.Context, fn func(store.Tx) error) error

	// Per-instance caches, created on first use so a zero-value Service works.
	cacheMu    sync.Mutex
	imageCache *ttlCache[[]ImageTagInfo]
	revCache   *ttlCache[[]string]
}

func (s *Service) imageTagCache() *ttlCache[[]ImageTagInfo] {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.imageCache == nil {
		s.imageCache = newTTLCache[[]ImageTagInfo](imageTagsTTL)
	}
	return s.imageCache
}

func (s *Service) revisionCache() *ttlCache[[]string] {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.revCache == nil {
		s.revCache = newTTLCache[[]string](revisionsTTL)
	}
	return s.revCache
}

// inTx runs fn in one transaction on the injected or the real store.
func (s *Service) inTx(ctx context.Context, fn func(store.Tx) error) error {
	if s.InTx != nil {
		return s.InTx(ctx, fn)
	}
	return s.Store.InTx(ctx, fn)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// AWSError is an error response from AWS. Validation is true when AWS rejected
// the request's content (ClientException, InvalidParameterException); the API
// shows such messages verbatim as 422, anything else as 502.
type AWSError struct {
	Code, Message string
	Validation    bool
	cause         error
}

// Unwrap exposes the SDK error so callers can still classify it (awsx.IsThrottle).
func (e *AWSError) Unwrap() error { return e.cause }

func (e *AWSError) Error() string { return e.Message }

// wrapAWS converts an AWS SDK error into *AWSError. Errors that are not API errors pass through.
func wrapAWS(err error) error {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	code := apiErr.ErrorCode()
	return &AWSError{
		Code:       code,
		Message:    apiErr.ErrorMessage(),
		Validation: code == "ClientException" || code == "InvalidParameterException",
		cause:      err,
	}
}

// Deployed is the task definition a service currently runs (its PRIMARY deployment).
type Deployed struct {
	Family        string
	Revision      int
	ARN           string
	MainContainer string
	Definition    Definition
	Tags          []ecstypes.Tag
	Controller    ControllerInfo
	Strategy      string
}

// DeployedDefinition describes the PRIMARY deployment's task definition of a
// service, from the polled snapshot. The definition is read from the store,
// falling back to DescribeTaskDefinition (and caching the result) when the
// poller has not stored it yet.
func (s *Service) DeployedDefinition(ctx context.Context, key model.ServiceKey) (Deployed, error) {
	snapshot, err := s.Store.GetService(ctx, key)
	if err != nil {
		return Deployed{}, fmt.Errorf("get service: %w", err)
	}
	svc, err := ParseService(snapshot.Raw)
	if err != nil {
		return Deployed{}, err
	}
	return s.deployedFrom(ctx, key, svc)
}

// deployedFrom builds Deployed for the PRIMARY deployment of svc.
func (s *Service) deployedFrom(ctx context.Context, key model.ServiceKey, svc ecstypes.Service) (Deployed, error) {
	primary, ok := PrimaryDeployment(svc)
	if !ok {
		return Deployed{}, errors.New("service has no primary deployment")
	}
	arn := aws.ToString(primary.TaskDefinition)
	family, revision, err := ParseRevisionARN(arn)
	if err != nil {
		return Deployed{}, err
	}
	ref, err := s.loadTaskDefinition(ctx, key, arn)
	if err != nil {
		return Deployed{}, err
	}
	var described ecstypes.TaskDefinition
	if err := json.Unmarshal(ref.Raw, &described); err != nil {
		return Deployed{}, fmt.Errorf("decode task definition %s: %w", arn, err)
	}
	definition, err := ToRegisterReady(described)
	if err != nil {
		return Deployed{}, err
	}
	ecsAPI, err := s.Clients.ECS(ctx, targetOf(key))
	if err != nil {
		return Deployed{}, err
	}
	tags, err := ecsAPI.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	if err != nil {
		return Deployed{}, wrapAWS(err)
	}
	container, _ := MainContainer(definition, key.Service)
	return Deployed{
		Family: family, Revision: revision, ARN: arn, MainContainer: container, Definition: definition,
		Tags: tags.Tags, Controller: Controller(svc), Strategy: StrategyText(svc),
	}, nil
}

// loadTaskDefinition returns the stored task definition, describing and storing it when absent.
func (s *Service) loadTaskDefinition(ctx context.Context, key model.ServiceKey, arn string) (model.TaskDefinitionRef, error) {
	ref, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, arn)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.TaskDefinitionRef{}, err
	}
	ecsAPI, err := s.Clients.ECS(ctx, targetOf(key))
	if err != nil {
		return model.TaskDefinitionRef{}, err
	}
	out, err := ecsAPI.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(arn)})
	if err != nil {
		return model.TaskDefinitionRef{}, wrapAWS(err)
	}
	ref = taskDefinitionRef(key, *out.TaskDefinition, s.now())
	if err := s.Store.UpsertTaskDefinition(ctx, ref); err != nil {
		return model.TaskDefinitionRef{}, err
	}
	return ref, nil
}

func targetOf(key model.ServiceKey) awsx.Target {
	return awsx.Target{AccountID: key.AccountID, Region: key.Region}
}

// ECSClient is the ECS API the service calls.
type ECSClient = awsx.ECSAPI

// liveService re-reads the service with DescribeServices. It never uses the snapshot.
func (s *Service) liveService(ctx context.Context, key model.ServiceKey) (ECSClient, ecstypes.Service, error) {
	ecsAPI, err := s.Clients.ECS(ctx, targetOf(key))
	if err != nil {
		return nil, ecstypes.Service{}, err
	}
	out, err := ecsAPI.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(key.Cluster), Services: []string{key.Service},
	})
	if err != nil {
		return nil, ecstypes.Service{}, wrapAWS(err)
	}
	if len(out.Services) == 0 {
		return nil, ecstypes.Service{}, store.ErrNotFound
	}
	return ecsAPI, out.Services[0], nil
}

// taskDefinitionRef summarises a described task definition for the store's snapshot table.
func taskDefinitionRef(key model.ServiceKey, td ecstypes.TaskDefinition, now time.Time) model.TaskDefinitionRef {
	return poller.NormalizeTaskDefinition(targetOf(key), td, now)
}
