package config

import "errors"

// Role selects which parts of Harbormaster a process runs.
type Role string

const (
	RoleAll         Role = "all"
	RoleWeb         Role = "web"
	RolePoller      Role = "poller"
	RoleCoordinator Role = "coordinator"
)

// ParseRole accepts the four role names exactly; the empty string means all.
func ParseRole(raw string) (Role, error) {
	switch Role(raw) {
	case "":
		return RoleAll, nil
	case RoleAll, RoleWeb, RolePoller, RoleCoordinator:
		return Role(raw), nil
	}
	return "", errors.New("HM_ROLE: must be all, web, poller or coordinator")
}

// ServesAPI reports whether the role serves the HTTP API and the web app.
func (r Role) ServesAPI() bool { return r == RoleAll || r == RoleWeb }

// Polls reports whether the role owns polling slices.
func (r Role) Polls() bool { return r == RoleAll || r == RolePoller }

// Coordinates reports whether the role runs the coordinator's jobs.
func (r Role) Coordinates() bool { return r == RoleAll || r == RoleCoordinator }

// minDBMaxConns is the smallest pool that still leaves the lease gate, a claim and a write each a connection.
const minDBMaxConns = 3

// PoolSize is the database pool's maximum size: DBMaxConns when set, else the role's default. 0 leaves pgx's default,
// which role all keeps so a single-task install does not change.
func (c Config) PoolSize() int {
	if c.DBMaxConns > 0 {
		return c.DBMaxConns
	}
	switch c.Role {
	case RoleWeb:
		return 6
	case RolePoller:
		return 8
	case RoleCoordinator:
		return 4
	}
	return 0
}
