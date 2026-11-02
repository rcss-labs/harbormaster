package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoBreakGlassAdmin is returned when SSO would be enabled without a break-glass admin.
var ErrNoBreakGlassAdmin = errors.New("no break-glass admin")

// SAMLConfigRow is the single saml_config row.
type SAMLConfigRow struct {
	Enabled                                       bool
	IdPEntityID, IdPSSORedirectURL, IdPSSOPostURL string
	IdPCertsPEM                                   []string
	MetadataURL, MetadataXML                      *string
	MetadataFetchedAt                             *time.Time
	SPEntityID, NameIDFormat                      string
	AttrEmail, AttrDisplayName, AttrUsername      *string
	ClockSkewSeconds                              int
	ButtonLabel                                   string
	UpdatedAt                                     time.Time
	UpdatedByUserID                               *int64
}

// SAMLConfigChange reports the side effects of a configuration write.
type SAMLConfigChange struct {
	SessionsEnded      int64
	IdentitiesDetached int64
}

const samlConfigColumns = `enabled, idp_entity_id, idp_sso_redirect_url, coalesce(idp_sso_post_url, ''), idp_certs,
	metadata_url, metadata_xml, metadata_fetched_at, sp_entity_id, name_id_format,
	attr_email, attr_display_name, attr_username, clock_skew_seconds, button_label, updated_at, updated_by_user_id`

type samlRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadSAMLConfig(ctx context.Context, q samlRowQuerier, lock bool) (SAMLConfigRow, error) {
	query := `select ` + samlConfigColumns + ` from saml_config where id = 1`
	if lock {
		query += ` for update`
	}
	var c SAMLConfigRow
	err := q.QueryRow(ctx, query).Scan(&c.Enabled, &c.IdPEntityID, &c.IdPSSORedirectURL, &c.IdPSSOPostURL, &c.IdPCertsPEM,
		&c.MetadataURL, &c.MetadataXML, &c.MetadataFetchedAt, &c.SPEntityID, &c.NameIDFormat,
		&c.AttrEmail, &c.AttrDisplayName, &c.AttrUsername, &c.ClockSkewSeconds, &c.ButtonLabel, &c.UpdatedAt, &c.UpdatedByUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SAMLConfigRow{}, ErrNotFound
	}
	if err != nil {
		return SAMLConfigRow{}, fmt.Errorf("load saml config: %w", err)
	}
	return c, nil
}

// GetSAMLConfig returns the SAML configuration, or ErrNotFound when none was saved.
func (s *Store) GetSAMLConfig(ctx context.Context) (SAMLConfigRow, error) {
	return loadSAMLConfig(ctx, s.Pool, false)
}

// SAMLEnabled reports whether SSO is enabled; false when unconfigured.
func (s *Store) SAMLEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	if err := s.Pool.QueryRow(ctx, `select coalesce((select enabled from saml_config where id = 1), false)`).Scan(&enabled); err != nil {
		return false, fmt.Errorf("read saml enabled: %w", err)
	}
	return enabled, nil
}

// certFingerprint identifies a certificate by its DER hash, falling back to the trimmed text when
// the value is not parseable PEM, so re-wrapped PEM of the same certificate compares equal.
func certFingerprint(p string) string {
	if block, _ := pem.Decode([]byte(p)); block != nil {
		sum := sha256.Sum256(block.Bytes)
		return hex.EncodeToString(sum[:])
	}
	return strings.TrimSpace(p)
}

func certsDropped(old, updated []string) bool {
	kept := map[string]bool{}
	for _, c := range updated {
		kept[certFingerprint(c)] = true
	}
	for _, c := range old {
		if !kept[certFingerprint(c)] {
			return true
		}
	}
	return false
}

