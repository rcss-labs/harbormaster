package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Retention sets how long each kind of row is kept; a zero duration keeps that table forever.
type Retention struct {
	StoppedTasks, ServiceEvents, CountsHistory, OperationLog, DeploymentHistory, AuditLog, OrphanTags time.Duration
	// Daily analytics tables are keyed by day and pruned on day < cutoff date.
	AvailabilityDaily, CapacityDaily, UtilDaily time.Duration
	// ScanRuns prunes finished scanner runs; ScanImages ages out running images no longer seen.
	ScanRuns, ScanImages time.Duration
}

// RetentionResult maps a table key ("tasks", "service_events", ...) to rows deleted.
type RetentionResult map[string]int64

// loginAttemptsRetention is fixed: it only has to outlast any sane login throttle window.
const loginAttemptsRetention = 24 * time.Hour

// metricsCacheRetention is fixed: cached CloudWatch series are only useful for a few minutes.
const metricsCacheRetention = 10 * time.Minute

// samlRetiredKeyRetention is fixed: SP metadata advertises a retired key for 7 days (spec D4).
const samlRetiredKeyRetention = 7 * 24 * time.Hour

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
		// Daily tables are keyed by UTC date; the cutoff is passed as a timestamp and cast.
		{"service_availability_daily", `delete from service_availability_daily where day < $1::date`, r.AvailabilityDaily},
		{"cluster_capacity_daily", `delete from cluster_capacity_daily where day < $1::date`, r.CapacityDaily},
		{"service_util_daily", `delete from service_util_daily where day < $1::date`, r.UtilDaily},
		// Deleting a run cascades to its scan_run_images batch.
		{"scan_runs", `delete from scan_runs where finished_at < $1 and status not in ('launching', 'running')`, r.ScanRuns},
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

	n, err = s.pruneMetricsCacheBefore(ctx, now.Add(-metricsCacheRetention))
	if err != nil {
		return result, fmt.Errorf("retention metrics_cache: %w", err)
	}
	result["metrics_cache"] = n

	for _, step := range []struct{ key, sql string }{
		{"saml_requests", `delete from saml_requests where expires_at < $1`},
		{"saml_assertion_ids", `delete from saml_assertion_ids where expires_at < $1`},
		{"saml_sp_keys", `delete from saml_sp_keys where retired_at < $1`},
	} {
		cutoff := now
		if step.key == "saml_sp_keys" {
			cutoff = now.Add(-samlRetiredKeyRetention)
		}
		tag, err := s.Pool.Exec(ctx, step.sql, cutoff)
		if err != nil {
			return result, fmt.Errorf("retention %s: %w", step.key, err)
		}
		result[step.key] = tag.RowsAffected()
	}

	if r.ScanImages > 0 {
		aged, err := s.ageOutScanImages(ctx, now, now.Add(-r.ScanImages))
		if err != nil {
			return result, fmt.Errorf("retention scan_images_aged: %w", err)
		}
		result["scan_images_aged"] = aged
	}

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

// ageOutScanImages marks running images not seen since cutoff as aged_out and drops what only they kept alive:
// their findings, candidate links, fix candidate, and candidate images no remaining link points to.
// A candidate that still has an SBOM object becomes aged_out instead of being deleted, so its object key
// stays listed for AgedOutSBOMKeys; such a row is deleted on a later run once the key is cleared.
func (s *Store) ageOutScanImages(ctx context.Context, now, cutoff time.Time) (int64, error) {
	var aged int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `update scan_images set status = 'aged_out', updated_at = $2
			where status <> 'aged_out' and role = 'running' and coalesce(last_seen_running_at, first_seen_at) < $1
			returning digest`, cutoff, now)
		if err != nil {
			return err
		}
		agedDigests, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		aged = int64(len(agedDigests))
		for _, sql := range []string{
			`delete from scan_findings where digest = any($1)`,
			`delete from scan_candidate_links where running_digest = any($1)`,
			`delete from scan_fix_candidates where running_digest = any($1)`,
		} {
			if _, err := tx.Exec(ctx, sql, agedDigests); err != nil {
				return err
			}
		}
		const unlinkedCandidates = `role = 'candidate' and not exists (
			select 1 from scan_candidate_links where candidate_digest = scan_images.digest)`
		if _, err := tx.Exec(ctx, `delete from scan_findings where digest in (
			select digest from scan_images where `+unlinkedCandidates+`)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update scan_images set status = 'aged_out', updated_at = $1
			where `+unlinkedCandidates+` and sbom_key is not null and status <> 'aged_out'`, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `delete from scan_images where `+unlinkedCandidates+` and sbom_key is null`)
		return err
	})
	return aged, err
}
