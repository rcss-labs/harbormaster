package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenOptions tunes a pool beyond the database URL.
type OpenOptions struct {
	// RuntimeParams are Postgres runtime parameters (for example search_path) applied to every pooled connection.
	RuntimeParams map[string]string
	// MaxConns caps the pool; 0 keeps pgx's default.
	MaxConns int
}

// OpenWithOptions connects to databaseURL with o and verifies the connection with a ping.
func OpenWithOptions(ctx context.Context, databaseURL string, o OpenOptions) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	for k, v := range o.RuntimeParams {
		cfg.ConnConfig.RuntimeParams[k] = v
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = int32(o.MaxConns)
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
