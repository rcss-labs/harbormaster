package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Reliability reads for the analytics tab (docs/analytics_design/PLAN.md section 2.2). Query shapes are the
// measured ones from section 3: per-bucket aggregates with visibility as a semi-join on the visible service
// keys, and per-service aggregates. Never group service_counts_history by (service, bucket).

// expectedStopReasons are the stop-reason groups that are a normal part of operating a service.
var expectedStopReasons = []string{"Replaced by a deployment", "Scaled in", "Stopped by a user"}

// IsExpectedStopReason reports whether a stop-reason group is routine (deployments, scale-in, manual stops).
func IsExpectedStopReason(group string) bool {
	for _, expected := range expectedStopReasons {
		if group == expected {
			return true
		}
	}
	return false
}

// seconds is a duration as the float8 that make_interval(secs => ...) takes.
func seconds(d time.Duration) float64 { return d.Seconds() }

// AvailBucket is the counts-history sample tally of one time bucket.
type AvailBucket struct {
	Bucket        time.Time
	Samples       int // every sample of a visible service
	OK            int // samples with running >= desired
	BelowServices int // distinct services with at least one sample below desired
}

// AnalyticsAvailabilitySeries tallies counts-history samples per bucket over [from, to) for the visible services.
func (s *Store) AnalyticsAvailabilitySeries(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time, bucket time.Duration) ([]AvailBucket, error) {
	var q queryArgs
	fromArg, toArg, bucketArg := q.add(from), q.add(to), q.add(seconds(bucket))
	historyTarget := q.target("h", f)
	serviceTarget := q.target("s", f)
	v := q.visibility("s", vis)
	sql := `select date_bin(make_interval(secs => ` + bucketArg + `::float8), h.ts, ` + fromArg + `::timestamptz) as b,
	    count(*),
	    count(*) filter (where coalesce(h.running, 0) >= coalesce(h.desired, 0)),
	    count(distinct (h.cluster_name, h.service_name)) filter (where coalesce(h.running, 0) < coalesce(h.desired, 0))
	  from service_counts_history h
	  where h.ts >= ` + fromArg + ` and h.ts < ` + toArg + ` and ` + historyTarget + `
	    and (h.account_id, h.region, h.cluster_name, h.service_name) in (
	      select s.account_id, s.region, s.cluster_name, s.service_name from services s` + v.Joins + `
	      where ` + serviceTarget + ` and ` + v.Predicate + `)
	  group by 1 order by 1`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics availability series: %w", err)
	}
	defer rows.Close()
	out := []AvailBucket{}
	for rows.Next() {
		var b AvailBucket
		if err := rows.Scan(&b.Bucket, &b.Samples, &b.OK, &b.BelowServices); err != nil {
			return nil, fmt.Errorf("scan availability bucket: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics availability series: %w", err)
	}
	return out, nil
}

// ServiceAvail is one service's counts-history tally over the window.
type ServiceAvail struct {
	Key                model.ServiceKey
	EffectiveTagID     authz.TagID
	TagInherited       bool
	Samples, OK, Below int
}

// MinutesBelow is the time under desired, assuming one sample per interval.
func (a ServiceAvail) MinutesBelow(interval time.Duration) float64 {
	return float64(a.Below) * interval.Minutes()
}

// AnalyticsAvailabilityByService tallies counts-history samples per visible service over [from, to). The history is
// aggregated per service first (600 groups) and only then joined to services for visibility and tag.
func (s *Store) AnalyticsAvailabilityByService(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]ServiceAvail, error) {
	var q queryArgs
	fromArg, toArg := q.add(from), q.add(to)
	historyTarget := q.target("h", f)
	v := q.visibility("s", vis)
	sql := `select s.account_id, s.region, s.cluster_name, s.service_name, ` + v.Effective + `, ` + v.Inherited + `,
	    a.samples, a.ok, a.below
	  from (
	    select h.account_id, h.region, h.cluster_name, h.service_name, count(*) as samples,
	      count(*) filter (where coalesce(h.running, 0) >= coalesce(h.desired, 0)) as ok,
	      count(*) filter (where coalesce(h.running, 0) < coalesce(h.desired, 0)) as below
	    from service_counts_history h
	    where h.ts >= ` + fromArg + ` and h.ts < ` + toArg + ` and ` + historyTarget + `
	    group by 1, 2, 3, 4) a
	  join services s on s.account_id = a.account_id and s.region = a.region and s.cluster_name = a.cluster_name
	    and s.service_name = a.service_name` + v.Joins + `
	  where ` + v.Predicate + `
	  order by s.account_id, s.region, s.cluster_name, s.service_name`
	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics availability by service: %w", err)
	}
	defer rows.Close()
	out := []ServiceAvail{}
	for rows.Next() {
		var a ServiceAvail
		if err := rows.Scan(&a.Key.AccountID, &a.Key.Region, &a.Key.Cluster, &a.Key.Service, &a.EffectiveTagID,
			&a.TagInherited, &a.Samples, &a.OK, &a.Below); err != nil {
			return nil, fmt.Errorf("scan service availability: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics availability by service: %w", err)
	}
	return out, nil
}

// StopCount is a count of stopped tasks (or exit codes) in one bucket under one key.
type StopCount struct {
	Bucket time.Time
	Key    string
	Count  int
}

// ServiceStops is one service's unexpected stops and the most frequent unexpected reason.
type ServiceStops struct {
	Key        model.ServiceKey
	Unexpected int
	TopReason  string
}

