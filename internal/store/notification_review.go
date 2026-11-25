package store

import (
	"context"
	"fmt"
	"time"
)

// InsertReviewDelivery plans the message of review run runID for d's route, due at dueAt. It forces the event, seq 0,
// status pending, severity info, no incident, no next_attempt_at, no lease, no result and no attempts: the older
// claim reads next_attempt_at and would render the row as an Opened message. heldUntil is stored for display only
// (quiet hours are already part of dueAt). It returns false when the route and run already have a review row, so a
// row exists at most once; it is sent at least once.
func (s *Store) InsertReviewDelivery(ctx context.Context, d NotificationDelivery, runID int64, dueAt time.Time, heldUntil *time.Time) (bool, error) {
	if d.RouteID == nil {
		return false, fmt.Errorf("insert review delivery: a route is required")
	}
	d.Event, d.Seq, d.Severity, d.Status, d.IncidentID = deliveryEventReview, 0, "info", "pending", nil
	d.NextAttemptAt, d.HeldUntil = nil, heldUntil
	d.ClaimedUntil, d.FinishedAt, d.StatusCode, d.DurationMS, d.Attempts = nil, nil, nil, nil, 0
	d.Details.ReviewRunID = runID
	tag, err := s.Pool.Exec(ctx, `insert into notification_deliveries
		(route_id, route_name, destination, target, incident_id, event, seq, severity, alert_title, alert_text,
		 account_id, region, cluster_name, service_name, kind, status, attempts, next_attempt_at, held_until,
		 status_code, duration_ms, note, created_at, finished_at, details, claimed_until, diagnosis_due_at, review_run_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		        coalesce($23, now()), $24, $25, $26, $27, $28)
		on conflict (route_id, review_run_id) where review_run_id is not null and route_id is not null do nothing`,
		append(deliveryArgs(d), dueAt, runID)...)
	if err != nil {
		return false, fmt.Errorf("insert review delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimReviewDeliveries is ClaimDiagnosisDeliveries for review rows. It never returns any other event.
func (s *Store) ClaimReviewDeliveries(ctx context.Context, now time.Time, lease time.Duration, f ClaimFilter) ([]NotificationDelivery, error) {
	return s.claimFollowUpDeliveries(ctx, deliveryEventReview, now, lease, f)
}

// CancelReviewDeliveries cancels the pending review rows of run runID (of one route, or of every route when routeID
// is nil) that are not under a live lease with note: a row being sent is left to finish. It returns how many it
// cancelled.
func (s *Store) CancelReviewDeliveries(ctx context.Context, runID int64, routeID *int64, note string, now time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `update notification_deliveries
		set status = 'cancelled', note = $3, finished_at = $4, claimed_until = null
		where event = 'review' and review_run_id = $1 and ($2::bigint is null or route_id = $2)
		  and status = 'pending' and (claimed_until is null or claimed_until <= $4)`, runID, routeID, note, now)
	if err != nil {
		return 0, fmt.Errorf("cancel review deliveries: %w", err)
	}
	return tag.RowsAffected(), nil
}
