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
	ScannedAt                                                                                                    *time.Time
	RequestedBy                                                                                                  *int64
}

// maxScanAttempts is the number of scan attempts after which an image is marked failed.
const maxScanAttempts = 3

const scanImageColumns = `digest, coalesce(platform_digest, ''), coalesce(platform, ''), image_ref, coalesce(tag, ''),
	registry_kind, coalesce(registry_account_id, ''), coalesce(registry_region, ''), coalesce(repository, ''),
	status, coalesce(failure_code, ''), coalesce(failure_detail, ''), attempts, next_attempt_at, large,
	coalesce(syft_version, ''), coalesce(scanner_image_digest, ''),
	last_matched_run_id, matched_at, matched_db_built_at, packages, truncated, pushed_at,
	first_seen_at, last_requested_at, scanned_at, requested_by`

// scanScanImage reads a row of scanImageColumns followed by any extra columns.
func scanScanImage(row pgx.Row, extra ...any) (ScanImage, error) {
	var i ScanImage
	dest := append([]any{&i.Digest, &i.PlatformDigest, &i.Platform, &i.ImageRef, &i.Tag,
		&i.RegistryKind, &i.RegistryAccountID, &i.RegistryRegion, &i.Repository,
		&i.Status, &i.FailureCode, &i.FailureDetail, &i.Attempts, &i.NextAttemptAt, &i.Large,
		&i.SyftVersion, &i.ScannerImageDigest,
		&i.LastMatchedRunID, &i.MatchedAt, &i.MatchedDBBuiltAt, &i.Packages, &i.Truncated, &i.PushedAt,
		&i.FirstSeenAt, &i.LastRequestedAt, &i.ScannedAt, &i.RequestedBy}, extra...)
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

// "xmax = 0" is true only for a row this statement inserted.
// A failed row asked for again starts over: queued, no attempts, no failure.
const upsertRequestedScanImageSQL = `insert into scan_images (digest, platform, image_ref, tag, registry_kind, registry_account_id,
	  registry_region, repository, status, first_seen_at, last_requested_at, requested_by, updated_at)
	values ($1, nullif($2, ''), $3, nullif($4, ''), $5, nullif($6, ''), nullif($7, ''), nullif($8, ''),
	  'queued', $9, $9, $10, $9)
	on conflict (digest) do update set
	  last_requested_at = $9,
	  status = case when scan_images.status = 'failed' then 'queued' else scan_images.status end,
	  attempts = case when scan_images.status = 'failed' then 0 else scan_images.attempts end,
	  next_attempt_at = case when scan_images.status = 'failed' then null else scan_images.next_attempt_at end,
	  failure_code = case when scan_images.status = 'failed' then null else scan_images.failure_code end,
	  failure_detail = case when scan_images.status = 'failed' then null else scan_images.failure_detail end,
	  updated_at = $9
	returning ` + scanImageColumns + `, (xmax = 0)`

// UpsertRequestedScanImage records a request to scan an image: a new digest is queued, a known one has its request time
// moved, and a failed one is queued again with its attempts reset. created is true only for a new row.
func (s *Store) UpsertRequestedScanImage(ctx context.Context, img ScanImage, now time.Time) (ScanImage, bool, error) {
	var created bool
	stored, err := scanScanImage(s.Pool.QueryRow(ctx, upsertRequestedScanImageSQL, img.Digest, img.Platform, img.ImageRef, img.Tag,
		img.RegistryKind, img.RegistryAccountID, img.RegistryRegion, img.Repository, now, img.RequestedBy), &created)
	if err != nil {
		return ScanImage{}, false, fmt.Errorf("upsert requested scan image %s: %w", img.Digest, err)
	}
	return stored, created, nil
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
		  failure_code = nullif($3, ''), failure_detail = nullif($4, ''), updated_at = now()
		where digest = any($1)`, digests, nextAttempt, failureCode, detail)
	if err != nil {
		return fmt.Errorf("defer scan images: %w", err)
	}
	return nil
}

// RequeueScanningWithoutRun queues images left scanning that no active run lists, and returns how many.
func (s *Store) RequeueScanningWithoutRun(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `update scan_images set status = 'queued', updated_at = now()
		where status = 'scanning' and not exists (
		  select 1 from scan_run_images ri join scan_runs r on r.id = ri.run_id
		  where ri.digest = scan_images.digest and r.status in ('launching', 'running'))`)
	if err != nil {
		return 0, fmt.Errorf("requeue scanning without run: %w", err)
	}
	return tag.RowsAffected(), nil
}

// expiredScanImagesWhere is the one definition of an expired scan result: finished, and not requested since the cutoff.
const expiredScanImagesWhere = `status in ('scanned', 'failed') and last_requested_at < $1`

// ExpiredScanImages lists digests of finished scans last requested before cutoff, oldest request first.
func (s *Store) ExpiredScanImages(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `select digest from scan_images where `+expiredScanImagesWhere+`
		order by last_requested_at, digest limit $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("expired scan images: %w", err)
	}
	digests, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("expired scan images: %w", err)
	}
	return digests, nil
}

// DeleteScanImages deletes the given images; their findings, requests and risk acceptances go with them.
func (s *Store) DeleteScanImages(ctx context.Context, digests []string) error {
	if _, err := s.Pool.Exec(ctx, `delete from scan_images where digest = any($1)`, digests); err != nil {
		return fmt.Errorf("delete scan images: %w", err)
	}
	return nil
}