// UpsertSAMLConfig writes the configuration in one transaction. It never changes `enabled` (only
// SetSAMLEnabled, which has the lockout guard, does). It ends every SAML session when the IdP entity
// ID changes or a previously stored certificate is dropped. An entity ID change also clears every
// pending link and detaches the SSO identity of users who have a password; identities of
// password-less users are kept (they cannot match under the new entity ID, and an admin recovers
// them by unlinking, which sets a temporary password). Passwords are never touched.
func (s *Store) UpsertSAMLConfig(ctx context.Context, row SAMLConfigRow) (SAMLConfigChange, error) {
	var change SAMLConfigChange
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return change, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	old, err := loadSAMLConfig(ctx, tx, true)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return change, err
	}
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	certs := row.IdPCertsPEM
	if certs == nil {
		certs = []string{}
	}
	_, err = tx.Exec(ctx, `insert into saml_config
		(id, enabled, idp_entity_id, idp_sso_redirect_url, idp_sso_post_url, idp_certs, metadata_url, metadata_xml,
		 metadata_fetched_at, sp_entity_id, name_id_format, attr_email, attr_display_name, attr_username,
		 clock_skew_seconds, button_label, updated_at, updated_by_user_id)
		values (1, false, $1, $2, nullif($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		on conflict (id) do update set
		 idp_entity_id = excluded.idp_entity_id, idp_sso_redirect_url = excluded.idp_sso_redirect_url,
		 idp_sso_post_url = excluded.idp_sso_post_url, idp_certs = excluded.idp_certs,
		 metadata_url = excluded.metadata_url, metadata_xml = excluded.metadata_xml,
		 metadata_fetched_at = excluded.metadata_fetched_at, sp_entity_id = excluded.sp_entity_id,
		 name_id_format = excluded.name_id_format, attr_email = excluded.attr_email,
		 attr_display_name = excluded.attr_display_name, attr_username = excluded.attr_username,
		 clock_skew_seconds = excluded.clock_skew_seconds, button_label = excluded.button_label,
		 updated_at = excluded.updated_at, updated_by_user_id = excluded.updated_by_user_id`,
		row.IdPEntityID, row.IdPSSORedirectURL, row.IdPSSOPostURL, certs, row.MetadataURL, row.MetadataXML,
		row.MetadataFetchedAt, row.SPEntityID, row.NameIDFormat, row.AttrEmail, row.AttrDisplayName, row.AttrUsername,
		row.ClockSkewSeconds, row.ButtonLabel, updatedAt, row.UpdatedByUserID)
	if err != nil {
		return change, fmt.Errorf("upsert saml config: %w", err)
	}

	entityChanged := exists && old.IdPEntityID != row.IdPEntityID
	if entityChanged || (exists && certsDropped(old.IdPCertsPEM, certs)) {
		tag, err := tx.Exec(ctx, `delete from sessions where auth_source = 'saml'`)
		if err != nil {
			return change, fmt.Errorf("end saml sessions: %w", err)
		}
		change.SessionsEnded = tag.RowsAffected()
	}
	if entityChanged {
		// Pending links are scoped to the old IdP, so all are cleared. Identities are detached only
		// for users with a password: a password-less user keeps theirs (users_has_credential), which
		// is scoped to the old entity ID and so can never match a sign-in under the new one.
		tag, err := tx.Exec(ctx, `update users set pending_link_name_id = null, pending_link_set_at = null,
			pending_link_expires_at = null, pending_link_set_by_user_id = null
			where pending_link_name_id is not null`)
		if err != nil {
			return change, fmt.Errorf("clear pending links: %w", err)
		}
		change.IdentitiesDetached = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `update users set idp_entity_id = null, external_id = null, external_id_format = null
			where external_id is not null and password_hash is not null`)
		if err != nil {
			return change, fmt.Errorf("detach identities: %w", err)
		}
		change.IdentitiesDetached += tag.RowsAffected()
	}
	return change, tx.Commit(ctx)
}

// SetSAMLEnabled turns SSO on or off. Enabling takes the admin row locks first and then requires at
// least one break-glass admin in the same transaction (ErrNoBreakGlassAdmin), so it serialises with
// every guarded admin write. Disabling an enabled configuration ends all SAML sessions. It never
// touches users or passwords. ErrNotFound when SSO is unconfigured.
func (s *Store) SetSAMLEnabled(ctx context.Context, enabled bool) (SAMLConfigChange, error) {
	var change SAMLConfigChange
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return change, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockAdminsTx(ctx, tx); err != nil {
		return change, err
	}
	var current bool
	if err := tx.QueryRow(ctx, `select enabled from saml_config where id = 1 for update`).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return change, ErrNotFound
		}
		return change, fmt.Errorf("load saml enabled: %w", err)
	}
	if current == enabled {
		return change, tx.Commit(ctx)
	}
	if enabled {
		bg, err := breakGlassAdminsTx(ctx, tx)
		if err != nil {
			return change, err
		}
		if len(bg) == 0 {
			return change, ErrNoBreakGlassAdmin
		}
	}
	if _, err := tx.Exec(ctx, `update saml_config set enabled = $1 where id = 1`, enabled); err != nil {
		return change, fmt.Errorf("set saml enabled: %w", err)
	}
	if !enabled {
		tag, err := tx.Exec(ctx, `delete from sessions where auth_source = 'saml'`)
		if err != nil {
			return change, fmt.Errorf("end saml sessions: %w", err)
		}
		change.SessionsEnded = tag.RowsAffected()
	}
	return change, tx.Commit(ctx)
}

// SPKeyRow is a stored service-provider key pair; KeyEnc is the encrypted private key.
type SPKeyRow struct {
	ID        int64
	CertPEM   string
	KeyEnc    []byte
	CreatedAt time.Time
	RetiredAt *time.Time
}

