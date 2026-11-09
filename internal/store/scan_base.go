package store

import (
	"context"
	"fmt"
	"time"
)

// RepoKey names an ECR repository: the registry account, its region and the repository path.
type RepoKey struct{ AccountID, Region, Repository string }

// RepoBases returns the base image ref configured for each of the repositories that has one.
func (s *Store) RepoBases(ctx context.Context, keys []RepoKey) (map[RepoKey]string, error) {
	accounts, regions, repositories := make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys))
	for i, key := range keys {
		accounts[i], regions[i], repositories[i] = key.AccountID, key.Region, key.Repository
	}
	rows, err := s.Pool.Query(ctx, `select b.registry_account_id, b.registry_region, b.repository, b.base_ref
		from scan_base_images b
		join unnest($1::text[], $2::text[], $3::text[]) as k(account_id, region, repository)
		  on b.registry_account_id = k.account_id and b.registry_region = k.region and b.repository = k.repository`,
		accounts, regions, repositories)
	if err != nil {
		return nil, fmt.Errorf("repo bases: %w", err)
	}
	defer rows.Close()
	out := map[RepoKey]string{}
	for rows.Next() {
		var key RepoKey
		var ref string
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Repository, &ref); err != nil {
			return nil, fmt.Errorf("repo bases: %w", err)
		}
		out[key] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repo bases: %w", err)
	}
	return out, nil
}

// SetRepoBase stores the base image ref of a repository, shared by all its tags and digests; an empty ref clears it.
// A zero by records no user.
func (s *Store) SetRepoBase(ctx context.Context, key RepoKey, ref string, by int64, now time.Time) error {
	var err error
	if ref == "" {
		_, err = s.Pool.Exec(ctx, `delete from scan_base_images
			where registry_account_id = $1 and registry_region = $2 and repository = $3`, key.AccountID, key.Region, key.Repository)
	} else {
		_, err = s.Pool.Exec(ctx, `insert into scan_base_images (registry_account_id, registry_region, repository, base_ref, updated_by, updated_at)
			values ($1, $2, $3, $4, nullif($5, 0::bigint), $6)
			on conflict (registry_account_id, registry_region, repository) do update set
			  base_ref = excluded.base_ref, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			key.AccountID, key.Region, key.Repository, ref, by, now)
	}
	if err != nil {
		return fmt.Errorf("set repo base: %w", err)
	}
	return nil
}
