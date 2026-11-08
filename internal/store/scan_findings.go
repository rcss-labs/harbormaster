package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
)

// ScanFinding is one stored vulnerability finding of an image.
type ScanFinding struct {
	scan.Finding
	Digest                  string
	FirstSeenAt, LastSeenAt time.Time
	RunID                   int64
}

// ScanImageResult is what one scan run reported for one image. MatchedDBBuiltAt is the run's recorded DB time, never
// read from a scanner file.
type ScanImageResult struct {
	Status, FailureCode, FailureDetail, PlatformDigest, Platform, SyftVersion, ScannerImageDigest string
	ScannedAt                                                                                     *time.Time
	Packages                                                                                      *int
	Truncated                                                                                     bool
	MatchedDBBuiltAt                                                                              *time.Time
}

// findingInsertChunk bounds the statements queued in one batch round trip.
const findingInsertChunk = 500

// RecordScanResult stores one image's result in one transaction and is safe to replay. A scanned result replaces the
// findings (existing keys keep first_seen_at) and resets attempts and defers; any other status only sets the status,
// failure fields and defers, leaving the earlier findings. When the image row is gone (0 rows updated) no findings are
// written.
func (s *Store) RecordScanResult(ctx context.Context, digest string, runID int64, res ScanImageResult, findings []scan.Finding, now time.Time) error {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if res.Status != "scanned" {
			_, err := tx.Exec(ctx, `update scan_images set status = $2, failure_code = nullif($3, ''), failure_detail = nullif($4, ''),
				  defers = 0, updated_at = $5
				where digest = $1`, digest, res.Status, res.FailureCode, res.FailureDetail, now)
			return err
		}
		scannedAt := now
		if res.ScannedAt != nil {
			scannedAt = *res.ScannedAt
		}
		updated, err := tx.Exec(ctx, `update scan_images set status = 'scanned', failure_code = null, failure_detail = null,
			  attempts = 0, defers = 0, next_attempt_at = null,
			  platform_digest = nullif($2, ''), platform = coalesce(nullif($3, ''), platform),
			  syft_version = nullif($4, ''), scanner_image_digest = nullif($5, ''),
			  last_matched_run_id = $6, matched_at = $7, matched_db_built_at = $8,
			  packages = $9, truncated = $10, scanned_at = $11, updated_at = $7
			where digest = $1`, digest, res.PlatformDigest, res.Platform, res.SyftVersion, res.ScannerImageDigest,
			runID, now, res.MatchedDBBuiltAt, res.Packages, res.Truncated, scannedAt)
		if err != nil {
			return err
		}
		if updated.RowsAffected() == 0 {
			return nil
		}
		return replaceScanFindings(ctx, tx, digest, runID, findings, now)
	})
	if err != nil {
		return fmt.Errorf("record scan result %s: %w", digest, err)
	}
	return nil
}

