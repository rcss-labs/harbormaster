package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrLastAdmin is returned when a write would leave Harbormaster without a global admin.
var ErrLastAdmin = errors.New("last global admin")

// ErrLastBreakGlassAdmin is returned when SSO is enabled and a write would leave no break-glass admin.
var ErrLastBreakGlassAdmin = errors.New("last break-glass admin")

// lockAdminsTx locks every admin row for the rest of tx, so concurrent demotions and deletions
// serialize and each one counts admins after the previous one committed. A single guarded UPDATE
// is not enough: under READ COMMITTED two of them would both see two admins.
func lockAdminsTx(ctx context.Context, tx pgx.Tx) (adminIDs map[int64]bool, err error) {
	rows, err := tx.Query(ctx, `select id from users where is_admin order by id for update`)
	if err != nil {
		return nil, fmt.Errorf("lock admins: %w", err)
	}
	defer rows.Close()
	adminIDs = map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan admin id: %w", err)
		}
		adminIDs[id] = true
	}
	return adminIDs, rows.Err()
}

// samlEnabledTx reports whether SSO is enabled. Call it after lockAdminsTx so it serialises with
// every other guarded write.
func samlEnabledTx(ctx context.Context, tx pgx.Tx) (bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `select coalesce((select enabled from saml_config where id = 1), false)`).Scan(&enabled); err != nil {
		return false, fmt.Errorf("read saml enabled: %w", err)
	}
	return enabled, nil
}

// breakGlassAdminsTx returns the IDs of admins flagged break-glass (admin rows are locked by lockAdminsTx).
func breakGlassAdminsTx(ctx context.Context, tx pgx.Tx) (map[int64]bool, error) {
	rows, err := tx.Query(ctx, `select id from users where is_admin and break_glass`)
	if err != nil {
		return nil, fmt.Errorf("list break-glass admins: %w", err)
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan break-glass admin id: %w", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// wouldRemoveLastBreakGlassTx reports whether taking id out of the break-glass admin set, while SSO
// is enabled, leaves nobody.
func wouldRemoveLastBreakGlassTx(ctx context.Context, tx pgx.Tx, id int64) (bool, error) {
	bg, err := breakGlassAdminsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !bg[id] || len(bg) > 1 {
		return false, nil
	}
	return samlEnabledTx(ctx, tx)
}

// SetBreakGlassChecked sets the break-glass flag and reports whether it changed. Granting needs a
// password (ErrNeedsPassword); revoking the last break-glass admin while SSO is enabled returns
// ErrLastBreakGlassAdmin. Returns ErrNotFound if the user is absent.
func (s *Store) SetBreakGlassChecked(ctx context.Context, id int64, on bool) (changed bool, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockAdminsTx(ctx, tx); err != nil {
		return false, err
	}
	var current, hasPassword bool
	if err := tx.QueryRow(ctx, `select break_glass, password_hash is not null from users where id = $1 for update`, id).
		Scan(&current, &hasPassword); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("load user: %w", err)
	}
	if on && !hasPassword {
		return false, ErrNeedsPassword
	}
	if current == on {
		return false, nil
	}
	if !on {
		last, err := wouldRemoveLastBreakGlassTx(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if last {
			return false, ErrLastBreakGlassAdmin
		}
	}
	if _, err := tx.Exec(ctx, `update users set break_glass = $2 where id = $1`, id, on); err != nil {
		return false, fmt.Errorf("set break-glass: %w", err)
	}
	return true, tx.Commit(ctx)
}

// SetAdminChecked grants or revokes the admin flag and reports whether the flag changed.
// Revoking the last admin returns ErrLastAdmin; a revoke also ends that user's sessions in the
// same transaction. Returns ErrNotFound if the user is absent.
func (s *Store) SetAdminChecked(ctx context.Context, id int64, admin bool) (changed bool, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	admins, err := lockAdminsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	var current bool
	if err := tx.QueryRow(ctx, `select is_admin from users where id = $1 for update`, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("load user: %w", err)
	}
	if current == admin {
		return false, nil
	}
	if !admin && len(admins) <= 1 {
		return false, ErrLastAdmin
	}
	if !admin {
		last, err := wouldRemoveLastBreakGlassTx(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if last {
			return false, ErrLastBreakGlassAdmin
		}
	}
	if _, err := tx.Exec(ctx, `update users set is_admin = $2 where id = $1`, id, admin); err != nil {
		return false, fmt.Errorf("set admin: %w", err)
	}
	if !admin {
		if _, err := tx.Exec(ctx, `delete from sessions where user_id = $1`, id); err != nil {
			return false, fmt.Errorf("end sessions: %w", err)
		}
	}
	return true, tx.Commit(ctx)
}

// DeleteUserChecked deletes a user, refusing with ErrLastAdmin if they are the only admin.
// Returns ErrNotFound if the user is absent.
func (s *Store) DeleteUserChecked(ctx context.Context, id int64) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	admins, err := lockAdminsTx(ctx, tx)
	if err != nil {
		return err
	}
	if admins[id] && len(admins) <= 1 {
		return ErrLastAdmin
	}
	if admins[id] {
		last, err := wouldRemoveLastBreakGlassTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if last {
			return ErrLastBreakGlassAdmin
		}
	}
	tag, err := tx.Exec(ctx, `delete from users where id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}
