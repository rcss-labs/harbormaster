package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

// NotificationConfig is the non-secret, displayable part of a route's destination (stored as jsonb).
type NotificationConfig struct {
	Label           string   `json:"label,omitempty"`            // Slack channel label, PagerDuty service label
	Recipients      []string `json:"recipients,omitempty"`       // email
	PagerDutyRegion string   `json:"pagerduty_region,omitempty"` // "us" | "eu"
	URLHost         string   `json:"url_host,omitempty"`         // host of the stored URL, for display
	ChatID          string   `json:"chat_id,omitempty"`          // telegram
	ThreadID        int      `json:"thread_id,omitempty"`        // telegram topic, 0 = none
	Signed          bool     `json:"signed,omitempty"`           // a signing secret is stored
}

// Scope selector kinds of a notification route. The two tag kinds share off-hours' constants.
const (
	NotificationSelectorAccount      = "account"
	NotificationSelectorCluster      = "cluster"
	NotificationSelectorService      = "service"
	NotificationSelectorAWS          = OffHoursSelectorAWS
	NotificationSelectorHarbormaster = OffHoursSelectorHarbormaster
)

// NotificationSelector is one scope selector. Kind decides which fields are set: "account": AccountID and
// optionally Region (empty = every region); "cluster": AccountID, Region, Cluster; "service": plus Service;
// "aws": Key, Value; "harbormaster": TagID.
type NotificationSelector struct {
	Kind                                string
	AccountID, Region, Cluster, Service string
	Key, Value                          string
	TagID                               authz.TagID
	// Threshold is the number of failing services that makes a cluster or account entry notify (1 to 500). It is 0
	// for service and tag entries, which notify per alert.
	Threshold int
}

// NotificationRoute is one channel: where to send, which alerts, and when.
type NotificationRoute struct {
	ID             int64
	Name           string
	Enabled        bool
	Destination    string
	Config         NotificationConfig
	SecretEnc      []byte
	MinSeverity    string
	Kinds          []string
	RepeatSeconds  int
	QuietEnabled   bool
	QuietTimezone  string
	NotifyResolved bool
	Scope          []NotificationSelector // empty = everything
	Fallback       bool                   // only when no other route matches
	Investigate    bool                   // start an automatic investigation when an alert opens on this route
	Version        int
	UpdatedAt      time.Time
	UpdatedBy      *int64
}

// TargetLabel is what the delivery log and the route list show as the route's destination. It never includes a URL
// path, a routing key or a token.
func (r NotificationRoute) TargetLabel() string {
	c := r.Config
	switch r.Destination {
	case "slack":
		if c.Label != "" {
			return c.Label
		}
		if c.URLHost != "" {
			return c.URLHost
		}
		return "Slack"
	case "pagerduty":
		if c.Label != "" {
			return c.Label
		}
		return "PagerDuty"
	case "webhook":
		return c.URLHost
	case "telegram":
		if c.Label != "" {
			return c.Label
		}
		return c.ChatID
	case "email":
		return strings.Join(c.Recipients, ", ")
	}
	return ""
}

const MaxNotificationRoutes = 50

var ErrRouteLimit = errors.New("notification route limit reached")

const notificationRouteColumns = `id, name, enabled, destination, config, secret_enc, fallback, min_severity, kinds,
	repeat_seconds, quiet_enabled, quiet_timezone, notify_resolved, version, updated_at, updated_by, investigate`

func scanNotificationRoute(row pgx.Row) (NotificationRoute, error) {
	var r NotificationRoute
	var config []byte
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.Destination, &config, &r.SecretEnc, &r.Fallback, &r.MinSeverity, &r.Kinds,
		&r.RepeatSeconds, &r.QuietEnabled, &r.QuietTimezone, &r.NotifyResolved, &r.Version, &r.UpdatedAt, &r.UpdatedBy, &r.Investigate)
	if err != nil {
		return NotificationRoute{}, err
	}
	if err := json.Unmarshal(config, &r.Config); err != nil {
		return NotificationRoute{}, fmt.Errorf("decode route config: %w", err)
	}
	return r, nil
}

