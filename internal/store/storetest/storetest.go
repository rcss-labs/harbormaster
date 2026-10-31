// Package storetest gives tests an isolated, migrated Postgres schema.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// Open returns a migrated Store in a fresh isolated schema (see OpenEmpty).
func Open(t *testing.T) *store.Store {
	t.Helper()
	s := OpenEmpty(t)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// OpenEmpty returns a Store bound to a fresh, unmigrated schema t_<8 hex>,
// dropped on cleanup. It skips the test when HM_TEST_DATABASE_URL is unset.
func OpenEmpty(t *testing.T) *store.Store {
	t.Helper()
	url := lookupURL()
	if url == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(buf)

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "drop schema if exists "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		admin.Close(context.Background())
	})

	s, err := store.OpenWithParams(ctx, url, map[string]string{"search_path": schema})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close) // runs before the drop (LIFO)
	return s
}
