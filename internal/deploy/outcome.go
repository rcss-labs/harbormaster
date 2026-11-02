package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// supersededAfter is how long a deployment may be missing from the service
// snapshot before it is considered replaced by a newer one. The grace period
// covers the poller lagging behind a deployment that was just started.
const supersededAfter = 2 * time.Minute

// settledBefore is how old a deployment's last update may be for its outcome to
// count as news. Older ones were already settled when Harbormaster first saw
// them (e.g. a steady-state PRIMARY), so the row is closed without a log line.
const settledBefore = 10 * time.Minute

// OutcomeLogger closes deployment_history rows once ECS reports how a
// deployment ended, and writes one line per outcome to the operations log.
// It runs on every Harbormaster task; FinishDeployment's atomic claim ensures
// only one task writes the line.
type OutcomeLogger struct {
	Store    *store.Store
	Interval time.Duration
	Now      func() time.Time
	// InTx replaces Store.InTx; tests use it to make the claim-and-log step fail.
	InTx func(ctx context.Context, fn func(store.Tx) error) error
}

func NewOutcomeLogger(st *store.Store, interval time.Duration) *OutcomeLogger {
	return &OutcomeLogger{Store: st, Interval: interval, Now: time.Now}
}

// Run calls Once every Interval until ctx is done. Errors are logged, never fatal.
func (l *OutcomeLogger) Run(ctx context.Context) {
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()
	for {
		if err := l.Once(ctx); err != nil && ctx.Err() == nil {
			slog.Error("outcome logger pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Once runs one pass over all open deployments. A failure on one row does not
// stop the others; the errors are joined.
func (l *OutcomeLogger) Once(ctx context.Context) error {
	open, err := l.Store.OpenDeployments(ctx)
	if err != nil {
		return err
	}
	now := l.Now().UTC()
	var errs []error
	for _, row := range open {
		outcome, reason, silent := l.decide(row, now)
		if outcome == "" {
			continue
		}
		var failure *store.FailureInfo
		if outcome == "failed" {
			info, err := l.failureInfo(ctx, row, reason)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			failure = &info
		}
		// Claim and log together: if the log write fails the claim rolls back and the next pass retries.
		err := l.inTx(ctx, func(tx store.Tx) error {
			claimed, err := tx.FinishDeployment(ctx, row.Key, row.DeploymentID, outcome, now, failure)
			if err != nil || !claimed || silent {
				return err
			}
			details := map[string]any{
				"op": "outcome", "deployment_id": row.DeploymentID, "outcome": outcome,
				"kind": row.Kind, "started_by_user_id": row.StartedByUserID,
			}
			if failure != nil {
				details["failure_reason"] = failure.Group
			}
			return tx.AppendOperation(ctx, store.Operation{
				AccountID: row.Key.AccountID, Region: row.Key.Region, Cluster: row.Key.Cluster,
				Service: &row.Key.Service, Source: "harbormaster",
				Text:    outcomeText(deploymentLabel(row), outcome, reason),
				Details: details,
			})
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *OutcomeLogger) inTx(ctx context.Context, fn func(store.Tx) error) error {
	if l.InTx != nil {
		return l.InTx(ctx, fn)
	}
	return l.Store.InTx(ctx, fn)
}

// decide returns the outcome of an open deployment, or "" to leave it open.
func (l *OutcomeLogger) decide(row store.OpenDeployment, now time.Time) (outcome, reason string, silent bool) {
	if deployment, found := findDeployment(row); found {
		changedAt := lastChange(deployment)
		stale := !changedAt.IsZero() && now.Sub(changedAt) > settledBefore
		switch deployment.RolloutState {
		case ecstypes.DeploymentRolloutStateCompleted:
			return "completed", "", stale
		case ecstypes.DeploymentRolloutStateFailed:
			return "failed", derefString(deployment.RolloutStateReason), stale
		}
		return "", "", false
	}
	if now.Sub(row.StartedAt) > supersededAfter {
		return "superseded", "", false
	}
	return "", "", false
}

// lastChange is when ECS last updated the deployment; the zero time (treated
// as fresh) when the snapshot carries no timestamps.
func lastChange(d ecstypes.Deployment) time.Time {
	if d.UpdatedAt != nil {
		return *d.UpdatedAt
	}
	if d.CreatedAt != nil {
		return *d.CreatedAt
	}
	return time.Time{}
}

func findDeployment(row store.OpenDeployment) (ecstypes.Deployment, bool) {
	if row.ServiceRaw == nil {
		return ecstypes.Deployment{}, false
	}
	svc, err := ParseService(row.ServiceRaw)
	if err != nil {
		return ecstypes.Deployment{}, false
	}
	for _, d := range svc.Deployments {
		if derefString(d.Id) == row.DeploymentID {
			return d, true
		}
	}
	return ecstypes.Deployment{}, false
}

// deploymentLabel is "{family}:{revision}" from the row's task definition, falling
// back to the deployment in the snapshot, then to the bare service name.
func deploymentLabel(row store.OpenDeployment) string {
	arn := row.TaskDefinitionARN
	if arn == "" {
		if d, found := findDeployment(row); found {
			arn = derefString(d.TaskDefinition)
		}
	}
	if family, revision, err := ParseRevisionARN(arn); err == nil {
		return fmt.Sprintf("%s:%d", family, revision)
	}
	return row.Key.Service
}

func outcomeText(label, outcome, reason string) string {
	switch outcome {
	case "completed":
		return fmt.Sprintf("Deployment of %s completed.", label)
	case "failed":
		return fmt.Sprintf("Deployment of %s failed: %s", label, reason)
	default:
		return fmt.Sprintf("Deployment of %s was superseded.", label)
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// failureInfo classifies a failed deployment. The last stopped task is only
// looked up when the rollout reason alone does not say why.
func (l *OutcomeLogger) failureInfo(ctx context.Context, row store.OpenDeployment, rolloutReason string) (store.FailureInfo, error) {
	var lastStop *store.StoppedTaskReason
	if classifyRolloutReason(rolloutReason) == "" {
		var err error
		if lastStop, err = l.Store.LastStoppedTaskReason(ctx, row.Key, row.DeploymentID); err != nil {
			return store.FailureInfo{}, err
		}
	}
	detail := rolloutReason
	if detail == "" && lastStop != nil {
		detail = lastStop.Reason
	}
	return store.FailureInfo{Group: ClassifyFailure(rolloutReason, lastStop), Detail: detail}, nil
}

// classifyRolloutReason recognises ECS rollout reasons that name the cause; "" otherwise.
func classifyRolloutReason(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "circuit breaker"):
		return "circuit_breaker"
	case strings.Contains(lower, "health check"):
		return "health_check"
	}
	return ""
}

// ClassifyFailure buckets a failed deployment as circuit_breaker, health_check,
// failed_to_start, out_of_memory, stopped_by_user or other. The ECS rollout
// reason wins; otherwise the stop-reason group of the deployment's newest
// stopped task decides.
func ClassifyFailure(rolloutStateReason string, lastStop *store.StoppedTaskReason) string {
	if group := classifyRolloutReason(rolloutStateReason); group != "" {
		return group
	}
	if lastStop == nil {
		return "other"
	}
	switch lastStop.Group {
	case status.StopReasonGroups[0]:
		return "out_of_memory"
	case status.StopReasonGroups[1]:
		return "health_check"
	case status.StopReasonGroups[2]:
		return "stopped_by_user"
	case status.StopReasonGroups[4]:
		return "failed_to_start"
	}
	return "other"
}
