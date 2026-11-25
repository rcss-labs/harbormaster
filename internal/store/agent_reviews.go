package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/authz"
)

// MaxAgentReviews is the most review definitions that may exist.
const MaxAgentReviews = 20

// Limits of one definition.
const (
	maxReviewRoutes       = 5
	maxReviewScopeEntries = 10
	maxReviewNameRunes    = 80
	// maxReviewInstructionRunes is the length check of the column; the API applies the stricter rules.
	maxReviewInstructionRunes = 2000
)

// ErrReviewLimit is returned for the 21st review.
var ErrReviewLimit = errors.New("review limit reached")

// ErrReviewInvalid wraps every refusal of a definition's shape: a name, type, schedule, scope entry or route the
// rules do not allow. Nothing is written when it is returned.
var ErrReviewInvalid = errors.New("review refused")

// ReviewTypeCustom is the type whose instructions the admin writes.
const ReviewTypeCustom = "custom"

// ErrReviewTypeFixed is returned by UpdateReview for a change of type to or from custom.
var ErrReviewTypeFixed = errors.New("review type cannot change to or from custom")

// Review types and cadences. The type list is closed: a new type is a code and migration change.
var (
	reviewTypes    = []string{"cost", "security", "reliability", "capacity", ReviewTypeCustom}
	reviewCadences = []string{"daily", "weekly", "monthly"}
)

// Review is one scheduled review definition. Weekday is set (1 to 7) for weekly reviews and MonthDay (1 to 28) for
// monthly ones; both are 0 otherwise. Scope entries use the notification selector kinds account, cluster and
// harbormaster, without a threshold; no entry means everything the agent identity sees.
type Review struct {
	ID                int64
	Name, Type        string
	Enabled           bool
	Cadence           string
	AtMinute          int
	Weekday, MonthDay int
	Timezone          string
	Scope             []NotificationSelector
	RouteIDs          []int64
	// Instructions is the admin's text of a custom review, and empty for every other type.
	Instructions string
	// ScheduleChangedAt is when the schedule last changed or the review was last turned on; occurrences at or before
	// it are never recorded.
	ScheduleChangedAt time.Time
	Version           int
	UpdatedAt         time.Time
	UpdatedBy         *int64
}

const reviewColumns = `id, name, type, enabled, cadence, at_minute, coalesce(weekday, 0), coalesce(month_day, 0), timezone,
	schedule_changed_at, version, updated_at, updated_by, instructions`

func scanReview(row pgx.Row) (Review, error) {
	var r Review
	err := row.Scan(&r.ID, &r.Name, &r.Type, &r.Enabled, &r.Cadence, &r.AtMinute, &r.Weekday, &r.MonthDay, &r.Timezone,
		&r.ScheduleChangedAt, &r.Version, &r.UpdatedAt, &r.UpdatedBy, &r.Instructions)
	return r, err
}

func invalidReview(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrReviewInvalid, fmt.Sprintf(format, args...))
}

// validateReview checks a definition before any SQL runs. It does not load the time zone: the API validates the
// name and the schema stores the text.
func validateReview(r Review) error { return validateReviewRules(r, true) }

