package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// IncidentMember is one failing service in a group incident, with its most severe counted alert.
type IncidentMember struct {
	Cluster  string `json:"cluster"`
	Service  string `json:"service"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Critical bool   `json:"critical"`
}

// NotificationIncident is one continuous period during which an alert was present (an alert incident), or during
// which a route's cluster or account entry met its threshold (a group incident, GroupRouteID set). A flap shorter
// than the resolve grace stays one incident.
type NotificationIncident struct {
	ID          int64
	Key         model.ServiceKey
	Kind        string
	Severity    model.AlertSeverity
	Title, Text string
	OpenedAt    time.Time
	// PresentSince is when the alert was last absent-then-back (or opened); the settle time counts from it, so a
	// scale-out that flaps inside the resolve grace does not add up to a notification.
	PresentSince             time.Time
	MissingSince, ResolvedAt *time.Time
	// GroupRouteID is 0 for an alert incident, else the route that owns this group; ScopeKind is "alert",
	// "cluster" or "account".
	GroupRouteID              int64
	ScopeKind                 string
	Threshold, Failing, Total int
	Members                   []IncidentMember
	// Generation rises each time a group gets worse (a service joins, or the severity rises), never when it improves.
	Generation int
}

const notificationIncidentColumns = `id, account_id, region, cluster_name, service_name, kind, severity, title, text,
	opened_at, present_since, missing_since, resolved_at, group_route_id, scope_kind, coalesce(threshold, 0), coalesce(failing, 0),
	coalesce(total, 0), members, generation`

func scanNotificationIncident(row pgx.Row) (NotificationIncident, error) {
	var i NotificationIncident
	var severity string
	var members []byte
	err := row.Scan(&i.ID, &i.Key.AccountID, &i.Key.Region, &i.Key.Cluster, &i.Key.Service, &i.Kind, &severity,
		&i.Title, &i.Text, &i.OpenedAt, &i.PresentSince, &i.MissingSince, &i.ResolvedAt, &i.GroupRouteID, &i.ScopeKind, &i.Threshold,
		&i.Failing, &i.Total, &members, &i.Generation)
	i.Severity = model.AlertSeverity(severity)
	if err == nil {
		err = json.Unmarshal(members, &i.Members)
	}
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

	openRows, err := tx.Query(ctx, `select `+notificationIncidentColumns+` from notification_incidents where resolved_at is null and group_route_id = 0`)
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
			batch.Queue(`insert into notification_incidents (account_id, region, cluster_name, service_name, kind, severity, title, text, opened_at, present_since)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9) on conflict do nothing`,
				id.account, id.region, id.cluster, id.service, id.kind, alert.severity, alert.title, alert.text, now)
		case incident.MissingSince != nil || string(incident.Severity) != alert.severity || incident.Title != alert.title || incident.Text != alert.text:
			batch.Queue(`update notification_incidents set present_since = case when missing_since is not null then $5 else present_since end,
					missing_since = null, severity = $2, title = $3, text = $4 where id = $1`,
				incident.ID, alert.severity, alert.title, alert.text, now)
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

	open, err = queryNotificationIncidents(ctx, tx, `where resolved_at is null and group_route_id = 0`)
	if err != nil {
		return nil, nil, err
	}
	resolved, err = queryNotificationIncidents(ctx, tx, `where resolved_at >= $1 and group_route_id = 0`, now.Add(-recentResolved))
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

// ListOpenNotificationIncidents returns the open alert incidents without changing anything; the preview reads
// them. Group incidents are not included.
func (s *Store) ListOpenNotificationIncidents(ctx context.Context) ([]NotificationIncident, error) {
	rows, err := s.Pool.Query(ctx, `select `+notificationIncidentColumns+` from notification_incidents
		where resolved_at is null and group_route_id = 0 order by id`)
	if err != nil {
		return nil, fmt.Errorf("list open notification incidents: %w", err)
	}
	defer rows.Close()
	out := []NotificationIncident{}
	for rows.Next() {
		incident, err := scanNotificationIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("list open notification incidents: %w", err)
		}
		out = append(out, incident)
	}
	return out, rows.Err()
}

// GroupState is a group incident that should be open now, as the planner computed it.
type GroupState struct {
	RouteID                   int64
	ScopeKind                 string // "cluster" | "account"
	Key                       model.ServiceKey
	Kind                      string
	Severity                  model.AlertSeverity
	Title, Text               string
	Threshold, Failing, Total int
	Members                   []IncidentMember
}

type groupIdentity struct {
	route                                   int64
	account, region, cluster, service, kind string
}

// SyncNotificationGroups makes the open group incidents equal to desired, in one transaction under the same
// advisory lock as SyncNotificationIncidents. A new group inserts an incident (opened_at = now, generation 0). A
// present one clears missing_since and updates severity, title, text, threshold, failing, total and members when
// any changed; its generation rises by one when a {cluster, service} joined its members or the severity rose from
// warning to danger, and never when it improves (D36). An absent one gets missing_since = now once and resolved_at
// once it has been absent for grace. It writes only on a change. It returns the open group incidents and those
// resolved within recentResolved.
func (s *Store) SyncNotificationGroups(ctx context.Context, now time.Time, grace, recentResolved time.Duration, desired []GroupState) (open, resolved []NotificationIncident, err error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("sync notification groups: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('notification_incidents'))`); err != nil {
		return nil, nil, fmt.Errorf("sync notification groups: %w", err)
	}
	current, err := queryNotificationIncidents(ctx, tx, `where resolved_at is null and group_route_id <> 0`)
	if err != nil {
		return nil, nil, err
	}
	existing := map[groupIdentity]NotificationIncident{}
	for _, incident := range current {
		existing[groupIdentity{incident.GroupRouteID, incident.Key.AccountID, incident.Key.Region, incident.Key.Cluster, incident.Key.Service, incident.Kind}] = incident
	}

	batch := &pgx.Batch{}
	wanted := map[groupIdentity]bool{}
	for _, g := range desired {
		id := groupIdentity{g.RouteID, g.Key.AccountID, g.Key.Region, g.Key.Cluster, g.Key.Service, g.Kind}
		wanted[id] = true
		members, err := json.Marshal(nonNilMembers(g.Members))
		if err != nil {
			return nil, nil, err
		}
		old, found := existing[id]
		if !found {
			batch.Queue(`insert into notification_incidents (account_id, region, cluster_name, service_name, kind, severity, title, text,
					opened_at, group_route_id, scope_kind, threshold, failing, total, members)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) on conflict do nothing`,
				g.Key.AccountID, g.Key.Region, g.Key.Cluster, g.Key.Service, g.Kind, string(g.Severity), g.Title, g.Text,
				now, g.RouteID, g.ScopeKind, g.Threshold, g.Failing, g.Total, members)
			continue
		}
		oldMembers, _ := json.Marshal(nonNilMembers(old.Members))
		changed := old.MissingSince != nil || old.Severity != g.Severity || old.Title != g.Title || old.Text != g.Text ||
			old.Threshold != g.Threshold || old.Failing != g.Failing || old.Total != g.Total || !bytes.Equal(oldMembers, members)
		if !changed {
			continue
		}
		generation := old.Generation
		if joined(old.Members, g.Members) || (old.Severity != model.AlertDanger && g.Severity == model.AlertDanger) {
			generation++
		}
		batch.Queue(`update notification_incidents set missing_since = null, severity = $2, title = $3, text = $4, threshold = $5,
				failing = $6, total = $7, members = $8, generation = $9 where id = $1`,
			old.ID, string(g.Severity), g.Title, g.Text, g.Threshold, g.Failing, g.Total, members, generation)
	}
	for id, incident := range existing {
		if wanted[id] {
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
				return nil, nil, fmt.Errorf("sync notification groups: write: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return nil, nil, fmt.Errorf("sync notification groups: write: %w", err)
		}
	}
	open, err = queryNotificationIncidents(ctx, tx, `where resolved_at is null and group_route_id <> 0`)
	if err != nil {
		return nil, nil, err
	}
	resolved, err = queryNotificationIncidents(ctx, tx, `where resolved_at >= $1 and group_route_id <> 0`, now.Add(-recentResolved))
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("sync notification groups: %w", err)
	}
	return open, resolved, nil
}

