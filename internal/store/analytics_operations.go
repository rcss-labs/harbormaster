package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// opKindSQL classifies an operation_log row (alias o) into a kind. New rows carry details.op; the CASE is
// the fallback for rows written before that key existed. Kinds that are not operator actions come out as
// system (no actor: the OutcomeLogger), outcome, terminal_close or other (admin tag changes) and are
// filtered by countedOpKindsSQL.
const opKindSQL = `coalesce(o.details->>'op',
 case when o.actor_user_id is null then 'system'
      when o.details ? 'session_id' and o.text like 'Opened terminal%' then 'terminal'
      when o.details ? 'session_id' then 'terminal_close'
      when o.details ? 'task_definition_arn' then 'task_definition'
      when o.details ? 'deployment_id' and o.text like 'Rolled back%' then 'rollback'
      when o.details ? 'deployment_id' and o.text like 'Restarted%' then 'restart'
      when o.details ? 'deployment_id' then 'deploy'
      when o.text like 'Changed desired count%' then 'scale'
      when o.text like 'Stopped task%' then 'stop_task'
      else 'other' end)`

// countedOpKindsSQL keeps the kinds shown on the Operations tab.
const countedOpKindsSQL = `in ('deploy', 'rollback', 'task_definition', 'scale', 'restart', 'stop_task', 'terminal')`

// OperationBucketCount is how many operations of one kind started in one time bucket.
type OperationBucketCount struct {
	Bucket time.Time
	Kind   string
	Count  int
}

// AnalyticsOperations counts visible Harbormaster operations in [from, to) per bucket and kind. Buckets
// are aligned to from; buckets and kinds without operations are absent.
func (s *Store) AnalyticsOperations(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time, bucket time.Duration) ([]OperationBucketCount, error) {
	var q queryArgs
	target := q.target("o", f)
	v := q.visibility("o", vis)
	fromParam, toParam := q.add(from), q.add(to)
	bucketParam := q.add(bucket.Seconds())
	sql := `select date_bin(make_interval(secs => ` + bucketParam + `::float8), o.ts, ` + fromParam + `::timestamptz) as bucket,
	    ` + opKindSQL + ` as op_kind, count(*)
	  from operation_log o` + v.Joins + `
	  where o.source = 'harbormaster' and ` + target + ` and ` + v.Predicate + `
	    and o.ts >= ` + fromParam + ` and o.ts < ` + toParam + `
	    and ` + opKindSQL + ` ` + countedOpKindsSQL + `
	  group by 1, 2 order by 1, 2`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics operations: %w", err)
	}
	defer rows.Close()
	counts := []OperationBucketCount{}
	for rows.Next() {
		var c OperationBucketCount
		if err := rows.Scan(&c.Bucket, &c.Kind, &c.Count); err != nil {
			return nil, fmt.Errorf("scan analytics operation: %w", err)
		}
		counts = append(counts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics operations: %w", err)
	}
	return counts, nil
}

// OperatorCounts is what one person did in the window: Counts by kind and the time of their last operation.
type OperatorCounts struct {
	UserID   int64
	Username string
	Counts   map[string]int
	LastAt   time.Time
}

// AnalyticsOperators groups visible operations by the person who made them, busiest first. Every person
// with at least one counted operation is returned; callers cap the table and use the length as the people count.
func (s *Store) AnalyticsOperators(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]OperatorCounts, error) {
	var q queryArgs
	target := q.target("o", f)
	v := q.visibility("o", vis)
	fromParam, toParam := q.add(from), q.add(to)
	sql := `select o.actor_user_id, coalesce(max(o.actor_username), ''), ` + opKindSQL + ` as op_kind, count(*), max(o.ts)
	  from operation_log o` + v.Joins + `
	  where o.source = 'harbormaster' and o.actor_user_id is not null and ` + target + ` and ` + v.Predicate + `
	    and o.ts >= ` + fromParam + ` and o.ts < ` + toParam + `
	    and ` + opKindSQL + ` ` + countedOpKindsSQL + `
	  group by o.actor_user_id, 3`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics operators: %w", err)
	}
	defer rows.Close()
	byUser := map[int64]*OperatorCounts{}
	var order []int64
	for rows.Next() {
		var userID int64
		var username, kind string
		var n int
		var last time.Time
		if err := rows.Scan(&userID, &username, &kind, &n, &last); err != nil {
			return nil, fmt.Errorf("scan analytics operator: %w", err)
		}
		op, ok := byUser[userID]
		if !ok {
			op = &OperatorCounts{UserID: userID, Username: username, Counts: map[string]int{}}
			byUser[userID] = op
			order = append(order, userID)
		}
		op.Counts[kind] += n
		if last.After(op.LastAt) {
			op.LastAt = last
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics operators: %w", err)
	}
	operators := make([]OperatorCounts, 0, len(order))
	for _, id := range order {
		operators = append(operators, *byUser[id])
	}
	sortOperators(operators)
	return operators, nil
}

func sortOperators(operators []OperatorCounts) {
	total := func(o OperatorCounts) (sum int) {
		for _, n := range o.Counts {
			sum += n
		}
		return sum
	}
	slices.SortFunc(operators, func(a, b OperatorCounts) int {
		return cmp.Or(cmp.Compare(total(b), total(a)), cmp.Compare(a.Username, b.Username))
	})
}

// RestartedService is a service that was restarted from Harbormaster in the window.
type RestartedService struct {
	Key          model.ServiceKey
	TagID        int64
	TagInherited bool
	Restarts     int
	People       int
	LastAt       time.Time
}

// AnalyticsRestartedServices lists visible services restarted in [from, to), most restarts first.
func (s *Store) AnalyticsRestartedServices(ctx context.Context, f TargetFilter, vis Visibility, from, to time.Time) ([]RestartedService, error) {
	var q queryArgs
	target := q.target("o", f)
	v := q.visibility("o", vis)
	fromParam, toParam := q.add(from), q.add(to)
	sql := `select o.account_id, o.region, o.cluster_name, o.service_name, ` + v.Effective + `, bool_and(` + v.Inherited + `),
	    count(*), count(distinct o.actor_user_id), max(o.ts)
	  from operation_log o` + v.Joins + `
	  where o.source = 'harbormaster' and o.service_name is not null and ` + target + ` and ` + v.Predicate + `
	    and o.ts >= ` + fromParam + ` and o.ts < ` + toParam + `
	    and ` + opKindSQL + ` = 'restart'
	  group by o.account_id, o.region, o.cluster_name, o.service_name, 5
	  order by count(*) desc, max(o.ts) desc, o.cluster_name, o.service_name`

	rows, err := s.Pool.Query(ctx, sql, q.values...)
	if err != nil {
		return nil, fmt.Errorf("analytics restarted services: %w", err)
	}
	defer rows.Close()
	services := []RestartedService{}
	for rows.Next() {
		var r RestartedService
		key := &r.Key
		if err := rows.Scan(&key.AccountID, &key.Region, &key.Cluster, &key.Service, &r.TagID, &r.TagInherited,
			&r.Restarts, &r.People, &r.LastAt); err != nil {
			return nil, fmt.Errorf("scan restarted service: %w", err)
		}
		services = append(services, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics restarted services: %w", err)
	}
	return services, nil
}
