package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Daily analytics tables (docs/analytics_design/PLAN.md section 4). Days are UTC dates.

// Names of the daily tables, as used in DailyDataSince, DailyWatermarks and rollup results.
const (
	TableServiceAvailabilityDaily = "service_availability_daily"
	TableClusterCapacityDaily     = "cluster_capacity_daily"
	TableServiceUtilDaily         = "service_util_daily"
)

// ClusterCapacitySample is one cluster's running-task capacity at a single poll.
type ClusterCapacitySample struct {
	Cluster model.ClusterKey
	VCPU    float64 // sum of task cpu units / 1024
	MemGiB  float64 // sum of task memory MiB / 1024
	Tasks   int
	Spot    int // tasks on FARGATE_SPOT
}

// ServiceUtilRow is one service's utilisation for a day; percent fields are nil when CloudWatch had no datapoint.
type ServiceUtilRow struct {
	Key                              model.ServiceKey
	CPUAvg, CPUP95                   *float64
	MemAvg, MemP95                   *float64
	CPUReservedUnits, MemReservedMiB *int
}

// ServiceReservation is a service's size from its current task definition; nil when unknown.
type ServiceReservation struct {
	Key                 model.ServiceKey
	CPUUnits, MemoryMiB *int
}

// dayStart returns midnight UTC of the calendar day t falls on.
func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// RollupAvailabilityDay recomputes service_availability_daily for one UTC day from counts history and stopped tasks.
// A service gets a row when it has at least one sample that day. minutes_below is the below-desired samples times
// sampleInterval; unexpected_stops counts that day's stopped tasks outside the expected stop reasons (the same
// set the Reliability tab uses). Re-running a day overwrites its rows. It returns the number of rows written.
func (s *Store) RollupAvailabilityDay(ctx context.Context, day time.Time, sampleInterval time.Duration) (int, error) {
	from := dayStart(day)
	to := from.AddDate(0, 0, 1)
	tag, err := s.Pool.Exec(ctx,
		`insert into service_availability_daily
		   (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops)
		 select a.account_id, a.region, a.cluster_name, a.service_name, $1::date, a.samples, a.ok,
		   (a.below * $2::numeric)::int, coalesce(st.unexpected, 0)
		 from (
		   select h.account_id, h.region, h.cluster_name, h.service_name, count(*) as samples,
		     count(*) filter (where coalesce(h.running, 0) >= coalesce(h.desired, 0)) as ok,
		     count(*) filter (where coalesce(h.running, 0) < coalesce(h.desired, 0)) as below
		   from service_counts_history h
		   where h.ts >= $3 and h.ts < $4
		   group by 1, 2, 3, 4) a
		 left join (
		   select t.account_id, t.region, t.cluster_name, t.service_name, count(*) as unexpected
		   from tasks t
		   where t.last_status = 'STOPPED' and t.stopped_at >= $3 and t.stopped_at < $4 and t.service_name is not null
		     and not (coalesce(t.stop_reason_group, '') = any($5::text[]))
		   group by 1, 2, 3, 4) st
		   on st.account_id = a.account_id and st.region = a.region and st.cluster_name = a.cluster_name
		   and st.service_name = a.service_name
		 on conflict (account_id, region, cluster_name, service_name, day) do update set
		   samples = excluded.samples, samples_ok = excluded.samples_ok,
		   minutes_below = excluded.minutes_below, unexpected_stops = excluded.unexpected_stops`,
		from.Format(time.DateOnly), sampleInterval.Minutes(), from, to, expectedStopReasons)
	if err != nil {
		return 0, fmt.Errorf("rollup availability day %s: %w", from.Format(time.DateOnly), err)
	}
	return int(tag.RowsAffected()), nil
}

// dailyBound returns min(day) or max(day) of every daily table; empty tables map to nil.
func (s *Store) dailyBound(ctx context.Context, aggregate string) (map[string]*time.Time, error) {
	out := map[string]*time.Time{}
	for _, table := range []string{TableServiceAvailabilityDaily, TableClusterCapacityDaily, TableServiceUtilDaily} {
		var bound *time.Time
		if err := s.Pool.QueryRow(ctx, `select `+aggregate+`(day)::timestamp at time zone 'UTC' from `+table).Scan(&bound); err != nil {
			return nil, fmt.Errorf("daily %s of %s: %w", aggregate, table, err)
		}
		out[table] = bound
	}
	return out, nil
}

// DailyDataSince returns min(day) of each daily table (UTC midnight); the value is nil for an empty table.
func (s *Store) DailyDataSince(ctx context.Context) (map[string]*time.Time, error) {
	return s.dailyBound(ctx, "min")
}

// DailyWatermarks returns max(day) of each daily table (UTC midnight); the value is nil for an empty table.
func (s *Store) DailyWatermarks(ctx context.Context) (map[string]*time.Time, error) {
	return s.dailyBound(ctx, "max")
}

