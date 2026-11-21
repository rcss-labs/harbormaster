package store

import (
	"context"
	"fmt"
	"time"
)

// ImageRef is one image string of a task definition and how its last check went.
type ImageRef struct {
	Image, Registry, Repository, Digest, Status, Detail string
	CheckedAt                                           *time.Time
}

// ImagePlatforms is an ImageRef joined with the manifest its digest points to.
type ImagePlatforms struct {
	ImageRef
	IsIndex   bool
	Platforms []string
	Known     bool // a manifest row exists for Digest
}

// TrackX86FargateImages makes sure every container image of the task definition of every ACTIVE service that requires only
// Fargate, is not Windows and is not ARM64 has an image_refs row, due now. It moves last_seen_at of the images still in
// use and returns how many rows are new. Registry and repository stay empty: the resolver parses the reference.
func (s *Store) TrackX86FargateImages(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.Pool.Query(ctx,
		`insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at)
		 select distinct c.image, '', '', 'pending', $1::timestamptz, $1::timestamptz
		 from services s
		 join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 cross join lateral (
		   select e ->> 'Image' as image
		   from jsonb_array_elements(case when jsonb_typeof(td.raw -> 'ContainerDefinitions') = 'array'
		                                  then td.raw -> 'ContainerDefinitions' else '[]'::jsonb end) e) c
		 where s.status = 'ACTIVE'
		   and td.requires_compatibilities = array['FARGATE']::text[]
		   and upper(coalesce(td.raw #>> '{RuntimePlatform,OperatingSystemFamily}', '')) not like 'WINDOWS%'
		   and coalesce(td.raw #>> '{RuntimePlatform,CpuArchitecture}', '') <> 'ARM64'
		   and coalesce(c.image, '') <> ''
		 on conflict (image) do update set last_seen_at = excluded.last_seen_at
		 returning (xmax = 0)`, now)
	if err != nil {
		return 0, fmt.Errorf("track images: %w", err)
	}
	defer rows.Close()
	added := 0
	for rows.Next() {
		var inserted bool
		if err := rows.Scan(&inserted); err != nil {
			return 0, fmt.Errorf("track images: %w", err)
		}
		if inserted {
			added++
		}
	}
	return added, rows.Err()
}

// DueImageRefs lists the refs whose next check is due, oldest first.
func (s *Store) DueImageRefs(ctx context.Context, now time.Time, limit int) ([]ImageRef, error) {
	rows, err := s.Pool.Query(ctx,
		`select image, registry, repository, coalesce(digest, ''), status, coalesce(detail, ''), checked_at
		 from image_refs where next_check_at <= $1 order by next_check_at, image limit $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("due image refs: %w", err)
	}
	defer rows.Close()
	var out []ImageRef
	for rows.Next() {
		var ref ImageRef
		if err := rows.Scan(&ref.Image, &ref.Registry, &ref.Repository, &ref.Digest, &ref.Status, &ref.Detail, &ref.CheckedAt); err != nil {
			return nil, fmt.Errorf("due image refs: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// SaveImageRefResult records the outcome of one check and when the next is due. A result without a digest keeps the last
// known one, so a failed re-check does not forget what the image resolved to.
func (s *Store) SaveImageRefResult(ctx context.Context, ref ImageRef, nextCheck time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`insert into image_refs (image, registry, repository, digest, status, detail, checked_at, next_check_at, last_seen_at)
		 values ($1, $2, $3, nullif($4, ''), $5, nullif($6, ''), coalesce($7, now()), $8, now())
		 on conflict (image) do update set registry = excluded.registry, repository = excluded.repository,
		   digest = coalesce(excluded.digest, image_refs.digest), status = excluded.status, detail = excluded.detail,
		   checked_at = excluded.checked_at, next_check_at = excluded.next_check_at`,
		ref.Image, ref.Registry, ref.Repository, ref.Digest, ref.Status, ref.Detail, ref.CheckedAt, nextCheck)
	if err != nil {
		return fmt.Errorf("save image ref: %w", err)
	}
	return nil
}

// HasImageManifest reports whether the manifest of the digest is stored.
func (s *Store) HasImageManifest(ctx context.Context, registry, repository, digest string) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `select exists (select 1 from image_manifests where registry = $1 and repository = $2 and digest = $3)`,
		registry, repository, digest).Scan(&ok)
	return ok, err
}

// SaveImageManifest stores the platforms of an immutable manifest once.
func (s *Store) SaveImageManifest(ctx context.Context, registry, repository, digest, mediaType string, isIndex bool, platforms []string, now time.Time) error {
	if platforms == nil {
		platforms = []string{}
	}
	_, err := s.Pool.Exec(ctx,
		`insert into image_manifests (registry, repository, digest, media_type, is_index, platforms, fetched_at)
		 values ($1, $2, $3, $4, $5, $6, $7) on conflict (registry, repository, digest) do nothing`,
		registry, repository, digest, mediaType, isIndex, platforms, now)
	if err != nil {
		return fmt.Errorf("save image manifest: %w", err)
	}
	return nil
}

// ImagePlatformsFor returns the tracked images among images, each with the manifest its digest points to when one is stored.
func (s *Store) ImagePlatformsFor(ctx context.Context, images []string) (map[string]ImagePlatforms, error) {
	rows, err := s.Pool.Query(ctx,
		`select r.image, r.registry, r.repository, coalesce(r.digest, ''), r.status, coalesce(r.detail, ''), r.checked_at,
		   m.digest is not null, coalesce(m.is_index, false), coalesce(m.platforms, '{}')
		 from image_refs r
		 left join image_manifests m on m.registry = r.registry and m.repository = r.repository and m.digest = r.digest
		 where r.image = any($1)`, images)
	if err != nil {
		return nil, fmt.Errorf("image platforms: %w", err)
	}
	defer rows.Close()
	out := map[string]ImagePlatforms{}
	for rows.Next() {
		var p ImagePlatforms
		if err := rows.Scan(&p.Image, &p.Registry, &p.Repository, &p.Digest, &p.Status, &p.Detail, &p.CheckedAt, &p.Known, &p.IsIndex, &p.Platforms); err != nil {
			return nil, fmt.Errorf("image platforms: %w", err)
		}
		out[p.Image] = p
	}
	return out, rows.Err()
}

// PruneImagePlatforms deletes refs not seen since before, then the manifests no ref points to, and returns the rows deleted.
func (s *Store) PruneImagePlatforms(ctx context.Context, before time.Time) (int64, error) {
	refs, err := s.Pool.Exec(ctx, `delete from image_refs where last_seen_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune image refs: %w", err)
	}
	manifests, err := s.Pool.Exec(ctx,
		`delete from image_manifests m where not exists (
		   select 1 from image_refs r where r.registry = m.registry and r.repository = m.repository and r.digest = m.digest)`)
	if err != nil {
		return refs.RowsAffected(), fmt.Errorf("prune image manifests: %w", err)
	}
	return refs.RowsAffected() + manifests.RowsAffected(), nil
}