// StopsResult is everything the stops panels need from the stopped tasks of a window.
type StopsResult struct {
	ByReason         []StopCount // every stop, by stop_reason_group
	UnexpectedByZone []StopCount // stops outside expectedStopReasons, by availability zone ("" = unknown)
	ExitCodes        []StopCount // non-zero container exit codes
	ByService        []ServiceStops
}

// AnalyticsStops reads the visible tasks stopped in [from, to): per-bucket counts by reason, unexpected stops by
// availability zone and non-zero exit codes, plus unexpected stops per service. Standalone tasks count in the
// per-bucket series (they take their cluster's tag) but not in ByService.
func (s *Store) AnalyticsStops(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time, bucket time.Duration) (StopsResult, error) {
	// stopped builds one query over the visible tasks stopped in the window. Each query gets its own
	// placeholder set because Postgres cannot type a parameter the statement never uses.
	stopped := func(selectList, lateral, extraWhere, groupBy string, withExpected bool) (string, []any) {
		var q queryArgs
		fromArg, toArg := q.add(from), q.add(to)
		if strings.Contains(selectList, "{bucket}") {
			selectList = strings.ReplaceAll(selectList, "{bucket}",
				`date_bin(make_interval(secs => `+q.add(seconds(bucket))+`::float8), t.stopped_at, `+fromArg+`::timestamptz)`)
		}
		target := q.target("t", f)
		v := q.visibility("t", vis)
		sql := `select ` + selectList + ` from tasks t` + v.Joins + lateral +
			` where t.last_status = 'STOPPED' and t.stopped_at >= ` + fromArg + ` and t.stopped_at < ` + toArg +
			` and ` + target + ` and ` + v.Predicate + extraWhere
		if withExpected {
			sql += ` and not (coalesce(t.stop_reason_group, '') = any(` + q.add(expectedStopReasons) + `::text[]))`
		}
		return sql + groupBy, q.values
	}

	var res StopsResult
	var err error
	sql, args := stopped(`{bucket}, coalesce(t.stop_reason_group, ''), count(*)`, ``, ``, ` group by 1, 2 order by 1, 2`, false)
	if res.ByReason, err = s.stopCounts(ctx, sql, args); err != nil {
		return StopsResult{}, err
	}
	sql, args = stopped(`{bucket}, coalesce(t.availability_zone, ''), count(*)`, ``, ``, ` group by 1, 2 order by 1, 2`, true)
	if res.UnexpectedByZone, err = s.stopCounts(ctx, sql, args); err != nil {
		return StopsResult{}, err
	}
	sql, args = stopped(`{bucket}, c->>'ExitCode', count(*)`,
		` cross join lateral jsonb_array_elements(case when jsonb_typeof(t.containers) = 'array' then t.containers else '[]'::jsonb end) c`,
		` and jsonb_typeof(c->'ExitCode') = 'number' and (c->>'ExitCode')::int <> 0`, ` group by 1, 2 order by 1, 2`, false)
	if res.ExitCodes, err = s.stopCounts(ctx, sql, args); err != nil {
		return StopsResult{}, err
	}

	sql, args = stopped(`t.account_id, t.region, t.cluster_name, t.service_name, coalesce(t.stop_reason_group, ''), count(*)`, ``,
		` and t.service_name is not null`, ` group by 1, 2, 3, 4, 5`, true)
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return StopsResult{}, fmt.Errorf("analytics stops by service: %w", err)
	}
	defer rows.Close()
	byService := map[model.ServiceKey]*ServiceStops{}
	topCount := map[model.ServiceKey]int{}
	for rows.Next() {
		var key model.ServiceKey
		var group string
		var n int
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &group, &n); err != nil {
			return StopsResult{}, fmt.Errorf("scan stops by service: %w", err)
		}
		entry := byService[key]
		if entry == nil {
			entry = &ServiceStops{Key: key}
			byService[key] = entry
		}
		entry.Unexpected += n
		if n > topCount[key] || (n == topCount[key] && group < entry.TopReason) {
			topCount[key], entry.TopReason = n, group
		}
	}
	if err := rows.Err(); err != nil {
		return StopsResult{}, fmt.Errorf("analytics stops by service: %w", err)
	}
	res.ByService = make([]ServiceStops, 0, len(byService))
	for _, entry := range byService {
		res.ByService = append(res.ByService, *entry)
	}
	sort.Slice(res.ByService, func(i, j int) bool { return res.ByService[i].Key.ID() < res.ByService[j].Key.ID() })
	return res, nil
}

func (s *Store) stopCounts(ctx context.Context, sql string, args []any) ([]StopCount, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("analytics stops: %w", err)
	}
	defer rows.Close()
	out := []StopCount{}
	for rows.Next() {
		var c StopCount
		var key *string
		if err := rows.Scan(&c.Bucket, &key, &c.Count); err != nil {
			return nil, fmt.Errorf("scan stop count: %w", err)
		}
		if key != nil {
			c.Key = *key
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics stops: %w", err)
	}
	return out, nil
}

// AnalyticsPlacementFailures counts visible service events in [from, to) saying ECS was unable to place a task.
func (s *Store) AnalyticsPlacementFailures(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) (int, error) {
	var q queryArgs
	fromArg, toArg := q.add(from), q.add(to)
	target := q.target("e", f)
	v := q.visibility("e", vis)
	var n int
	err := s.Pool.QueryRow(ctx,
		`select count(*) from service_events e`+v.Joins+` where e.created_at >= `+fromArg+` and e.created_at < `+toArg+
			` and `+target+` and `+v.Predicate+` and e.message ilike '%unable to place%'`, q.values...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("analytics placement failures: %w", err)
	}
	return n, nil
}
