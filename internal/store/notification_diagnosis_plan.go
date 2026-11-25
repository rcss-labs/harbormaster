package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PlanDiagnosisDeliveries is level-based and idempotent. For every route and unresolved incident whose investigation
// is done with a summary, where the route is enabled, has the switch on and is not PagerDuty, its opened row was
// delivered and no diagnosis row exists, it inserts the diagnosis row. The row copies the opened row's severity, key,
// kind and alert snapshot, names the investigation in its details and is due at now, or at the end of quiet hours when
// heldUntil says the severity is held for that route. Its name, destination and target are the route's current ones. It
// plans at most limit rows and returns how many it inserted; a row that fails does not stop the others.
func (s *Store) PlanDiagnosisDeliveries(ctx context.Context, now time.Time, heldUntil func(r NotificationRoute, severity string) (time.Time, bool), limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	rows, err := s.Pool.Query(ctx, `select d.id, v.id
		from notification_deliveries d
		join notification_routes r on r.id = d.route_id and r.enabled and r.investigate and r.destination <> 'pagerduty'
		join notification_incidents i on i.id = d.incident_id and i.resolved_at is null
		join agent_investigation_incidents l on l.incident_id = i.id
		join agent_investigations v on v.id = l.investigation_id and v.state = 'done' and v.summary <> ''
		where d.event = 'opened' and d.status = 'delivered'
		  and not exists (select 1 from notification_deliveries x
		    where x.route_id = d.route_id and x.incident_id = d.incident_id and x.event = 'diagnosis')
		order by d.id
		limit $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("plan diagnosis deliveries: %w", err)
	}
	investigationOf := map[int64]int64{} // opened row id -> investigation id
	var openedIDs []int64
	for rows.Next() {
		var openedID, investigationID int64
		if err := rows.Scan(&openedID, &investigationID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("plan diagnosis deliveries: %w", err)
		}
		investigationOf[openedID] = investigationID
		openedIDs = append(openedIDs, openedID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("plan diagnosis deliveries: %w", err)
	}
	if len(openedIDs) == 0 {
		return 0, nil
	}
	openedRows, err := s.Pool.Query(ctx, `select `+notificationDeliveryColumns+` from notification_deliveries
		where id = any($1) order by id`, openedIDs)
	if err != nil {
		return 0, fmt.Errorf("plan diagnosis deliveries: %w", err)
	}
	opened, err := collectClaimedDeliveries(openedRows, "plan diagnosis deliveries")
	if err != nil {
		return 0, err
	}

	routes, err := s.ListNotificationRoutes(ctx)
	if err != nil {
		return 0, err
	}
	routeByID := make(map[int64]NotificationRoute, len(routes))
	for _, r := range routes {
		routeByID[r.ID] = r
	}

	planned := 0
	var errs []error
	for _, o := range opened {
		route, ok := routeByID[*o.RouteID]
		if !ok {
			continue // deleted since the query
		}
		due, held := now, (*time.Time)(nil)
		if until, isHeld := heldUntil(route, o.Severity); isHeld {
			due, held = until, &until
		}
		d := o
		d.RouteName, d.Destination, d.Target = route.Name, route.Destination, route.TargetLabel()
		d.ID, d.Details.InvestigationID, d.CreatedAt, d.Note = 0, investigationOf[o.ID], now, ""
		inserted, err := s.InsertDiagnosisDelivery(ctx, d, due, held)
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break // the rest would fail the same way
			}
			continue
		}
		if inserted {
			planned++
		}
	}
	return planned, errors.Join(errs...)
}
