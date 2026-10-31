package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/authz"
)

// UserActions pairs a username with the actions it holds on a tag.
type UserActions struct {
	Username string
	Actions  authz.ActionSet
}

// SetPermission stores the normalised action set for a user on a tag.
// A normalised set of 0 deletes the row.
func (s *Store) SetPermission(ctx context.Context, userID int64, tag authz.TagID, actions authz.ActionSet) error {
	actions = authz.Normalize(actions)
	if actions == 0 {
		if _, err := s.Pool.Exec(ctx,
			`delete from permissions where user_id = $1 and tag_id = $2`, userID, int64(tag)); err != nil {
			return fmt.Errorf("delete permission: %w", err)
		}
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`insert into permissions (user_id, tag_id, actions) values ($1, $2, $3)
		 on conflict (user_id, tag_id) do update set actions = excluded.actions`,
		userID, int64(tag), int16(actions))
	if err != nil {
		return fmt.Errorf("set permission: %w", err)
	}
	return nil
}

// PermissionsOf returns one user's per-tag action sets (never nil).
func (s *Store) PermissionsOf(ctx context.Context, userID int64) (map[authz.TagID]authz.ActionSet, error) {
	rows, err := s.Pool.Query(ctx, `select tag_id, actions from permissions where user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("permissions of user: %w", err)
	}
	defer rows.Close()
	perms := map[authz.TagID]authz.ActionSet{}
	for rows.Next() {
		var tag int64
		var actions int16
		if err := rows.Scan(&tag, &actions); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		perms[authz.TagID(tag)] = authz.ActionSet(actions)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("permissions of user: %w", err)
	}
	return perms, nil
}

// AllPermissions returns every user's per-tag action sets keyed by user id.
func (s *Store) AllPermissions(ctx context.Context) (map[int64]map[authz.TagID]authz.ActionSet, error) {
	rows, err := s.Pool.Query(ctx, `select user_id, tag_id, actions from permissions`)
	if err != nil {
		return nil, fmt.Errorf("all permissions: %w", err)
	}
	defer rows.Close()
	all := map[int64]map[authz.TagID]authz.ActionSet{}
	for rows.Next() {
		var userID, tag int64
		var actions int16
		if err := rows.Scan(&userID, &tag, &actions); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		if all[userID] == nil {
			all[userID] = map[authz.TagID]authz.ActionSet{}
		}
		all[userID][authz.TagID(tag)] = authz.ActionSet(actions)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("all permissions: %w", err)
	}
	return all, nil
}

// UsersWithView lists users holding View on the tag, sorted by username.
func (s *Store) UsersWithView(ctx context.Context, tag authz.TagID) ([]UserActions, error) {
	rows, err := s.Pool.Query(ctx,
		`select u.username, p.actions from permissions p join users u on u.id = p.user_id
		 where p.tag_id = $1 and (p.actions & $2) <> 0 order by u.username`,
		int64(tag), int16(authz.View))
	if err != nil {
		return nil, fmt.Errorf("users with view: %w", err)
	}
	defer rows.Close()
	users := []UserActions{}
	for rows.Next() {
		var u UserActions
		var actions int16
		if err := rows.Scan(&u.Username, &actions); err != nil {
			return nil, fmt.Errorf("scan user actions: %w", err)
		}
		u.Actions = authz.ActionSet(actions)
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("users with view: %w", err)
	}
	return users, nil
}

// LoadSubject builds the authz subject for a user, or ErrNotFound if the user is gone.
func (s *Store) LoadSubject(ctx context.Context, userID int64) (authz.Subject, error) {
	var sub authz.Subject
	err := s.Pool.QueryRow(ctx, `select id, username, is_admin from users where id = $1`, userID).
		Scan(&sub.UserID, &sub.Username, &sub.Admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.Subject{}, ErrNotFound
	}
	if err != nil {
		return authz.Subject{}, fmt.Errorf("load subject: %w", err)
	}
	if sub.Perms, err = s.PermissionsOf(ctx, userID); err != nil {
		return authz.Subject{}, err
	}
	return sub, nil
}
