package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIToken is a stored access token. SecretHash is sha256 of the secret; the secret itself is never stored.
// PublicID is the display form (hmp_<id>, hmo_<id>, hmr_<id>) and is safe to log. GrantID is set exactly for the OAuth
// kinds; RotatedAt is set on a refresh token once it has been exchanged for a new pair.
type APIToken struct {
	ID         int64
	PublicID   string
	UserID     int64
	Kind       string
	Name       string
	SecretHash string
	Scope      string
	AuthSource string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	GrantID    *int64
	RotatedAt  *time.Time
}

// ErrTokenLimit is returned by CreateAPIToken when the user already holds the maximum number of unexpired tokens.
var ErrTokenLimit = errors.New("token limit reached")

const apiTokenColumns = `id, public_id, user_id, kind, name, secret_hash, scope, auth_source,
	created_at, expires_at, last_used_at, last_used_ip, grant_id, rotated_at`

func scanAPIToken(row pgx.Row) (APIToken, error) {
	var t APIToken
	var lastUsedIP *string
	err := row.Scan(&t.ID, &t.PublicID, &t.UserID, &t.Kind, &t.Name, &t.SecretHash, &t.Scope, &t.AuthSource,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastUsedIP, &t.GrantID, &t.RotatedAt)
	t.LastUsedIP = derefString(lastUsedIP)
	return t, err
}

// CreateAPIToken stores a new token and returns it with its id and creation time. In one transaction it locks the
// user's row (so concurrent creates serialise), counts the user's unexpired personal tokens and returns ErrTokenLimit
// when maxPerUser are already held. An empty AuthSource is stored as "password". Returns ErrNotFound when the user
// is absent.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, maxPerUser int) (APIToken, error) {
	return s.CreateAPITokenWithin(ctx, t, maxPerUser, nil)
}

