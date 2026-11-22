package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OAuthCode is a stored authorization code. CodeHash is sha256 of the code; the code itself is never stored.
type OAuthCode struct {
	CodeHash      string
	UserID        int64
	ClientID      string
	ClientName    string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	Resource      string
	AuthSource    string
	ExpiresAt     time.Time
}

// OAuthGrant is one approval of an OAuth client by a user. ExpiresAt is absolute: no token of the grant outlives it.
type OAuthGrant struct {
	ID         int64
	UserID     int64
	ClientID   string
	ClientName string
	Scope      string
	Resource   string
	AuthSource string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

var (
	// ErrRefreshRetry is returned by RotateOAuthRefresh for a refresh token that was rotated within the grace window:
	// most likely a client retrying after a lost answer. Nothing changes.
	ErrRefreshRetry = errors.New("refresh token already rotated")
	// ErrRefreshReused is returned by RotateOAuthRefresh for a refresh token rotated before the grace window: treated
	// as theft, so the whole grant (and every token of it) has been deleted.
	ErrRefreshReused = errors.New("refresh token reused; grant revoked")
)

const oauthGrantColumns = `id, user_id, client_id, client_name, scope, resource, auth_source, created_at, expires_at,
	last_used_at`

func scanOAuthGrant(row pgx.Row) (OAuthGrant, error) {
	var g OAuthGrant
	err := row.Scan(&g.ID, &g.UserID, &g.ClientID, &g.ClientName, &g.Scope, &g.Resource, &g.AuthSource,
		&g.CreatedAt, &g.ExpiresAt, &g.LastUsedAt)
	return g, err
}

// isForeignKeyViolation reports a write that referenced a missing row (here: a user deleted meanwhile).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// CreateOAuthCode stores an authorization code. An empty AuthSource is stored as "password". Returns ErrNotFound
// when the user is absent.
func (s *Store) CreateOAuthCode(ctx context.Context, c OAuthCode) error {
	_, err := s.Pool.Exec(ctx,
		`insert into oauth_codes (code_hash, user_id, client_id, client_name, redirect_uri, code_challenge, scope,
		   resource, auth_source, expires_at)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, coalesce(nullif($9, ''), 'password'), $10)`,
		c.CodeHash, c.UserID, c.ClientID, c.ClientName, c.RedirectURI, c.CodeChallenge, c.Scope, c.Resource,
		c.AuthSource, c.ExpiresAt)
	if isForeignKeyViolation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("insert oauth code: %w", err)
	}
	return nil
}

// ConsumeOAuthCode deletes the code with codeHash and returns it. The code is spent whatever the caller does next.
// Returns ErrNotFound when it is absent or expired (an expired code is deleted too).
func (s *Store) ConsumeOAuthCode(ctx context.Context, codeHash string) (OAuthCode, error) {
	var c OAuthCode
	var live bool
	err := s.Pool.QueryRow(ctx,
		`delete from oauth_codes where code_hash = $1
		 returning code_hash, user_id, client_id, client_name, redirect_uri, code_challenge, scope, resource,
		   auth_source, expires_at, expires_at > now()`, codeHash).
		Scan(&c.CodeHash, &c.UserID, &c.ClientID, &c.ClientName, &c.RedirectURI, &c.CodeChallenge, &c.Scope,
			&c.Resource, &c.AuthSource, &c.ExpiresAt, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthCode{}, ErrNotFound
	}
	if err != nil {
		return OAuthCode{}, fmt.Errorf("consume oauth code: %w", err)
	}
	if !live {
		return OAuthCode{}, ErrNotFound
	}
	return c, nil
}

// insertGrantToken inserts an OAuth token of grant g. The token takes the grant's user and auth source, and its
// expiry is capped at the grant's.
func insertGrantToken(ctx context.Context, tx pgx.Tx, g OAuthGrant, t APIToken) error {
	expiresAt := t.ExpiresAt
	if expiresAt.After(g.ExpiresAt) {
		expiresAt = g.ExpiresAt
	}
	_, err := tx.Exec(ctx,
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at, grant_id)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		t.PublicID, g.UserID, t.Kind, t.Name, t.SecretHash, t.Scope, g.AuthSource, expiresAt, g.ID)
	if err != nil {
		return fmt.Errorf("insert %s token: %w", t.Kind, err)
	}
	return nil
}

