package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestMigrate_AppliesAllFilesOnce(t *testing.T) {
	s := storetest.OpenEmpty(t)
	ctx := context.Background()

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if !reflect.DeepEqual(applied, []int{1, 2, 20, 30, 40, 50, 60, 70, 80, 90, 100}) {
		t.Fatalf("first Migrate applied %v, want [1 2 20 30 40 50 60 70 80 90 100]", applied)
	}
	applied, err = s.Migrate(ctx)
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
	if n != 11 {
		t.Fatalf("schema_migrations rows = %d, want 11", n)
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
	if !reflect.DeepEqual(versions, []int{1, 2, 20, 30, 40, 50, 60, 70, 80, 90, 100}) {
		t.Fatalf("versions = %v, want [1 2 20 30 40 50 60 70 80 90 100]", versions)
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

func TestMigrate_AnalyticsTablesAndColumns(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for _, table := range []string{"service_availability_daily", "cluster_capacity_daily", "service_util_daily"} {
		if _, err := s.Pool.Exec(ctx, `select day from `+table+` limit 1`); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `select failure_reason, failure_detail from deployment_history limit 1`); err != nil {
		t.Errorf("deployment_history failure columns: %v", err)
	}
}
