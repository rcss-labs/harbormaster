package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestMigrate_AppliesAllFilesOnce(t *testing.T) {
	s := storetest.Open(t) // already ran Migrate once; a second run applies nothing
	ctx := context.Background()

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("second Migrate applied %v, want none", applied)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", n)
	}
	var versions []int
	rows, err := s.Pool.Query(ctx, `select version from schema_migrations order by version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if !reflect.DeepEqual(versions, []int{1, 2}) {
		t.Fatalf("versions = %v, want [1 2]", versions)
	}
}

func TestMigrate_SeedsDevTag(t *testing.T) {
	s := storetest.Open(t)
	var name string
	var builtin bool
	if err := s.Pool.QueryRow(context.Background(), `select name, builtin from tags`).Scan(&name, &builtin); err != nil {
		t.Fatal(err)
	}
	if name != "Dev" || !builtin {
		t.Fatalf("got %q builtin=%v, want Dev true", name, builtin)
	}
}

func TestStoretest_IsolatedSchema(t *testing.T) {
	a, b := storetest.Open(t), storetest.Open(t)
	var sa, sb string
	if err := a.Pool.QueryRow(context.Background(), `select current_schema()`).Scan(&sa); err != nil {
		t.Fatal(err)
	}
	if err := b.Pool.QueryRow(context.Background(), `select current_schema()`).Scan(&sb); err != nil {
		t.Fatal(err)
	}
	if sa == sb {
		t.Fatalf("both stores use schema %q", sa)
	}
}
