package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConflict is returned when a write violates a uniqueness constraint.
var ErrConflict = errors.New("conflict")

const uniqueViolationCode = "23505"

// User is a Harbormaster account.
type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	Admin              bool
	MustChangePassword bool
	CreatedAt          time.Time
	LastSignInAt       *time.Time
	LockedUntil        *time.Time

	// BreakGlass lets the user sign in with a password while SSO is on; it requires a password.
	BreakGlass bool
	// The SSO identity: all three are empty or all three are set.
	IdPEntityID, ExternalID, ExternalIDFormat string
	Email, DisplayName                        *string
	// Admin-set pre-link (empty PendingLinkNameID when none).
	PendingLinkNameID      string
	PendingLinkExpiresAt   *time.Time
	PendingLinkSetAt       *time.Time
	PendingLinkSetByUserID *int64
}

// HasPassword reports whether the user can sign in with a password. PasswordHash is empty when
// the column is NULL.
func (u User) HasPassword() bool { return u.PasswordHash != "" }

// SSOLinked reports whether an SSO identity is bound to the user.
func (u User) SSOLinked() bool { return u.ExternalID != "" }

// PendingLinkActive reports whether an unexpired admin-set pending link exists at now.
func (u User) PendingLinkActive(now time.Time) bool {
	return u.PendingLinkNameID != "" && u.PendingLinkExpiresAt != nil && now.Before(*u.PendingLinkExpiresAt)
}

const userColumns = `id, username, coalesce(password_hash, ''), is_admin, must_change_password, created_at, last_sign_in_at, locked_until,
	break_glass, coalesce(idp_entity_id, ''), coalesce(external_id, ''), coalesce(external_id_format, ''), email, display_name,
	coalesce(pending_link_name_id, ''), pending_link_expires_at, pending_link_set_at, pending_link_set_by_user_id`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Admin, &u.MustChangePassword,
		&u.CreatedAt, &u.LastSignInAt, &u.LockedUntil,
		&u.BreakGlass, &u.IdPEntityID, &u.ExternalID, &u.ExternalIDFormat, &u.Email, &u.DisplayName,
		&u.PendingLinkNameID, &u.PendingLinkExpiresAt, &u.PendingLinkSetAt, &u.PendingLinkSetByUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

// CreateUser inserts a user, keeping the username as given. Duplicates that
// differ only in case return ErrConflict.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, admin, mustChange bool) (User, error) {
	row := s.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, is_admin, must_change_password)
		 values ($1, $2, $3, $4) returning `+userColumns,
		username, passwordHash, admin, mustChange)
	u, err := scanUser(row)
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// GetUser returns the user with the given ID, or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `select `+userColumns+` from users where id = $1`, id))
}

// GetUserByUsername matches usernames case-insensitively, or returns ErrNotFound.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`select `+userColumns+` from users where lower(username) = lower($1)`, username))
}

// ListUsers returns all users in creation order.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `select `+userColumns+` from users order by id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `select count(*) from users`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of global admins.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `select count(*) from users where is_admin`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// DeleteUser removes a user; their sessions and permissions cascade. Returns ErrNotFound if absent.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.execExpectOne(ctx, `delete from users where id = $1`, id)
}

// SetPassword replaces a user's password hash and must-change flag. Returns ErrNotFound if absent.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	return s.execExpectOne(ctx,
		`update users set password_hash = $2, must_change_password = $3 where id = $1`,
		id, hash, mustChange)
}

// SetAdmin grants or revokes the global admin flag. Returns ErrNotFound if absent.
func (s *Store) SetAdmin(ctx context.Context, id int64, admin bool) error {
	return s.execExpectOne(ctx, `update users set is_admin = $2 where id = $1`, id, admin)
}

// TouchSignIn records the time of a successful sign-in. Returns ErrNotFound if absent.
func (s *Store) TouchSignIn(ctx context.Context, id int64, at time.Time) error {
	return s.execExpectOne(ctx, `update users set last_sign_in_at = $2 where id = $1`, id, at)
}

// LockUser locks sign-in for the user until the given time. Returns ErrNotFound if absent.
func (s *Store) LockUser(ctx context.Context, id int64, until time.Time) error {
	return s.execExpectOne(ctx, `update users set locked_until = $2 where id = $1`, id, until)
}

