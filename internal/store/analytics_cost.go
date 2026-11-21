package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
)

// CostServiceFact is what the cost evaluation needs to know about one service. Used values come from the daily
// p95 columns (never averages), already converted to absolute units; running task counts are not read here.
type CostServiceFact struct {
	Key                      model.ServiceKey
	TagID                    authz.TagID
	TagInherited             bool
	CPUUnits, MemoryMiB      *int // task definition size, nil when it is not a plain number
	RequiresFargate          bool
	Architecture, OSFamily   string
	ContainerDefinitions     []byte // raw JSON of ContainerDefinitions; nil means floors unknown
	UsedCPUUnits, UsedMemMiB *float64
	HistoryDays              int // days with every utilisation column present
	LatestDay                *time.Time
	AutoScaled, RecentOOM    bool

	// Fargate Spot advisor facts.
	Desired           int
	LaunchType        string
	StrategyProviders []string // providers of the service's capacity provider strategy; empty = a launch type
	// Controller is ECS for the rolling controller, BLUE_GREEN for ECS blue/green, else CODE_DEPLOY or EXTERNAL.
	Controller         string
	ClusterProviders   []string
	NonProduction      bool // the effective tag's non-production flag
	TerminalSessions7d int  // sessions opened through Harbormaster since the lookback start
	// ExpectedStops7d counts the service's deployment and scale-in stops since the lookback start; KilledStops7d
	// counts those that ended in exit 137, a container that did not stop within its stop timeout.
	ExpectedStops7d, KilledStops7d int
	StopTimeoutSeconds             *int // highest container stopTimeout; nil when none is set
}

// AnalyticsCostServices returns every visible service with the highest daily p95 use since from. Each day's p95
// is a percentage of that day's average reservation, so a day is converted to absolute units before the maximum
// is taken; percentages cannot be compared across a resize. Services without complete days are still listed.
func (s *Store) AnalyticsCostServices(ctx context.Context, f TargetFilter, vis Visibility, from, spotSince time.Time) ([]CostServiceFact, error) {
	var q queryArgs
	target := q.target("s", f)
	v := q.visibility("s", vis)
	fromArg := q.add(from)
	sinceArg := q.add(spotSince)
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, `+v.Effective+`, `+v.Inherited+`,
		   case when td.cpu ~ '^[0-9]+$' then td.cpu::int end, case when td.memory ~ '^[0-9]+$' then td.memory::int end,
		   coalesce('FARGATE' = any(td.requires_compatibilities), false),
		   coalesce(td.raw #>> '{RuntimePlatform,CpuArchitecture}', ''), coalesce(td.raw #>> '{RuntimePlatform,OperatingSystemFamily}', ''),
		   nullif(td.raw -> 'ContainerDefinitions', 'null'::jsonb),
		   used.cpu_units, used.mem_mib, used.days, used.latest_day,
		   coalesce(scaling.has_target, false),
		   exists (select 1 from tasks t
		     where t.account_id = s.account_id and t.region = s.region and t.cluster_name = s.cluster_name
		       and t.service_name = s.service_name and t.last_status = 'STOPPED' and t.stop_reason_group = `+q.add(status.StopReasonOutOfMemory)+`),
		   s.desired, coalesce(s.launch_type, ''),
		   coalesce(array(select e ->> 'CapacityProvider' from jsonb_array_elements(
		     case when jsonb_typeof(s.raw -> 'CapacityProviderStrategy') = 'array' then s.raw -> 'CapacityProviderStrategy' else '[]'::jsonb end) e
		     order by 1), '{}'::text[]),
		   case when s.raw #>> '{DeploymentConfiguration,Strategy}' = 'BLUE_GREEN' then 'BLUE_GREEN'
		        else coalesce(nullif(s.deployment_controller, ''), 'ECS') end,
		   coalesce(cl.capacity_providers, '{}'::text[]),
		   coalesce((select tg.non_production from tags tg where tg.id = `+v.Effective+`), false),
		   (select count(*) from operation_log o
		     where o.account_id = s.account_id and o.region = s.region and o.cluster_name = s.cluster_name
		       and o.service_name = s.service_name and o.source = 'harbormaster' and o.details ->> 'op' = 'terminal'
		       and o.ts >= `+sinceArg+`),
		   stops.expected, stops.killed,
		   (select max((e ->> 'StopTimeout')::int) from jsonb_array_elements(
		     case when jsonb_typeof(td.raw -> 'ContainerDefinitions') = 'array' then td.raw -> 'ContainerDefinitions' else '[]'::jsonb end) e)
		 from services s`+v.Joins+`
		 left join clusters cl on cl.account_id = s.account_id and cl.region = s.region and cl.cluster_name = s.cluster_name
		 left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 left join scaling on scaling.account_id = s.account_id and scaling.region = s.region
		   and scaling.cluster_name = s.cluster_name and scaling.service_name = s.service_name
		 cross join lateral (
		   select count(*) as expected, count(*) filter (where t.exit_code = 137) as killed
		   from tasks t
		   where t.account_id = s.account_id and t.region = s.region and t.cluster_name = s.cluster_name
		     and t.service_name = s.service_name and t.last_status = 'STOPPED' and t.stopped_at >= `+sinceArg+`
		     and t.stop_code = 'ServiceSchedulerInitiated'
		     and (t.stopped_reason ilike '%desired count%' or t.stopped_reason ilike '%deployment%' or t.stopped_reason ilike '%replaced%')
		     and t.stopped_reason not ilike '%outofmemory%' and t.stopped_reason not ilike '%health checks%') stops
		 cross join lateral (
		   select max(u.cpu_p95 / 100 * u.cpu_reserved_units) as cpu_units, max(u.mem_p95 / 100 * u.mem_reserved_mib) as mem_mib,
		     count(*) as days, max(u.day) as latest_day
		   from service_util_daily u
		   where u.account_id = s.account_id and u.region = s.region and u.cluster_name = s.cluster_name
		     and u.service_name = s.service_name and u.day >= `+fromArg+`::date
		     and u.cpu_p95 is not null and u.mem_p95 is not null
		     and u.cpu_reserved_units is not null and u.mem_reserved_mib is not null) used
		 where `+target+` and `+v.Predicate+`
		 order by s.account_id, s.region, s.cluster_name, s.service_name`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics cost services: %w", err)
	}
	defer rows.Close()
	out := []CostServiceFact{}
	for rows.Next() {
		var fact CostServiceFact
		key := &fact.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &fact.TagID, &fact.TagInherited,
			&fact.CPUUnits, &fact.MemoryMiB, &fact.RequiresFargate, &fact.Architecture, &fact.OSFamily,
			&fact.ContainerDefinitions, &fact.UsedCPUUnits, &fact.UsedMemMiB, &fact.HistoryDays, &fact.LatestDay,
			&fact.AutoScaled, &fact.RecentOOM, &fact.Desired, &fact.LaunchType, &fact.StrategyProviders, &fact.Controller,
			&fact.ClusterProviders, &fact.NonProduction, &fact.TerminalSessions7d, &fact.ExpectedStops7d, &fact.KilledStops7d,
			&fact.StopTimeoutSeconds); err != nil {
			return nil, fmt.Errorf("scan cost service: %w", err)
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics cost services: %w", err)
	}
	return out, nil
}
