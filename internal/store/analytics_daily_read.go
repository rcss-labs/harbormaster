package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Reads of the daily rollup tables for the "Proposed" analytics panels (docs/analytics_design/PLAN.md
// sections 2.2, 2.3 and 4). Every query is bounded by the (day) index of its table and holds at most
// about 600 rows a day, so a year of data stays a few hundred thousand rows at most. Days are UTC dates.

// DailyAvailabilityPoint is the visible services' summed availability counts for one day.
type DailyAvailabilityPoint struct {
	Day                       time.Time
	Samples, OK, MinutesBelow int
}

// AnalyticsAvailabilityDaily sums service_availability_daily over the visible services per day, for days in
// [from, to) (only the date part of each bound counts). Days without rows are absent.
func (s *Store) AnalyticsAvailabilityDaily(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]DailyAvailabilityPoint, error) {
	var q queryArgs
	target := q.target("a", f)
	v := q.visibility("a", vis)
	fromArg, toArg := q.add(from), q.add(to)
	rows, err := s.Pool.Query(ctx,
		`select a.day, sum(a.samples), sum(a.samples_ok), sum(a.minutes_below)
		 from service_availability_daily a`+v.Joins+`
		 where `+target+` and `+v.Predicate+` and a.day >= `+fromArg+`::date and a.day < `+toArg+`::date
		 group by a.day order by a.day`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics availability daily: %w", err)
	}
	defer rows.Close()
	out := []DailyAvailabilityPoint{}
	for rows.Next() {
		var p DailyAvailabilityPoint
		if err := rows.Scan(&p.Day, &p.Samples, &p.OK, &p.MinutesBelow); err != nil {
			return nil, fmt.Errorf("scan availability day: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics availability daily: %w", err)
	}
	return out, nil
}

// AvailabilityTrendFact is one service's availability in the last 30 complete days and the 30 before, as 0..1
// fractions (samples_ok / samples).
type AvailabilityTrendFact struct {
	Key               model.ServiceKey
	TagID             int64
	TagInherited      bool
	Previous, Current float64
}

// AnalyticsAvailabilityTrend compares each visible service's availability over [today-30d, today) with
// [today-60d, today-30d). today is a UTC midnight; the running day is excluded because its rollup is not
// written yet. A service needs samples in both periods to appear.
func (s *Store) AnalyticsAvailabilityTrend(ctx context.Context, f TargetFilter, vis Visibility, today time.Time) ([]AvailabilityTrendFact, error) {
	today = dayStart(today)
	var q queryArgs
	target := q.target("a", f)
	v := q.visibility("a", vis)
	currentFrom, previousFrom := q.add(today.AddDate(0, 0, -30)), q.add(today.AddDate(0, 0, -60))
	todayArg := q.add(today)
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, cluster_name, service_name, tag_id, inherited,
		   sum(samples_ok) filter (where day < `+currentFrom+`::date)::float8 / nullif(sum(samples) filter (where day < `+currentFrom+`::date), 0),
		   sum(samples_ok) filter (where day >= `+currentFrom+`::date)::float8 / nullif(sum(samples) filter (where day >= `+currentFrom+`::date), 0)
		 from (
		   select a.account_id, a.region, a.cluster_name, a.service_name, a.day, a.samples, a.samples_ok,
		     `+v.Effective+` as tag_id, `+v.Inherited+` as inherited
		   from service_availability_daily a`+v.Joins+`
		   where `+target+` and `+v.Predicate+` and a.day >= `+previousFrom+`::date and a.day < `+todayArg+`::date) x
		 group by account_id, region, cluster_name, service_name, tag_id, inherited
		 having sum(samples) filter (where day < `+currentFrom+`::date) > 0 and sum(samples) filter (where day >= `+currentFrom+`::date) > 0
		 order by account_id, region, cluster_name, service_name`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics availability trend: %w", err)
	}
	defer rows.Close()
	out := []AvailabilityTrendFact{}
	for rows.Next() {
		var fact AvailabilityTrendFact
		key := &fact.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &fact.TagID, &fact.TagInherited, &fact.Previous, &fact.Current); err != nil {
			return nil, fmt.Errorf("scan availability trend: %w", err)
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics availability trend: %w", err)
	}
	return out, nil
}

// CapacityDailyPoint is the reserved capacity of the visible clusters for one day: each cluster's daily
// average (sum / samples) added up. VCPUMax adds up the clusters' daily peaks, an upper bound for the peak of the sum.
type CapacityDailyPoint struct {
	Day                          time.Time
	VCPU, VCPUMax, MemGiB, Tasks float64
	Spot                         float64 // average tasks on FARGATE_SPOT
}

// AnalyticsCapacityDaily reads cluster_capacity_daily for days in [from, to). A cluster counts when the caller
// is admin or can see at least one of its services, like the clusters list. The totals are deliberately for the
// whole cluster, across all tags: the rollup stores one row per cluster, not per service, so a viewer who sees
// only some of a cluster's services still gets its full reservation. The Capacity tab labels the panels so.
func (s *Store) AnalyticsCapacityDaily(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]CapacityDailyPoint, error) {
	var q queryArgs
	target := q.target("c", f)
	svcVis := q.visibility("sv", vis)
	visible := clusterVisiblePredicate("c", svcVis, len(q.values)-1)
	fromArg, toArg := q.add(from), q.add(to)
	rows, err := s.Pool.Query(ctx,
		`select c.day, sum(c.vcpu_sum / c.samples), sum(c.vcpu_max), sum(c.mem_gib_sum / c.samples),
		   sum(c.tasks_sum::float8 / c.samples), sum(c.spot_sum::float8 / c.samples)
		 from cluster_capacity_daily c
		 where `+target+` and `+visible+` and c.samples > 0 and c.day >= `+fromArg+`::date and c.day < `+toArg+`::date
		 group by c.day order by c.day`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics capacity daily: %w", err)
	}
	defer rows.Close()
	out := []CapacityDailyPoint{}
	for rows.Next() {
		var p CapacityDailyPoint
		if err := rows.Scan(&p.Day, &p.VCPU, &p.VCPUMax, &p.MemGiB, &p.Tasks, &p.Spot); err != nil {
			return nil, fmt.Errorf("scan capacity day: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics capacity daily: %w", err)
	}
	return out, nil
}

// UtilDailyPoint is the visible services' measured use for one day, in absolute units: the sum over services of
// (percent of reserved / 100) x reserved size. A field is nil when no service had a datapoint that day.
type UtilDailyPoint struct {
	Day                               time.Time
	CPUAvgVCPU, CPUP95VCPU, MemP95GiB *float64
}

// AnalyticsUtilDaily reads service_util_daily for days in [from, to). Adding per-service p95s overstates the p95
// of the total; it is the sizing figure (what the services needed at their busiest), not a statistic of the sum.
func (s *Store) AnalyticsUtilDaily(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]UtilDailyPoint, error) {
	var q queryArgs
	target := q.target("u", f)
	v := q.visibility("u", vis)
	fromArg, toArg := q.add(from), q.add(to)
	rows, err := s.Pool.Query(ctx,
		`select u.day, sum(u.cpu_avg / 100 * u.cpu_reserved_units / 1024.0), sum(u.cpu_p95 / 100 * u.cpu_reserved_units / 1024.0),
		   sum(u.mem_p95 / 100 * u.mem_reserved_mib / 1024.0)
		 from service_util_daily u`+v.Joins+`
		 where `+target+` and `+v.Predicate+` and u.day >= `+fromArg+`::date and u.day < `+toArg+`::date
		 group by u.day order by u.day`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics util daily: %w", err)
	}
	defer rows.Close()
	out := []UtilDailyPoint{}
	for rows.Next() {
		var p UtilDailyPoint
		if err := rows.Scan(&p.Day, &p.CPUAvgVCPU, &p.CPUP95VCPU, &p.MemP95GiB); err != nil {
			return nil, fmt.Errorf("scan util day: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics util daily: %w", err)
	}
	return out, nil
}

// OverReservedFact is one service's reservation against its measured use. Sizes are per task; Running is the
// current running task count, which turns the per-task gap into unused capacity.
type OverReservedFact struct {
	Key                                                      model.ServiceKey
	TagID                                                    int64
	TagInherited                                             bool
	ReservedVCPU, UsedP95VCPU, ReservedMemGiB, UsedP95MemGiB float64
	Running                                                  int
}

// AnalyticsOverReserved returns every visible, still existing service with a CPU p95 since from. Reservation is
// the latest recorded size; use is the 95th percentile of the daily p95 percentages (percentile_cont) of that period.
// Ranking and capping is the caller's job (at most about 600 rows).
func (s *Store) AnalyticsOverReserved(ctx context.Context, f TargetFilter, vis Visibility, from time.Time) ([]OverReservedFact, error) {
	var q queryArgs
	target := q.target("s", f)
	v := q.visibility("s", vis)
	fromArg := q.add(from)
	// Driven from the (at most about 600) services so each one reads its own days through the primary key; the
	// planner otherwise sorts every service-day for the percentile, which is several times slower.
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, `+v.Effective+`, `+v.Inherited+`,
		   latest.cpu_reserved_units, latest.mem_reserved_mib, p95.cpu_p95, p95.mem_p95, coalesce(s.running, 0)
		 from services s`+v.Joins+`
		 cross join lateral (
		   select count(u.cpu_p95) as samples, percentile_cont(0.95) within group (order by u.cpu_p95) as cpu_p95,
		     percentile_cont(0.95) within group (order by u.mem_p95) as mem_p95
		   from service_util_daily u
		   where u.account_id = s.account_id and u.region = s.region and u.cluster_name = s.cluster_name
		     and u.service_name = s.service_name and u.day >= `+fromArg+`::date) p95
		 cross join lateral (
		   select r.cpu_reserved_units, r.mem_reserved_mib from service_util_daily r
		   where r.account_id = s.account_id and r.region = s.region and r.cluster_name = s.cluster_name
		     and r.service_name = s.service_name and r.day >= `+fromArg+`::date and r.cpu_reserved_units is not null
		   order by r.day desc limit 1) latest
		 where `+target+` and `+v.Predicate+` and p95.samples > 0
		 order by s.account_id, s.region, s.cluster_name, s.service_name`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics over reserved: %w", err)
	}
	defer rows.Close()
	out := []OverReservedFact{}
	for rows.Next() {
		var fact OverReservedFact
		var cpuUnits, memMiB *int
		var cpuP95, memP95 *float64
		key := &fact.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &fact.TagID, &fact.TagInherited,
			&cpuUnits, &memMiB, &cpuP95, &memP95, &fact.Running); err != nil {
			return nil, fmt.Errorf("scan over-reserved service: %w", err)
		}
		fact.ReservedVCPU = float64(*cpuUnits) / 1024
		fact.UsedP95VCPU = *cpuP95 / 100 * fact.ReservedVCPU
		if memMiB != nil {
			fact.ReservedMemGiB = float64(*memMiB) / 1024
			if memP95 != nil {
				fact.UsedP95MemGiB = *memP95 / 100 * fact.ReservedMemGiB
			}
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics over reserved: %w", err)
	}
	return out, nil
}

