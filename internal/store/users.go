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
}

const userColumns = `id, username, password_hash, is_admin, must_change_password, created_at, last_sign_in_at, locked_until`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Admin, &u.MustChangePassword,
		&u.CreatedAt, &u.LastSignInAt, &u.LockedUntil)
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