func nonNilMembers(m []IncidentMember) []IncidentMember {
	if m == nil {
		return []IncidentMember{}
	}
	return m
}

// joined reports whether next holds a {cluster, service} that old did not.
func joined(old, next []IncidentMember) bool {
	known := make(map[[2]string]bool, len(old))
	for _, m := range old {
		known[[2]string{m.Cluster, m.Service}] = true
	}
	for _, m := range next {
		if !known[[2]string{m.Cluster, m.Service}] {
			return true
		}
	}
	return false
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
	// Details is the snapshot a group message is rendered from; zero for a per-alert message.
	Details DeliveryDetails
}

// DeliveryDetails is what a group delivery's message needs, so a retry or a late send renders what was planned.
type DeliveryDetails struct {
	Scope     string           `json:"scope"`
	Threshold int              `json:"threshold"`
	Failing   int              `json:"failing"`
	Total     int              `json:"total"`
	Members   []IncidentMember `json:"members"`
}

const notificationDeliveryColumns = `id, route_id, route_name, destination, target, incident_id, event, seq, severity,
	alert_title, alert_text, account_id, region, cluster_name, service_name, kind, status, attempts, retries,
	next_attempt_at, held_until, claimed_until, status_code, duration_ms, note, created_at, finished_at, details`

func scanNotificationDelivery(row pgx.Row) (NotificationDelivery, error) {
	var d NotificationDelivery
	var details []byte
	err := row.Scan(&d.ID, &d.RouteID, &d.RouteName, &d.Destination, &d.Target, &d.IncidentID, &d.Event, &d.Seq, &d.Severity,
		&d.AlertTitle, &d.AlertText, &d.Key.AccountID, &d.Key.Region, &d.Key.Cluster, &d.Key.Service, &d.Kind, &d.Status,
		&d.Attempts, &d.Retries, &d.NextAttemptAt, &d.HeldUntil, &d.ClaimedUntil, &d.StatusCode, &d.DurationMS, &d.Note,
		&d.CreatedAt, &d.FinishedAt, &details)
	if err == nil && len(details) > 0 {
		err = json.Unmarshal(details, &d.Details)
	}
	return d, err
}

