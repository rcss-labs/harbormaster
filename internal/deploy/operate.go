package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// DesiredCountInfo is a service's live desired and running task counts.
// AutoScaling is nil when no auto scaling range is registered for the service.
type DesiredCountInfo struct {
	Desired, Running, Max int
	AutoScaling           *ScalingRange
}

// ScalingRange is the task count range auto scaling keeps a service within.
type ScalingRange struct{ Min, Max int }

// DesiredCountResult reports a desired count request. Warning is set when auto
// scaling will move the requested count back into its range.
type DesiredCountResult struct {
	Desired, Previous int
	Changed           bool
	Warning           string
}

// Stopped is a task Harbormaster asked ECS to stop. ServiceName is empty for standalone tasks.
type Stopped struct {
	TaskARN, Reason, ServiceName string
	// Warning is set when AWS stopped the task but Harbormaster could not log it.
	Warning string
}

// maxDesired is the configured desired count ceiling, or DefaultMaxDesiredCount when unset.
func (s *Service) maxDesired() int {
	if s.MaxDesiredCount < 1 {
		return DefaultMaxDesiredCount
	}
	return s.MaxDesiredCount
}

// autoScalingRange returns the range registered for key, or nil when there is none.
func (s *Service) autoScalingRange(ctx context.Context, key model.ServiceKey) (*ScalingRange, error) {
	if s.Scaling == nil {
		return nil, nil
	}
	low, high, ok, err := s.Scaling.Range(ctx, key)
	if err != nil || !ok {
		return nil, err
	}
	return &ScalingRange{Min: low, Max: high}, nil
}

// scalingWarning explains that auto scaling will move n back into scaling's range, or is empty when n is inside it.
func scalingWarning(scaling *ScalingRange, n int) string {
	if scaling == nil || (n >= scaling.Min && n <= scaling.Max) {
		return ""
	}
	return fmt.Sprintf("Auto scaling keeps this service between %d and %d. It will move %d back into that range.",
		scaling.Min, scaling.Max, n)
}

// DesiredCount reads the service's desired and running counts live from ECS.
func (s *Service) DesiredCount(ctx context.Context, key model.ServiceKey) (DesiredCountInfo, error) {
	_, live, err := s.liveService(ctx, key)
	if err != nil {
		return DesiredCountInfo{}, err
	}
	scaling, err := s.autoScalingRange(ctx, key)
	if err != nil {
		return DesiredCountInfo{}, err
	}
	return DesiredCountInfo{
		Desired: int(live.DesiredCount), Running: int(live.RunningCount),
		Max: s.maxDesired(), AutoScaling: scaling,
	}, nil
}

// SetDesiredCount sets the service's desired count to n. Setting the current count is a
// no-op: no UpdateService call and no operation. Rollouts and unsupported controllers are allowed.
func (s *Service) SetDesiredCount(ctx context.Context, subj authz.Subject, key model.ServiceKey, n int) (DesiredCountResult, error) {
	if ceiling := s.maxDesired(); n < 0 || n > ceiling {
		return DesiredCountResult{}, &ValidationError{Message: fmt.Sprintf("Desired count must be between 0 and %d", ceiling)}
	}
	if s.Guard != nil {
		if err := s.Guard.DesiredCount(ctx, key); err != nil {
			return DesiredCountResult{}, err
		}
	}
	api, live, err := s.liveService(ctx, key)
	if err != nil {
		return DesiredCountResult{}, err
	}
	scaling, err := s.autoScalingRange(ctx, key)
	if err != nil {
		return DesiredCountResult{}, err
	}
	previous := int(live.DesiredCount)
	result := DesiredCountResult{Desired: n, Previous: previous, Warning: scalingWarning(scaling, n)}
	if n == previous {
		return result, nil
	}
	if _, err := api.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(key.Cluster), Service: aws.String(key.Service), DesiredCount: aws.Int32(int32(n)),
	}); err != nil {
		return DesiredCountResult{}, wrapAWS(err)
	}
	result.Changed = true
	// AWS has applied the change; a failed log write is a warning, not an error that invites a retry.
	logged := store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
		ActorUserID: &subj.UserID, Source: "harbormaster",
		Text:    fmt.Sprintf("Changed desired count from %d to %d.", previous, n),
		Details: map[string]any{"op": "scale", "from_desired": previous, "to_desired": n},
	}
	if warning := s.logAfterAWS(ctx, logged, "desired count change"); warning != "" {
		result.Warning = strings.TrimSpace(result.Warning + " " + warning)
	}
	return result, nil
}

// logAfterAWS appends op in a transaction after AWS already made the change. It returns a
// user-facing warning when that fails, and "" on success.
func (s *Service) logAfterAWS(ctx context.Context, op store.Operation, what string) string {
	recordCtx := context.WithoutCancel(ctx)
	err := s.inTx(recordCtx, func(tx store.Tx) error { return tx.AppendOperation(recordCtx, op) })
	if err == nil {
		return ""
	}
	slog.ErrorContext(ctx, "AWS accepted the change but logging it failed", "change", what, "service", op.Service, "error", err)
	return "The change was applied, but Harbormaster could not record it in the operations log."
}

// StopTask asks ECS to stop a task. The operation is logged against the task's service,
// or as a standalone operation with the cluster and task ARN in its details.
func (s *Service) StopTask(ctx context.Context, subj authz.Subject, accountID, region, taskARN string) (Stopped, error) {
	task, err := s.Store.GetTask(ctx, accountID, region, taskARN)
	if err != nil {
		return Stopped{}, err
	}
	ecsAPI, err := s.Clients.ECS(ctx, awsx.Target{AccountID: accountID, Region: region})
	if err != nil {
		return Stopped{}, err
	}
	reason := fmt.Sprintf("Stopped by %s via Harbormaster", subj.Username)
	if _, err := ecsAPI.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(task.Cluster.Cluster), Task: aws.String(taskARN), Reason: aws.String(reason),
	}); err != nil {
		return Stopped{}, wrapAWS(err)
	}
	logged := store.Operation{
		AccountID: accountID, Region: region, Cluster: task.Cluster.Cluster,
		ActorUserID: &subj.UserID, Source: "harbormaster",
		Text:    fmt.Sprintf("Stopped task %s.", shortTaskID(taskARN)),
		Details: map[string]any{"op": "stop_task"},
	}
	if task.ServiceName != "" {
		logged.Service = &task.ServiceName
	} else {
		logged.Details["cluster"], logged.Details["task_arn"] = task.Cluster.Cluster, taskARN
	}
	warning := s.logAfterAWS(ctx, logged, "stop task")
	return Stopped{TaskARN: taskARN, Reason: reason, ServiceName: task.ServiceName, Warning: warning}, nil
}

// shortTaskID is the first 12 characters of a task's id, the last segment of its ARN.
func shortTaskID(taskARN string) string {
	id := taskARN[strings.LastIndex(taskARN, "/")+1:]
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}
