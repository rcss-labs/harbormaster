package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// AlertFilter narrows alerts to one account and/or region; empty means all.
type AlertFilter struct {
	AccountID, Region string
}

// ReplaceAlerts makes the target's alerts equal to alerts, writing only the difference, in one transaction
// so readers never see a partial set.
func (s *Store) ReplaceAlerts(ctx context.Context, t awsx.Target, alerts []model.Alert) error {
	return s.ReplaceAlertsAndStatusRuns(ctx, t, alerts, nil)
}

// alertIdentity is what makes an alert "the same alert" between cycles. The deriving code emits at most
// one alert per kind per service (and one account_unreachable per target).
type alertIdentity struct{ cluster, service, kind string }

type storedAlert struct {
	id    int64
	alert model.Alert
}

// sameAlertContent reports whether two alerts with the same identity need no write. Postgres keeps
// microseconds, so times are compared at that resolution.
func sameAlertContent(a, b model.Alert) bool {
	return a.Severity == b.Severity && a.Title == b.Title && a.Text == b.Text && a.Tab == b.Tab &&
		a.At.Truncate(time.Microsecond).Sub(b.At.Truncate(time.Microsecond)).Abs() <= time.Microsecond
}

// ReplaceAlertsAndStatusRuns makes the target's alerts equal to alerts and upserts its status runs in one
// transaction, so a reader never sees new alerts next to the previous cycle's poll counts. Alerts are diffed
// against the stored rows: unchanged ones are not written, changed ones are updated in place (keeping their
// id), new ones are inserted and resolved ones deleted. A static estate with standing alerts writes nothing.
func (s *Store) ReplaceAlertsAndStatusRuns(ctx context.Context, t awsx.Target, alerts []model.Alert, runs []StatusRun) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("replace alerts: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	rows, err := tx.Query(ctx,
		`select id, cluster_name, service_name, kind, severity, title, text, at, tab
		 from alerts where account_id = $1 and region = $2 order by id`, t.AccountID, t.Region)
	if err != nil {
		return fmt.Errorf("replace alerts: read: %w", err)
	}
	stored := map[alertIdentity][]storedAlert{}
	for rows.Next() {
		var row storedAlert
		var severity string
		if err := rows.Scan(&row.id, &row.alert.Key.Cluster, &row.alert.Key.Service, &row.alert.Kind, &severity,
			&row.alert.Title, &row.alert.Text, &row.alert.At, &row.alert.Tab); err != nil {
			rows.Close()
			return fmt.Errorf("replace alerts: scan: %w", err)
		}
		row.alert.Severity = model.AlertSeverity(severity)
		identity := alertIdentity{row.alert.Key.Cluster, row.alert.Key.Service, row.alert.Kind}
		stored[identity] = append(stored[identity], row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("replace alerts: read: %w", err)
	}

	batch := &pgx.Batch{}
	wanted := map[alertIdentity]bool{}
	for _, alert := range alerts {
		identity := alertIdentity{alert.Key.Cluster, alert.Key.Service, alert.Kind}
		if wanted[identity] {
			continue // the identity is unique by contract; ignore a repeat rather than store duplicates
		}
		wanted[identity] = true
		existing := stored[identity]
		switch {
		case len(existing) == 0:
			batch.Queue(
				`insert into alerts (account_id, region, cluster_name, service_name, kind, severity, title, text, at, tab, polled_at)
				 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
				t.AccountID, t.Region, alert.Key.Cluster, alert.Key.Service, alert.Kind, string(alert.Severity),
				alert.Title, alert.Text, alert.At, alert.Tab)
		case len(existing) == 1 && sameAlertContent(existing[0].alert, alert):
			// unchanged: no write
		default:
			batch.Queue(
				`update alerts set severity = $2, title = $3, text = $4, at = $5, tab = $6, polled_at = now() where id = $1`,
				existing[0].id, string(alert.Severity), alert.Title, alert.Text, alert.At, alert.Tab)
			for _, duplicate := range existing[1:] {
				batch.Queue(`delete from alerts where id = $1`, duplicate.id)
			}
		}
	}
	for identity, existing := range stored {
		if wanted[identity] {
			continue
		}
		for _, row := range existing {
			batch.Queue(`delete from alerts where id = $1`, row.id)
		}
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