const insertNotificationDeliverySQL = `insert into notification_deliveries
	(route_id, route_name, destination, target, incident_id, event, seq, severity, alert_title, alert_text,
	 account_id, region, cluster_name, service_name, kind, status, attempts, next_attempt_at, held_until,
	 status_code, duration_ms, note, created_at, finished_at, details, claimed_until)
	values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
	        coalesce($23, now()), $24, $25, $26)`

func deliveryArgs(d NotificationDelivery) []any {
	var created *time.Time
	if !d.CreatedAt.IsZero() {
		created = &d.CreatedAt
	}
	details, _ := json.Marshal(d.Details) // plain fields only: cannot fail
	return []any{d.RouteID, d.RouteName, d.Destination, d.Target, d.IncidentID, d.Event, d.Seq, d.Severity, d.AlertTitle, d.AlertText,
		d.Key.AccountID, d.Key.Region, d.Key.Cluster, d.Key.Service, d.Kind, d.Status, d.Attempts, d.NextAttemptAt, d.HeldUntil,
		d.StatusCode, d.DurationMS, d.Note, created, d.FinishedAt, details, d.ClaimedUntil}
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
	LastSentAt           *time.Time // created_at of the newest opened/repeat/update row that is pending (being sent), delivered or failed
	MaxRepeatSeq         int
	MaxUpdateSeq         int
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
		max(created_at) filter (where event in ('opened', 'repeat', 'update') and status in ('pending', 'delivered', 'failed')),
		coalesce(max(seq) filter (where event = 'repeat'), 0),
		coalesce(max(seq) filter (where event = 'update'), 0),
		coalesce(bool_or(status = 'held' and event in ('opened', 'repeat', 'update')), false),
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
		if err := rows.Scan(&routeID, &incidentID, &st.OpenedStatus, &st.LastSentAt, &st.MaxRepeatSeq, &st.MaxUpdateSeq, &st.HasHeld, &st.HasResolved); err != nil {
			return nil, fmt.Errorf("notification plan states: %w", err)
		}
		out[[2]int64{routeID, incidentID}] = st
	}
	return out, rows.Err()
}

// ClaimFilter narrows what one claim takes: at most Limit rows, at most PerRoute of them for any one route
// (0 = no cap, so one route with a long backlog cannot use the whole claim), and none for the routes in Exclude
// (the ones whose previous sends are still running).
type ClaimFilter struct {
	Limit, PerRoute int
	Exclude         []int64
}

