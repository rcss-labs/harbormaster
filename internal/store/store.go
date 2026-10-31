// Package store holds the Postgres access layer: pool, migrations and queries.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the pgx connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// TargetFilter narrows reads to one account and/or region; empty means all.
type TargetFilter struct {
	AccountID, Region string
}

// Visibility describes what a caller may see. Admin short-circuits the tag filter.
type Visibility struct {
	Admin  bool
	TagIDs []int64
}

// Open connects to databaseURL and verifies the connection with a ping.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	return OpenWithParams(ctx, databaseURL, nil)
}

// OpenWithParams is Open with extra Postgres runtime parameters (for example
// search_path) applied to every pooled connection.
func OpenWithParams(ctx context.Context, databaseURL string, runtimeParams map[string]string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	for k, v := range runtimeParams {
		cfg.ConnConfig.RuntimeParams[k] = v
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	s := &Store{Pool: pool}
	if err := s.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}
