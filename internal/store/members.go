package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Member is one running process in poll_members.
type Member struct {
	InstanceID             uuid.UUID
	Roles                  []string
	Version                string
	StartedAt, HeartbeatAt time.Time
	Slices, Weight         int
	FocusObjects           int
}

// HeartbeatMember inserts or updates the member's row and stamps heartbeat_at with the database clock.
// started_at is set on insert only.
func (s *Store) HeartbeatMember(ctx context.Context, m Member) error {
	roles := m.Roles
	if roles == nil {
		roles = []string{}
	}
	var startedAt *time.Time
	if !m.StartedAt.IsZero() {
		startedAt = &m.StartedAt
	}
	_, err := s.Pool.Exec(ctx,
		`insert into poll_members (instance_id, roles, version, started_at, heartbeat_at, slices, weight, focus_objects)
		 values ($1, $2, $3, coalesce($4, now()), now(), $5, $6, $7)
		 on conflict (instance_id) do update set roles = excluded.roles, version = excluded.version,
		   heartbeat_at = now(), slices = excluded.slices, weight = excluded.weight, focus_objects = excluded.focus_objects`,
		m.InstanceID, roles, m.Version, startedAt, m.Slices, m.Weight, m.FocusObjects)
	if err != nil {
		return fmt.Errorf("heartbeat member: %w", err)
	}
	return nil
}

// LiveMembers returns the members whose heartbeat is newer than window, oldest process first.
func (s *Store) LiveMembers(ctx context.Context, window time.Duration) ([]Member, error) {
	rows, err := s.Pool.Query(ctx,
		`select instance_id, roles, version, started_at, heartbeat_at, slices, weight, focus_objects
		 from poll_members where heartbeat_at > now() - $1 * interval '1 millisecond'
		 order by started_at, instance_id`, window.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("live members: %w", err)
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.InstanceID, &m.Roles, &m.Version, &m.StartedAt, &m.HeartbeatAt,
			&m.Slices, &m.Weight, &m.FocusObjects); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMember removes a member row; a missing row is not an error.
func (s *Store) DeleteMember(ctx context.Context, id uuid.UUID) error {
	if _, err := s.Pool.Exec(ctx, `delete from poll_members where instance_id = $1`, id); err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	return nil
}

// DeleteStaleMembers removes members that have not beaten for olderThan and returns how many.
func (s *Store) DeleteStaleMembers(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx,
		`delete from poll_members where heartbeat_at < now() - $1 * interval '1 millisecond'`, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("delete stale members: %w", err)
	}
	return tag.RowsAffected(), nil
}