// ClaimDueNotificationDeliveries takes up to limit pending rows that are due and have no live lease.
func (s *Store) ClaimDueNotificationDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]NotificationDelivery, error) {
	return s.ClaimNotificationDeliveries(ctx, now, lease, ClaimFilter{Limit: limit})
}

// ClaimNotificationDeliveries takes the pending rows that are due and have no live lease, within the filter, using
// FOR UPDATE SKIP LOCKED so concurrent claimers get disjoint rows, and leases them for lease.
func (s *Store) ClaimNotificationDeliveries(ctx context.Context, now time.Time, lease time.Duration, f ClaimFilter) ([]NotificationDelivery, error) {
	rows, err := s.Pool.Query(ctx, `with ranked as (
			select id, row_number() over (partition by coalesce(route_id, -id) order by next_attempt_at, id) as n
			from notification_deliveries
			where status = 'pending' and next_attempt_at <= $1 and (claimed_until is null or claimed_until <= $1)
			  and (route_id is null or route_id <> all(coalesce($5::bigint[], '{}')))),
		due as (
			select d.id from notification_deliveries d join ranked using (id)
			where ($4 <= 0 or ranked.n <= $4)
			order by d.next_attempt_at, d.id limit $3 for update of d skip locked)
		update notification_deliveries d set claimed_until = $1::timestamptz + $2::interval
		from due where d.id = due.id returning `+prefixColumns("d", notificationDeliveryColumns),
		now, fmt.Sprintf("%d microseconds", lease.Microseconds()), f.Limit, f.PerRoute, f.Exclude)
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

// GetNotificationDelivery returns one delivery row, or ErrNotFound.
func (s *Store) GetNotificationDelivery(ctx context.Context, id int64) (NotificationDelivery, error) {
	d, err := scanNotificationDelivery(s.Pool.QueryRow(ctx, `select `+notificationDeliveryColumns+` from notification_deliveries where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationDelivery{}, ErrNotFound
	}
	if err != nil {
		return NotificationDelivery{}, fmt.Errorf("get notification delivery: %w", err)
	}
	return d, nil
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
func (s *Store) FinishNotificationDelivery(ctx context.Context, id int64, lease *time.Time, attempts int, o DeliveryOutcome, now time.Time) error {
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
		where id = $1 and status = 'pending' and claimed_until is not distinct from $9`,
		id, status, attempts, next, code, duration, o.Note, finished, lease)
	if err != nil {
		return fmt.Errorf("finish notification delivery: %w", err)
	}
	return nil
}

// ReleaseNotificationDelivery gives a claimed row back without counting an attempt (a shutdown or a send budget
// that ran out), under the same ownership rule as FinishNotificationDelivery.
func (s *Store) ReleaseNotificationDelivery(ctx context.Context, id int64, lease *time.Time) error {
	if _, err := s.Pool.Exec(ctx, `update notification_deliveries set claimed_until = null
		where id = $1 and status = 'pending' and claimed_until is not distinct from $2`, id, lease); err != nil {
		return fmt.Errorf("release notification delivery: %w", err)
	}
	return nil
}

// ExpireStaleNotificationTests fails the test rows an API task inserted and never finished (it crashed between the
// insert and the finish): pending rows with no next attempt whose lease has passed.
func (s *Store) ExpireStaleNotificationTests(ctx context.Context, now time.Time) (int, error) {
	tag, err := s.Pool.Exec(ctx, `update notification_deliveries set status = 'failed', claimed_until = null, finished_at = $1,
		note = 'The test did not finish.'
		where event = 'test' and status = 'pending' and next_attempt_at is null and claimed_until <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("expire notification tests: %w", err)
	}
	return int(tag.RowsAffected()), nil
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

// CancelUnsentNotificationDeliveries cancels the opened, repeat and update rows of the given incidents that are still
// pending (with pendingNote) or held (with heldNote) and not under a live lease: a row being sent is left to finish.
func (s *Store) CancelUnsentNotificationDeliveries(ctx context.Context, incidentIDs []int64, pendingNote, heldNote string, now time.Time) error {
	if len(incidentIDs) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `update notification_deliveries
		set status = 'cancelled', note = case when status = 'held' then $3 else $2 end, finished_at = $4, claimed_until = null
		where incident_id = any($1) and event in ('opened', 'repeat', 'update') and status in ('pending', 'held')
		  and (claimed_until is null or claimed_until <= $4)`, incidentIDs, pendingNote, heldNote, now)
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