func replaceScanFindings(ctx context.Context, tx pgx.Tx, digest string, runID int64, findings []scan.Finding, now time.Time) error {
	vulnIDs, names, versions := make([]string, len(findings)), make([]string, len(findings)), make([]string, len(findings))
	for i, f := range findings {
		vulnIDs[i], names[i], versions[i] = f.VulnID, f.PackageName, f.PackageVersion
	}
	if _, err := tx.Exec(ctx, `delete from scan_findings where digest = $1
		and (vuln_id, package_name, package_version) not in (select * from unnest($2::text[], $3::text[], $4::text[]))`,
		digest, vulnIDs, names, versions); err != nil {
		return err
	}
	// An existing key keeps its first_seen_at: the update leaves that column out.
	const upsert = `insert into scan_findings (digest, vuln_id, package_name, package_version, package_type, location, severity,
		  cvss, fixed_version, fix_state, data_source, url, first_seen_at, last_seen_at, run_id)
		values ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, $8, nullif($9, ''), nullif($10, ''), nullif($11, ''), nullif($12, ''), $13, $13, $14)
		on conflict (digest, vuln_id, package_name, package_version) do update set
		  package_type = excluded.package_type, location = excluded.location, severity = excluded.severity, cvss = excluded.cvss,
		  fixed_version = excluded.fixed_version, fix_state = excluded.fix_state, data_source = excluded.data_source,
		  url = excluded.url, last_seen_at = excluded.last_seen_at, run_id = excluded.run_id`
	for start := 0; start < len(findings); start += findingInsertChunk {
		end := min(start+findingInsertChunk, len(findings))
		var batch pgx.Batch
		for _, f := range findings[start:end] {
			batch.Queue(upsert, digest, f.VulnID, f.PackageName, f.PackageVersion, f.PackageType, f.Location, f.Severity,
				f.CVSS, f.FixedVersion, f.FixState, f.DataSource, f.URL, now, runID)
		}
		results := tx.SendBatch(ctx, &batch)
		for range end - start {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
	}
	return nil
}

// severityRankSQL orders findings from worst to least; anything unlisted sorts with unknown.
const severityRankSQL = `case severity when 'critical' then 0 when 'high' then 1 when 'medium' then 2 when 'low' then 3
	when 'negligible' then 4 else 5 end`

// ScanFindings lists an image's findings by severity, then vulnerability id.
func (s *Store) ScanFindings(ctx context.Context, digest string) ([]ScanFinding, error) {
	rows, err := s.Pool.Query(ctx, `select digest, vuln_id, severity, cvss, package_name, package_version,
		  coalesce(package_type, ''), coalesce(location, ''), coalesce(fixed_version, ''), coalesce(fix_state, ''),
		  coalesce(data_source, ''), coalesce(url, ''), first_seen_at, last_seen_at, run_id
		from scan_findings where digest = $1 order by `+severityRankSQL+`, vuln_id, package_name, package_version`, digest)
	if err != nil {
		return nil, fmt.Errorf("scan findings: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScanFinding, error) {
		var f ScanFinding
		err := row.Scan(&f.Digest, &f.VulnID, &f.Severity, &f.CVSS, &f.PackageName, &f.PackageVersion, &f.PackageType,
			&f.Location, &f.FixedVersion, &f.FixState, &f.DataSource, &f.URL, &f.FirstSeenAt, &f.LastSeenAt, &f.RunID)
		return f, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan findings: %w", err)
	}
	return out, nil
}

// OpenFindingCounts counts each digest's findings by severity, leaving out accepted ones, and counts the accepted ones.
// An acceptance is active while it is not revoked and expires after now. With a scope, a finding is accepted when every
// service of the set has an active acceptance; without one, only when the digest has requests and every requesting
// service has.
func (s *Store) OpenFindingCounts(ctx context.Context, digests []string, scope []model.ServiceKey, now time.Time) (map[string]scan.SeverityCounts, map[string]int, error) {
	args := []any{digests, now}
	const activeAcceptance = `from scan_risk_acceptances a where a.digest = f.digest and a.vuln_id = f.vuln_id
		and a.package_name = f.package_name and a.package_version = f.package_version
		and a.revoked_at is null and a.expires_at > $2`
	var acceptedSQL string
	if len(scope) > 0 {
		accounts, regions, clusters, services := make([]string, len(scope)), make([]string, len(scope)), make([]string, len(scope)), make([]string, len(scope))
		for i, key := range scope {
			accounts[i], regions[i], clusters[i], services[i] = key.AccountID, key.Region, key.Cluster, key.Service
		}
		args = append(args, accounts, regions, clusters, services)
		acceptedSQL = `not exists (select 1 from unnest($3::text[], $4::text[], $5::text[], $6::text[]) as s(account_id, region, cluster_name, service_name)
			where not exists (select 1 ` + activeAcceptance + `
			  and a.account_id = s.account_id and a.region = s.region and a.cluster_name = s.cluster_name and a.service_name = s.service_name))`
	} else {
		acceptedSQL = `(exists (select 1 from scan_requests r where r.digest = f.digest) and not exists (
			select 1 from scan_requests r where r.digest = f.digest and not exists (select 1 ` + activeAcceptance + `
			  and a.account_id = r.account_id and a.region = r.region and a.cluster_name = r.cluster_name and a.service_name = r.service_name)))`
	}
	rows, err := s.Pool.Query(ctx, `select f.digest, f.severity, `+acceptedSQL+` as accepted, count(*)
		from scan_findings f where f.digest = any($1) group by 1, 2, 3`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("open finding counts: %w", err)
	}
	defer rows.Close()
	counts, accepted := map[string]scan.SeverityCounts{}, map[string]int{}
	for rows.Next() {
		var digest, severity string
		var isAccepted bool
		var n int
		if err := rows.Scan(&digest, &severity, &isAccepted, &n); err != nil {
			return nil, nil, fmt.Errorf("open finding counts: %w", err)
		}
		if isAccepted {
			accepted[digest] += n
			continue
		}
		c := counts[digest]
		switch severity {
		case scan.SeverityCritical:
			c.Critical += n
		case scan.SeverityHigh:
			c.High += n
		case scan.SeverityMedium:
			c.Medium += n
		case scan.SeverityLow:
			c.Low += n
		case scan.SeverityNegligible:
			c.Negligible += n
		default:
			c.Unknown += n
		}
		counts[digest] = c
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("open finding counts: %w", err)
	}
	return counts, accepted, nil
}
