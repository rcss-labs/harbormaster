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

// insertGrantToken inserts an OAuth token of grant g. The token takes the grant's user and auth source, its expiry is
// capped at the grant's, and an empty name becomes the grant's client name.
func insertGrantToken(ctx context.Context, tx pgx.Tx, g OAuthGrant, t APIToken) error {
	expiresAt := t.ExpiresAt
	if expiresAt.After(g.ExpiresAt) {
		expiresAt = g.ExpiresAt
	}
	if t.Name == "" {
		t.Name = g.ClientName
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
	return s.CreateOAuthGrantWithTokensWithin(ctx, g, access, refresh, nil)
}

// CreateOAuthGrantWithTokensWithin is CreateOAuthGrantWithTokens with a lifetime ceiling decided from the user's row
// as read under the lock: latestEnd maps that fresh row (admin flag, SSO link) to the latest allowed grant end, and a
// later requested end is cut back to it (the tokens follow, being capped at the grant's). This closes the race with a
// promotion or SSO link that commits after the caller loaded the user. A nil latestEnd applies no ceiling.
func (s *Store) CreateOAuthGrantWithTokensWithin(ctx context.Context, g OAuthGrant, access, refresh APIToken, latestEnd func(User) time.Time) (OAuthGrant, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := scanUser(tx.QueryRow(ctx, `select `+userColumns+` from users where id = $1 for update`, g.UserID))
	if errors.Is(err, ErrNotFound) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("lock user: %w", err)
	}
	if latestEnd != nil {
		if ceiling := latestEnd(locked); g.ExpiresAt.After(ceiling) {
			g.ExpiresAt = ceiling
		}
	}
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
	return s.RotateOAuthRefreshWithin(ctx, publicID, secretHash, newAccess, newRefresh, now, grace, nil)
}

// RotateOAuthRefreshWithin is RotateOAuthRefresh with a lifetime ceiling from the grant user's current state: the
// grant may not run past its created_at plus maxLifetime(user), so a user who has since become an admin or SSO-linked
// cannot keep refreshing under the longer cap they were approved with. Past that ceiling the token is ErrNotFound; a
// nil maxLifetime applies none. The returned grant carries the clamped end.
func (s *Store) RotateOAuthRefreshWithin(ctx context.Context, publicID, secretHash string, newAccess, newRefresh APIToken,
	now time.Time, grace time.Duration, maxLifetime func(User) time.Duration) (OAuthGrant, error) {
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
	if maxLifetime != nil {
		// Read without a lock: taking the user's lock after the grant's would invert the order revocation uses.
		current, err := scanUser(tx.QueryRow(ctx, `select `+userColumns+` from users where id = $1`, grant.UserID))
		if errors.Is(err, ErrNotFound) {
			return OAuthGrant{}, ErrNotFound
		}
		if err != nil {
			return OAuthGrant{}, fmt.Errorf("load user: %w", err)
		}
		if ceiling := grant.CreatedAt.Add(maxLifetime(current)); grant.ExpiresAt.After(ceiling) {
			grant.ExpiresAt = ceiling
		}
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

// TouchOAuthGrant records the grant's last use. A missing grant is not an error: its tokens are gone with it.
func (s *Store) TouchOAuthGrant(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.Pool.Exec(ctx, `update oauth_grants set last_used_at = $2 where id = $1`, id, at); err != nil {
		return fmt.Errorf("touch oauth grant: %w", err)
	}
	return nil
}

// oauthCodeRetention and oauthGrantRetention are fixed: an expired code is useless at once and kept 10 minutes; an
// expired grant (and, by cascade, any token of it) is kept a day, like an expired access token.
const (
	oauthCodeRetention  = 10 * time.Minute
	oauthGrantRetention = 24 * time.Hour
	// rotatedRefreshRetention must stay above the reuse grace window (60 s): inside it a replay is a retry.
	rotatedRefreshRetention = 5 * time.Minute
)

// DeleteExpiredOAuth removes codes expired over 10 minutes before now and grants expired over a day before now,
// and rotated refresh tokens rotated over 5 minutes before now (the latest rotated one of each grant stays, so a
// replay of it is still recognised as reuse), returning how many rows were removed.
func (s *Store) DeleteExpiredOAuth(ctx context.Context, now time.Time) (int64, error) {
	codes, err := s.Pool.Exec(ctx, `delete from oauth_codes where expires_at < $1`, now.Add(-oauthCodeRetention))
	if err != nil {
		return 0, fmt.Errorf("delete expired oauth codes: %w", err)
	}
	grants, err := s.Pool.Exec(ctx, `delete from oauth_grants where expires_at < $1`, now.Add(-oauthGrantRetention))
	if err != nil {
		return codes.RowsAffected(), fmt.Errorf("delete expired oauth grants: %w", err)
	}
	rotated, err := s.Pool.Exec(ctx,
		`delete from api_tokens t where t.kind = 'oauth_refresh' and t.rotated_at < $1
		   and exists (select 1 from api_tokens newer where newer.grant_id = t.grant_id and newer.kind = 'oauth_refresh'
		               and newer.rotated_at is not null and (newer.rotated_at, newer.id) > (t.rotated_at, t.id))`,
		now.Add(-rotatedRefreshRetention))
	if err != nil {
		return codes.RowsAffected() + grants.RowsAffected(), fmt.Errorf("delete rotated refresh tokens: %w", err)
	}
	return codes.RowsAffected() + grants.RowsAffected() + rotated.RowsAffected(), nil
}