// CreateOAuthGrantWithTokens stores a grant and its first access and refresh tokens in one transaction. The tokens'
// UserID and AuthSource are taken from the grant and their expiry is capped at the grant's. An empty AuthSource is
// stored as "password". Returns ErrNotFound when the user is absent.
func (s *Store) CreateOAuthGrantWithTokens(ctx context.Context, g OAuthGrant, access, refresh APIToken) (OAuthGrant, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := scanOAuthGrant(tx.QueryRow(ctx,
		`insert into oauth_grants (user_id, client_id, client_name, scope, resource, auth_source, expires_at)
		 values ($1, $2, $3, $4, $5, coalesce(nullif($6, ''), 'password'), $7)
		 returning `+oauthGrantColumns,
		g.UserID, g.ClientID, g.ClientName, g.Scope, g.Resource, g.AuthSource, g.ExpiresAt))
	if isForeignKeyViolation(err) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("insert oauth grant: %w", err)
	}
	if err := insertGrantToken(ctx, tx, created, access); err != nil {
		return OAuthGrant{}, err
	}
	if err := insertGrantToken(ctx, tx, created, refresh); err != nil {
		return OAuthGrant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OAuthGrant{}, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// RotateOAuthRefresh exchanges a refresh token for a new access and refresh pair, in one transaction that locks the
// refresh token's row:
//   - unknown, not a refresh token, secretHash mismatch, or the token or its grant expired at now: ErrNotFound;
//   - already rotated within grace of now: ErrRefreshRetry, nothing changes;
//   - already rotated earlier: the grant and all its tokens are deleted, ErrRefreshReused;
//   - otherwise the old refresh token is marked rotated, the grant's access tokens are deleted and the new pair is
//     stored with expiries no later than the grant's.
func (s *Store) RotateOAuthRefresh(ctx context.Context, publicID, secretHash string, newAccess, newRefresh APIToken,
	now time.Time, grace time.Duration) (OAuthGrant, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		tokenID    int64
		kind       string
		storedHash string
		expiresAt  time.Time
		grantID    *int64
		rotatedAt  *time.Time
	)
	err = tx.QueryRow(ctx,
		`select id, kind, secret_hash, expires_at, grant_id, rotated_at from api_tokens where public_id = $1
		 for update`, publicID).Scan(&tokenID, &kind, &storedHash, &expiresAt, &grantID, &rotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("lock refresh token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(secretHash), []byte(storedHash)) != 1 {
		return OAuthGrant{}, ErrNotFound
	}
	if kind != "oauth_refresh" || grantID == nil || !now.Before(expiresAt) {
		return OAuthGrant{}, ErrNotFound
	}
	grant, err := scanOAuthGrant(tx.QueryRow(ctx,
		`select `+oauthGrantColumns+` from oauth_grants where id = $1 for update`, *grantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("lock oauth grant: %w", err)
	}
	if !now.Before(grant.ExpiresAt) {
		return OAuthGrant{}, ErrNotFound
	}

	if rotatedAt != nil {
		if now.Sub(*rotatedAt) <= grace {
			return OAuthGrant{}, ErrRefreshRetry
		}
		if _, err := tx.Exec(ctx, `delete from oauth_grants where id = $1`, grant.ID); err != nil {
			return OAuthGrant{}, fmt.Errorf("revoke reused grant: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return OAuthGrant{}, fmt.Errorf("commit: %w", err)
		}
		return grant, ErrRefreshReused
	}

	if _, err := tx.Exec(ctx, `update api_tokens set rotated_at = $2 where id = $1`, tokenID, now); err != nil {
		return OAuthGrant{}, fmt.Errorf("mark refresh token rotated: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`delete from api_tokens where grant_id = $1 and kind = 'oauth_access'`, grant.ID); err != nil {
		return OAuthGrant{}, fmt.Errorf("delete previous access tokens: %w", err)
	}
	if err := insertGrantToken(ctx, tx, grant, newAccess); err != nil {
		return OAuthGrant{}, err
	}
	if err := insertGrantToken(ctx, tx, grant, newRefresh); err != nil {
		return OAuthGrant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OAuthGrant{}, fmt.Errorf("commit: %w", err)
	}
	return grant, nil
}

// ListOAuthGrantsForUser returns the user's unexpired grants, newest first.
func (s *Store) ListOAuthGrantsForUser(ctx context.Context, userID int64) ([]OAuthGrant, error) {
	rows, err := s.Pool.Query(ctx,
		`select `+oauthGrantColumns+` from oauth_grants where user_id = $1 and expires_at >= now()
		 order by created_at desc, id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("list oauth grants: %w", err)
	}
	defer rows.Close()
	grants := []OAuthGrant{}
	for rows.Next() {
		g, err := scanOAuthGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan oauth grant: %w", err)
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// GetOAuthGrant returns one of the user's grants, or ErrNotFound.
func (s *Store) GetOAuthGrant(ctx context.Context, id, userID int64) (OAuthGrant, error) {
	g, err := scanOAuthGrant(s.Pool.QueryRow(ctx,
		`select `+oauthGrantColumns+` from oauth_grants where id = $1 and user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("get oauth grant: %w", err)
	}
	return g, nil
}

// DeleteOAuthGrant revokes one grant and, by cascade, all its tokens. Returns ErrNotFound when no grant with id
// belongs to userID.
func (s *Store) DeleteOAuthGrant(ctx context.Context, id, userID int64) error {
	return s.execExpectOne(ctx, `delete from oauth_grants where id = $1 and user_id = $2`, id, userID)
}
