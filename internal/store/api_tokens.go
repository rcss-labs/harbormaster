package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIToken is a stored access token. SecretHash is sha256 of the secret; the secret itself is never stored.
// PublicID is the display form (hmp_<id>) and is safe to log.
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
}

// ErrTokenLimit is returned by CreateAPIToken when the user already holds the maximum number of unexpired tokens.
var ErrTokenLimit = errors.New("token limit reached")

const apiTokenColumns = `id, public_id, user_id, kind, name, secret_hash, scope, auth_source,
	created_at, expires_at, last_used_at, last_used_ip`

func scanAPIToken(row pgx.Row) (APIToken, error) {
	var t APIToken
	var lastUsedIP *string
	err := row.Scan(&t.ID, &t.PublicID, &t.UserID, &t.Kind, &t.Name, &t.SecretHash, &t.Scope, &t.AuthSource,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastUsedIP)
	t.LastUsedIP = derefString(lastUsedIP)
	return t, err
}

// CreateAPIToken stores a new token and returns it with its id and creation time. In one transaction it locks the
// user's row (so concurrent creates serialise), counts the user's unexpired personal tokens and returns ErrTokenLimit
// when maxPerUser are already held. An empty AuthSource is stored as "password". Returns ErrNotFound when the user
// is absent.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, maxPerUser int) (APIToken, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return APIToken{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedID int64
	if err := tx.QueryRow(ctx, `select id from users where id = $1 for update`, t.UserID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIToken{}, ErrNotFound
		}
		return APIToken{}, fmt.Errorf("lock user: %w", err)
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

// ListAPITokensForUser returns the user's unexpired tokens, newest first.
func (s *Store) ListAPITokensForUser(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.Pool.Query(ctx,
		`select `+apiTokenColumns+` from api_tokens where user_id = $1 and expires_at >= now()
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

// DeleteAPIToken revokes one token. Returns ErrNotFound when no token with id belongs to userID.
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID int64) error {
	return s.execExpectOne(ctx, `delete from api_tokens where id = $1 and user_id = $2`, id, userID)
}

// DeleteAPITokensForUser revokes the user's tokens created under authSource ("password" or "saml"), or all of the
// user's tokens when authSource is empty. It returns how many were removed.
func (s *Store) DeleteAPITokensForUser(ctx context.Context, userID int64, authSource string) (int64, error) {
	return deleteAPITokensForUser(ctx, s.Pool, userID, authSource)
}

// deleteAPITokensForUser is DeleteAPITokensForUser on q, so a transaction that ends sessions ends tokens with them.
func deleteAPITokensForUser(ctx context.Context, q execer, userID int64, authSource string) (int64, error) {
	tag, err := q.Exec(ctx,
		`delete from api_tokens where user_id = $1 and ($2 = '' or auth_source = $2)`, userID, authSource)
	if err != nil {
		return 0, fmt.Errorf("delete api tokens for user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAPITokensBySource revokes every user's tokens created under authSource.
func (s *Store) DeleteAPITokensBySource(ctx context.Context, authSource string) (int64, error) {
	return deleteAPITokensBySource(ctx, s.Pool, authSource)
}

func deleteAPITokensBySource(ctx context.Context, q execer, authSource string) (int64, error) {
	if authSource == "" {
		return 0, errors.New("delete api tokens by source: empty auth source")
	}
	tag, err := q.Exec(ctx, `delete from api_tokens where auth_source = $1`, authSource)
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
