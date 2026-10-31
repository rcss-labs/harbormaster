package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.up.sql
var migrationFiles embed.FS

// migrationLockKey serialises concurrent Migrate calls (e.g. two replicas starting together).
const migrationLockKey = 726_001_001

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		prefix, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad numeric prefix: %w", e.Name(), err)
		}
		body, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every unapplied embedded migration, each in its own
// transaction, and returns the versions applied in ascending order.
func (s *Store) Migrate(ctx context.Context) (applied []int, err error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `create table if not exists schema_migrations (
		version int primary key, applied_at timestamptz not null default now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	applied = []int{}
	for _, m := range migrations {
		done, err := s.applyOne(ctx, m)
		if err != nil {
			return applied, fmt.Errorf("apply %s: %w", m.name, err)
		}
		if done {
			applied = append(applied, m.version)
		}
	}
	return applied, nil
}

func (s *Store) applyOne(ctx context.Context, m migration) (bool, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, migrationLockKey); err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from schema_migrations where version = $1)`, m.version).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, m.version); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
