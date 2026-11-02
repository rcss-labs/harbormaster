package store_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestSettings_StoppedTasksWindowDefaultAndUpsert(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	got, err := s.StoppedTasksWindowMinutes(ctx)
	if err != nil {
		t.Fatalf("StoppedTasksWindowMinutes on fresh store: %v", err)
	}
	if got != store.DefaultStoppedTasksWindowMinutes {
		t.Fatalf("fresh store window = %d, want default %d", got, store.DefaultStoppedTasksWindowMinutes)
	}

	if err := s.SetStoppedTasksWindowMinutes(ctx, 25); err != nil {
		t.Fatalf("set 25: %v", err)
	}
	if got, _ := s.StoppedTasksWindowMinutes(ctx); got != 25 {
		t.Fatalf("after set 25 window = %d, want 25", got)
	}

	if err := s.SetStoppedTasksWindowMinutes(ctx, 7); err != nil {
		t.Fatalf("set 7: %v", err)
	}
	if got, _ := s.StoppedTasksWindowMinutes(ctx); got != 7 {
		t.Fatalf("after set 7 window = %d, want 7", got)
	}

	var rows int
	if err := s.Pool.QueryRow(ctx, `select count(*) from settings`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("settings rows = %d, want 1 (upsert, not insert)", rows)
	}
}

func TestSettings_UnparsableWindowFallsBackToDefault(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.Pool.Exec(ctx,
		`insert into settings (key, value, updated_at) values ($1, 'soon', now())`,
		store.SettingStoppedTasksWindow); err != nil {
		t.Fatal(err)
	}
	got, err := s.StoppedTasksWindowMinutes(ctx)
	if err != nil {
		t.Fatalf("StoppedTasksWindowMinutes with unparsable value: %v", err)
	}
	if got != store.DefaultStoppedTasksWindowMinutes {
		t.Fatalf("unparsable window = %d, want default %d", got, store.DefaultStoppedTasksWindowMinutes)
	}
}

func TestSettings_OutOfRangeWindowFallsBackToDefault(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	for _, raw := range []string{"0", "-5", "1441", "1000000"} {
		if _, err := s.Pool.Exec(ctx,
			`insert into settings (key, value, updated_at) values ($1, $2, now())
			 on conflict (key) do update set value = excluded.value`,
			store.SettingStoppedTasksWindow, raw); err != nil {
			t.Fatal(err)
		}
		got, err := s.StoppedTasksWindowMinutes(ctx)
		if err != nil {
			t.Fatalf("window %q: %v", raw, err)
		}
		if got != store.DefaultStoppedTasksWindowMinutes {
			t.Errorf("stored %q: window = %d, want default %d", raw, got, store.DefaultStoppedTasksWindowMinutes)
		}
	}
	for _, minutes := range []int{1, 1440} {
		if err := s.SetStoppedTasksWindowMinutes(ctx, minutes); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.StoppedTasksWindowMinutes(ctx); got != minutes {
			t.Errorf("stored %d: window = %d, want it kept", minutes, got)
		}
	}
}
