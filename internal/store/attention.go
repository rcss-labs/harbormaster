package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// ChangeRef is a recorded change to a service: a Harbormaster operation (Source "harbormaster")
// or a deployment_history row (Source "deployment"). ECS-observed events are never a ChangeRef.
type ChangeRef struct {
	At           time.Time
	Text, Source string
}

// ChangeQuery asks for the newest change to Key strictly before Before.
type ChangeQuery struct {
	Key    model.ServiceKey
	Before time.Time
}

// LastChangeBefore returns the newest Harbormaster operation or deployment of the service with
// at < before and before-at <= window, or nil when there is none. It never guesses.
func (s *Store) LastChangeBefore(ctx context.Context, key model.ServiceKey, before time.Time, window time.Duration) (*ChangeRef, error) {
	query := ChangeQuery{Key: key, Before: before}
	found, err := s.LastChangesBefore(ctx, []ChangeQuery{query}, window)
	if err != nil {
		return nil, err
	}
	return found[query], nil
}

// LastChangesBefore resolves many ChangeQuery values in one round trip. Queries with no change
// in range are absent from the result.
func (s *Store) LastChangesBefore(ctx context.Context, queries []ChangeQuery, window time.Duration) (map[ChangeQuery]*ChangeRef, error) {
	out := map[ChangeQuery]*ChangeRef{}
	if len(queries) == 0 {
		return out, nil
	}
	accounts := make([]string, len(queries))
	regions := make([]string, len(queries))
	clusters := make([]string, len(queries))
	services := make([]string, len(queries))
	befores := make([]time.Time, len(queries))
	for i, q := range queries {
		accounts[i], regions[i], clusters[i], services[i], befores[i] =
			q.Key.AccountID, q.Key.Region, q.Key.Cluster, q.Key.Service, q.Before
	}
	rows, err := s.Pool.Query(ctx,
		`select q.idx, c.at, c.text, c.source
		 from unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::timestamptz[])
		        with ordinality as q(account_id, region, cluster_name, service_name, before, idx)
		 cross join lateral (
		   select * from (
		     select o.ts as at, o.text, 'harbormaster' as source
		     from operation_log o
		     where o.source = 'harbormaster' and o.account_id = q.account_id and o.region = q.region
		       and o.cluster_name = q.cluster_name and o.service_name = q.service_name
		       and o.ts < q.before and q.before - o.ts <= make_interval(secs => $6)
		     union all
		     select d.started_at,
		       case d.kind when 'deploy' then case when coalesce(d.revision, 0) > 0 then 'Deployed :' || d.revision else 'Deployed' end
		                   when 'restart' then 'Restarted' else 'Scaled' end,
		       'deployment'
		     from deployment_history d
		     where d.account_id = q.account_id and d.region = q.region
		       and d.cluster_name = q.cluster_name and d.service_name = q.service_name
		       and d.started_at < q.before and q.before - d.started_at <= make_interval(secs => $6)
		   ) change
		   order by change.at desc limit 1
		 ) c`,
		accounts, regions, clusters, services, befores, window.Seconds())
	if err != nil {
		return nil, fmt.Errorf("last changes before: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var ref ChangeRef
		if err := rows.Scan(&idx, &ref.At, &ref.Text, &ref.Source); err != nil {
			return nil, fmt.Errorf("scan change: %w", err)
		}
		out[queries[idx-1]] = &ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("last changes before: %w", err)
	}
	return out, nil
}
