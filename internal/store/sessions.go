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
	// AuthSource is "password" (the default when empty) or "saml".
	AuthSource string
	// NotAfter is the IdP's SessionNotOnOrAfter, when it sent one.
	NotAfter *time.Time
}

// CreateSession stores a new session. Only a human user can hold one: the row is selected from the user's, so the agent
// identity, an unknown kind or an absent user store nothing and return ErrNotFound.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	tag, err := s.Pool.Exec(ctx,
		`insert into sessions (id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent, auth_source, not_after)
		 select $1, id, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''), coalesce(nullif($9, ''), 'password'), $10
		 from users where id = $2 and kind = 'human'`,
		sess.IDHash, sess.UserID, sess.CSRFToken, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt,
		sess.IP, sess.UserAgent, sess.AuthSource, sess.NotAfter)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetSession returns the session for idHash. Expired or missing sessions return ErrNotFound.
func (s *Store) GetSession(ctx context.Context, idHash string) (Session, error) {
	var sess Session
	var ip, userAgent *string
	err := s.Pool.QueryRow(ctx,
		`select id_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent, auth_source, not_after
		 from sessions where id_hash = $1 and expires_at >= now()`, idHash).
		Scan(&sess.IDHash, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.LastSeenAt,
			&sess.ExpiresAt, &ip, &userAgent, &sess.AuthSource, &sess.NotAfter)
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

// LastSessionActivity returns the most recent sign of a person or agent using Harbormaster: the newer of the
// latest last_seen_at over unexpired sessions and the latest last_used_at over unexpired access tokens (owner's
// decision 15: a token call wakes polling like a UI session), but only a person's: a token of the agent identity is a background
// run and must not wake or keep AWS polling awake (ruling P2-R18). It returns false when neither exists. Authenticated
// requests refresh both at most once a minute, so it lags real activity by up to that.
func (s *Store) LastSessionActivity(ctx context.Context, now time.Time) (time.Time, bool, error) {
	var last *time.Time
	err := s.Pool.QueryRow(ctx,
		`select max(seen) from (
		   select max(last_seen_at) as seen from sessions where expires_at >= $1
		   union all
		   select max(t.last_used_at) as seen from api_tokens t join users u on u.id = t.user_id
		   where t.expires_at >= $1 and u.kind = 'human'
		 ) activity`, now).Scan(&last)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("last session activity: %w", err)
	}
	if last == nil {
		return time.Time{}, false, nil
	}
	return *last, true, nil
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