// execExpectOne runs a single-row write and maps "no rows affected" to ErrNotFound.
func (s *Store) execExpectOne(ctx context.Context, sql string, args ...any) error {
	tag, err := s.Pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordLoginAttempt appends a sign-in attempt for brute-force accounting.
func (s *Store) RecordLoginAttempt(ctx context.Context, username, ip string, ok bool) error {
	_, err := s.Pool.Exec(ctx,
		`insert into login_attempts (username, ip, ok) values ($1, $2, $3)`, username, ip, ok)
	if err != nil {
		return fmt.Errorf("record login attempt: %w", err)
	}
	return nil
}

// FailedAttempts counts failed sign-ins since the given time, for this username (case-insensitive)
// and for this IP.
func (s *Store) FailedAttempts(ctx context.Context, username, ip string, since time.Time) (byUser, byIP int, err error) {
	err = s.Pool.QueryRow(ctx,
		`select count(*) filter (where lower(username) = lower($1)),
		        count(*) filter (where ip = $2)
		 from login_attempts where ok = false and ts >= $3`,
		username, ip, since).Scan(&byUser, &byIP)
	if err != nil {
		return 0, 0, fmt.Errorf("count failed attempts: %w", err)
	}
	return byUser, byIP, nil
}

// DeleteLoginAttemptsBefore removes sign-in attempts older than t and returns how many were removed.
func (s *Store) DeleteLoginAttemptsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from login_attempts where ts < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete login attempts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Errors returned by the SSO identity and break-glass methods.
var (
	ErrAlreadyLinked    = errors.New("already linked")
	ErrSSOIdentityTaken = errors.New("sso identity taken")
	ErrNotLinked        = errors.New("not linked")
	ErrNeedsPassword    = errors.New("needs password")
)

// NewSAMLUser describes a user created on first SSO sign-in.
type NewSAMLUser struct {
	Username, IdPEntityID, ExternalID, ExternalIDFormat string
	Email, DisplayName                                  *string
}

// CreateSAMLUser inserts a password-less, non-admin user bound to an SSO identity.
// A taken username or identity returns ErrConflict.
func (s *Store) CreateSAMLUser(ctx context.Context, n NewSAMLUser) (User, error) {
	u, err := scanUser(s.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, is_admin, must_change_password,
		                    idp_entity_id, external_id, external_id_format, email, display_name)
		 values ($1, null, false, false, $2, $3, $4, $5, $6) returning `+userColumns,
		n.Username, n.IdPEntityID, n.ExternalID, n.ExternalIDFormat, n.Email, n.DisplayName))
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, fmt.Errorf("create saml user: %w", err)
	}
	return u, nil
}

// GetUserByExternalID returns the user linked to (idpEntityID, externalID), or ErrNotFound.
func (s *Store) GetUserByExternalID(ctx context.Context, idpEntityID, externalID string) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`select `+userColumns+` from users where idp_entity_id = $1 and external_id = $2`,
		idpEntityID, externalID))
}

// GetUserByPendingLink returns the user whose unexpired pending link equals nameID, or ErrNotFound.
// Pending links are not stored with an entity ID; the caller passes the configured one and the
// store clears pending links when the entity ID changes. The match is exact and case-sensitive.
func (s *Store) GetUserByPendingLink(ctx context.Context, _ /* idpEntityID */, nameID string, now time.Time) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`select `+userColumns+` from users
		 where pending_link_name_id = $1 and pending_link_expires_at > $2`, nameID, now))
}

// UpdateSAMLProfile changes only the non-nil fields. Returns ErrNotFound if the user is absent.
func (s *Store) UpdateSAMLProfile(ctx context.Context, id int64, email, displayName *string) error {
	return s.execExpectOne(ctx,
		`update users set email = coalesce($2, email), display_name = coalesce($3, display_name) where id = $1`,
		id, email, displayName)
}

// CountBreakGlassAdmins returns the number of global admins flagged break-glass.
func (s *Store) CountBreakGlassAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from users where is_admin and break_glass`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count break-glass admins: %w", err)
	}
	return n, nil
}

// CountPasswordUsersNotBreakGlass counts users who have a password but are not break-glass, i.e.
// who lose password sign-in when SSO is enabled.
func (s *Store) CountPasswordUsersNotBreakGlass(ctx context.Context) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx,
		`select count(*) from users where password_hash is not null and not break_glass`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count password users: %w", err)
	}
	return n, nil
}

// BindSSO describes an identity to attach to an existing user.
type BindSSO struct {
	UserID                                    int64
	IdPEntityID, ExternalID, ExternalIDFormat string
	Email, DisplayName                        *string
}

// BindSSOIdentity attaches an SSO identity to a user in one transaction: it clears any pending
// link, syncs the non-nil profile fields and never touches the password. ErrAlreadyLinked when the
// user has an identity, ErrSSOIdentityTaken when another user holds it, ErrNotFound when absent.
func (s *Store) BindSSOIdentity(ctx context.Context, b BindSSO) (User, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockNameIDTx(ctx, tx, b.ExternalID); err != nil {
		return User{}, err
	}
	var linked bool
	if err := tx.QueryRow(ctx, `select external_id is not null from users where id = $1 for update`, b.UserID).Scan(&linked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("load user: %w", err)
	}
	if linked {
		return User{}, ErrAlreadyLinked
	}
	// Another user's active pre-link owns this NameID; checked here so it cannot race SetPendingLink.
	var pendingElsewhere bool
	if err := tx.QueryRow(ctx,
		`select exists (select 1 from users where id <> $1 and pending_link_name_id = $2 and pending_link_expires_at > now())`,
		b.UserID, b.ExternalID).Scan(&pendingElsewhere); err != nil {
		return User{}, fmt.Errorf("check pending link: %w", err)
	}
	if pendingElsewhere {
		return User{}, ErrSSOIdentityTaken
	}
	u, err := scanUser(tx.QueryRow(ctx,
		`update users set idp_entity_id = $2, external_id = $3, external_id_format = $4,
		        email = coalesce($5, email), display_name = coalesce($6, display_name),
		        pending_link_name_id = null, pending_link_set_at = null,
		        pending_link_expires_at = null, pending_link_set_by_user_id = null
		 where id = $1 returning `+userColumns,
		b.UserID, b.IdPEntityID, b.ExternalID, b.ExternalIDFormat, b.Email, b.DisplayName))
	if isUniqueViolation(err) {
		return User{}, ErrSSOIdentityTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("bind identity: %w", err)
	}
	return u, tx.Commit(ctx)
}

