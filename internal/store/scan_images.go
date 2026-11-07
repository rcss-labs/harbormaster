package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScanImage is one container image digest known to the image-security scanner.
type ScanImage struct {
	Digest, PlatformDigest, Platform, ImageRef, Tag, RegistryKind, RegistryAccountID, RegistryRegion, Repository, Role, Status, FailureCode, FailureDetail string
	Attempts                                                                                                                                               int
	NextAttemptAt                                                                                                                                          *time.Time
	Large                                                                                                                                                  bool
	SBOMKey                                                                                                                                                string
	SBOMGeneratedAt                                                                                                                                        *time.Time
	SyftVersion, ScannerImageDigest                                                                                                                        string
	LastMatchedRunID                                                                                                                                       *int64
	MatchedAt, MatchedDBBuiltAt                                                                                                                            *time.Time
	Packages                                                                                                                                               *int
	Truncated                                                                                                                                              bool
	PushedAt, CandidatesCheckedAt                                                                                                                          *time.Time
	FirstSeenAt                                                                                                                                            time.Time
	LastSeenRunningAt                                                                                                                                      *time.Time
	RequestedBy                                                                                                                                            *int64
}

// ScanImageSeen is an image the poller observed, as input to UpsertScanImagesSeen.
type ScanImageSeen struct {
	Digest, ImageRef, Tag, RegistryKind, RegistryAccountID, RegistryRegion, Repository, Platform, Role string
}

// maxScanAttempts is the number of scan attempts after which an image is marked failed.
const maxScanAttempts = 3

const scanImageColumns = `digest, coalesce(platform_digest, ''), coalesce(platform, ''), image_ref, coalesce(tag, ''),
	registry_kind, coalesce(registry_account_id, ''), coalesce(registry_region, ''), coalesce(repository, ''),
	role, status, coalesce(failure_code, ''), coalesce(failure_detail, ''), attempts, next_attempt_at, large,
	coalesce(sbom_key, ''), sbom_generated_at, coalesce(syft_version, ''), coalesce(scanner_image_digest, ''),
	last_matched_run_id, matched_at, matched_db_built_at, packages, truncated, pushed_at, candidates_checked_at,
	first_seen_at, last_seen_running_at, requested_by`

