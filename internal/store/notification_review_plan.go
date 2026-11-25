package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReviewMessageMaxAge is how long after a run finished its messages may still be planned. A task that was down longer
// than that does not send yesterday's result, and a run whose rows were already deleted is never planned again.
const ReviewMessageMaxAge = 24 * time.Hour

// PlanReviewDeliveries plans the messages of finished review runs, exactly once per run. Each run that is done, has a
// summary and has no delivery_planned_at is handled in one transaction that locks its row: for every route on its
// review that is enabled and not PagerDuty it inserts a review row, due at now or at the end of quiet hours when
// heldUntil says severity info is held for that route, and then it sets delivery_planned_at. A run whose review is
// gone, or that finished more than 24 hours ago, is marked with no rows. A run another planner holds is left to it.
// It handles at most limit runs, on one connection at a time, and returns how many rows it inserted; a run that fails
// leaves nothing behind, is tried again by the next call, and does not stop the others.
func (s *Store) PlanReviewDeliveries(ctx context.Context, now time.Time, heldUntil func(r NotificationRoute, severity string) (time.Time, bool), limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	rows, err := s.Pool.Query(ctx, `select id from agent_review_runs
		where state = 'done' and summary <> '' and delivery_planned_at is null order by id limit $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("plan review deliveries: %w", err)
	}
	runIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, fmt.Errorf("plan review deliveries: %w", err)
	}
	planned := 0
	var errs []error
	for _, runID := range runIDs {
		inserted := 0
		err := s.InTx(ctx, func(t Tx) error {
			var err error
			inserted, err = t.planReviewRun(ctx, runID, now, heldUntil)
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("plan review deliveries of run %d: %w", runID, err))
			if ctx.Err() != nil {
				break // the rest would fail the same way
			}
			continue
		}
		planned += inserted
	}
	return planned, errors.Join(errs...)
}

func (t Tx) planReviewRun(ctx context.Context, runID int64, now time.Time, heldUntil func(r NotificationRoute, severity string) (time.Time, bool)) (int, error) {
	var reviewID *int64
	var name string
	var finishedAt *time.Time
	err := t.tx.QueryRow(ctx, `select review_id, review_name, finished_at from agent_review_runs
		where id = $1 and state = 'done' and summary <> '' and delivery_planned_at is null
		for update skip locked`, runID).Scan(&reviewID, &name, &finishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // another planner has it, or had it
	}
	if err != nil {
		return 0, err
	}
	var routes []NotificationRoute
	if reviewID != nil && finishedAt != nil && now.Sub(*finishedAt) <= ReviewMessageMaxAge {
		rows, err := t.tx.Query(ctx, `select `+prefixColumns("r", notificationRouteColumns)+` from notification_routes r
			join agent_review_routes l on l.route_id = r.id
			where l.review_id = $1 and r.enabled and r.destination <> 'pagerduty' order by r.id`, *reviewID)
		if err != nil {
			return 0, err
		}
		routes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (NotificationRoute, error) { return scanNotificationRoute(row) })
		if err != nil {
			return 0, err
		}
	}
	inserted := 0
	for _, route := range routes {
		due, held := now, (*time.Time)(nil)
		if until, isHeld := heldUntil(route, "info"); isHeld {
			due, held = until, &until
		}
		ok, err := t.InsertReviewDelivery(ctx, NotificationDelivery{
			RouteID: &route.ID, RouteName: route.Name, Destination: route.Destination, Target: route.TargetLabel(),
			AlertTitle: name, Kind: deliveryEventReview, CreatedAt: now,
		}, runID, due, held)
		if err != nil {
			return 0, err
		}
		if ok {
			inserted++
		}
	}
	if _, err := t.tx.Exec(ctx, `update agent_review_runs set delivery_planned_at = $2 where id = $1`, runID, now); err != nil {
		return 0, err
	}
	return inserted, nil
}

// ReviewHasRoute reports whether the review exists and sends to the route.
func (s *Store) ReviewHasRoute(ctx context.Context, reviewID, routeID int64) (bool, error) {
	var has bool
	if err := s.Pool.QueryRow(ctx, `select exists (select 1 from agent_review_routes where review_id = $1 and route_id = $2)`,
		reviewID, routeID).Scan(&has); err != nil {
		return false, fmt.Errorf("review has route: %w", err)
	}
	return has, nil
}
