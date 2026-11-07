package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
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
	ContainerDefinitions     []byte // raw JSON of the task definition's ContainerDefinitions
	UsedCPUUnits, UsedMemMiB *float64
	HistoryDays              int // days with every utilisation column present
	LatestDay                *time.Time
	AutoScaled, RecentOOM    bool
}

// AnalyticsCostServices returns every visible service with the highest daily p95 use since from. Each day's p95
// is a percentage of that day's average reservation, so a day is converted to absolute units before the maximum
// is taken; percentages cannot be compared across a resize. Services without complete days are still listed.
func (s *Store) AnalyticsCostServices(ctx context.Context, f TargetFilter, vis Visibility, from time.Time) ([]CostServiceFact, error) {
	var q queryArgs
	target := q.target("s", f)
	v := q.visibility("s", vis)
	fromArg := q.add(from)
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, `+v.Effective+`, `+v.Inherited+`,
		   case when td.cpu ~ '^[0-9]+$' then td.cpu::int end, case when td.memory ~ '^[0-9]+$' then td.memory::int end,
		   coalesce('FARGATE' = any(td.requires_compatibilities), false),
		   coalesce(td.raw #>> '{RuntimePlatform,CpuArchitecture}', ''), coalesce(td.raw #>> '{RuntimePlatform,OperatingSystemFamily}', ''),
		   (td.raw -> 'ContainerDefinitions')::text::bytea,
		   used.cpu_units, used.mem_mib, used.days, used.latest_day,
		   coalesce(scaling.has_target, false),
		   exists (select 1 from tasks t
		     where t.account_id = s.account_id and t.region = s.region and t.cluster_name = s.cluster_name
		       and t.service_name = s.service_name and t.last_status = 'STOPPED' and t.stopped_reason like '%OutOfMemoryError%')
		 from services s`+v.Joins+`
		 left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 left join scaling on scaling.account_id = s.account_id and scaling.region = s.region
		   and scaling.cluster_name = s.cluster_name and scaling.service_name = s.service_name
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
			&fact.AutoScaled, &fact.RecentOOM); err != nil {
			return nil, fmt.Errorf("scan cost service: %w", err)
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics cost services: %w", err)
	}
	return out, nil
}
