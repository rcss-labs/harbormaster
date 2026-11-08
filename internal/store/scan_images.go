package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScanImage is one container image digest that someone asked Harbormaster to scan.
type ScanImage struct {
	Digest, PlatformDigest, Platform, ImageRef, Tag, RegistryKind, RegistryAccountID, RegistryRegion, Repository string
	Status, FailureCode, FailureDetail                                                                           string
	Attempts                                                                                                     int
	NextAttemptAt                                                                                                *time.Time
	Large                                                                                                        bool
	SyftVersion, ScannerImageDigest                                                                              string
	LastMatchedRunID                                                                                             *int64
	MatchedAt, MatchedDBBuiltAt                                                                                  *time.Time
	Packages                                                                                                     *int
	Truncated                                                                                                    bool
	PushedAt                                                                                                     *time.Time
	FirstSeenAt, LastRequestedAt                                                                                 time.Time
	QueuedAt, ScannedAt                                                                                          *time.Time
	RequestedBy                                                                                                  *int64
}

// maxScanAttempts is the number of scan attempts after which an image is marked failed.
const maxScanAttempts = 3

const scanImageColumns = `digest, coalesce(platform_digest, ''), coalesce(platform, ''), image_ref, coalesce(tag, ''),
	registry_kind, coalesce(registry_account_id, ''), coalesce(registry_region, ''), coalesce(repository, ''),
	status, coalesce(failure_code, ''), coalesce(failure_detail, ''), attempts, next_attempt_at, large,
	coalesce(syft_version, ''), coalesce(scanner_image_digest, ''),
	last_matched_run_id, matched_at, matched_db_built_at, packages, truncated, pushed_at,
	first_seen_at, last_requested_at, queued_at, scanned_at, requested_by`

// scanScanImage reads a row of scanImageColumns followed by any extra columns.
func scanScanImage(row pgx.Row, extra ...any) (ScanImage, error) {
	var i ScanImage
	dest := append([]any{&i.Digest, &i.PlatformDigest, &i.Platform, &i.ImageRef, &i.Tag,
		&i.RegistryKind, &i.RegistryAccountID, &i.RegistryRegion, &i.Repository,
		&i.Status, &i.FailureCode, &i.FailureDetail, &i.Attempts, &i.NextAttemptAt, &i.Large,
		&i.SyftVersion, &i.ScannerImageDigest,
		&i.LastMatchedRunID, &i.MatchedAt, &i.MatchedDBBuiltAt, &i.Packages, &i.Truncated, &i.PushedAt,
		&i.FirstSeenAt, &i.LastRequestedAt, &i.QueuedAt, &i.ScannedAt, &i.RequestedBy}, extra...)
	return i, row.Scan(dest...)
}

