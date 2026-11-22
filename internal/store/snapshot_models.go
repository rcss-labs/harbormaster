package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// RunningTasksIn is RunningTasks narrowed to one cluster ("" means every cluster of the target).
func (s *Store) RunningTasksIn(ctx context.Context, f TargetFilter, cluster string, vis Visibility) ([]TaskRow, error) {
	var q queryArgs
	target := q.target("t", f)
	v := q.visibility("t", vis)
	where := target + ` and ` + v.Predicate + ` and ` + runningSQL
	if cluster != "" {
		where += ` and t.cluster_name = ` + q.add(cluster)
	}
	sql := `select ` + taskColumns("t", true) + `, ` + v.Effective + `, ` + coalesceBool("ts", "enable_execute_command") +
		taskFrom(v) + ` where ` + where + ` order by t.account_id, t.region, t.cluster_name, t.task_arn`
	return s.queryTaskRows(ctx, sql, q.values)
}

// EventStamp is what the pages read from a service's events: the time of the event the newest-first order puts first
// (zero when that event has no time) and the newest time of any event (HasMax is false when none has one).
type EventStamp struct {
	Key    model.ServiceKey
	First  time.Time
	Max    time.Time
	HasMax bool
}

// ServiceEventStamps returns one stamp per service that has events, within the target and, when cluster is set, that
// cluster. It applies no visibility; the snapshot cache filters per request.
func (s *Store) ServiceEventStamps(ctx context.Context, f TargetFilter, cluster string) ([]EventStamp, error) {
	var q queryArgs
	where := q.target("e", f)
	if cluster != "" {
		where += ` and e.cluster_name = ` + q.add(cluster)
	}
	rows, err := s.Pool.Query(ctx,
		`select e.account_id, e.region, e.cluster_name, e.service_name,
		   (array_agg(e.created_at order by e.created_at desc, e.event_id))[1], max(e.created_at)
		 from service_events e where `+where+`
		 group by 1, 2, 3, 4 order by 1, 2, 3, 4`, q.values...)
	if err != nil {
		return nil, fmt.Errorf("service event stamps: %w", err)
	}
	defer rows.Close()
	stamps := []EventStamp{}
	for rows.Next() {
		var st EventStamp
		var first, newest *time.Time
		if err := rows.Scan(&st.Key.AccountID, &st.Key.Region, &st.Key.Cluster, &st.Key.Service, &first, &newest); err != nil {
			return nil, fmt.Errorf("scan service event stamp: %w", err)
		}
		nullable(first, &st.First)
		if newest != nil {
			st.Max, st.HasMax = *newest, true
		}
		stamps = append(stamps, st)
	}
	return stamps, rows.Err()
}

// TargetKeys lists the account x region pairs that hold a cluster or service row, in order. The list pages read
// nothing outside them.
func (s *Store) TargetKeys(ctx context.Context) ([]TargetFilter, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region from clusters union select account_id, region from services order by 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("target keys: %w", err)
	}
	defer rows.Close()
	var out []TargetFilter
	for rows.Next() {
		var f TargetFilter
		if err := rows.Scan(&f.AccountID, &f.Region); err != nil {
			return nil, fmt.Errorf("scan target key: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
