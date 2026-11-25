package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

// The two kinds of user. Every switch over a kind refuses an unknown value.
const (
	UserKindHuman = "human"
	UserKindAgent = "agent"
)

// AgentUsernamePrefix is reserved: no human user may be created with a name that starts with it, so the identity can
// always be given one of the names below.
const AgentUsernamePrefix = "harbormaster-agent"

// agentUsernameCandidates are the names the identity may take, in order: the first one no user holds.
func agentUsernameCandidates() []string {
	names := []string{AgentUsernamePrefix}
	for i := 1; i <= 9; i++ {
		names = append(names, fmt.Sprintf("%s-%d", AgentUsernamePrefix, i))
	}
	return names
}

// IsAgent reports whether the user is the agent identity.
func (u User) IsAgent() bool { return u.Kind == UserKindAgent }

// EnsureAgentIdentity returns the one agent identity, creating it with the first free reserved username. It is safe
// to call from several tasks at once: the unique index admits one agent row, and a caller that loses the race reads
// the winner's. The row is created lazily, when the agent is on, never by a migration.
func (s *Store) EnsureAgentIdentity(ctx context.Context) (User, error) {
	for _, name := range agentUsernameCandidates() {
		if u, err := s.GetAgentIdentity(ctx); err == nil {
			return u, nil
		} else if !errors.Is(err, ErrNotFound) {
			return User{}, err
		}
		// A conflict on the username or on the single-agent index inserts nothing. Either the name is taken (try the next)
		// or another caller just created the identity (found at the top of the next round, or after the last one).
		u, err := scanUser(s.Pool.QueryRow(ctx,
			`insert into users (username, kind) values ($1, 'agent') on conflict do nothing returning `+userColumns, name))
		if err == nil {
			if name != AgentUsernamePrefix {
				slog.Warn("agent identity took a suffixed username because the reserved name was already in use", "username", name)
			}
			return u, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return User{}, fmt.Errorf("create agent identity: %w", err)
		}
	}
	if u, err := s.GetAgentIdentity(ctx); err == nil {
		return u, nil
	} else if !errors.Is(err, ErrNotFound) {
		return User{}, err
	}
	return User{}, errors.New("create agent identity: every reserved username is taken")
}

// GetAgentIdentity returns the agent identity, or ErrNotFound when none exists.
func (s *Store) GetAgentIdentity(ctx context.Context) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `select `+userColumns+` from users where kind = 'agent'`))
}

// UsernameTaken reports whether a user of any kind holds the username (case-insensitively), for callers that pick a
// free name: a lookup by name finds humans only.
func (s *Store) UsernameTaken(ctx context.Context, username string) (bool, error) {
	var taken bool
	if err := s.Pool.QueryRow(ctx, `select exists (select 1 from users where lower(username) = lower($1))`, username).Scan(&taken); err != nil {
		return false, fmt.Errorf("username taken: %w", err)
	}
	return taken, nil
}

// SetAgentIdentityPermissions replaces every permission row of the agent identity in one transaction and records the
// change in the audit log under by, the admin who made it. An action other than View or Logs, or Logs without View, is
// an error and nothing is written. A tag whose set is empty gets no row. ErrNotFound when the identity does not exist.
func (s *Store) SetAgentIdentityPermissions(ctx context.Context, perms map[authz.TagID]authz.ActionSet, by int64) error {
	tags := make([]authz.TagID, 0, len(perms))
	for tag, actions := range perms {
		if actions&^agentIdentityActions != 0 {
			return fmt.Errorf("agent identity permissions: tag %d: only View and Logs may be granted", tag)
		}
		if actions.Has(authz.Logs) && !actions.Has(authz.View) {
			return fmt.Errorf("agent identity permissions: tag %d: Logs needs View", tag)
		}
		if actions != 0 {
			tags = append(tags, tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })

	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The audit row names who made the change, so it must be a person: an absent id, the identity itself or an unknown
	// kind writes nothing.
	var actorUsername string
	if err := tx.QueryRow(ctx, `select username from users where id = $1 and kind = 'human'`, by).Scan(&actorUsername); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load actor: %w", err)
	}
	var identityID int64
	if err := tx.QueryRow(ctx, `select id from users where kind = 'agent' for update`).Scan(&identityID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock agent identity: %w", err)
	}
	before := map[string][]string{}
	rows, err := tx.Query(ctx, `select tag_id, actions from permissions where user_id = $1 order by tag_id`, identityID)
	if err != nil {
		return fmt.Errorf("read agent identity permissions: %w", err)
	}
	for rows.Next() {
		var tag int64
		var actions int16
		if err := rows.Scan(&tag, &actions); err != nil {
			rows.Close()
			return fmt.Errorf("read agent identity permissions: %w", err)
		}
		before[fmt.Sprint(tag)] = agentActionNames(authz.ActionSet(actions))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read agent identity permissions: %w", err)
	}
	if _, err := tx.Exec(ctx, `delete from permissions where user_id = $1`, identityID); err != nil {
		return fmt.Errorf("clear agent identity permissions: %w", err)
	}
	granted := map[string][]string{}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, `insert into permissions (user_id, tag_id, actions) values ($1, $2, $3)`,
			identityID, int64(tag), int16(perms[tag])); err != nil {
			if isForeignKeyViolation(err) {
				return ErrNotFound // a tag that does not exist
			}
			return fmt.Errorf("set agent identity permission: %w", err)
		}
		granted[fmt.Sprint(int64(tag))] = agentActionNames(perms[tag])
	}
	entry := AuditEntry{
		ActorUserID: &by, ActorUsername: actorUsername, Action: "agent_identity_update", Target: fmt.Sprintf("user:%d", identityID),
		Summary: fmt.Sprintf("Set the access of the agent identity to %d tags.", len(tags)),
		Details: map[string]any{"before": before, "tags": granted},
	}
	if err := insertAudit(ctx, tx, entry); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// agentActionNames lists the actions of a set the identity may hold, as the permission_set audit rows name them (View, Logs).
func agentActionNames(set authz.ActionSet) []string {
	var names []string
	if set.Has(authz.View) {
		names = append(names, "View")
	}
	if set.Has(authz.Logs) {
		names = append(names, "Logs")
	}
	return names
}

// ErrReservedUsername is returned when a human user would take a name that starts with the reserved prefix.
var ErrReservedUsername = errors.New("reserved username")

// IsReservedUsername reports whether a name is reserved for the agent identity: it starts with AgentUsernamePrefix
// once case is folded and "_" and "." are read as "-". It is the one rule: creators, sign-in derivation and the API
// all ask it.
func IsReservedUsername(username string) bool {
	folded := strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(username))
	return strings.HasPrefix(folded, AgentUsernamePrefix)
}

// GetHumanUser returns the human user with the given ID, or ErrNotFound for an absent user, the agent identity and any
// other kind. It is the lookup for every action only a person can take.
func (s *Store) GetHumanUser(ctx context.Context, id int64) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `select `+userColumns+` from users where id = $1 and kind = 'human'`, id))
}