func (s *Store) queryScanImages(ctx context.Context, sql string, args ...any) ([]ScanImage, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanImage
	for rows.Next() {
		img, err := scanScanImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// "xmax = 0" is true only for a row this statement inserted. A request only moves last_requested_at of a known row:
// putting a scanned or failed image back in the queue is RequeueScanImage's job.
const upsertRequestedScanImageSQL = `insert into scan_images (digest, platform, image_ref, tag, registry_kind, registry_account_id,
	  registry_region, repository, status, first_seen_at, last_requested_at, queued_at, requested_by, updated_at)
	values ($1, nullif($2, ''), $3, nullif($4, ''), $5, nullif($6, ''), nullif($7, ''), nullif($8, ''),
	  'queued', $9, $9, $9, $10, $9)
	on conflict (digest) do update set last_requested_at = $9, updated_at = $9
	returning ` + scanImageColumns + `, (xmax = 0)`

// UpsertRequestedScanImage records a request to scan an image: a new digest is queued, a known one only has its request
// time moved. created is true only for a new row.
func (s *Store) UpsertRequestedScanImage(ctx context.Context, img ScanImage, now time.Time) (ScanImage, bool, error) {
	var created bool
	stored, err := scanScanImage(s.Pool.QueryRow(ctx, upsertRequestedScanImageSQL, img.Digest, img.Platform, img.ImageRef, img.Tag,
		img.RegistryKind, img.RegistryAccountID, img.RegistryRegion, img.Repository, now, img.RequestedBy), &created)
	if err != nil {
		return ScanImage{}, false, fmt.Errorf("upsert requested scan image %s: %w", img.Digest, err)
	}
	return stored, created, nil
}

// RequeueScanImage puts a scanned or failed image back in the queue with a clean slate (attempts, defers, retry time and
// failure cleared; large is left to the importer) and reports whether it did. A queued or scanning image is not touched.
func (s *Store) RequeueScanImage(ctx context.Context, digest string, now time.Time) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued', attempts = 0, defers = 0, next_attempt_at = null,
		  failure_code = null, failure_detail = null, queued_at = $2, updated_at = $2
		where digest = $1 and status in ('scanned', 'failed')`, digest, now)
	if err != nil {
		return false, fmt.Errorf("requeue scan image: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DueScanImages returns queued images whose retry time has come, least recently requested first.
func (s *Store) DueScanImages(ctx context.Context, now time.Time, limit int) ([]ScanImage, error) {
	imgs, err := s.queryScanImages(ctx, `select `+scanImageColumns+` from scan_images
		where status = 'queued' and (next_attempt_at is null or next_attempt_at <= $1)
		order by last_requested_at, digest limit $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("due scan images: %w", err)
	}
	return imgs, nil
}

func (s *Store) GetScanImage(ctx context.Context, digest string) (ScanImage, error) {
	img, err := scanScanImage(s.Pool.QueryRow(ctx, `select `+scanImageColumns+` from scan_images where digest = $1`, digest))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScanImage{}, ErrNotFound
	}
	if err != nil {
		return ScanImage{}, fmt.Errorf("get scan image: %w", err)
	}
	return img, nil
}

func (s *Store) ListScanImages(ctx context.Context, digests []string) ([]ScanImage, error) {
	imgs, err := s.queryScanImages(ctx, `select `+scanImageColumns+` from scan_images where digest = any($1) order by digest`, digests)
	if err != nil {
		return nil, fmt.Errorf("list scan images: %w", err)
	}
	return imgs, nil
}

// MarkScanImagesScanning moves the given digests of a run's batch to scanning; scanned images are allowed for rescans.
func (s *Store) MarkScanImagesScanning(ctx context.Context, digests []string, runID int64) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanning', updated_at = now()
		where digest = any($1) and status in ('queued', 'scanned')
		  and digest in (select digest from scan_run_images where run_id = $2)`, digests, runID)
	if err != nil {
		return fmt.Errorf("mark scan images scanning: %w", err)
	}
	return nil
}

// RequeueScanImages counts a failed attempt of images still scanning and queues them again; the third attempt marks them failed.
// Rows in any other state are left alone, so a repeated import after failover counts nothing twice.
func (s *Store) RequeueScanImages(ctx context.Context, digests []string, nextAttempt time.Time, large bool, failureCode, detail string) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set
		  attempts = attempts + 1,
		  status = case when attempts + 1 >= $6 then 'failed' else 'queued' end,
		  queued_at = case when attempts + 1 >= $6 then queued_at else now() end,
		  next_attempt_at = case when attempts + 1 >= $6 then null else $2::timestamptz end,
		  large = large or $3,
		  failure_code = nullif($4, ''), failure_detail = nullif($5, ''),
		  updated_at = now()
		where digest = any($1) and status = 'scanning'`, digests, nextAttempt, large, failureCode, detail, maxScanAttempts)
	if err != nil {
		return fmt.Errorf("requeue scan images: %w", err)
	}
	return nil
}