// loadNotificationScopes fills Scope of every route with one query.
func loadNotificationScopes(ctx context.Context, q offHoursQuerier, routes []NotificationRoute) error {
	if len(routes) == 0 {
		return nil
	}
	ids := make([]int64, len(routes))
	index := make(map[int64]int, len(routes))
	for i := range routes {
		ids[i], index[routes[i].ID] = routes[i].ID, i
		routes[i].Scope = []NotificationSelector{}
	}
	rows, err := q.Query(ctx, `select route_id, kind, coalesce(account_id, ''), coalesce(region, ''), coalesce(cluster_name, ''),
		coalesce(service_name, ''), coalesce(aws_key, ''), coalesce(aws_value, ''), coalesce(tag_id, 0), coalesce(threshold, 0)
		from notification_route_scopes where route_id = any($1)
		order by route_id, kind, account_id, region, cluster_name, service_name, aws_key, aws_value, tag_id`, ids)
	if err != nil {
		return fmt.Errorf("load notification scopes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sel NotificationSelector
		if err := rows.Scan(&id, &sel.Kind, &sel.AccountID, &sel.Region, &sel.Cluster, &sel.Service, &sel.Key, &sel.Value, &sel.TagID, &sel.Threshold); err != nil {
			return fmt.Errorf("scan notification scope: %w", err)
		}
		route := &routes[index[id]]
		route.Scope = append(route.Scope, sel)
	}
	return rows.Err()
}

func replaceNotificationScope(ctx context.Context, tx pgx.Tx, routeID int64, scope []NotificationSelector) error {
	if _, err := tx.Exec(ctx, `delete from notification_route_scopes where route_id = $1`, routeID); err != nil {
		return fmt.Errorf("clear notification scope: %w", err)
	}
	for _, sel := range scope {
		_, err := tx.Exec(ctx, `insert into notification_route_scopes
			(route_id, kind, account_id, region, cluster_name, service_name, aws_key, aws_value, tag_id, threshold)
			values ($1, $2, nullif($3, ''), nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''), case when $2 = 'aws' then $8 else nullif($8, '') end, nullif($9, 0::bigint), nullif($10, 0))
			on conflict do nothing`,
			routeID, sel.Kind, sel.AccountID, sel.Region, sel.Cluster, sel.Service, sel.Key, sel.Value, int64(sel.TagID), sel.Threshold)
		if err != nil {
			return fmt.Errorf("store notification scope: %w", err)
		}
	}
	return nil
}

// ListNotificationRoutes returns every route ordered by lower(name), with scopes.
func (s *Store) ListNotificationRoutes(ctx context.Context) ([]NotificationRoute, error) {
	rows, err := s.Pool.Query(ctx, `select `+notificationRouteColumns+` from notification_routes order by lower(name)`)
	if err != nil {
		return nil, fmt.Errorf("list notification routes: %w", err)
	}
	defer rows.Close()
	out := []NotificationRoute{}
	for rows.Next() {
		r, err := scanNotificationRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification route: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := loadNotificationScopes(ctx, s.Pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetNotificationRoute returns one route, or ErrNotFound.
func (s *Store) GetNotificationRoute(ctx context.Context, id int64) (NotificationRoute, error) {
	r, err := scanNotificationRoute(s.Pool.QueryRow(ctx, `select `+notificationRouteColumns+` from notification_routes where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationRoute{}, ErrNotFound
	}
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("get notification route: %w", err)
	}
	one := []NotificationRoute{r}
	if err := loadNotificationScopes(ctx, s.Pool, one); err != nil {
		return NotificationRoute{}, err
	}
	return one[0], nil
}

// CreateNotificationRoute inserts a route at version 1 with its scope. A duplicate name (any case) returns
// ErrConflict; the 51st route returns ErrRouteLimit. A zero by records no user.
func (s *Store) CreateNotificationRoute(ctx context.Context, r NotificationRoute, by int64) (NotificationRoute, error) {
	config, err := json.Marshal(r.Config)
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// Serialise creators so two requests cannot both pass the limit.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('notification_routes'))`); err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from notification_routes`).Scan(&count); err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	if count >= MaxNotificationRoutes {
		return NotificationRoute{}, ErrRouteLimit
	}
	created, err := scanNotificationRoute(tx.QueryRow(ctx,
		`insert into notification_routes (name, enabled, destination, config, secret_enc, fallback, min_severity, kinds,
		   repeat_seconds, quiet_enabled, quiet_timezone, notify_resolved, updated_by, investigate)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, nullif($13, 0::bigint), $14)
		 returning `+notificationRouteColumns,
		r.Name, r.Enabled, r.Destination, config, r.SecretEnc, r.Fallback, r.MinSeverity, r.Kinds,
		r.RepeatSeconds, r.QuietEnabled, r.QuietTimezone, r.NotifyResolved, by, r.Investigate))
	if isUniqueViolation(err) {
		return NotificationRoute{}, ErrConflict
	}
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	if err := replaceNotificationScope(ctx, tx, created.ID, r.Scope); err != nil {
		return NotificationRoute{}, err
	}
	one := []NotificationRoute{created}
	if err := loadNotificationScopes(ctx, tx, one); err != nil {
		return NotificationRoute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NotificationRoute{}, fmt.Errorf("create notification route: %w", err)
	}
	return one[0], nil
}

// UpdateNotificationRoute replaces a route's fields and scope when r.Version is still current and bumps the
// version. A stale version or a duplicate name returns ErrConflict; a missing route returns ErrNotFound.
// SecretEnc is written as given: the API passes the stored blob through when the request has no new secret.
func (s *Store) UpdateNotificationRoute(ctx context.Context, r NotificationRoute, by int64) (NotificationRoute, error) {
	config, err := json.Marshal(r.Config)
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("update notification route: %w", err)
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("update notification route: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	updated, err := scanNotificationRoute(tx.QueryRow(ctx,
		`update notification_routes set name = $3, enabled = $4, destination = $5, config = $6, secret_enc = $7,
		   fallback = $8, min_severity = $9, kinds = $10, repeat_seconds = $11, quiet_enabled = $12,
		   quiet_timezone = $13, notify_resolved = $14, version = version + 1, updated_at = now(),
		   updated_by = nullif($15, 0::bigint), investigate = $16
		 where id = $1 and version = $2
		 returning `+notificationRouteColumns,
		r.ID, r.Version, r.Name, r.Enabled, r.Destination, config, r.SecretEnc,
		r.Fallback, r.MinSeverity, r.Kinds, r.RepeatSeconds, r.QuietEnabled, r.QuietTimezone, r.NotifyResolved, by, r.Investigate))
	if isUniqueViolation(err) {
		return NotificationRoute{}, ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx) //nolint:errcheck // nothing was written
		if _, getErr := s.GetNotificationRoute(ctx, r.ID); getErr != nil {
			return NotificationRoute{}, getErr
		}
		return NotificationRoute{}, ErrConflict
	}
	if err != nil {
		return NotificationRoute{}, fmt.Errorf("update notification route: %w", err)
	}
	if err := replaceNotificationScope(ctx, tx, updated.ID, r.Scope); err != nil {
		return NotificationRoute{}, err
	}
	one := []NotificationRoute{updated}
	if err := loadNotificationScopes(ctx, tx, one); err != nil {
		return NotificationRoute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NotificationRoute{}, fmt.Errorf("update notification route: %w", err)
	}
	return one[0], nil
}

// DeleteNotificationRoute removes a route. Its delivery log rows stay (route_id becomes null, names are snapshots).
func (s *Store) DeleteNotificationRoute(ctx context.Context, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from notification_routes where id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete notification route: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
