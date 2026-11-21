package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// NotificationIncident is one continuous period during which an alert was present. A flap shorter than the
// resolve grace stays one incident.
type NotificationIncident struct {
	ID                       int64
	Key                      model.ServiceKey
	Kind                     string
	Severity                 model.AlertSeverity
	Title, Text              string
	OpenedAt                 time.Time
	MissingSince, ResolvedAt *time.Time
}

const notificationIncidentColumns = `id, account_id, region, cluster_name, service_name, kind, severity, title, text,
	opened_at, missing_since, resolved_at`

func scanNotificationIncident(row pgx.Row) (NotificationIncident, error) {
	var i NotificationIncident
	var severity string
	err := row.Scan(&i.ID, &i.Key.AccountID, &i.Key.Region, &i.Key.Cluster, &i.Key.Service, &i.Kind, &severity,
		&i.Title, &i.Text, &i.OpenedAt, &i.MissingSince, &i.ResolvedAt)
	i.Severity = model.AlertSeverity(severity)
	return i, err
}

type incidentIdentity struct{ account, region, cluster, service, kind string }

// SyncNotificationIncidents makes the open incidents match the non-info rows of alerts, in one transaction.
// A new identity inserts an incident (opened_at = now; a concurrent writer's insert is ignored). A present one
// clears missing_since and updates severity, title and text when they changed. An absent one gets
// missing_since = now once, and resolved_at = now once it has been absent for grace. It writes only on a change
// (no write per cycle for a steady estate). It returns the open incidents and those resolved within
// recentResolved.
func (s *Store) SyncNotificationIncidents(ctx context.Context, now time.Time, grace, recentResolved time.Duration) (open, resolved []NotificationIncident, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// One syncer at a time: concurrent syncs (a failover overlap) queue instead of racing on the same rows.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('notification_incidents'))`); err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: %w", err)
	}

	alertRows, err := tx.Query(ctx, `select account_id, region, cluster_name, service_name, kind, severity, title, text
		from alerts where severity <> 'info' order by id`)
	if err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: read alerts: %w", err)
	}
	type alertRow struct{ severity, title, text string }
	alerts := map[incidentIdentity]alertRow{}
	for alertRows.Next() {
		var id incidentIdentity
		var row alertRow
		if err := alertRows.Scan(&id.account, &id.region, &id.cluster, &id.service, &id.kind, &row.severity, &row.title, &row.text); err != nil {
			alertRows.Close()
			return nil, nil, fmt.Errorf("sync notification incidents: scan alert: %w", err)
		}
		alerts[id] = row
	}
	alertRows.Close()
	if err := alertRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: read alerts: %w", err)
	}

	openRows, err := tx.Query(ctx, `select `+notificationIncidentColumns+` from notification_incidents where resolved_at is null`)
	if err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: read incidents: %w", err)
	}
	existing := map[incidentIdentity]NotificationIncident{}
	for openRows.Next() {
		incident, err := scanNotificationIncident(openRows)
		if err != nil {
			openRows.Close()
			return nil, nil, fmt.Errorf("sync notification incidents: scan incident: %w", err)
		}
		existing[incidentIdentity{incident.Key.AccountID, incident.Key.Region, incident.Key.Cluster, incident.Key.Service, incident.Kind}] = incident
	}
	openRows.Close()
	if err := openRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: read incidents: %w", err)
	}

	batch := &pgx.Batch{}
	for id, alert := range alerts {
		incident, found := existing[id]
		switch {
		case !found:
			batch.Queue(`insert into notification_incidents (account_id, region, cluster_name, service_name, kind, severity, title, text, opened_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict do nothing`,
				id.account, id.region, id.cluster, id.service, id.kind, alert.severity, alert.title, alert.text, now)
		case incident.MissingSince != nil || string(incident.Severity) != alert.severity || incident.Title != alert.title || incident.Text != alert.text:
			batch.Queue(`update notification_incidents set missing_since = null, severity = $2, title = $3, text = $4 where id = $1`,
				incident.ID, alert.severity, alert.title, alert.text)
		}
	}
	for id, incident := range existing {
		if _, present := alerts[id]; present {
			continue
		}
		switch {
		case incident.MissingSince == nil:
			batch.Queue(`update notification_incidents set missing_since = $2 where id = $1`, incident.ID, now)
		case now.Sub(*incident.MissingSince) >= grace:
			batch.Queue(`update notification_incidents set resolved_at = $2 where id = $1`, incident.ID, now)
		}
	}
	if batch.Len() > 0 {
		results := tx.SendBatch(ctx, batch)
		for range batch.Len() {
			if _, err := results.Exec(); err != nil {
				results.Close()
				return nil, nil, fmt.Errorf("sync notification incidents: write: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return nil, nil, fmt.Errorf("sync notification incidents: write: %w", err)
		}
	}

	open, err = queryNotificationIncidents(ctx, tx, `where resolved_at is null`)
	if err != nil {
		return nil, nil, err
	}
	resolved, err = queryNotificationIncidents(ctx, tx, `where resolved_at >= $1`, now.Add(-recentResolved))
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("sync notification incidents: %w", err)
	}
	return open, resolved, nil
}

