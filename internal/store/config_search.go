package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// ConfigContainer is the name-only view of one container definition. It never holds an environment value or a
// secret reference.
type ConfigContainer struct {
	Name, Image, LogGroup string
	Ports                 []int
	EnvNames, SecretNames []string
}

// ConfigRevision is one task definition revision a service runs.
type ConfigRevision struct {
	ARN      string
	Revision int
	// Primary is false for an ACTIVE deployment that still has tasks.
	Primary bool
	// Loaded is false when the definition is not cached yet.
	Loaded                        bool
	TaskRoleARN, ExecutionRoleARN string
	Containers                    []ConfigContainer
}

// ConfigRow is one visible service with the revisions config search looks at.
type ConfigRow struct {
	Key            model.ServiceKey
	Family         string
	EffectiveTagID authz.TagID
	TagInherited   bool
	// Revisions lists the PRIMARY revision first, then running ACTIVE ones, newest revision first.
	Revisions []ConfigRevision
}

// projectedContainer is the JSON the query builds for each container.
type projectedContainer struct {
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	LogGroup    string   `json:"log_group"`
	Ports       []int    `json:"ports"`
	EnvNames    []string `json:"env_names"`
	SecretNames []string `json:"secret_names"`
}

// ConfigSearchRows returns every service the caller can see in scope with the names (never values) from its
// current and still-running task definitions. The names are projected out of task_definitions.raw inside
// Postgres, so environment values and secret references never reach this process.
func (s *Store) ConfigSearchRows(ctx context.Context, f TargetFilter, vis Visibility) ([]ConfigRow, error) {
	var q queryArgs
	where := q.target("sv", f)
	v := q.visibility("sv", vis)
	where += " and " + v.Predicate
	// One output row per (service, revision). The first lateral branch always yields the PRIMARY revision, even
	// when the service has no ARN yet, so every visible service is counted.
	sql := `select sv.account_id, sv.region, sv.cluster_name, sv.service_name, coalesce(sv.family, ''),
	    ` + v.Effective + `, ` + v.Inherited + `,
	    coalesce(rv.arn, ''), coalesce(rv.revision, 0), rv.is_primary,
	    td.arn is not null, coalesce(td.task_role_arn, ''), coalesce(td.execution_role_arn, ''),
	    cs.containers
	  from services sv` + v.Joins + `
	  cross join lateral (
	    select sv.task_definition_arn as arn, sv.revision as revision, true as is_primary
	    union all
	    select d->>'TaskDefinitionARN', (d->>'Revision')::int, false
	      from jsonb_array_elements(case when jsonb_typeof(sv.deployments) = 'array' then sv.deployments else '[]'::jsonb end) d
	     where d->>'Status' = 'ACTIVE'
	       and coalesce((d->>'Running')::int, 0) + coalesce((d->>'Pending')::int, 0) > 0
	       and d->>'TaskDefinitionARN' is distinct from sv.task_definition_arn
	  ) rv
	  left join task_definitions td on td.account_id = sv.account_id and td.region = sv.region and td.arn = rv.arn
	  left join lateral (
	    select coalesce(jsonb_agg(jsonb_build_object(
	             'name', coalesce(c->>'Name', ''), 'image', coalesce(c->>'Image', ''),
	             'log_group', coalesce(c #>> '{LogConfiguration,Options,awslogs-group}', ''),
	             'ports', jsonb_path_query_array(c, '$.PortMappings[*].ContainerPort'),
	             'env_names', jsonb_path_query_array(c, '$.Environment[*].Name'),
	             'secret_names', jsonb_path_query_array(c, '$.Secrets[*].Name')) order by ord), '[]'::jsonb) as containers
	      from jsonb_array_elements(case when jsonb_typeof(td.raw->'ContainerDefinitions') = 'array'
	                                     then td.raw->'ContainerDefinitions' else '[]'::jsonb end)
	           with ordinality as e(c, ord)
	  ) cs on true
	  where ` + where + `
	  order by sv.account_id, sv.region, sv.cluster_name, sv.service_name, rv.is_primary desc, rv.revision desc`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("config search rows: %w", err)
	}
	defer rows.Close()

	var out []ConfigRow
	for rows.Next() {
		var (
			key        model.ServiceKey
			family     string
			tagID      authz.TagID
			inherited  bool
			rev        ConfigRevision
			containers []byte
		)
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &family, &tagID, &inherited,
			&rev.ARN, &rev.Revision, &rev.Primary, &rev.Loaded, &rev.TaskRoleARN, &rev.ExecutionRoleARN, &containers); err != nil {
			return nil, fmt.Errorf("scan config search row: %w", err)
		}
		var projected []projectedContainer
		if len(containers) > 0 {
			if err := json.Unmarshal(containers, &projected); err != nil {
				return nil, fmt.Errorf("decode config search containers: %w", err)
			}
		}
		for _, c := range projected {
			rev.Containers = append(rev.Containers, ConfigContainer{
				Name: c.Name, Image: c.Image, LogGroup: c.LogGroup,
				Ports: nonNilInts(c.Ports), EnvNames: nonNilStrings(c.EnvNames), SecretNames: nonNilStrings(c.SecretNames),
			})
		}
		if n := len(out); n == 0 || out[n-1].Key != key {
			out = append(out, ConfigRow{Key: key, Family: family, EffectiveTagID: tagID, TagInherited: inherited})
		}
		last := &out[len(out)-1]
		last.Revisions = append(last.Revisions, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config search rows: %w", err)
	}
	return out, nil
}

func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
