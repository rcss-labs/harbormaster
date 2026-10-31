package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// ErrBuiltin is returned when an operation is not allowed on the built-in Dev tag.
var ErrBuiltin = errors.New("built-in tag")

// Tag is a named, coloured label that resources carry and permissions refer to.
type Tag struct {
	ID      authz.TagID
	Name    string
	Color   string
	Builtin bool
}

const tagColumns = `id, name, color, builtin`

func scanTag(row pgx.Row) (Tag, error) {
	var t Tag
	err := row.Scan(&t.ID, &t.Name, &t.Color, &t.Builtin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	return t, err
}

// ListTags returns all tags ordered by id (Dev first).
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.Pool.Query(ctx, `select `+tagColumns+` from tags order by id`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	tags := []Tag{}
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return tags, nil
}

// GetTag returns the tag with the given id, or ErrNotFound.
func (s *Store) GetTag(ctx context.Context, id authz.TagID) (Tag, error) {
	return scanTag(s.Pool.QueryRow(ctx, `select `+tagColumns+` from tags where id = $1`, int64(id)))
}

// DevTag returns the built-in Dev tag.
func (s *Store) DevTag(ctx context.Context) (Tag, error) {
	return scanTag(s.Pool.QueryRow(ctx, `select `+tagColumns+` from tags where builtin order by id limit 1`))
}

// CreateTag adds a tag. Names that differ only in case return ErrConflict.
func (s *Store) CreateTag(ctx context.Context, name, color string) (Tag, error) {
	t, err := scanTag(s.Pool.QueryRow(ctx,
		`insert into tags (name, color) values ($1, $2) returning `+tagColumns, name, color))
	if isUniqueViolation(err) {
		return Tag{}, ErrConflict
	}
	if err != nil {
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

// UpdateTag changes a tag's name and colour. The built-in tag may only be
// recoloured; renaming it returns ErrBuiltin.
func (s *Store) UpdateTag(ctx context.Context, id authz.TagID, name, color string) error {
	current, err := s.GetTag(ctx, id)
	if err != nil {
		return err
	}
	if current.Builtin && current.Name != name {
		return ErrBuiltin
	}
	err = s.execExpectOne(ctx, `update tags set name = $2, color = $3 where id = $1`, int64(id), name, color)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// DeleteTag removes a tag; assignments and permissions cascade. The built-in
// tag returns ErrBuiltin and an unknown id returns ErrNotFound.
func (s *Store) DeleteTag(ctx context.Context, id authz.TagID) error {
	current, err := s.GetTag(ctx, id)
	if err != nil {
		return err
	}
	if current.Builtin {
		return ErrBuiltin
	}
	return s.execExpectOne(ctx, `delete from tags where id = $1`, int64(id))
}

// TagImpact lists what deleting a tag would affect.
type TagImpact struct {
	Clusters  []model.ClusterKey
	Services  []model.ServiceKey
	Usernames []string
}

// TagImpact returns the clusters and services directly assigned the tag and the
// users who can view it.
func (s *Store) TagImpact(ctx context.Context, id authz.TagID) (TagImpact, error) {
	impact := TagImpact{Clusters: []model.ClusterKey{}, Services: []model.ServiceKey{}, Usernames: []string{}}

	rows, err := s.Pool.Query(ctx,
		`select account_id, region, cluster_name from cluster_tags where tag_id = $1
		 order by account_id, region, cluster_name`, int64(id))
	if err != nil {
		return TagImpact{}, fmt.Errorf("tag impact clusters: %w", err)
	}
	for rows.Next() {
		var k model.ClusterKey
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster); err != nil {
			rows.Close()
			return TagImpact{}, fmt.Errorf("scan cluster tag: %w", err)
		}
		impact.Clusters = append(impact.Clusters, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TagImpact{}, fmt.Errorf("tag impact clusters: %w", err)
	}

	rows, err = s.Pool.Query(ctx,
		`select account_id, region, cluster_name, service_name from service_tags where tag_id = $1
		 order by account_id, region, cluster_name, service_name`, int64(id))
	if err != nil {
		return TagImpact{}, fmt.Errorf("tag impact services: %w", err)
	}
	for rows.Next() {
		var k model.ServiceKey
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster, &k.Service); err != nil {
			rows.Close()
			return TagImpact{}, fmt.Errorf("scan service tag: %w", err)
		}
		impact.Services = append(impact.Services, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TagImpact{}, fmt.Errorf("tag impact services: %w", err)
	}

	users, err := s.UsersWithView(ctx, id)
	if err != nil {
		return TagImpact{}, err
	}
	for _, u := range users {
		impact.Usernames = append(impact.Usernames, u.Username)
	}
	return impact, nil
}

// SetClusterTag assigns a tag to a cluster (upsert) and clears any orphaned mark.
func (s *Store) SetClusterTag(ctx context.Context, k model.ClusterKey, tag authz.TagID) error {
	_, err := s.Pool.Exec(ctx,
		`insert into cluster_tags (account_id, region, cluster_name, tag_id) values ($1, $2, $3, $4)
		 on conflict (account_id, region, cluster_name)
		 do update set tag_id = excluded.tag_id, orphaned_at = null`,
		k.AccountID, k.Region, k.Cluster, int64(tag))
	if err != nil {
		return fmt.Errorf("set cluster tag: %w", err)
	}
	return nil
}

// ClearClusterTag removes the cluster's explicit tag so it falls back to Dev. Clearing an
// untagged cluster is not an error.
func (s *Store) ClearClusterTag(ctx context.Context, k model.ClusterKey) error {
	_, err := s.Pool.Exec(ctx,
		`delete from cluster_tags where account_id = $1 and region = $2 and cluster_name = $3`,
		k.AccountID, k.Region, k.Cluster)
	if err != nil {
		return fmt.Errorf("clear cluster tag: %w", err)
	}
	return nil
}

// SetServiceTag assigns a tag directly to a service; nil removes the row so the
// service inherits from its cluster.
func (s *Store) SetServiceTag(ctx context.Context, k model.ServiceKey, tag *authz.TagID) error {
	if tag == nil {
		_, err := s.Pool.Exec(ctx,
			`delete from service_tags where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
			k.AccountID, k.Region, k.Cluster, k.Service)
		if err != nil {
			return fmt.Errorf("clear service tag: %w", err)
		}
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`insert into service_tags (account_id, region, cluster_name, service_name, tag_id) values ($1, $2, $3, $4, $5)
		 on conflict (account_id, region, cluster_name, service_name)
		 do update set tag_id = excluded.tag_id, orphaned_at = null`,
		k.AccountID, k.Region, k.Cluster, k.Service, int64(*tag))
	if err != nil {
		return fmt.Errorf("set service tag: %w", err)
	}
	return nil
}

// ClusterTag returns the cluster's explicit tag; false means untagged.
func (s *Store) ClusterTag(ctx context.Context, k model.ClusterKey) (authz.TagID, bool, error) {
	var id int64
	err := s.Pool.QueryRow(ctx,
		`select tag_id from cluster_tags where account_id = $1 and region = $2 and cluster_name = $3`,
		k.AccountID, k.Region, k.Cluster).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("cluster tag: %w", err)
	}
	return authz.TagID(id), true, nil
}

// ServiceTag returns the service's explicit tag; false means it inherits.
func (s *Store) ServiceTag(ctx context.Context, k model.ServiceKey) (authz.TagID, bool, error) {
	var id int64
	err := s.Pool.QueryRow(ctx,
		`select tag_id from service_tags where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		k.AccountID, k.Region, k.Cluster, k.Service).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("service tag: %w", err)
	}
	return authz.TagID(id), true, nil
}

// tagsSnapshot is an in-memory copy of all tag assignments; it implements authz.Tags.
type tagsSnapshot struct {
	dev      authz.TagID
	clusters map[model.ClusterKey]authz.TagID
	services map[model.ServiceKey]authz.TagID
}

func (t *tagsSnapshot) DevTagID() authz.TagID { return t.dev }

func (t *tagsSnapshot) ClusterTag(k model.ClusterKey) (authz.TagID, bool) {
	id, ok := t.clusters[k]
	return id, ok
}

func (t *tagsSnapshot) ServiceTag(k model.ServiceKey) (authz.TagID, bool) {
	id, ok := t.services[k]
	return id, ok
}

// TagsSnapshot loads every assignment (orphaned ones included, since they
// survive until retention deletes them) and the Dev tag id.
func (s *Store) TagsSnapshot(ctx context.Context) (authz.Tags, error) {
	dev, err := s.DevTag(ctx)
	if err != nil {
		return nil, fmt.Errorf("tags snapshot: %w", err)
	}
	snap := &tagsSnapshot{
		dev:      dev.ID,
		clusters: map[model.ClusterKey]authz.TagID{},
		services: map[model.ServiceKey]authz.TagID{},
	}

	rows, err := s.Pool.Query(ctx, `select account_id, region, cluster_name, tag_id from cluster_tags`)
	if err != nil {
		return nil, fmt.Errorf("tags snapshot clusters: %w", err)
	}
	for rows.Next() {
		var k model.ClusterKey
		var id int64
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster, &id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan cluster tag: %w", err)
		}
		snap.clusters[k] = authz.TagID(id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags snapshot clusters: %w", err)
	}

	rows, err = s.Pool.Query(ctx, `select account_id, region, cluster_name, service_name, tag_id from service_tags`)
	if err != nil {
		return nil, fmt.Errorf("tags snapshot services: %w", err)
	}
	for rows.Next() {
		var k model.ServiceKey
		var id int64
		if err := rows.Scan(&k.AccountID, &k.Region, &k.Cluster, &k.Service, &id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan service tag: %w", err)
		}
		snap.services[k] = authz.TagID(id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags snapshot services: %w", err)
	}
	return snap, nil
}

// TagCounts summarises how many resources carry a tag.
type TagCounts struct {
	Clusters          int // cluster_tags rows
	ServicesDirect    int // service_tags rows
	ServicesEffective int // snapshot services whose effective tag is this one
}

// TagCounts returns counts for every tag, zero counts included.
func (s *Store) TagCounts(ctx context.Context) (map[authz.TagID]TagCounts, error) {
	rows, err := s.Pool.Query(ctx, `
select t.id,
  (select count(*) from cluster_tags ct where ct.tag_id = t.id),
  (select count(*) from service_tags st where st.tag_id = t.id),
  (select count(*)
     from services sv
     left join service_tags st on (st.account_id, st.region, st.cluster_name, st.service_name)
                                = (sv.account_id, sv.region, sv.cluster_name, sv.service_name)
     left join cluster_tags ct on (ct.account_id, ct.region, ct.cluster_name)
                                = (sv.account_id, sv.region, sv.cluster_name)
    where coalesce(st.tag_id, ct.tag_id, (select id from tags where builtin order by id limit 1)) = t.id)
from tags t`)
	if err != nil {
		return nil, fmt.Errorf("tag counts: %w", err)
	}
	defer rows.Close()
	counts := map[authz.TagID]TagCounts{}
	for rows.Next() {
		var id int64
		var c TagCounts
		if err := rows.Scan(&id, &c.Clusters, &c.ServicesDirect, &c.ServicesEffective); err != nil {
			return nil, fmt.Errorf("scan tag counts: %w", err)
		}
		counts[authz.TagID(id)] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tag counts: %w", err)
	}
	return counts, nil
}

// MarkOrphanedTags flags assignments whose cluster/service is missing from the
// snapshot tables (orphaned_at = now, keeping any earlier mark) and clears the
// flag where the resource has reappeared.
func (s *Store) MarkOrphanedTags(ctx context.Context, now time.Time) error {
	statements := []string{
		`update cluster_tags ct set orphaned_at = $1
		 where ct.orphaned_at is null and not exists (
		   select 1 from clusters c where (c.account_id, c.region, c.cluster_name)
		     = (ct.account_id, ct.region, ct.cluster_name))`,
		`update cluster_tags ct set orphaned_at = null
		 where ct.orphaned_at is not null and exists (
		   select 1 from clusters c where (c.account_id, c.region, c.cluster_name)
		     = (ct.account_id, ct.region, ct.cluster_name))`,
		`update service_tags st set orphaned_at = $1
		 where st.orphaned_at is null and not exists (
		   select 1 from services s where (s.account_id, s.region, s.cluster_name, s.service_name)
		     = (st.account_id, st.region, st.cluster_name, st.service_name))`,
		`update service_tags st set orphaned_at = null
		 where st.orphaned_at is not null and exists (
		   select 1 from services s where (s.account_id, s.region, s.cluster_name, s.service_name)
		     = (st.account_id, st.region, st.cluster_name, st.service_name))`,
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark orphaned tags: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	for i, stmt := range statements {
		if i%2 == 0 {
			_, err = tx.Exec(ctx, stmt, now)
		} else {
			_, err = tx.Exec(ctx, stmt)
		}
		if err != nil {
			return fmt.Errorf("mark orphaned tags: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark orphaned tags: %w", err)
	}
	return nil
}

// DeleteOrphanedTagsBefore removes assignments orphaned before t and returns how many.
func (s *Store) DeleteOrphanedTagsBefore(ctx context.Context, t time.Time) (int64, error) {
	clusters, services, err := s.deleteOrphanedTagsBeforeCounts(ctx, t)
	return clusters + services, err
}

// deleteOrphanedTagsBeforeCounts is DeleteOrphanedTagsBefore with per-table counts.
func (s *Store) deleteOrphanedTagsBeforeCounts(ctx context.Context, t time.Time) (clusters, services int64, err error) {
	counts := [2]int64{}
	for i, table := range []string{"cluster_tags", "service_tags"} {
		tag, err := s.Pool.Exec(ctx, `delete from `+table+` where orphaned_at < $1`, t)
		if err != nil {
			return 0, 0, fmt.Errorf("delete orphaned %s: %w", table, err)
		}
		counts[i] = tag.RowsAffected()
	}
	return counts[0], counts[1], nil
}