// validateReviewRules is validateReview; routesRequired false skips the count of routes (1 to 5) only.
func validateReviewRules(r Review, routesRequired bool) error {
	if n := utf8.RuneCountInString(r.Name); n < 1 || n > maxReviewNameRunes || !utf8.ValidString(r.Name) || strings.ContainsRune(r.Name, 0) {
		return invalidReview("the name must be 1 to %d characters", maxReviewNameRunes)
	}
	if !slices.Contains(reviewTypes, r.Type) {
		return invalidReview("unknown type %q", r.Type)
	}
	if !slices.Contains(reviewCadences, r.Cadence) {
		return invalidReview("unknown cadence %q", r.Cadence)
	}
	if r.AtMinute < 0 || r.AtMinute > 1439 {
		return invalidReview("the minute must be 0 to 1439")
	}
	if (r.Cadence == "weekly") != (r.Weekday != 0) || r.Weekday < 0 || r.Weekday > 7 {
		return invalidReview("a weekday from 1 to 7 is for weekly reviews only")
	}
	if (r.Cadence == "monthly") != (r.MonthDay != 0) || r.MonthDay < 0 || r.MonthDay > 28 {
		return invalidReview("a month day from 1 to 28 is for monthly reviews only")
	}
	if r.Timezone == "" || !utf8.ValidString(r.Timezone) || strings.ContainsRune(r.Timezone, 0) {
		return invalidReview("a time zone is required")
	}
	if routesRequired && (len(r.RouteIDs) < 1 || len(r.RouteIDs) > maxReviewRoutes) {
		return invalidReview("a review needs 1 to %d routes", maxReviewRoutes)
	}
	if len(r.Scope) > maxReviewScopeEntries {
		return invalidReview("a review has at most %d scope entries", maxReviewScopeEntries)
	}
	for i, sel := range r.Scope {
		if sel.Threshold != 0 || sel.Service != "" || sel.Key != "" || sel.Value != "" {
			return invalidReview("scope entry %d has a field its kind does not use", i+1)
		}
		for _, text := range []string{sel.AccountID, sel.Region, sel.Cluster} {
			if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
				return invalidReview("scope entry %d is not valid text", i+1)
			}
		}
		switch sel.Kind {
		case NotificationSelectorAccount:
			if sel.AccountID == "" || sel.Cluster != "" || sel.TagID != 0 {
				return invalidReview("scope entry %d: an account entry names an account and optionally a region", i+1)
			}
		case NotificationSelectorCluster:
			if sel.AccountID == "" || sel.Region == "" || sel.Cluster == "" || sel.TagID != 0 {
				return invalidReview("scope entry %d: a cluster entry names an account, a region and a cluster", i+1)
			}
		case NotificationSelectorHarbormaster:
			if sel.TagID <= 0 || sel.AccountID != "" || sel.Region != "" || sel.Cluster != "" {
				return invalidReview("scope entry %d: a tag entry names a tag", i+1)
			}
		default:
			return invalidReview("scope entry %d has kind %q; reviews take account, cluster and harbormaster", i+1, sel.Kind)
		}
	}
	return nil
}

// checkReview is validateReviewRules plus the rules of the instructions: a custom review has text and every other type
// has none, and the text is valid and at most 2000 characters. The store does no cleaning, scrubbing or hidden
// character check; those belong to the API, and these are the backstop.
func checkReview(r Review, routesRequired bool) error {
	if err := validateReviewRules(r, routesRequired); err != nil {
		return err
	}
	if (r.Type == ReviewTypeCustom) != (r.Instructions != "") {
		return invalidReview("instructions are for custom reviews only, and a custom review needs them")
	}
	if err := checkInvestigationText("instructions", r.Instructions, maxReviewInstructionRunes, 0); err != nil {
		return invalidReview("%v", err)
	}
	return nil
}

