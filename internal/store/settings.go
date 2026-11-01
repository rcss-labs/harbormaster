package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const (
	// SettingStoppedTasksWindow is the settings key for how many minutes stopped tasks stay listed.
	SettingStoppedTasksWindow = "stopped_tasks_window_minutes"
	// DefaultStoppedTasksWindowMinutes applies when the setting is absent or not an integer.
	DefaultStoppedTasksWindowMinutes = 10
)

// StoppedTasksWindowMinutes returns the stopped-tasks window, or the default when the
// setting is absent or does not parse as an integer.
func (s *Store) StoppedTasksWindowMinutes(ctx context.Context) (int, error) {
	var raw string
	err := s.Pool.QueryRow(ctx, `select value from settings where key = $1`, SettingStoppedTasksWindow).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultStoppedTasksWindowMinutes, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read stopped tasks window: %w", err)
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil {
		return DefaultStoppedTasksWindowMinutes, nil
	}
	return minutes, nil
}

// SetStoppedTasksWindowMinutes stores the stopped-tasks window, replacing any previous value.
func (s *Store) SetStoppedTasksWindowMinutes(ctx context.Context, minutes int) error {
	_, err := s.Pool.Exec(ctx,
		`insert into settings (key, value, updated_at) values ($1, $2, now())
		 on conflict (key) do update set value = excluded.value, updated_at = excluded.updated_at`,
		SettingStoppedTasksWindow, strconv.Itoa(minutes))
	if err != nil {
		return fmt.Errorf("set stopped tasks window: %w", err)
	}
	return nil
}