// InsertSPKey retires every active key and inserts the new one in one transaction.
func (s *Store) InsertSPKey(ctx context.Context, certPEM string, keyEnc []byte) (SPKeyRow, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SPKeyRow{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `update saml_sp_keys set retired_at = now() where retired_at is null`); err != nil {
		return SPKeyRow{}, fmt.Errorf("retire sp keys: %w", err)
	}
	var k SPKeyRow
	err = tx.QueryRow(ctx, `insert into saml_sp_keys (cert_pem, key_pem_enc) values ($1, $2)
		returning id, cert_pem, key_pem_enc, created_at, retired_at`, certPEM, keyEnc).
		Scan(&k.ID, &k.CertPEM, &k.KeyEnc, &k.CreatedAt, &k.RetiredAt)
	if err != nil {
		return SPKeyRow{}, fmt.Errorf("insert sp key: %w", err)
	}
	return k, tx.Commit(ctx)
}

// ListSPKeys returns the active key first, then retired keys newest first.
func (s *Store) ListSPKeys(ctx context.Context) ([]SPKeyRow, error) {
	rows, err := s.Pool.Query(ctx, `select id, cert_pem, key_pem_enc, created_at, retired_at from saml_sp_keys
		order by (retired_at is not null), coalesce(retired_at, created_at) desc, id desc`)
	if err != nil {
		return nil, fmt.Errorf("list sp keys: %w", err)
	}
	defer rows.Close()
	var out []SPKeyRow
	for rows.Next() {
		var k SPKeyRow
		if err := rows.Scan(&k.ID, &k.CertPEM, &k.KeyEnc, &k.CreatedAt, &k.RetiredAt); err != nil {
			return nil, fmt.Errorf("scan sp key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SAMLRequest is one in-flight sign-in. Purpose is "login", "test" or "link"; UserID is set only for "link".
type SAMLRequest struct {
	ID, Purpose, ReturnTo, BindingHash string
	UserID                             *int64
	CreatedAt, ExpiresAt               time.Time
	Result                             []byte
}

// CreateSAMLRequest stores a request row.
func (s *Store) CreateSAMLRequest(ctx context.Context, r SAMLRequest) error {
	_, err := s.Pool.Exec(ctx, `insert into saml_requests (id, purpose, user_id, return_to, binding_hash, expires_at)
		values ($1, $2, $3, $4, $5, $6)`, r.ID, r.Purpose, r.UserID, r.ReturnTo, r.BindingHash, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create saml request: %w", err)
	}
	return nil
}

// ConsumeSAMLRequest deletes and returns the unexpired request in a single statement, so exactly one
// concurrent caller gets it. ErrNotFound when missing, already consumed or expired at now.
func (s *Store) ConsumeSAMLRequest(ctx context.Context, id string, now time.Time) (SAMLRequest, error) {
	var r SAMLRequest
	err := s.Pool.QueryRow(ctx, `delete from saml_requests where id = $1 and expires_at > $2
		returning id, purpose, user_id, return_to, binding_hash, created_at, expires_at, result`, id, now).
		Scan(&r.ID, &r.Purpose, &r.UserID, &r.ReturnTo, &r.BindingHash, &r.CreatedAt, &r.ExpiresAt, &r.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return SAMLRequest{}, ErrNotFound
	}
	if err != nil {
		return SAMLRequest{}, fmt.Errorf("consume saml request: %w", err)
	}
	return r, nil
}

// PutSAMLTestResult re-inserts a consumed test request with its outcome, readable once until expires.
func (s *Store) PutSAMLTestResult(ctx context.Context, r SAMLRequest, result []byte, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `insert into saml_requests (id, purpose, user_id, return_to, binding_hash, expires_at, result)
		values ($1, 'test', null, $2, $3, $4, $5)
		on conflict (id) do update set result = excluded.result, expires_at = excluded.expires_at`,
		r.ID, r.ReturnTo, r.BindingHash, expires, result)
	if err != nil {
		return fmt.Errorf("put saml test result: %w", err)
	}
	return nil
}

// TakeSAMLTestResult deletes the test row and returns its result once. ErrNotFound when absent,
// not a test row, or no result yet.
func (s *Store) TakeSAMLTestResult(ctx context.Context, id string) ([]byte, error) {
	var result []byte
	err := s.Pool.QueryRow(ctx, `delete from saml_requests where id = $1 and purpose = 'test' and result is not null
		returning result`, id).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("take saml test result: %w", err)
	}
	return result, nil
}

// RememberAssertionID records an accepted assertion or response ID; ErrConflict means it was seen before (replay).
func (s *Store) RememberAssertionID(ctx context.Context, id string, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `insert into saml_assertion_ids (id, expires_at) values ($1, $2)`, id, expires)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("remember assertion id: %w", err)
	}
	return nil
}