func queryNotificationIncidents(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]NotificationIncident, error) {
	rows, err := tx.Query(ctx, `select `+notificationIncidentColumns+` from notification_incidents `+where+` order by id`, args...)
	if err != nil {
		return nil, fmt.Errorf("sync notification incidents: list: %w", err)
	}
	defer rows.Close()
	out := []NotificationIncident{}
	for rows.Next() {
		incident, err := scanNotificationIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("sync notification incidents: scan: %w", err)
		}
		out = append(out, incident)
	}
	return out, rows.Err()
}

// NotificationDelivery is one row of the queue and of the delivery log. Route name, destination and target are
// snapshots so the log survives a deleted or edited route.
type NotificationDelivery struct {
	ID                                                 int64
	RouteID                                            *int64
	RouteName, Destination, Target                     string
	IncidentID                                         *int64
	Event                                              string
	Seq                                                int
	Severity, AlertTitle, AlertText                    string
	Key                                                model.ServiceKey
	Kind                                               string
	Status                                             string
	Attempts, Retries                                  int
	NextAttemptAt, HeldUntil, ClaimedUntil, FinishedAt *time.Time
	StatusCode, DurationMS                             *int
	Note                                               string
	CreatedAt                                          time.Time
}

const notificationDeliveryColumns = `id, route_id, route_name, destination, target, incident_id, event, seq, severity,
	alert_title, alert_text, account_id, region, cluster_name, service_name, kind, status, attempts, retries,
	next_attempt_at, held_until, claimed_until, status_code, duration_ms, note, created_at, finished_at`

func scanNotificationDelivery(row pgx.Row) (NotificationDelivery, error) {
	var d NotificationDelivery
	err := row.Scan(&d.ID, &d.RouteID, &d.RouteName, &d.Destination, &d.Target, &d.IncidentID, &d.Event, &d.Seq, &d.Severity,
		&d.AlertTitle, &d.AlertText, &d.Key.AccountID, &d.Key.Region, &d.Key.Cluster, &d.Key.Service, &d.Kind, &d.Status,
		&d.Attempts, &d.Retries, &d.NextAttemptAt, &d.HeldUntil, &d.ClaimedUntil, &d.StatusCode, &d.DurationMS, &d.Note,
		&d.CreatedAt, &d.FinishedAt)
	return d, err
}

const insertNotificationDeliverySQL = `insert into notification_deliveries
	(route_id, route_name, destination, target, incident_id, event, seq, severity, alert_title, alert_text,
	 account_id, region, cluster_name, service_name, kind, status, attempts, next_attempt_at, held_until,
	 status_code, duration_ms, note, created_at, finished_at)
	values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
	        coalesce($23, now()), $24)`