// SetPendingLink records an admin pre-link on an unlinked user, replacing any earlier one.
// ErrAlreadyLinked when the user has an identity; ErrSSOIdentityTaken when nameID is linked to or
// pending on another user.
func (s *Store) SetPendingLink(ctx context.Context, id int64, nameID string, setBy int64, expires time.Time) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockNameIDTx(ctx, tx, nameID); err != nil {
		return err
	}
	var linked bool
	if err := tx.QueryRow(ctx, `select external_id is not null from users where id = $1 for update`, id).Scan(&linked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load user: %w", err)
	}
	if linked {
		return ErrAlreadyLinked
	}
	var taken bool
	if err := tx.QueryRow(ctx,
		`select exists (select 1 from users where id <> $1 and (external_id = $2 or pending_link_name_id = $2))`,
		id, nameID).Scan(&taken); err != nil {
		return fmt.Errorf("check name id: %w", err)
	}
	if taken {
		return ErrSSOIdentityTaken
	}
	_, err = tx.Exec(ctx,
		`update users set pending_link_name_id = $2, pending_link_set_at = now(),
		        pending_link_expires_at = $3, pending_link_set_by_user_id = $4 where id = $1`,
		id, nameID, expires, setBy)
	if isUniqueViolation(err) {
		return ErrSSOIdentityTaken
	}
	if err != nil {
		return fmt.Errorf("set pending link: %w", err)
	}
	return tx.Commit(ctx)
}

// ClearPendingLink removes a pending link. Returns ErrNotFound when the user has none.
func (s *Store) ClearPendingLink(ctx context.Context, id int64) error {
	return s.execExpectOne(ctx,
		`update users set pending_link_name_id = null, pending_link_set_at = null,
		        pending_link_expires_at = null, pending_link_set_by_user_id = null
		 where id = $1 and pending_link_name_id is not null`, id)
}

// UnlinkSSO detaches the user's SSO identity and ends their SSO sessions. When the user has no
// password it sets newHash with must_change_password so they keep a credential (passwordSet).
// ErrNotLinked when no identity is bound; ErrNotFound when the user is absent.
func (s *Store) UnlinkSSO(ctx context.Context, id int64, newHash string) (passwordSet bool, sessionsEnded int64, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var linked, hasPassword bool
	if err := tx.QueryRow(ctx,
		`select external_id is not null, password_hash is not null from users where id = $1 for update`, id).
		Scan(&linked, &hasPassword); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, ErrNotFound
		}
		return false, 0, fmt.Errorf("load user: %w", err)
	}
	if !linked {
		return false, 0, ErrNotLinked
	}
	if hasPassword {
		_, err = tx.Exec(ctx,
			`update users set idp_entity_id = null, external_id = null, external_id_format = null where id = $1`, id)
	} else {
		_, err = tx.Exec(ctx,
			`update users set idp_entity_id = null, external_id = null, external_id_format = null,
			        password_hash = $2, must_change_password = true where id = $1`, id, newHash)
	}
	if err != nil {
		return false, 0, fmt.Errorf("unlink: %w", err)
	}
	tag, err := tx.Exec(ctx, `delete from sessions where user_id = $1 and auth_source = 'saml'`, id)
	if err != nil {
		return false, 0, fmt.Errorf("end saml sessions: %w", err)
	}
	return !hasPassword, tag.RowsAffected(), tx.Commit(ctx)
}

// CreateBreakGlassAdmin inserts an administrator with break_glass and must_change_password set in a
// single statement, so there is no window where the admin exists without the flag. Duplicates
// (case-insensitive) return ErrConflict, like CreateUser.
func (s *Store) CreateBreakGlassAdmin(ctx context.Context, username, passwordHash string) (User, error) {
	row := s.Pool.QueryRow(ctx,
		`insert into users (username, password_hash, is_admin, must_change_password, break_glass)
		 values ($1, $2, true, true, true) returning `+userColumns,
		username, passwordHash)
	u, err := scanUser(row)
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	if err != nil {
		return User{}, fmt.Errorf("create break-glass admin: %w", err)
	}
	return u, nil
}

// lockNameIDTx serialises transactions that bind or pre-link the same NameID. Row locks cannot do it:
// the two sides lock different users' rows, and neither sees the other's uncommitted change.
func lockNameIDTx(ctx context.Context, tx pgx.Tx, nameID string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "sso-nameid:"+nameID); err != nil {
		return fmt.Errorf("lock name id: %w", err)
	}
	return nil
}
