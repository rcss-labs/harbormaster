package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// AlertFilter narrows alerts to one account and/or region; empty means all.
type AlertFilter struct {
	AccountID, Region string
}

// ReplaceAlerts swaps the target's alerts for alerts in one transaction, so readers
// never see a partial set.
func (s *Store) ReplaceAlerts(ctx context.Context, t awsx.Target, alerts []model.Alert) error {
	return s.ReplaceAlertsAndStatusRuns(ctx, t, alerts, nil)
}

// ReplaceAlertsAndStatusRuns swaps the target's alerts and upserts its status runs in one transaction,
// so a reader never sees new alerts next to the previous cycle's poll counts.
func (s *Store) ReplaceAlertsAndStatusRuns(ctx context.Context, t awsx.Target, alerts []model.Alert, runs []StatusRun) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("replace alerts: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `delete from alerts where account_id = $1 and region = $2`, t.AccountID, t.Region); err != nil {
		return fmt.Errorf("replace alerts: delete: %w", err)
	}
	batch := &pgx.Batch{}
	for _, alert := range alerts {
		batch.Queue(
			`insert into alerts (account_id, region, cluster_name, service_name, kind, severity, title, text, at, tab, polled_at)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
			t.AccountID, t.Region, alert.Key.Cluster, alert.Key.Service, alert.Kind, string(alert.Severity),
			alert.Title, alert.Text, alert.At, alert.Tab)
	}
	for _, run := range runs {
		batch.Queue(upsertStatusRunSQL,
			run.Key.AccountID, run.Key.Region, run.Key.Cluster, run.Key.Service, string(run.Status), run.Since, run.Polls)
	}
	if batch.Len() > 0 {
		results := tx.SendBatch(ctx, batch)
		for range batch.Len() {
			if _, err := results.Exec(); err != nil {
				results.Close()
				return fmt.Errorf("replace alerts: write: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("replace alerts: write: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace alerts: commit: %w", err)
	}
	return nil
}

// ListAlerts returns every alert matching f, in insertion order. Visibility filtering and sorting happen in the API.
func (s *Store) ListAlerts(ctx context.Context, f AlertFilter) ([]model.Alert, error) {
	rows, err := s.Pool.Query(ctx,
		`select account_id, region, cluster_name, service_name, kind, severity, title, text, at, tab
		 from alerts
		 where ($1 = '' or account_id = $1) and ($2 = '' or region = $2)
		 order by id`,
		f.AccountID, f.Region)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	alerts := []model.Alert{}
	for rows.Next() {
		var alert model.Alert
		var severity string
		if err := rows.Scan(&alert.Key.AccountID, &alert.Key.Region, &alert.Key.Cluster, &alert.Key.Service,
			&alert.Kind, &severity, &alert.Title, &alert.Text, &alert.At, &alert.Tab); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		alert.Severity = model.AlertSeverity(severity)
		alerts = append(alerts, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	return alerts, nil
}
