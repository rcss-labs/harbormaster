package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// failingThreshold is the consecutive-failure count at which a poll loop counts as failing.
const failingThreshold = 3

// PollLoop is the status of one polling loop (an account, region and tier).
// Interval is the current period and BaseInterval the normal one; a longer
// current interval means the loop is throttled.
type PollLoop struct {
	Target       awsx.Target
	Tier         string
	LastOKAt     *time.Time
	LastError    string
	Interval     time.Duration
	BaseInterval time.Duration
	Failures     int
	UpdatedAt    time.Time
}

// Throttled reports whether the loop is polling less often than its base interval.
func (p PollLoop) Throttled() bool { return p.Interval > p.BaseInterval }

// Failing reports whether the loop has failed too many times in a row.
func (p PollLoop) Failing() bool { return p.Failures >= failingThreshold }

// UpsertPollLoop stores a loop's status, replacing any previous status for the same
// account, region and tier. A zero UpdatedAt stores the current time.
func (s *Store) UpsertPollLoop(ctx context.Context, p PollLoop) error {
	var updatedAt *time.Time
	if !p.UpdatedAt.IsZero() {
		updatedAt = &p.UpdatedAt
	}
	_, err := s.Pool.Exec(ctx,
		`insert into poll_loops (account_id, region, tier, last_ok_at, last_error, interval_ms,
		   base_interval_ms, failures, updated_at)
		 values ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, coalesce($9, now()))
		 on conflict (account_id, region, tier) do update set
		   last_ok_at = excluded.last_ok_at, last_error = excluded.last_error,
		   interval_ms = excluded.interval_ms, base_interval_ms = excluded.base_interval_ms,
		   failures = excluded.failures, updated_at = excluded.updated_at`,
		p.Target.AccountID, p.Target.Region, p.Tier, p.LastOKAt, p.LastError,
		p.Interval.Milliseconds(), p.BaseInterval.Milliseconds(), p.Failures, updatedAt)
	if err != nil {
		return fmt.Errorf("upsert poll loop: %w", err)
	}
	return nil
}

// ListPollLoops returns every poll loop ordered by account, region and tier.
func (s *Store) ListPollLoops(ctx context.Context) ([]PollLoop, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, tier, last_ok_at, coalesce(last_error, ''), interval_ms,
		   base_interval_ms, failures, updated_at
		 from poll_loops order by account_id, region, tier`)
	if err != nil {
		return nil, fmt.Errorf("list poll loops: %w", err)
	}
	defer rows.Close()
	loops := []PollLoop{}
	for rows.Next() {
		var p PollLoop
		var intervalMS, baseIntervalMS int64
		if err := rows.Scan(&p.Target.AccountID, &p.Target.Region, &p.Tier, &p.LastOKAt, &p.LastError,
			&intervalMS, &baseIntervalMS, &p.Failures, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan poll loop: %w", err)
		}
		p.Interval = time.Duration(intervalMS) * time.Millisecond
		p.BaseInterval = time.Duration(baseIntervalMS) * time.Millisecond
		loops = append(loops, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list poll loops: %w", err)
	}
	return loops, nil
}

// PolledAt returns the oldest successful services-tier poll among the targets
// matching f, which is when the data is stale up to. Zero when none has succeeded.
func (s *Store) PolledAt(ctx context.Context, f TargetFilter) (time.Time, error) {
	var q queryArgs
	where := q.target("p", f)
	var oldest *time.Time
	err := s.Pool.QueryRow(ctx,
		`select min(p.last_ok_at) from poll_loops p where p.tier = 'services' and `+where,
		q.values...).Scan(&oldest)
	if err != nil {
		return time.Time{}, fmt.Errorf("polled at: %w", err)
	}
	if oldest == nil {
		return time.Time{}, nil
	}
	return *oldest, nil
}
