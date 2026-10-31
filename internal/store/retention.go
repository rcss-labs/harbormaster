package store

import (
	"context"
	"fmt"
	"time"
)

// Retention sets how long each kind of row is kept; a zero duration keeps that table forever.
type Retention struct {
	StoppedTasks, ServiceEvents, CountsHistory, OperationLog, DeploymentHistory, AuditLog, OrphanTags time.Duration
}

// RetentionResult maps a table key ("tasks", "service_events", ...) to rows deleted.
type RetentionResult map[string]int64

// loginAttemptsRetention is fixed: it only has to outlast any sane login throttle window.
const loginAttemptsRetention = 24 * time.Hour

// RunRetention deletes rows older than the configured ages, measured from now.
func (s *Store) RunRetention(ctx context.Context, now time.Time, r Retention) (RetentionResult, error) {
	result := RetentionResult{}

	timed := []struct {
		key, sql string
		age      time.Duration
	}{
		{"tasks", `delete from tasks where last_status = 'STOPPED' and stopped_at < $1`, r.StoppedTasks},
		{"service_events", `delete from service_events where created_at < $1`, r.ServiceEvents},
		{"service_counts_history", `delete from service_counts_history where ts < $1`, r.CountsHistory},
		{"operation_log", `delete from operation_log where ts < $1`, r.OperationLog},
		{"deployment_history", `delete from deployment_history where started_at < $1`, r.DeploymentHistory},
	}
	for _, step := range timed {
		if step.age <= 0 {
			continue
		}
		tag, err := s.Pool.Exec(ctx, step.sql, now.Add(-step.age))
		if err != nil {
			return result, fmt.Errorf("retention %s: %w", step.key, err)
		}
		result[step.key] = tag.RowsAffected()
	}

	if r.AuditLog > 0 {
		n, err := s.DeleteAuditBefore(ctx, now.Add(-r.AuditLog))
		if err != nil {
			return result, fmt.Errorf("retention audit_log: %w", err)
		}
		result["audit_log"] = n
	}

	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil {
		return result, fmt.Errorf("retention sessions: %w", err)
	}
	result["sessions"] = n

	n, err = s.DeleteLoginAttemptsBefore(ctx, now.Add(-loginAttemptsRetention))
	if err != nil {
		return result, fmt.Errorf("retention login_attempts: %w", err)
	}
	result["login_attempts"] = n

	if r.OrphanTags > 0 {
		clusters, services, err := s.deleteOrphanedTagsBeforeCounts(ctx, now.Add(-r.OrphanTags))
		if err != nil {
			return result, fmt.Errorf("retention orphan tags: %w", err)
		}
		result["cluster_tags"] = clusters
		result["service_tags"] = services
	}
	return result, nil
}