// DeferScanImages queues images again without counting an attempt.
func (s *Store) DeferScanImages(ctx context.Context, digests []string, nextAttempt time.Time, failureCode, detail string) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued', next_attempt_at = $2,
		  queued_at = case when status = 'queued' then queued_at else now() end,
		  failure_code = nullif($3, ''), failure_detail = nullif($4, ''), updated_at = now()
		where digest = any($1)`, digests, nextAttempt, failureCode, detail)
	if err != nil {
		return fmt.Errorf("defer scan images: %w", err)
	}
	return nil
}

// RequeueScanningWithoutRun queues images left scanning that no active run lists, and returns how many.
func (s *Store) RequeueScanningWithoutRun(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued', queued_at = now(), updated_at = now()
		where status = 'scanning' and not exists (
		  select 1 from scan_run_images ri join scan_runs r on r.id = ri.run_id
		  where ri.digest = scan_images.digest and r.status in ('launching', 'running'))`)
	if err != nil {
		return 0, fmt.Errorf("requeue scanning without run: %w", err)
	}
	return tag.RowsAffected(), nil
}

// expiredScanImagesWhere is the one definition of an expired scan result: finished, not requested since the cutoff ($1),
// and with no risk acceptance still in force at now ($2), so an acceptance outlives an idle image only while it applies.
const expiredScanImagesWhere = `status in ('scanned', 'failed') and last_requested_at < $1
	and not exists (select 1 from scan_risk_acceptances a where a.digest = scan_images.digest
	  and a.revoked_at is null and a.expires_at > $2)`

// ExpiredScanImages lists digests of finished scans last requested before cutoff, oldest request first.
func (s *Store) ExpiredScanImages(ctx context.Context, cutoff, now time.Time, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `select digest from scan_images where `+expiredScanImagesWhere+`
		order by last_requested_at, digest limit $3`, cutoff, now, limit)
	if err != nil {
		return nil, fmt.Errorf("expired scan images: %w", err)
	}
	digests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("expired scan images: %w", err)
	}
	return digests, nil
}

// BumpScanImageDefers queues scanning images again like DeferScanImages and counts the defer. It returns the digests whose
// defers reached limit, which the caller fails. Rows not scanning are left alone, so a repeated import counts nothing twice.
func (s *Store) BumpScanImageDefers(ctx context.Context, digests []string, nextAttempt time.Time, failureCode, detail string, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `update scan_images set status = 'queued', next_attempt_at = $2, defers = defers + 1,
		  queued_at = now(), failure_code = nullif($3, ''), failure_detail = nullif($4, ''), updated_at = now()
		where digest = any($1) and status = 'scanning'
		returning digest, defers`, digests, nextAttempt, failureCode, detail)
	if err != nil {
		return nil, fmt.Errorf("bump scan image defers: %w", err)
	}
	defer rows.Close()
	var over []string
	for rows.Next() {
		var digest string
		var defers int
		if err := rows.Scan(&digest, &defers); err != nil {
			return nil, fmt.Errorf("bump scan image defers: %w", err)
		}
		if defers >= limit {
			over = append(over, digest)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bump scan image defers: %w", err)
	}
	return over, nil
}

// FailScanImages marks queued or scanning images failed at once, with no retry.
func (s *Store) FailScanImages(ctx context.Context, digests []string, code, detail string) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set status = 'failed', next_attempt_at = null, defers = 0,
		  failure_code = nullif($2, ''), failure_detail = nullif($3, ''), updated_at = now()
		where digest = any($1) and status in ('queued', 'scanning')`, digests, code, detail)
	if err != nil {
		return fmt.Errorf("fail scan images: %w", err)
	}
	return nil
}

// CountQueuedScanImages counts the images waiting for a scan, retries not yet due included.
func (s *Store) CountQueuedScanImages(ctx context.Context) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from scan_images where status = 'queued'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count queued scan images: %w", err)
	}
	return n, nil
}
