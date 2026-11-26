package store

import (
	"context"
	"fmt"
	"time"
)

// InsertDiagnosisDelivery plans the diagnosis message of d's route and incident, due at dueAt. It forces the event,
// seq 0, status pending, no next_attempt_at, no lease, no result and no attempts: the older claim reads
// next_attempt_at and would render the row as an Opened message. heldUntil is stored for display only (quiet hours
// are already part of dueAt). It returns false when the route and incident already have a diagnosis row, so a row
// exists at most once; it is sent at least once.
func (s *Store) InsertDiagnosisDelivery(ctx context.Context, d NotificationDelivery, dueAt time.Time, heldUntil *time.Time) (bool, error) {
	if d.RouteID == nil || d.IncidentID == nil {
		return false, fmt.Errorf("insert diagnosis delivery: a route and an incident are required")
	}
	d.Event, d.Seq, d.Status, d.NextAttemptAt, d.HeldUntil = "diagnosis", 0, "pending", nil, heldUntil
	d.ClaimedUntil, d.FinishedAt, d.StatusCode, d.DurationMS, d.Attempts = nil, nil, nil, nil, 0
	tag, err := s.Pool.Exec(ctx, `insert into notification_deliveries
		(route_id, route_name, destination, target, incident_id, event, seq, severity, alert_title, alert_text,
		 account_id, region, cluster_name, service_name, kind, status, attempts, next_attempt_at, held_until,
		 status_code, duration_ms, note, created_at, finished_at, details, claimed_until, diagnosis_due_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		        coalesce($23, now()), $24, $25, $26, $27)
		on conflict (route_id, incident_id, event, seq) where incident_id is not null and route_id is not null do nothing`,
		append(deliveryArgs(d), dueAt)...)
	if err != nil {
		return false, fmt.Errorf("insert diagnosis delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// The two follow-up events. They share one lane: a due time in diagnosis_due_at, never next_attempt_at, and their own
// claim; the older claim and the older held release cannot see them.
const (
	deliveryEventDiagnosis = "diagnosis"
	deliveryEventReview    = "review"
)

// ClaimDiagnosisDeliveries is ClaimNotificationDeliveries for diagnosis rows: pending, due by diagnosis_due_at and
// without a live lease, within the filter, leased for lease. It never returns any other event.
func (s *Store) ClaimDiagnosisDeliveries(ctx context.Context, now time.Time, lease time.Duration, f ClaimFilter) ([]NotificationDelivery, error) {
	return s.claimFollowUpDeliveries(ctx, deliveryEventDiagnosis, now, lease, f)
}

// claimFollowUpDeliveries claims the due rows of one follow-up event. The event is chosen here from the two constants,
// never from caller text, because it is written into the statement.
func (s *Store) claimFollowUpDeliveries(ctx context.Context, event string, now time.Time, lease time.Duration, f ClaimFilter) ([]NotificationDelivery, error) {
	var literal string
	switch event {
	case deliveryEventDiagnosis:
		literal = "'diagnosis'"
	case deliveryEventReview:
		literal = "'review'"
	default:
		return nil, fmt.Errorf("claim %s deliveries: not a follow-up event", event)
	}
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`with ranked as (
			select id, row_number() over (partition by coalesce(route_id, -id) order by diagnosis_due_at, id) as n
			from notification_deliveries
			where event = %[1]s and status = 'pending' and diagnosis_due_at <= $1 and (claimed_until is null or claimed_until <= $1)
			  and (route_id is null or route_id <> all(coalesce($5::bigint[], '{}')))),
		due as (
			select d.id from notification_deliveries d join ranked using (id)
			where ($4 <= 0 or ranked.n <= $4)
			  and d.event = %[1]s and d.status = 'pending' and d.diagnosis_due_at <= $1 and (d.claimed_until is null or d.claimed_until <= $1)
			order by d.diagnosis_due_at, d.id limit $3 for update of d skip locked)
		update notification_deliveries d set claimed_until = $1::timestamptz + $2::interval
		from due where d.id = due.id returning `, literal)+prefixColumns("d", notificationDeliveryColumns),
		now, fmt.Sprintf("%d microseconds", lease.Microseconds()), f.Limit, f.PerRoute, f.Exclude)
	if err != nil {
		return nil, fmt.Errorf("claim %s deliveries: %w", event, err)
	}
	return collectClaimedDeliveries(rows, "claim "+event+" deliveries")
}
