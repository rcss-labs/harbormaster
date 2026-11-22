package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// utcDate is day's calendar date in UTC, the key of the usage ledger.
func utcDate(day time.Time) time.Time {
	y, m, d := day.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TokenLogScanUsage returns the bytes and number of log searches made with access tokens by userID on the UTC day
// containing day. A day with no row is zero.
func (s *Store) TokenLogScanUsage(ctx context.Context, userID int64, day time.Time) (bytes int64, searches int, err error) {
	err = s.Pool.QueryRow(ctx,
		`select bytes_scanned, searches from token_log_scan_usage where user_id = $1 and day = $2`,
		userID, utcDate(day)).Scan(&bytes, &searches)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("token log scan usage: %w", err)
	}
	return bytes, searches, nil
}

// AddTokenLogScanUsage adds bytes and one search to the user's row for the UTC day containing day.
func (s *Store) AddTokenLogScanUsage(ctx context.Context, userID int64, day time.Time, bytes int64) error {
	_, err := s.Pool.Exec(ctx,
		`insert into token_log_scan_usage (user_id, day, bytes_scanned, searches) values ($1, $2, $3, 1)
		 on conflict (user_id, day) do update
		 set bytes_scanned = token_log_scan_usage.bytes_scanned + excluded.bytes_scanned,
		     searches = token_log_scan_usage.searches + 1`,
		userID, utcDate(day), bytes)
	if err != nil {
		return fmt.Errorf("add token log scan usage: %w", err)
	}
	return nil
}

// DeleteTokenLogScanUsageBefore removes usage rows for days before the UTC day containing day and returns how many.
func (s *Store) DeleteTokenLogScanUsageBefore(ctx context.Context, day time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from token_log_scan_usage where day < $1`, utcDate(day))
	if err != nil {
		return 0, fmt.Errorf("delete token log scan usage: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReserveTokenLogScan charges min(perSearchCap, what is left of dailyBytes) to the user's day before a search runs
// and returns it; perSearchCap 0 means no per-search cap. It counts one search. The row is locked while the
// remainder is read, so concurrent reserves cannot both take the same remaining budget. It returns 0 and changes
// nothing when the day is used up.
func (s *Store) ReserveTokenLogScan(ctx context.Context, userID int64, day time.Time, dailyBytes, perSearchCap int64) (int64, error) {
	var reserved int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`insert into token_log_scan_usage (user_id, day, bytes_scanned, searches) values ($1, $2, 0, 0)
			 on conflict (user_id, day) do nothing`, userID, utcDate(day)); err != nil {
			return err
		}
		var used int64
		if err := tx.QueryRow(ctx,
			`select bytes_scanned from token_log_scan_usage where user_id = $1 and day = $2 for update`,
			userID, utcDate(day)).Scan(&used); err != nil {
			return err
		}
		reserved = max(dailyBytes-used, 0)
		if perSearchCap > 0 {
			reserved = min(reserved, perSearchCap)
		}
		if reserved == 0 {
			return nil
		}
		_, err := tx.Exec(ctx,
			`update token_log_scan_usage set bytes_scanned = bytes_scanned + $3, searches = searches + 1
			 where user_id = $1 and day = $2`, userID, utcDate(day), reserved)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("reserve token log scan: %w", err)
	}
	return reserved, nil
}

// SettleTokenLogScan replaces a reservation made on day with the bytes the search actually scanned: the difference
// is refunded, or charged when the scan overran the reservation. The day's total never goes below zero.
func (s *Store) SettleTokenLogScan(ctx context.Context, userID int64, day time.Time, reserved, actual int64) error {
	_, err := s.Pool.Exec(ctx,
		`update token_log_scan_usage set bytes_scanned = greatest(bytes_scanned - $3 + $4, 0)
		 where user_id = $1 and day = $2`, userID, utcDate(day), reserved, actual)
	if err != nil {
		return fmt.Errorf("settle token log scan: %w", err)
	}
	return nil
}