// RunningTaskCapacityByCluster sums the RUNNING tasks of every cluster of the target; a cluster with no tasks
// yields a zero sample so idle time still lowers the daily average. Tasks without a task-level size (EC2) add zero.
func (s *Store) RunningTaskCapacityByCluster(ctx context.Context, target TargetFilter) ([]ClusterCapacitySample, error) {
	rows, err := s.Pool.Query(ctx,
		`select c.cluster_name,
		   coalesce(sum(case when t.cpu ~ '^[0-9]+$' then t.cpu::bigint end), 0) / 1024.0,
		   coalesce(sum(case when t.memory ~ '^[0-9]+$' then t.memory::bigint end), 0) / 1024.0,
		   count(t.task_arn), count(t.task_arn) filter (where t.capacity_provider = 'FARGATE_SPOT')
		 from clusters c
		 left join tasks t on t.account_id = c.account_id and t.region = c.region and t.cluster_name = c.cluster_name
		   and t.last_status = 'RUNNING'
		 where c.account_id = $1 and c.region = $2
		 group by c.cluster_name order by c.cluster_name`, target.AccountID, target.Region)
	if err != nil {
		return nil, fmt.Errorf("running task capacity: %w", err)
	}
	defer rows.Close()
	out := []ClusterCapacitySample{}
	for rows.Next() {
		c := ClusterCapacitySample{Cluster: model.ClusterKey{AccountID: target.AccountID, Region: target.Region}}
		if err := rows.Scan(&c.Cluster.Cluster, &c.VCPU, &c.MemGiB, &c.Tasks, &c.Spot); err != nil {
			return nil, fmt.Errorf("scan running task capacity: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("running task capacity: %w", err)
	}
	return out, nil
}

// UpsertClusterCapacitySample folds one poll's samples into each cluster's row for day: sample count and sums grow,
// maxima keep the largest. One row per cluster per day, so the table never holds per-sample history.
func (s *Store) UpsertClusterCapacitySample(ctx context.Context, day time.Time, samples []ClusterCapacitySample) error {
	if len(samples) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	date := dayStart(day).Format(time.DateOnly)
	for _, c := range samples {
		batch.Queue(
			`insert into cluster_capacity_daily
			   (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
			 values ($1, $2, $3, $4::date, 1, $5, $5, $6, $6, $7, $7, $8)
			 on conflict (account_id, region, cluster_name, day) do update set
			   samples = cluster_capacity_daily.samples + 1,
			   vcpu_sum = cluster_capacity_daily.vcpu_sum + excluded.vcpu_sum,
			   vcpu_max = greatest(cluster_capacity_daily.vcpu_max, excluded.vcpu_max),
			   mem_gib_sum = cluster_capacity_daily.mem_gib_sum + excluded.mem_gib_sum,
			   mem_gib_max = greatest(cluster_capacity_daily.mem_gib_max, excluded.mem_gib_max),
			   tasks_sum = cluster_capacity_daily.tasks_sum + excluded.tasks_sum,
			   tasks_max = greatest(cluster_capacity_daily.tasks_max, excluded.tasks_max),
			   spot_sum = cluster_capacity_daily.spot_sum + excluded.spot_sum`,
			c.Cluster.AccountID, c.Cluster.Region, c.Cluster.Cluster, date, c.VCPU, c.MemGiB, c.Tasks, c.Spot)
	}
	results := s.Pool.SendBatch(ctx, batch)
	defer results.Close()
	for range samples {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert cluster capacity sample: %w", err)
		}
	}
	return nil
}

// UpsertServiceUtilDay writes the util rows of one UTC day in one transaction, replacing rows already there.
func (s *Store) UpsertServiceUtilDay(ctx context.Context, day time.Time, rows []ServiceUtilRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("upsert service util day: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	date := dayStart(day).Format(time.DateOnly)
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(
			`insert into service_util_daily
			   (account_id, region, cluster_name, service_name, day, cpu_avg, cpu_p95, mem_avg, mem_p95, cpu_reserved_units, mem_reserved_mib)
			 values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9, $10, $11)
			 on conflict (account_id, region, cluster_name, service_name, day) do update set
			   cpu_avg = excluded.cpu_avg, cpu_p95 = excluded.cpu_p95, mem_avg = excluded.mem_avg, mem_p95 = excluded.mem_p95,
			   cpu_reserved_units = excluded.cpu_reserved_units, mem_reserved_mib = excluded.mem_reserved_mib`,
			r.Key.AccountID, r.Key.Region, r.Key.Cluster, r.Key.Service, date,
			r.CPUAvg, r.CPUP95, r.MemAvg, r.MemP95, r.CPUReservedUnits, r.MemReservedMiB)
	}
	results := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("upsert service util day: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("upsert service util day: %w", err)
	}
	return tx.Commit(ctx)
}

// UtilDayRecorded reports whether the target already has util rows for the day.
func (s *Store) UtilDayRecorded(ctx context.Context, target TargetFilter, day time.Time) (bool, error) {
	var found bool
	err := s.Pool.QueryRow(ctx,
		`select exists (select 1 from service_util_daily where account_id = $1 and region = $2 and day = $3::date)`,
		target.AccountID, target.Region, dayStart(day).Format(time.DateOnly)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("util day recorded: %w", err)
	}
	return found, nil
}

// ServiceReservations lists every service of the target with the size of its current task definition.
func (s *Store) ServiceReservations(ctx context.Context, target TargetFilter) ([]ServiceReservation, error) {
	rows, err := s.Pool.Query(ctx,
		`select s.cluster_name, s.service_name,
		   case when td.cpu ~ '^[0-9]+$' then td.cpu::int end,
		   case when td.memory ~ '^[0-9]+$' then td.memory::int end
		 from services s
		 left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 where s.account_id = $1 and s.region = $2
		 order by s.cluster_name, s.service_name`, target.AccountID, target.Region)
	if err != nil {
		return nil, fmt.Errorf("service reservations: %w", err)
	}
	defer rows.Close()
	out := []ServiceReservation{}
	for rows.Next() {
		r := ServiceReservation{Key: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: target.AccountID, Region: target.Region}}}
		if err := rows.Scan(&r.Key.Cluster, &r.Key.Service, &r.CPUUnits, &r.MemoryMiB); err != nil {
			return nil, fmt.Errorf("scan service reservation: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service reservations: %w", err)
	}
	return out, nil
}