// CreateAPITokenWithin is CreateAPIToken with a lifetime ceiling decided from the user's row as read under the lock:
// latestExpiry maps that fresh row (admin flag, SSO link) to the latest allowed expires_at, and a later requested
// expiry is cut back to it. This closes the race with a promotion or SSO link that commits after the caller loaded
// the user. A nil latestExpiry applies no ceiling.
func (s *Store) CreateAPITokenWithin(ctx context.Context, t APIToken, maxPerUser int, latestExpiry func(User) time.Time) (APIToken, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return APIToken{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := scanUser(tx.QueryRow(ctx, `select `+userColumns+` from users where id = $1 for update`, t.UserID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return APIToken{}, ErrNotFound
		}
		return APIToken{}, fmt.Errorf("lock user: %w", err)
	}
	if latestExpiry != nil {
		if ceiling := latestExpiry(locked); t.ExpiresAt.After(ceiling) {
			t.ExpiresAt = ceiling
		}
	}
	var held int
	if err := tx.QueryRow(ctx,
		`select count(*) from api_tokens where user_id = $1 and kind = 'pat' and expires_at >= now()`,
		t.UserID).Scan(&held); err != nil {
		return APIToken{}, fmt.Errorf("count tokens: %w", err)
	}
	if held >= maxPerUser {
		return APIToken{}, ErrTokenLimit
	}
	created, err := scanAPIToken(tx.QueryRow(ctx,
		`insert into api_tokens (public_id, user_id, kind, name, secret_hash, scope, auth_source, expires_at)
		 values ($1, $2, $3, $4, $5, $6, coalesce(nullif($7, ''), 'password'), $8)
		 returning `+apiTokenColumns,
		t.PublicID, t.UserID, t.Kind, t.Name, t.SecretHash, t.Scope, t.AuthSource, t.ExpiresAt))
	if err != nil {
		return APIToken{}, fmt.Errorf("insert token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, fmt.Errorf("commit: %w", err)
	}
	return created, nil
}

// GetAPITokenByPublicID returns the token with publicID, or ErrNotFound when it is missing or expired.
func (s *Store) GetAPITokenByPublicID(ctx context.Context, publicID string) (APIToken, error) {
	t, err := scanAPIToken(s.Pool.QueryRow(ctx,
		`select `+apiTokenColumns+` from api_tokens where public_id = $1 and expires_at >= now()`, publicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("get api token: %w", err)
	}
	return t, nil
}

// ListAPITokensForUser returns the user's unexpired personal tokens, newest first. OAuth tokens are listed through
// their grants (ListOAuthGrantsForUser).
func (s *Store) ListAPITokensForUser(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.Pool.Query(ctx,
		`select `+apiTokenColumns+` from api_tokens where user_id = $1 and kind = 'pat' and expires_at >= now()
		 order by created_at desc, id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	tokens := []APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DeleteAPIToken revokes one personal token. Returns ErrNotFound when no personal token with id belongs to userID; an
// OAuth token is revoked by deleting its grant.
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID int64) error {
	return s.execExpectOne(ctx, `delete from api_tokens where id = $1 and user_id = $2 and kind = 'pat'`, id, userID)
}

// DeleteAPITokensForUser revokes the user's tokens and OAuth grants created under authSource ("password" or "saml"),
// or all of them when authSource is empty. It returns how many tokens were removed (a grant's tokens included).
func (s *Store) DeleteAPITokensForUser(ctx context.Context, userID int64, authSource string) (int64, error) {
	return deleteAPITokensForUser(ctx, s.Pool, userID, authSource)
}

// deleteAPITokensForUser is DeleteAPITokensForUser on q, so a transaction that ends sessions ends tokens with them.
// One statement removes the grants and the tokens, so it is atomic even on the pool. OAuth tokens carry their grant's
// user and auth source, so the token delete matches them too.
func deleteAPITokensForUser(ctx context.Context, q execer, userID int64, authSource string) (int64, error) {
	tag, err := q.Exec(ctx,
		`with revoked_grants as (
		   delete from oauth_grants where user_id = $1 and ($2 = '' or auth_source = $2)
		 )
		 delete from api_tokens where user_id = $1 and ($2 = '' or auth_source = $2)`, userID, authSource)
	if err != nil {
		return 0, fmt.Errorf("delete api tokens for user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAPITokensBySource revokes every user's tokens and OAuth grants created under authSource.
func (s *Store) DeleteAPITokensBySource(ctx context.Context, authSource string) (int64, error) {
	return deleteAPITokensBySource(ctx, s.Pool, authSource)
}

func deleteAPITokensBySource(ctx context.Context, q execer, authSource string) (int64, error) {
	if authSource == "" {
		return 0, errors.New("delete api tokens by source: empty auth source")
	}
	tag, err := q.Exec(ctx,
		`with revoked_grants as (delete from oauth_grants where auth_source = $1)
		 delete from api_tokens where auth_source = $1`, authSource)
	if err != nil {
		return 0, fmt.Errorf("delete api tokens by source: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TouchAPIToken records the token's last use. Returns ErrNotFound if the token is absent.
func (s *Store) TouchAPIToken(ctx context.Context, id int64, at time.Time, ip string) error {
	return s.execExpectOne(ctx,
		`update api_tokens set last_used_at = $2, last_used_ip = nullif($3, '') where id = $1`, id, at, ip)
}

// DeleteExpiredAPITokens removes tokens that expired before the given time and returns how many were removed.
func (s *Store) DeleteExpiredAPITokens(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from api_tokens where expires_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired api tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountAPITokensByUser returns the number of unexpired personal tokens plus unexpired OAuth grants per user, for users
// that hold at least one.
func (s *Store) CountAPITokensByUser(ctx context.Context) (map[int64]int, error) {
	rows, err := s.Pool.Query(ctx,
		`select user_id, count(*) from (
		   select user_id from api_tokens where kind = 'pat' and expires_at >= now()
		   union all
		   select user_id from oauth_grants where expires_at >= now()
		 ) held group by user_id`)
	if err != nil {
		return nil, fmt.Errorf("count api tokens by user: %w", err)
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var userID int64
		var n int
		if err := rows.Scan(&userID, &n); err != nil {
			return nil, fmt.Errorf("scan token count: %w", err)
		}
		counts[userID] = n
	}
	return counts, rows.Err()
}
