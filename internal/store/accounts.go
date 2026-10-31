package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AWSAccount is a connected AWS account whose resources Harbormaster polls.
type AWSAccount struct {
	ID         int64
	Name       string
	AccountID  string
	RoleARN    string
	ExternalID string
	Regions    []string
	Enabled    bool
	CreatedAt  time.Time
	LastOKAt   *time.Time
	LastError  string
}

const accountColumns = `id, name, account_id, role_arn, external_id, regions, enabled,
	coalesce(created_at, 'epoch'::timestamptz), last_ok_at, coalesce(last_error, '')`

func scanAccount(row pgx.Row) (AWSAccount, error) {
	var a AWSAccount
	err := row.Scan(&a.ID, &a.Name, &a.AccountID, &a.RoleARN, &a.ExternalID, &a.Regions, &a.Enabled,
		&a.CreatedAt, &a.LastOKAt, &a.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return AWSAccount{}, ErrNotFound
	}
	return a, err
}

// CreateAccount inserts an account. A duplicate account_id returns ErrConflict.
func (s *Store) CreateAccount(ctx context.Context, a AWSAccount) (AWSAccount, error) {
	regions := a.Regions
	if regions == nil {
		regions = []string{}
	}
	row := s.Pool.QueryRow(ctx,
		`insert into aws_accounts (name, account_id, role_arn, external_id, regions, enabled)
		 values ($1, $2, $3, $4, $5, $6) returning `+accountColumns,
		a.Name, a.AccountID, a.RoleARN, a.ExternalID, regions, a.Enabled)
	created, err := scanAccount(row)
	if isUniqueViolation(err) {
		return AWSAccount{}, ErrConflict
	}
	if err != nil {
		return AWSAccount{}, fmt.Errorf("create account: %w", err)
	}
	return created, nil
}

// UpdateAccount changes an account's display name and regions. Returns ErrNotFound if absent.
func (s *Store) UpdateAccount(ctx context.Context, id int64, name string, regions []string) error {
	if regions == nil {
		regions = []string{}
	}
	return s.execExpectOne(ctx, `update aws_accounts set name = $2, regions = $3 where id = $1`, id, name, regions)
}

// DeleteAccount removes an account and its polled snapshots in one transaction.
// Operation and deployment history is kept. Returns ErrNotFound if absent.
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var accountID string
	err = tx.QueryRow(ctx, `select account_id from aws_accounts where id = $1`, id).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	for _, table := range []string{"clusters", "services", "service_events", "tasks",
		"service_counts_history", "task_definitions", "poll_loops"} {
		if _, err := tx.Exec(ctx, `delete from `+table+` where account_id = $1`, accountID); err != nil {
			return fmt.Errorf("delete account %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(ctx, `delete from aws_accounts where id = $1`, id); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

// GetAccount returns the account with the given ID, or ErrNotFound.
func (s *Store) GetAccount(ctx context.Context, id int64) (AWSAccount, error) {
	return scanAccount(s.Pool.QueryRow(ctx, `select `+accountColumns+` from aws_accounts where id = $1`, id))
}

// GetAccountByAccountID returns the account with the given 12-digit AWS account ID, or ErrNotFound.
func (s *Store) GetAccountByAccountID(ctx context.Context, accountID string) (AWSAccount, error) {
	return scanAccount(s.Pool.QueryRow(ctx,
		`select `+accountColumns+` from aws_accounts where account_id = $1`, accountID))
}

// ListAccounts returns enabled and disabled accounts ordered by name.
func (s *Store) ListAccounts(ctx context.Context) ([]AWSAccount, error) {
	rows, err := s.Pool.Query(ctx, `select `+accountColumns+` from aws_accounts order by name, id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	accounts := []AWSAccount{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return accounts, nil
}

// SetAccountHealth records the outcome of the latest poll: a nil okAt stores NULL
// and an empty lastError stores NULL. Returns ErrNotFound if the account is absent.
func (s *Store) SetAccountHealth(ctx context.Context, accountID string, okAt *time.Time, lastError string) error {
	return s.execExpectOne(ctx,
		`update aws_accounts set last_ok_at = $2, last_error = nullif($3, '') where account_id = $1`,
		accountID, okAt, lastError)
}
