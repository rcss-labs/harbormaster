package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEntry is one row of the audit log. A zero TS is stamped by the database; empty
// ActorUsername, Target and IP are stored as NULL and read back as "".
type AuditEntry struct {
	ID            int64
	TS            time.Time
	ActorUserID   *int64
	ActorUsername string
	Action        string
	Target        string
	Summary       string
	Details       map[string]any
	IP            string
}

// Audit appends an entry to the audit log.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	var ts *time.Time
	if !e.TS.IsZero() {
		ts = &e.TS
	}
	var details *string
	if e.Details != nil {
		encoded, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("encode audit details: %w", err)
		}
		text := string(encoded)
		details = &text
	}
	_, err := s.Pool.Exec(ctx,
		`insert into audit_log (ts, actor_user_id, actor_username, action, target, summary, details, ip)
		 values (coalesce($1, now()), $2, nullif($3, ''), $4, nullif($5, ''), $6, $7, nullif($8, ''))`,
		ts, e.ActorUserID, e.ActorUsername, e.Action, e.Target, e.Summary, details, e.IP)
	if err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// ListAudit returns one page of audit entries, newest first.
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEntry, error) {
	rows, err := s.Pool.Query(ctx,
		`select id, ts, actor_user_id, actor_username, action, target, summary, details, ip
		 from audit_log order by ts desc, id desc limit $1 offset $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var actorUsername, target, ip *string
		var details []byte
		if err := rows.Scan(&e.ID, &e.TS, &e.ActorUserID, &actorUsername, &e.Action, &target,
			&e.Summary, &details, &ip); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		e.ActorUsername = derefString(actorUsername)
		e.Target = derefString(target)
		e.IP = derefString(ip)
		if details != nil {
			if err := json.Unmarshal(details, &e.Details); err != nil {
				return nil, fmt.Errorf("decode audit details: %w", err)
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	return entries, nil
}

// DeleteAuditBefore removes audit entries older than t and returns how many were removed.
func (s *Store) DeleteAuditBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from audit_log where ts < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete audit: %w", err)
	}
	return tag.RowsAffected(), nil
}