func scanScanImage(row pgx.Row) (ScanImage, error) {
	var i ScanImage
	err := row.Scan(&i.Digest, &i.PlatformDigest, &i.Platform, &i.ImageRef, &i.Tag,
		&i.RegistryKind, &i.RegistryAccountID, &i.RegistryRegion, &i.Repository,
		&i.Role, &i.Status, &i.FailureCode, &i.FailureDetail, &i.Attempts, &i.NextAttemptAt, &i.Large,
		&i.SBOMKey, &i.SBOMGeneratedAt, &i.SyftVersion, &i.ScannerImageDigest,
		&i.LastMatchedRunID, &i.MatchedAt, &i.MatchedDBBuiltAt, &i.Packages, &i.Truncated, &i.PushedAt, &i.CandidatesCheckedAt,
		&i.FirstSeenAt, &i.LastSeenRunningAt, &i.RequestedBy)
	return i, err
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

// The insert carries the row's first state; the conflict branch only touches rows seen running.
// "xmax = 0" is true only for a row this statement inserted.
const upsertScanImageSeenSQL = `insert into scan_images (digest, platform, image_ref, tag, registry_kind, registry_account_id,
	  registry_region, repository, role, status, failure_code, first_seen_at, last_seen_running_at, updated_at)
	values ($1, nullif($2, ''), $3, nullif($4, ''), $5, nullif($6, ''), nullif($7, ''), nullif($8, ''), $9,
	  $10, $11, $12, case when $9 = 'running' then $12::timestamptz end, $12)
	on conflict (digest) do update set
	  role = 'running',
	  last_seen_running_at = $12,
	  status = case when scan_images.status = 'aged_out' then 'queued' else scan_images.status end,
	  attempts = case when scan_images.status = 'aged_out' then 0 else scan_images.attempts end,
	  sbom_key = case when scan_images.status = 'aged_out' then null else scan_images.sbom_key end,
	  next_attempt_at = case when scan_images.status = 'aged_out' then null else scan_images.next_attempt_at end,
	  failure_code = case when scan_images.status = 'aged_out' then null else scan_images.failure_code end,
	  failure_detail = case when scan_images.status = 'aged_out' then null else scan_images.failure_detail end,
	  updated_at = $12
	where excluded.role = 'running'
	returning (xmax = 0)`

// UpsertScanImagesSeen records images the poller saw and returns the digests that were new.
// Only images seen running touch an existing row; a rollback to an aged-out image queues it again.
func (s *Store) UpsertScanImagesSeen(ctx context.Context, seen []ScanImageSeen, now time.Time) ([]string, error) {
	newDigests := []string{}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		done := map[string]bool{}
		for _, img := range seen {
			if done[img.Digest] {
				continue
			}
			done[img.Digest] = true
			status, failureCode := "queued", (*string)(nil)
			if img.RegistryKind != "ecr" {
				code := "unsupported_registry"
				status, failureCode = "unsupported", &code
			}
			var inserted bool
			err := tx.QueryRow(ctx, upsertScanImageSeenSQL, img.Digest, img.Platform, img.ImageRef, img.Tag, img.RegistryKind,
				img.RegistryAccountID, img.RegistryRegion, img.Repository, img.Role, status, failureCode, now).Scan(&inserted)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // an existing row seen as a candidate stays as it is
			}
			if err != nil {
				return fmt.Errorf("upsert scan image %s: %w", img.Digest, err)
			}
			if inserted {
				newDigests = append(newDigests, img.Digest)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newDigests, nil
}

// DueScanImages returns queued images whose retry time has come: running images first, oldest first.
func (s *Store) DueScanImages(ctx context.Context, now time.Time, limit int) ([]ScanImage, error) {
	imgs, err := s.queryScanImages(ctx, `select `+scanImageColumns+` from scan_images
		where status = 'queued' and (next_attempt_at is null or next_attempt_at <= $1)
		order by (role = 'running') desc, first_seen_at, digest limit $2`, now, limit)
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

// MarkScanImagesScanning moves the given digests of a run's batch to scanning.
func (s *Store) MarkScanImagesScanning(ctx context.Context, digests []string, runID int64) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set status = 'scanning', updated_at = now()
		where digest = any($1) and digest in (select digest from scan_run_images where run_id = $2)`, digests, runID)
	if err != nil {
		return fmt.Errorf("mark scan images scanning: %w", err)
	}
	return nil
}

// RequeueScanImages counts a failed attempt and queues the images again; the third attempt marks them failed.
func (s *Store) RequeueScanImages(ctx context.Context, digests []string, nextAttempt time.Time, large bool, failureCode, detail string) error {
	_, err := s.Pool.Exec(ctx, `update scan_images set
		  attempts = attempts + 1,
		  status = case when attempts + 1 >= $6 then 'failed' else 'queued' end,
		  next_attempt_at = case when attempts + 1 >= $6 then null else $2::timestamptz end,
		  large = large or $3,
		  failure_code = nullif($4, ''), failure_detail = nullif($5, ''),
		  updated_at = now()
		where digest = any($1)`, digests, nextAttempt, large, failureCode, detail, maxScanAttempts)
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

// AgedOutSBOMKeys lists aged-out images whose SBOM object still has to be deleted.
func (s *Store) AgedOutSBOMKeys(ctx context.Context, limit int) ([]struct{ Digest, SBOMKey string }, error) {
	rows, err := s.Pool.Query(ctx, `select digest, sbom_key from scan_images
		where status = 'aged_out' and sbom_key is not null order by digest limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("aged out sbom keys: %w", err)
	}
	defer rows.Close()
	var out []struct{ Digest, SBOMKey string }
	for rows.Next() {
		var row struct{ Digest, SBOMKey string }
		if err := rows.Scan(&row.Digest, &row.SBOMKey); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) ClearSBOMKey(ctx context.Context, digest string) error {
	if _, err := s.Pool.Exec(ctx, `update scan_images set sbom_key = null, updated_at = now() where digest = $1`, digest); err != nil {
		return fmt.Errorf("clear sbom key: %w", err)
	}
	return nil
}

// LiveSBOMDigests lists scanned images with an SBOM that were not matched against a database built at builtAt or later.
func (s *Store) LiveSBOMDigests(ctx context.Context, builtAt time.Time, limit int) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `select digest from scan_images
		where status = 'scanned' and sbom_key is not null and (matched_db_built_at is null or matched_db_built_at < $1)
		order by digest limit $2`, builtAt, limit)
	if err != nil {
		return nil, fmt.Errorf("live sbom digests: %w", err)
	}
	defer rows.Close()
	digests := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, rows.Err()
}