func deliveryArgs(d NotificationDelivery) []any {
	var created *time.Time
	if !d.CreatedAt.IsZero() {
		created = &d.CreatedAt
	}
	return []any{d.RouteID, d.RouteName, d.Destination, d.Target, d.IncidentID, d.Event, d.Seq, d.Severity, d.AlertTitle, d.AlertText,
		d.Key.AccountID, d.Key.Region, d.Key.Cluster, d.Key.Service, d.Kind, d.Status, d.Attempts, d.NextAttemptAt, d.HeldUntil,
		d.StatusCode, d.DurationMS, d.Note, created, d.FinishedAt}
}

// PlanNotificationDelivery inserts d; it returns false when a row with the same route, incident, event and seq
// already exists, so planning the same delivery twice (or from two replicas) sends once.
func (s *Store) PlanNotificationDelivery(ctx context.Context, d NotificationDelivery) (bool, error) {
	tag, err := s.Pool.Exec(ctx, insertNotificationDeliverySQL+` on conflict (route_id, incident_id, event, seq)
		where incident_id is not null and route_id is not null do nothing`, deliveryArgs(d)...)
	if err != nil {
		return false, fmt.Errorf("plan notification delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// InsertNotificationTest records a finished test delivery (no incident, no unique key).
func (s *Store) InsertNotificationTest(ctx context.Context, d NotificationDelivery) (NotificationDelivery, error) {
	d.IncidentID, d.Event = nil, "test"
	row := s.Pool.QueryRow(ctx, insertNotificationDeliverySQL+` returning `+notificationDeliveryColumns, deliveryArgs(d)...)
	out, err := scanNotificationDelivery(row)
	if err != nil {
		return NotificationDelivery{}, fmt.Errorf("record notification test: %w", err)
	}
	return out, nil
}

// DeliveryPlanState is what planning needs to know about one route x incident.
type DeliveryPlanState struct {
	OpenedStatus         string     // "" when no opened row exists
	LastSentAt           *time.Time // created_at of the newest opened/repeat row that is delivered or failed
	MaxRepeatSeq         int
	HasHeld, HasResolved bool
}

// NotificationPlanStates summarises the delivery rows of the given incidents per {route id, incident id}. Rows
// whose route was deleted are not included.
func (s *Store) NotificationPlanStates(ctx context.Context, incidentIDs []int64) (map[[2]int64]DeliveryPlanState, error) {
	out := map[[2]int64]DeliveryPlanState{}
	if len(incidentIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `select route_id, incident_id,
		coalesce(max(status) filter (where event = 'opened'), ''),
		max(created_at) filter (where event in ('opened', 'repeat') and status in ('delivered', 'failed')),
		coalesce(max(seq) filter (where event = 'repeat'), 0),
		coalesce(bool_or(status = 'held' and event in ('opened', 'repeat')), false),
		coalesce(bool_or(event = 'resolved'), false)
		from notification_deliveries
		where incident_id = any($1) and route_id is not null
		group by route_id, incident_id`, incidentIDs)
	if err != nil {
		return nil, fmt.Errorf("notification plan states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var routeID, incidentID int64
		var st DeliveryPlanState
		if err := rows.Scan(&routeID, &incidentID, &st.OpenedStatus, &st.LastSentAt, &st.MaxRepeatSeq, &st.HasHeld, &st.HasResolved); err != nil {
			return nil, fmt.Errorf("notification plan states: %w", err)
		}
		out[[2]int64{routeID, incidentID}] = st
	}
	return out, rows.Err()
}

// ClaimDueNotificationDeliveries takes up to limit pending rows that are due and have no live lease, using
// FOR UPDATE SKIP LOCKED so concurrent claimers get disjoint rows, and leases them for lease.
func (s *Store) ClaimDueNotificationDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]NotificationDelivery, error) {
	rows, err := s.Pool.Query(ctx, `with due as (
			select id from notification_deliveries
			where status = 'pending' and next_attempt_at <= $1 and (claimed_until is null or claimed_until <= $1)
			order by next_attempt_at, id limit $3 for update skip locked)
		update notification_deliveries d set claimed_until = $1::timestamptz + $2::interval
		from due where d.id = due.id returning `+prefixColumns("d", notificationDeliveryColumns),
		now, fmt.Sprintf("%d microseconds", lease.Microseconds()), limit)
	if err != nil {
		return nil, fmt.Errorf("claim notification deliveries: %w", err)
	}
	defer rows.Close()
	out := []NotificationDelivery{}
	for rows.Next() {
		d, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("claim notification deliveries: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// prefixColumns qualifies a comma-separated column list with a table alias.
func prefixColumns(alias, columns string) string {
	out := ""
	start := 0
	for i := 0; i <= len(columns); i++ {
		if i == len(columns) || columns[i] == ',' {
			name := columns[start:i]
			for len(name) > 0 && (name[0] == ' ' || name[0] == '\n' || name[0] == '\t') {
				name = name[1:]
			}
			if name != "" {
				if out != "" {
					out += ", "
				}
				out += alias + "." + name
			}
			start = i + 1
		}
	}
	return out
}

// DeliveryOutcome is the result of one send attempt. Delivered settles the row; otherwise Retryable puts it back
// to pending at RetryAt and anything else fails it.
type DeliveryOutcome struct {
	Delivered, Retryable bool
	RetryAt              time.Time
	StatusCode           int
	DurationMS           int
	Note                 string
}

// FinishNotificationDelivery records one attempt and clears the lease. It does nothing unless the row is still
// pending, so a late writer cannot undo a newer state.
func (s *Store) FinishNotificationDelivery(ctx context.Context, id int64, attempts int, o DeliveryOutcome, now time.Time) error {
	status, next, finished := "failed", (*time.Time)(nil), &now
	switch {
	case o.Delivered:
		status = "delivered"
	case o.Retryable:
		status, next, finished = "pending", &o.RetryAt, nil
	}
	var code, duration *int
	if o.StatusCode != 0 {
		code = &o.StatusCode
	}
	if o.DurationMS != 0 || o.Delivered {
		duration = &o.DurationMS
	}
	_, err := s.Pool.Exec(ctx, `update notification_deliveries set status = $2, attempts = $3, next_attempt_at = $4,
		claimed_until = null, status_code = $5, duration_ms = $6, note = $7, finished_at = $8
		where id = $1 and status = 'pending'`, id, status, attempts, next, code, duration, o.Note, finished)
	if err != nil {
		return fmt.Errorf("finish notification delivery: %w", err)
	}
	return nil
}

// ReleaseHeldNotificationDeliveries turns held rows whose held_until passed into pending (incident still open) or
// cancelled with resolvedNote (incident resolved).
func (s *Store) ReleaseHeldNotificationDeliveries(ctx context.Context, now time.Time, resolvedNote string) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("release held notifications: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `update notification_deliveries d set status = 'cancelled', note = $2, finished_at = $1
		from notification_incidents i
		where d.incident_id = i.id and d.status = 'held' and d.held_until <= $1 and i.resolved_at is not null`, now, resolvedNote); err != nil {
		return fmt.Errorf("release held notifications: %w", err)
	}
	if _, err := tx.Exec(ctx, `update notification_deliveries set status = 'pending', next_attempt_at = held_until
		where status = 'held' and held_until <= $1`, now); err != nil {
		return fmt.Errorf("release held notifications: %w", err)
	}
	return tx.Commit(ctx)
}

// CancelUnsentNotificationDeliveries cancels the opened and repeat rows of the given incidents that are still
// pending or held and not under a live lease (a row being sent is left to finish).
func (s *Store) CancelUnsentNotificationDeliveries(ctx context.Context, incidentIDs []int64, note string, now time.Time) error {
	if len(incidentIDs) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'cancelled', note = $2, finished_at = $3, claimed_until = null
		where incident_id = any($1) and event in ('opened', 'repeat') and status in ('pending', 'held')
		  and (claimed_until is null or claimed_until <= $3)`, incidentIDs, note, now)
	if err != nil {
		return fmt.Errorf("cancel unsent notifications: %w", err)
	}
	return nil
}

// RetryNotificationDelivery resets a failed row to pending with attempts 0 and one more manual retry. It returns
// ErrNotFound for an unknown id and ErrConflict when the row is not failed or its route was deleted.
func (s *Store) RetryNotificationDelivery(ctx context.Context, id int64, now time.Time) (NotificationDelivery, error) {
	row := s.Pool.QueryRow(ctx, `update notification_deliveries set status = 'pending', attempts = 0, retries = retries + 1,
		next_attempt_at = $2, finished_at = null, claimed_until = null
		where id = $1 and status = 'failed' and route_id is not null
		returning `+notificationDeliveryColumns, id, now)
	d, err := scanNotificationDelivery(row)
	if err == nil {
		return d, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return NotificationDelivery{}, fmt.Errorf("retry notification delivery: %w", err)
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `select exists(select 1 from notification_deliveries where id = $1)`, id).Scan(&exists); err != nil {
		return NotificationDelivery{}, fmt.Errorf("retry notification delivery: %w", err)
	}
	if !exists {
		return NotificationDelivery{}, ErrNotFound
	}
	return NotificationDelivery{}, ErrConflict
}

// DeliveryFilter selects log rows. Status is "" (all) or "failed".
type DeliveryFilter struct {
	Status string
	Before *DeliveryCursor
	Limit  int
}

// DeliveryCursor is the keyset position after which the next page starts.
type DeliveryCursor struct {
	CreatedAt time.Time
	ID        int64
}

// ListNotificationDeliveries returns log rows newest first and the cursor of the next page (nil at the end).
func (s *Store) ListNotificationDeliveries(ctx context.Context, f DeliveryFilter) ([]NotificationDelivery, *DeliveryCursor, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	var before *time.Time
	var beforeID int64
	if f.Before != nil {
		before, beforeID = &f.Before.CreatedAt, f.Before.ID
	}
	rows, err := s.Pool.Query(ctx, `select `+notificationDeliveryColumns+` from notification_deliveries
		where ($1 = '' or status = $1) and ($2::timestamptz is null or (created_at, id) < ($2, $3))
		order by created_at desc, id desc limit $4`, f.Status, before, beforeID, limit+1)
	if err != nil {
		return nil, nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	defer rows.Close()
	out := []NotificationDelivery{}
	for rows.Next() {
		d, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("list notification deliveries: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *DeliveryCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = &DeliveryCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

// NotificationSummary feeds the failed-deliveries banner and the Settings badge.
type NotificationSummary struct {
	Failed24h       int     // failed deliveries (tests included) created in the last 24 hours
	FailingRouteIDs []int64 // routes whose last 3 non-test finished deliveries all failed
}

// NotificationSummary counts recent failures and finds the routes that keep failing.
func (s *Store) NotificationSummary(ctx context.Context, now time.Time) (NotificationSummary, error) {
	var sum NotificationSummary
	if err := s.Pool.QueryRow(ctx, `select count(*) from notification_deliveries where status = 'failed' and created_at >= $1`,
		now.Add(-24*time.Hour)).Scan(&sum.Failed24h); err != nil {
		return NotificationSummary{}, fmt.Errorf("notification summary: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `select route_id from (
			select route_id, status, row_number() over (partition by route_id order by created_at desc, id desc) as n
			from notification_deliveries
			where route_id is not null and event <> 'test' and status in ('delivered', 'failed')
		) last_three where n <= 3 group by route_id having count(*) = 3 and bool_and(status = 'failed') order by route_id`)
	if err != nil {
		return NotificationSummary{}, fmt.Errorf("notification summary: %w", err)
	}
	defer rows.Close()
	sum.FailingRouteIDs = []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return NotificationSummary{}, fmt.Errorf("notification summary: %w", err)
		}
		sum.FailingRouteIDs = append(sum.FailingRouteIDs, id)
	}
	return sum, rows.Err()
}