// sortedUnique returns the route ids once each, ascending.
func sortedUnique(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// checkReviewRoutes refuses a route that does not exist or sends to PagerDuty. It holds the routes for share, so
// none is deleted or changed while the definition is written.
func checkReviewRoutes(ctx context.Context, tx pgx.Tx, ids []int64) error {
	rows, err := tx.Query(ctx, `select id, destination from notification_routes where id = any($1) order by id for share`, ids)
	if err != nil {
		return fmt.Errorf("check review routes: %w", err)
	}
	found := 0
	for rows.Next() {
		var id int64
		var destination string
		if err := rows.Scan(&id, &destination); err != nil {
			rows.Close()
			return fmt.Errorf("check review routes: %w", err)
		}
		if destination == "pagerduty" {
			rows.Close()
			return invalidReview("route %d sends to PagerDuty, which reviews do not use", id)
		}
		found++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check review routes: %w", err)
	}
	if found != len(ids) {
		return invalidReview("a route does not exist")
	}
	return nil
}

func replaceReviewScopeAndRoutes(ctx context.Context, tx pgx.Tx, reviewID int64, scope []NotificationSelector, routeIDs []int64) error {
	if _, err := tx.Exec(ctx, `delete from agent_review_scopes where review_id = $1`, reviewID); err != nil {
		return fmt.Errorf("clear review scope: %w", err)
	}
	for _, sel := range scope {
		if _, err := tx.Exec(ctx, `insert into agent_review_scopes (review_id, kind, account_id, region, cluster_name, tag_id)
			values ($1, $2, nullif($3, ''), nullif($4, ''), nullif($5, ''), nullif($6, 0::bigint)) on conflict do nothing`,
			reviewID, sel.Kind, sel.AccountID, sel.Region, sel.Cluster, int64(sel.TagID)); err != nil {
			return fmt.Errorf("store review scope: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `delete from agent_review_routes where review_id = $1`, reviewID); err != nil {
		return fmt.Errorf("clear review routes: %w", err)
	}
	for _, id := range sortedUnique(routeIDs) {
		if _, err := tx.Exec(ctx, `insert into agent_review_routes (review_id, route_id) values ($1, $2)`, reviewID, id); err != nil {
			return fmt.Errorf("store review routes: %w", err)
		}
	}
	return nil
}

// loadReviewParts fills Scope and RouteIDs of every review with two queries.
func loadReviewParts(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, reviews []Review) error {
	if len(reviews) == 0 {
		return nil
	}
	ids := make([]int64, len(reviews))
	index := make(map[int64]int, len(reviews))
	for i := range reviews {
		ids[i], index[reviews[i].ID] = reviews[i].ID, i
		reviews[i].Scope, reviews[i].RouteIDs = []NotificationSelector{}, []int64{}
	}
	rows, err := q.Query(ctx, `select review_id, kind, coalesce(account_id, ''), coalesce(region, ''), coalesce(cluster_name, ''), coalesce(tag_id, 0)
		from agent_review_scopes where review_id = any($1) order by review_id, kind, account_id, region, cluster_name, tag_id`, ids)
	if err != nil {
		return fmt.Errorf("load review scopes: %w", err)
	}
	for rows.Next() {
		var id int64
		var sel NotificationSelector
		var tag int64
		if err := rows.Scan(&id, &sel.Kind, &sel.AccountID, &sel.Region, &sel.Cluster, &tag); err != nil {
			rows.Close()
			return fmt.Errorf("scan review scope: %w", err)
		}
		sel.TagID = authz.TagID(tag)
		r := &reviews[index[id]]
		r.Scope = append(r.Scope, sel)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load review scopes: %w", err)
	}
	rows, err = q.Query(ctx, `select review_id, route_id from agent_review_routes where review_id = any($1) order by review_id, route_id`, ids)
	if err != nil {
		return fmt.Errorf("load review routes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, route int64
		if err := rows.Scan(&id, &route); err != nil {
			return fmt.Errorf("scan review route: %w", err)
		}
		r := &reviews[index[id]]
		r.RouteIDs = append(r.RouteIDs, route)
	}
	return rows.Err()
}

// ListReviews returns every review ordered by lower(name), with scope and routes.
func (s *Store) ListReviews(ctx context.Context) ([]Review, error) {
	rows, err := s.Pool.Query(ctx, `select `+reviewColumns+` from agent_reviews order by lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	out := []Review{}
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan review: %w", err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	if err := loadReviewParts(ctx, s.Pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetReview returns one review, or ErrNotFound.
func (s *Store) GetReview(ctx context.Context, id int64) (Review, error) {
	r, err := scanReview(s.Pool.QueryRow(ctx, `select `+reviewColumns+` from agent_reviews where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, ErrNotFound
	}
	if err != nil {
		return Review{}, fmt.Errorf("get review: %w", err)
	}
	one := []Review{r}
	if err := loadReviewParts(ctx, s.Pool, one); err != nil {
		return Review{}, err
	}
	return one[0], nil
}

// CreateReview inserts a review at version 1 with its scope and routes. A name used by another review (any case)
// returns ErrConflict, the 21st review ErrReviewLimit, a definition the rules refuse ErrReviewInvalid; nothing is
// written then. A zero by records no user.
func (s *Store) CreateReview(ctx context.Context, r Review, by int64) (Review, error) {
	if err := checkReview(r, true); err != nil {
		return Review{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Review{}, fmt.Errorf("create review: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// Serialise creators so two requests cannot both pass the limit.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('agent_reviews'))`); err != nil {
		return Review{}, fmt.Errorf("create review: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from agent_reviews`).Scan(&count); err != nil {
		return Review{}, fmt.Errorf("create review: %w", err)
	}
	if count >= MaxAgentReviews {
		return Review{}, ErrReviewLimit
	}
	if err := checkReviewRoutes(ctx, tx, sortedUnique(r.RouteIDs)); err != nil {
		return Review{}, err
	}
	created, err := scanReview(tx.QueryRow(ctx, `insert into agent_reviews
		(name, type, enabled, cadence, at_minute, weekday, month_day, timezone, updated_by, instructions)
		values ($1, $2, $3, $4, $5, nullif($6, 0), nullif($7, 0), $8, nullif($9, 0::bigint), $10)
		returning `+reviewColumns, r.Name, r.Type, r.Enabled, r.Cadence, r.AtMinute, r.Weekday, r.MonthDay, r.Timezone, by, r.Instructions))
	if isUniqueViolation(err) {
		return Review{}, ErrConflict
	}
	if err != nil {
		return Review{}, fmt.Errorf("create review: %w", err)
	}
	if err := replaceReviewScopeAndRoutes(ctx, tx, created.ID, r.Scope, r.RouteIDs); err != nil {
		return Review{}, err
	}
	one := []Review{created}
	if err := loadReviewParts(ctx, tx, one); err != nil {
		return Review{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Review{}, fmt.Errorf("create review: %w", err)
	}
	return one[0], nil
}

// isSwitchOff reports whether next is the stored review with nothing changed but the switch turned off: an admin can
// always do that, whatever became of the routes (PagerDuty, deleted, none) and the scope since it was saved.
func isSwitchOff(stored, next Review) bool {
	a, b := stored, next
	if !a.Enabled || b.Enabled {
		return false
	}
	return a.Name == b.Name && a.Type == b.Type && a.Cadence == b.Cadence && a.AtMinute == b.AtMinute && a.Weekday == b.Weekday &&
		a.MonthDay == b.MonthDay && a.Timezone == b.Timezone && a.Version == b.Version &&
		slices.Equal(a.Scope, b.Scope) && slices.Equal(sortedUnique(a.RouteIDs), sortedUnique(b.RouteIDs))
}

// UpdateReview replaces a review's fields, scope and routes when r.Version is still current and bumps the version.
// A stale version or a name used by another review returns ErrConflict, a missing review ErrNotFound, a change of type
// to or from custom ErrReviewTypeFixed. A change of the instructions raises the version and keeps the schedule. The schedule
// time moves (schedule_changed_at = now) only when the cadence, minute, weekday, month day or zone changes or the
// review goes from off to on. An update that only turns an enabled review off (isSwitchOff) is checked for the route
// count and the routes' destinations by nobody: it keeps what is stored.
func (s *Store) UpdateReview(ctx context.Context, r Review, by int64) (Review, error) {
	switchOff := false
	if !r.Enabled {
		if stored, err := s.GetReview(ctx, r.ID); err == nil {
			switchOff = isSwitchOff(stored, r)
		}
	}
	if err := checkReview(r, !switchOff); err != nil {
		return Review{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Review{}, fmt.Errorf("update review: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if !switchOff {
		if err := checkReviewRoutes(ctx, tx, sortedUnique(r.RouteIDs)); err != nil {
			return Review{}, err
		}
	}
	// Lock the row first: the type is read and the update written under the same lock.
	var storedType string
	if err := tx.QueryRow(ctx, `select type from agent_reviews where id = $1 for update`, r.ID).Scan(&storedType); errors.Is(err, pgx.ErrNoRows) {
		return Review{}, ErrNotFound
	} else if err != nil {
		return Review{}, fmt.Errorf("update review: %w", err)
	}
	if (storedType == ReviewTypeCustom) != (r.Type == ReviewTypeCustom) {
		return Review{}, ErrReviewTypeFixed
	}
	updated, err := scanReview(tx.QueryRow(ctx, `update agent_reviews set name = $3, type = $4, enabled = $5, cadence = $6, instructions = $12,
		at_minute = $7, weekday = nullif($8, 0), month_day = nullif($9, 0), timezone = $10,
		schedule_changed_at = case when cadence <> $6 or at_minute <> $7 or weekday is distinct from nullif($8, 0)
		    or month_day is distinct from nullif($9, 0) or timezone <> $10 or (not enabled and $5)
		  then now() else schedule_changed_at end,
		version = version + 1, updated_at = now(), updated_by = nullif($11, 0::bigint)
		where id = $1 and version = $2
		returning `+reviewColumns, r.ID, r.Version, r.Name, r.Type, r.Enabled, r.Cadence, r.AtMinute, r.Weekday, r.MonthDay, r.Timezone, by, r.Instructions))
	if isUniqueViolation(err) {
		return Review{}, ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx) //nolint:errcheck // nothing was written
		if _, getErr := s.GetReview(ctx, r.ID); getErr != nil {
			return Review{}, getErr
		}
		return Review{}, ErrConflict
	}
	if err != nil {
		return Review{}, fmt.Errorf("update review: %w", err)
	}
	if err := replaceReviewScopeAndRoutes(ctx, tx, updated.ID, r.Scope, r.RouteIDs); err != nil {
		return Review{}, err
	}
	one := []Review{updated}
	if err := loadReviewParts(ctx, tx, one); err != nil {
		return Review{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Review{}, fmt.Errorf("update review: %w", err)
	}
	return one[0], nil
}

// DeleteReview removes a review with its scope and routes. Its runs stay: review_id becomes null and their name and
// type are snapshots.
func (s *Store) DeleteReview(ctx context.Context, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from agent_reviews where id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete review: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