// FailedRolloutFact is a failed rollout with the reason recorded when it failed.
type FailedRolloutFact struct {
	Key          model.ServiceKey
	DeploymentID string
	At           time.Time // when the rollout failed (finished_at, or its start while unknown)
	FromRevision *int      // revision of the previous deploy or restart of the service, nil for its first
	ToRevision   int
	Reason       string
	Detail       string
	RanFor       *time.Duration
}

// AnalyticsRecentFailures returns up to limit failed rollouts started in [from, to) that have a failure_reason
// (older rows have none), newest first, and the total number of such rollouts. The previous revision is a
// primary-key prefix lookup per returned row.
func (s *Store) AnalyticsRecentFailures(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time, limit int) ([]FailedRolloutFact, int, error) {
	var q queryArgs
	target := q.target("d", f)
	v := q.visibility("d", vis)
	fromArg, toArg, limitArg := q.add(from), q.add(to), q.add(limit)
	rows, err := s.Pool.Query(ctx,
		`select d.account_id, d.region, d.cluster_name, d.service_name, d.deployment_id, d.started_at, d.finished_at,
		   coalesce(d.revision, 0), d.failure_reason, coalesce(d.failure_detail, ''),
		   (select p.revision from deployment_history p
		     where p.account_id = d.account_id and p.region = d.region and p.cluster_name = d.cluster_name
		       and p.service_name = d.service_name and p.kind in ('deploy', 'restart') and p.revision > 0
		       and p.started_at < d.started_at
		     order by p.started_at desc limit 1),
		   count(*) over ()
		 from deployment_history d`+v.Joins+`
		 where `+target+` and `+v.Predicate+` and d.outcome = 'failed' and d.failure_reason is not null
		   and d.started_at >= `+fromArg+` and d.started_at < `+toArg+`
		 order by d.started_at desc, d.deployment_id
		 limit `+limitArg, q.values...)
	if err != nil {
		return nil, 0, fmt.Errorf("analytics recent failures: %w", err)
	}
	defer rows.Close()
	out := []FailedRolloutFact{}
	total := 0
	for rows.Next() {
		var fact FailedRolloutFact
		var started time.Time
		var finished *time.Time
		key := &fact.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &fact.DeploymentID, &started, &finished,
			&fact.ToRevision, &fact.Reason, &fact.Detail, &fact.FromRevision, &total); err != nil {
			return nil, 0, fmt.Errorf("scan failed rollout: %w", err)
		}
		fact.At = started
		if finished != nil {
			fact.At = *finished
			ranFor := finished.Sub(started)
			fact.RanFor = &ranFor
		}
		out = append(out, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("analytics recent failures: %w", err)
	}
	return out, total, nil
}

// AnalyticsFailureReasonSince is the start of the earliest visible rollout that has a failure_reason, nil when
// there is none. It tells when failure reasons began to be recorded.
func (s *Store) AnalyticsFailureReasonSince(ctx context.Context, f TargetFilter, vis Visibility) (*time.Time, error) {
	var q queryArgs
	target := q.target("d", f)
	v := q.visibility("d", vis)
	var since *time.Time
	err := s.Pool.QueryRow(ctx,
		`select min(d.started_at) from deployment_history d`+v.Joins+`
		 where `+target+` and `+v.Predicate+` and d.failure_reason is not null`, q.values...).Scan(&since)
	if err != nil {
		return nil, fmt.Errorf("analytics failure reason since: %w", err)
	}
	return since, nil
}
