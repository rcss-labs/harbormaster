package main

import (
	"context"
	"fmt"
	"io"

	"github.com/rcss-labs/harbormaster/internal/config"
)

// runMigrate applies pending database migrations and prints the versions applied.
func runMigrate(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	dbURL, err := resolveDatabaseURL(ctx, cfg)
	if err != nil {
		return err
	}
	st, err := openStore(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "no migrations to apply")
		return nil
	}
	fmt.Fprintf(stdout, "applied migrations: %v\n", applied)
	return nil
}
