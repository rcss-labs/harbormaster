package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is a server-side login session. IDHash is the hash of the cookie value; the raw ID is never stored.
type Session struct {
	IDHash     string
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.Pool.Exec(ctx,
		`insert into sessions (id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent)
		 values ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''))`,
		sess.IDHash, sess.UserID, sess.CSRFToken, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt,
		sess.IP, sess.UserAgent)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession returns the session for idHash. Expired or missing sessions return ErrNotFound.
func (s *Store) GetSession(ctx context.Context, idHash string) (Session, error) {
	var sess Session
	var ip, userAgent *string
	err := s.Pool.QueryRow(ctx,
		`select id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent
		 from sessions where id_hash = $1 and expires_at >= now()`, idHash).
		Scan(&sess.IDHash, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.LastSeenAt,
			&sess.ExpiresAt, &ip, &userAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	sess.IP = derefString(ip)
	sess.UserAgent = derefString(userAgent)
	return sess, nil
}

// TouchSession records activity and extends expiry. Returns ErrNotFound if the session is absent.
func (s *Store) TouchSession(ctx context.Context, idHash string, lastSeen, expires time.Time) error {
	return s.execExpectOne(ctx,
		`update sessions set last_seen_at = $2, expires_at = $3 where id_hash = $1`,
		idHash, lastSeen, expires)
}

// DeleteSession removes one session. Deleting an absent session is not an error.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.Pool.Exec(ctx, `delete from sessions where id_hash = $1`, idHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteSessionsForUser removes every session belonging to userID.
func (s *Store) DeleteSessionsForUser(ctx context.Context, userID int64) error {
	_, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete sessions for user: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions whose expiry has passed and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from sessions where expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DeleteSessionsForUserExcept removes every session of userID except the one with keepIDHash.
func (s *Store) DeleteSessionsForUserExcept(ctx context.Context, userID int64, keepIDHash string) error {
	_, err := s.Pool.Exec(ctx, `delete from sessions where user_id = $1 and id_hash <> $2`, userID, keepIDHash)
	if err != nil {
		return fmt.Errorf("delete other sessions for user: %w", err)
	}
	return nil
}
