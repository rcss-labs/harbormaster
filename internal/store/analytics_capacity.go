package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Capacity reads for the analytics tab (docs/analytics_design/PLAN.md section 2.3).

// CapacityTaskFact is one RUNNING task with the size it reserves and the tag that decides who may see it.
type CapacityTaskFact struct {
	Cluster          model.ClusterKey
	ServiceName      string // "" for standalone tasks
	CPU, Memory      string // task-level CPU units and MiB as stored; "" when the task has none (EC2)
	CapacityProvider string
	LaunchType       string
	EffectiveTagID   authz.TagID
	TagInherited     bool
}

// AnalyticsRunningTasks returns the visible RUNNING tasks (at most about a thousand rows).
func (s *Store) AnalyticsRunningTasks(ctx context.Context, f TargetFilter, vis Visibility) ([]CapacityTaskFact, error) {
	var q queryArgs
	target := q.target("t", f)
	v := q.visibility("t", vis)
	rows, err := s.Pool.Query(ctx,
		`select t.account_id, t.region, t.cluster_name, coalesce(t.service_name, ''), coalesce(t.cpu, ''), coalesce(t.memory, ''),
		   coalesce(t.capacity_provider, ''), coalesce(t.launch_type, ''), `+v.Effective+`, `+v.Inherited+`
		 from tasks t`+v.Joins+` where t.last_status = 'RUNNING' and `+target+` and `+v.Predicate+`
		 order by t.account_id, t.region, t.cluster_name, t.task_arn`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics running tasks: %w", err)
	}
	defer rows.Close()
	out := []CapacityTaskFact{}
	for rows.Next() {
		var fact CapacityTaskFact
		if err := rows.Scan(&fact.Cluster.AccountID, &fact.Cluster.Region, &fact.Cluster.Cluster, &fact.ServiceName, &fact.CPU,
			&fact.Memory, &fact.CapacityProvider, &fact.LaunchType, &fact.EffectiveTagID, &fact.TagInherited); err != nil {
			return nil, fmt.Errorf("scan running task: %w", err)
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics running tasks: %w", err)
	}
	return out, nil
}

// AnalyticsServiceCount is the number of visible services.
func (s *Store) AnalyticsServiceCount(ctx context.Context, f TargetFilter, vis Visibility) (int, error) {
	var q queryArgs
	target := q.target("s", f)
	v := q.visibility("s", vis)
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from services s`+v.Joins+` where `+target+` and `+v.Predicate, q.values...).Scan(&n); err != nil {
		return 0, fmt.Errorf("analytics service count: %w", err)
	}
	return n, nil
}

// AtMaxRow is a service whose desired count equals its auto-scaling maximum.
type AtMaxRow struct {
	Key                  model.ServiceKey
	Desired, MaxCapacity int
	AtMaxSince           *time.Time // nil when counts history cannot say
}

// AnalyticsAtScalingMax lists visible services with desired >= max capacity. AtMaxSince is the first counts-history
// sample after the last one that had a lower desired count (the earliest sample when there never was one). The
// history lookup is a primary-key prefix scan per at-max service, which is fine for the handful that sit at max.
func (s *Store) AnalyticsAtScalingMax(ctx context.Context, f TargetFilter, vis Visibility) ([]AtMaxRow, error) {
	var q queryArgs
	target := q.target("s", f)
	v := q.visibility("s", vis)
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, coalesce(s.desired, 0), sc.max_capacity, since.ts
		 from services s`+v.Joins+`
		 join scaling sc on sc.account_id = s.account_id and sc.region = s.region and sc.cluster_name = s.cluster_name
		   and sc.service_name = s.service_name
		 left join lateral (
		   select min(h.ts) as ts from service_counts_history h
		   where h.account_id = s.account_id and h.region = s.region and h.cluster_name = s.cluster_name
		     and h.service_name = s.service_name
		     and h.ts > coalesce((select max(b.ts) from service_counts_history b
		       where b.account_id = s.account_id and b.region = s.region and b.cluster_name = s.cluster_name
		         and b.service_name = s.service_name and coalesce(b.desired, 0) < sc.max_capacity), '-infinity'::timestamptz)
		 ) since on true
		 where sc.has_target and sc.max_capacity > 0 and coalesce(s.desired, 0) >= sc.max_capacity and `+target+` and `+v.Predicate+`
		 order by s.account_id, s.region, s.cluster_name, s.service_name`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics at scaling max: %w", err)
	}
	defer rows.Close()
	out := []AtMaxRow{}
	for rows.Next() {
		var row AtMaxRow
		if err := rows.Scan(&row.Key.AccountID, &row.Key.Region, &row.Key.Cluster, &row.Key.Service, &row.Desired,
			&row.MaxCapacity, &row.AtMaxSince); err != nil {
			return nil, fmt.Errorf("scan at-max service: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics at scaling max: %w", err)
	}
	return out, nil
}

// SubnetLow is a subnet with under 10% of its usable addresses free.
type SubnetLow struct {
	AccountID, Region, SubnetID, Name, AZ string
	AvailableIPs, TotalIPs                int // total = CIDR size minus the 5 addresses AWS reserves
	UsedPct                               float64 // fraction 0..1 of usable addresses in use
}

// lowIPShare is the free-address share below which a subnet is reported.
const lowIPShare = 0.10

// AnalyticsSubnetsLow lists IPv4 subnets under lowIPShare free, most used first. Subnets have no tag of their
// own, so a non-admin sees one only when a service or running task they may view uses it.
func (s *Store) AnalyticsSubnetsLow(ctx context.Context, f TargetFilter, vis Visibility) ([]SubnetLow, error) {
	var q queryArgs
	target := q.target("sn", f)
	svcVis := q.visibility("sv", vis)
	adminParam := len(q.values) - 1
	taskVis := q.visibility("tv", vis)
	threshold := q.add(lowIPShare)
	rows, err := s.Pool.Query(ctx,
		`select sn.account_id, sn.region, sn.subnet_id, sn.name, sn.az, sn.available_ips, sn.total_ips,
		   (1 - sn.available_ips::float8 / sn.total_ips) as used_pct
		 from (select x.*, case when family(x.cidr::cidr) = 4 and masklen(x.cidr::cidr) between 8 and 28
		         then ((1::bigint << (32 - masklen(x.cidr::cidr))) - 5)::int end as total_ips
		       from subnets x) sn
		 where sn.total_ips is not null and `+target+` and sn.available_ips::float8 / sn.total_ips < `+threshold+`::float8
		   and ($`+fmt.Sprint(adminParam)+`::bool
		     or exists (select 1 from services sv`+svcVis.Joins+` where sv.account_id = sn.account_id and sv.region = sn.region
		       and sn.subnet_id = any(sv.subnets) and `+svcVis.Predicate+`)
		     or exists (select 1 from tasks tv`+taskVis.Joins+` where tv.account_id = sn.account_id and tv.region = sn.region
		       and tv.subnet_id = sn.subnet_id and tv.last_status = 'RUNNING' and `+taskVis.Predicate+`))
		 order by used_pct desc, sn.subnet_id`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics subnets low: %w", err)
	}
	defer rows.Close()
	out := []SubnetLow{}
	for rows.Next() {
		var row SubnetLow
		if err := rows.Scan(&row.AccountID, &row.Region, &row.SubnetID, &row.Name, &row.AZ, &row.AvailableIPs, &row.TotalIPs, &row.UsedPct); err != nil {
			return nil, fmt.Errorf("scan subnet: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics subnets low: %w", err)
	}
	return out, nil
}

// TrendPoint is the average reserved vCPU of one effective tag in one bucket.
type TrendPoint struct {
	Bucket time.Time
	TagID  authz.TagID
	VCPU   float64
}

// AnalyticsReservedTrend estimates reserved vCPU per bucket and effective tag as counts history times each
// service's current task definition CPU, averaged over the bucket's samples. The database sums per sample
// timestamp, target and tag (about 2,000 timestamps x targets x a few tags, a hash aggregate); averaging into
// buckets happens in Go because a DISTINCT sample count would force a sorted aggregate over the whole window.
// Every target's poll loop stamps its own timestamps, so each target is averaged over its own samples and the
// per-target averages are then summed; dividing by all distinct timestamps would understate the total N-fold.
func (s *Store) AnalyticsReservedTrend(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time, bucket time.Duration) ([]TrendPoint, error) {
	var q queryArgs
	fromArg, toArg := q.add(from), q.add(to)
	historyTarget := q.target("h", f)
	serviceTarget := q.target("s", f)
	v := q.visibility("s", vis)
	sql := `select h.ts, h.account_id, h.region, m.tag_id, sum(coalesce(h.running, 0)::float8 * m.cpu_units) / 1024.0
		 from service_counts_history h
		 join (
		   select s.account_id, s.region, s.cluster_name, s.service_name, ` + v.Effective + ` as tag_id,
		     case when td.cpu ~ '^[0-9]+$' then td.cpu::int else 0 end as cpu_units
		   from services s` + v.Joins + `
		   left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		   where ` + serviceTarget + ` and ` + v.Predicate + `) m
		   on m.account_id = h.account_id and m.region = h.region and m.cluster_name = h.cluster_name and m.service_name = h.service_name
		 where h.ts >= ` + fromArg + ` and h.ts < ` + toArg + ` and ` + historyTarget + `
		 group by 1, 2, 3, 4`

	type targetKey struct{ account, region string }
	type bucketKey struct {
		start time.Time
		tag   authz.TagID
	}
	type targetBucket struct {
		start  time.Time
		target targetKey
	}
	type targetTag struct {
		targetBucket
		tag authz.TagID
	}
	sums := map[targetTag]float64{}
	samples := map[targetBucket]map[time.Time]bool{} // target and bucket start -> distinct sample timestamps
	err := s.scanHistory(ctx, sql, q.values, func(rows pgx.Rows) error {
		var ts time.Time
		var target targetKey
		var tag authz.TagID
		var vcpu float64
		if err := rows.Scan(&ts, &target.account, &target.region, &tag, &vcpu); err != nil {
			return err
		}
		tb := targetBucket{from.Add(ts.Sub(from) / bucket * bucket), target}
		sums[targetTag{tb, tag}] += vcpu
		if samples[tb] == nil {
			samples[tb] = map[time.Time]bool{}
		}
		samples[tb][ts] = true
		return nil
	}, "enable_sort = off") // the planner overestimates the groups of the wider key and would sort 600k rows to disk instead
	if err != nil {
		return nil, fmt.Errorf("analytics reserved trend: %w", err)
	}
	totals := map[bucketKey]float64{}
	for key, total := range sums {
		totals[bucketKey{key.start, key.tag}] += total / float64(len(samples[key.targetBucket]))
	}
	out := make([]TrendPoint, 0, len(totals))
	for key, vcpu := range totals {
		out = append(out, TrendPoint{Bucket: key.start, TagID: key.tag, VCPU: vcpu})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Bucket.Equal(out[j].Bucket) {
			return out[i].Bucket.Before(out[j].Bucket)
		}
		return out[i].TagID < out[j].TagID
	})
	return out, nil
}
