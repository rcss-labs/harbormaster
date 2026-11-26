package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func reviewRoute(t *testing.T, s *store.Store, name string) store.NotificationRoute {
	t.Helper()
	r, err := s.CreateNotificationRoute(context.Background(), newRoute(name), 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newReview(name string, routeIDs ...int64) store.Review {
	return store.Review{Name: name, Type: "cost", Enabled: true, Cadence: "daily", AtMinute: 480, Timezone: "Europe/Berlin", RouteIDs: routeIDs}
}

func mustReview(t *testing.T, s *store.Store, r store.Review) store.Review {
	t.Helper()
	created, err := s.CreateReview(context.Background(), r, 0)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// longAgo makes every occurrence of the test clock newer than the review's last schedule change.
func longAgo(t *testing.T, s *store.Store, reviewID int64) {
	t.Helper()
	invExec(t, s, `update agent_reviews set schedule_changed_at = $2 where id = $1`, reviewID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

func TestReview_RoundTripsWithScopeAndRoutes(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	r1, r2 := reviewRoute(t, s, "r1"), reviewRoute(t, s, "r2")
	in := newReview("Weekly spend", r2.ID, r1.ID)
	in.Cadence, in.Weekday = "weekly", 3
	in.Scope = []store.NotificationSelector{
		{Kind: "account", AccountID: "111111111111"},
		{Kind: "account", AccountID: "222222222222", Region: "eu-west-1"},
		{Kind: "cluster", AccountID: "111111111111", Region: "us-east-1", Cluster: "core"},
		{Kind: "harbormaster", TagID: 7},
	}
	created, err := s.CreateReview(ctx, in, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReview(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.Name != "Weekly spend" || got.Type != "cost" || !got.Enabled || got.Cadence != "weekly" || got.AtMinute != 480 ||
		got.Weekday != 3 || got.MonthDay != 0 || got.Timezone != "Europe/Berlin" || got.Version != 1 || got.UpdatedBy != nil || got.ScheduleChangedAt.IsZero() {
		t.Fatalf("review = %+v", got)
	}
	if want := []int64{r1.ID, r2.ID}; !reflect.DeepEqual(got.RouteIDs, want) {
		t.Fatalf("routes = %v, want %v", got.RouteIDs, want)
	}
	if len(got.Scope) != 4 {
		t.Fatalf("scope = %+v", got.Scope)
	}
	byKind := map[string]store.NotificationSelector{}
	for _, sel := range got.Scope {
		byKind[sel.Kind+"/"+sel.Region] = sel
	}
	if byKind["account/eu-west-1"].AccountID != "222222222222" || byKind["cluster/us-east-1"].Cluster != "core" || byKind["harbormaster/"].TagID != 7 || byKind["account/"].AccountID != "111111111111" {
		t.Fatalf("scope = %+v", got.Scope)
	}
	list, err := s.ListReviews(ctx)
	if err != nil || len(list) != 1 || list[0].ID != created.ID || len(list[0].Scope) != 4 || len(list[0].RouteIDs) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetReview(ctx, created.ID+99); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing = %v, want ErrNotFound", err)
	}
	m := newReview("Monthly", r1.ID)
	m.Cadence, m.MonthDay = "monthly", 28
	if _, err := s.CreateReview(ctx, m, 0); err != nil {
		t.Fatal(err)
	}
}

func TestReview_LimitOfTwenty(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	for i := 0; i < store.MaxAgentReviews; i++ {
		mustReview(t, s, newReview(fmt.Sprintf("review %d", i), route.ID))
	}
	_, err := s.CreateReview(ctx, newReview("one too many", route.ID), 0)
	if !errors.Is(err, store.ErrReviewLimit) {
		t.Fatalf("21st review = %v, want ErrReviewLimit", err)
	}
	if n := invCount(t, s, "agent_reviews"); n != 20 {
		t.Fatalf("reviews = %d, want 20", n)
	}
}

func TestReview_NameConflictIgnoresCase(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	mustReview(t, s, newReview("Cost report", route.ID))
	if _, err := s.CreateReview(ctx, newReview("COST REPORT", route.ID), 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("create = %v, want ErrConflict", err)
	}
	other := mustReview(t, s, newReview("Other", route.ID))
	other.Name = "cost report"
	if _, err := s.UpdateReview(ctx, other, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update = %v, want ErrConflict", err)
	}
	if n := invCount(t, s, "agent_reviews"); n != 2 {
		t.Fatalf("reviews = %d, want 2", n)
	}
}

func TestReview_RefusesPagerDutyRoutes(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	ok := reviewRoute(t, s, "ok")
	pd := newRoute("pd")
	pd.Destination = "pagerduty"
	pdRoute, err := s.CreateNotificationRoute(ctx, pd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReview(ctx, newReview("x", ok.ID, pdRoute.ID), 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("create = %v, want ErrReviewInvalid", err)
	}
	if n := invCount(t, s, "agent_reviews"); n != 0 {
		t.Fatalf("reviews = %d, want none written", n)
	}
	rv := mustReview(t, s, newReview("y", ok.ID))
	rv.RouteIDs = []int64{pdRoute.ID}
	if _, err := s.UpdateReview(ctx, rv, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("update = %v, want ErrReviewInvalid", err)
	}
	if got, _ := s.GetReview(ctx, rv.ID); !reflect.DeepEqual(got.RouteIDs, []int64{ok.ID}) || got.Version != 1 {
		t.Fatalf("review changed: %+v", got)
	}
}

func TestReview_RefusesTooManyRoutesOrScopeEntries(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	var ids []int64
	for i := 0; i < 6; i++ {
		ids = append(ids, reviewRoute(t, s, fmt.Sprintf("r%d", i)).ID)
	}
	cases := map[string]func(*store.Review){
		"no route":      func(r *store.Review) { r.RouteIDs = nil },
		"six routes":    func(r *store.Review) { r.RouteIDs = ids },
		"unknown route": func(r *store.Review) { r.RouteIDs = []int64{ids[0] + 1000} },
		"eleven scope":  func(r *store.Review) { r.Scope = scopeEntries(11) },
		"service kind": func(r *store.Review) {
			r.Scope = []store.NotificationSelector{{Kind: "service", AccountID: "a", Region: "r", Cluster: "c", Service: "s"}}
		},
		"aws kind":           func(r *store.Review) { r.Scope = []store.NotificationSelector{{Kind: "aws", Key: "k", Value: "v"}} },
		"account without id": func(r *store.Review) { r.Scope = []store.NotificationSelector{{Kind: "account"}} },
		"cluster no region": func(r *store.Review) {
			r.Scope = []store.NotificationSelector{{Kind: "cluster", AccountID: "a", Cluster: "c"}}
		},
		"tag without id": func(r *store.Review) { r.Scope = []store.NotificationSelector{{Kind: "harbormaster"}} },
		"threshold on entry": func(r *store.Review) {
			r.Scope = []store.NotificationSelector{{Kind: "account", AccountID: "a", Threshold: 3}}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := newReview("x", ids[0])
			mut(&r)
			if _, err := s.CreateReview(ctx, r, 0); err == nil {
				t.Fatal("create succeeded")
			}
			if n := invCount(t, s, "agent_reviews") + invCount(t, s, "agent_review_scopes") + invCount(t, s, "agent_review_routes"); n != 0 {
				t.Fatalf("%d rows written by a refused review", n)
			}
		})
	}
	// The limits themselves are allowed.
	ok := newReview("edge", ids[:5]...)
	ok.Scope = scopeEntries(10)
	if _, err := s.CreateReview(ctx, ok, 0); err != nil {
		t.Fatalf("5 routes and 10 entries: %v", err)
	}
}

func scopeEntries(n int) []store.NotificationSelector {
	out := make([]store.NotificationSelector, n)
	for i := range out {
		out[i] = store.NotificationSelector{Kind: "account", AccountID: fmt.Sprintf("%012d", i)}
	}
	return out
}

func TestReview_ScheduleShapeChecks(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	cases := map[string]func(*store.Review){
		"weekly without a weekday": func(r *store.Review) { r.Cadence = "weekly" },
		"weekly weekday 8":         func(r *store.Review) { r.Cadence, r.Weekday = "weekly", 8 },
		"monthly without a day":    func(r *store.Review) { r.Cadence = "monthly" },
		"monthly on day 29":        func(r *store.Review) { r.Cadence, r.MonthDay = "monthly", 29 },
		"daily with a weekday":     func(r *store.Review) { r.Weekday = 1 },
		"daily with a month day":   func(r *store.Review) { r.MonthDay = 1 },
		"weekly with a month day":  func(r *store.Review) { r.Cadence, r.Weekday, r.MonthDay = "weekly", 1, 1 },
		"minute 1440":              func(r *store.Review) { r.AtMinute = 1440 },
		"minute -1":                func(r *store.Review) { r.AtMinute = -1 },
		"unknown cadence":          func(r *store.Review) { r.Cadence = "hourly" },
		"unknown type":             func(r *store.Review) { r.Type = "gossip" },
		"empty name":               func(r *store.Review) { r.Name = "" },
		"name of 81 characters":    func(r *store.Review) { r.Name = strings.Repeat("x", 81) },
		"empty zone":               func(r *store.Review) { r.Timezone = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := newReview("shape", route.ID)
			mut(&r)
			if _, err := s.CreateReview(ctx, r, 0); !errors.Is(err, store.ErrReviewInvalid) {
				t.Fatalf("create = %v, want ErrReviewInvalid", err)
			}
		})
	}
	good := newReview("minute 1439", route.ID)
	good.AtMinute = 1439
	if _, err := s.CreateReview(ctx, good, 0); err != nil {
		t.Fatal(err)
	}
	// The schema refuses the same shapes for a writer that skips the Go checks.
	for _, sql := range []string{
		`insert into agent_reviews (name, type, cadence, at_minute, timezone) values ('a', 'cost', 'weekly', 1, 'UTC')`,
		`insert into agent_reviews (name, type, cadence, at_minute, month_day, timezone) values ('b', 'cost', 'monthly', 1, 29, 'UTC')`,
		`insert into agent_reviews (name, type, cadence, at_minute, timezone) values ('c', 'cost', 'daily', 1440, 'UTC')`,
	} {
		if _, err := s.Pool.Exec(ctx, sql); err == nil {
			t.Fatalf("schema accepted %s", sql)
		}
	}
}

func TestReview_VersionConflict(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := mustReview(t, s, newReview("v", route.ID))
	first := rv
	first.Name = "first"
	updated, err := s.UpdateReview(ctx, first, 0)
	if err != nil || updated.Version != 2 || updated.Name != "first" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	stale := rv
	stale.Name = "stale"
	if _, err := s.UpdateReview(ctx, stale, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	if got, _ := s.GetReview(ctx, rv.ID); got.Name != "first" || got.Version != 2 {
		t.Fatalf("review = %+v", got)
	}
	missing := updated
	missing.ID += 100
	if _, err := s.UpdateReview(ctx, missing, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing update = %v, want ErrNotFound", err)
	}
	by, err := s.UpdateReview(ctx, updated, 0)
	if err != nil || by.UpdatedBy != nil {
		t.Fatalf("zero by recorded a user: %+v, %v", by, err)
	}
}

func TestReview_ScheduleChangedAtMovesOnlyForScheduleEdits(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	r1, r2 := reviewRoute(t, s, "r1"), reviewRoute(t, s, "r2")
	rv := mustReview(t, s, newReview("sched", r1.ID))
	longAgo(t, s, rv.ID)
	edit := func(mut func(*store.Review)) (before, after time.Time) {
		t.Helper()
		cur, err := s.GetReview(ctx, rv.ID)
		if err != nil {
			t.Fatal(err)
		}
		next := cur
		mut(&next)
		upd, err := s.UpdateReview(ctx, next, 0)
		if err != nil {
			t.Fatal(err)
		}
		return cur.ScheduleChangedAt, upd.ScheduleChangedAt
	}
	moves := map[string]func(*store.Review){
		"time":     func(r *store.Review) { r.AtMinute = 600 },
		"cadence":  func(r *store.Review) { r.Cadence, r.Weekday = "weekly", 2 },
		"weekday":  func(r *store.Review) { r.Weekday = 5 },
		"monthday": func(r *store.Review) { r.Cadence, r.Weekday, r.MonthDay = "monthly", 0, 4 },
		"day":      func(r *store.Review) { r.MonthDay = 9 },
		"zone":     func(r *store.Review) { r.Timezone = "Asia/Tokyo" },
	}
	stays := map[string]func(*store.Review){
		"rename":  func(r *store.Review) { r.Name = "renamed" },
		"route":   func(r *store.Review) { r.RouteIDs = []int64{r2.ID} },
		"scope":   func(r *store.Review) { r.Scope = scopeEntries(1) },
		"type":    func(r *store.Review) { r.Type = "security" },
		"disable": func(r *store.Review) { r.Enabled = false },
	}
	for _, name := range []string{"rename", "route", "scope", "type"} {
		before, after := edit(stays[name])
		if !after.Equal(before) {
			t.Fatalf("%s moved schedule_changed_at: %v -> %v", name, before, after)
		}
	}
	for _, name := range []string{"time", "cadence", "weekday", "monthday", "day", "zone"} {
		longAgo(t, s, rv.ID)
		before, after := edit(moves[name])
		if !after.After(before) {
			t.Fatalf("%s did not move schedule_changed_at: %v -> %v", name, before, after)
		}
	}
	longAgo(t, s, rv.ID)
	if before, after := edit(stays["disable"]); !after.Equal(before) {
		t.Fatalf("disabling moved it: %v -> %v", before, after)
	}
	if before, after := edit(func(r *store.Review) { r.Enabled = true }); !after.After(before) {
		t.Fatalf("re-enabling did not move it: %v -> %v", before, after)
	}
	if before, after := edit(func(r *store.Review) {}); !after.Equal(before) {
		t.Fatalf("saving with no change moved it: %v -> %v", before, after)
	}
}

func TestReview_DeletingARouteDetachesIt(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	r1, r2 := reviewRoute(t, s, "r1"), reviewRoute(t, s, "r2")
	both := mustReview(t, s, newReview("both", r1.ID, r2.ID))
	only := mustReview(t, s, newReview("only", r1.ID))
	if err := s.DeleteNotificationRoute(ctx, r1.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReview(ctx, both.ID); !reflect.DeepEqual(got.RouteIDs, []int64{r2.ID}) {
		t.Fatalf("both = %+v", got.RouteIDs)
	}
	got, err := s.GetReview(ctx, only.ID)
	if err != nil || len(got.RouteIDs) != 0 || got.Name != "only" {
		t.Fatalf("a review left with no route must stay: %+v, %v", got, err)
	}
}

func TestReview_DeletingATagKeepsTheEntry(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	tag, err := s.CreateTag(ctx, "Payments", "blue", false)
	if err != nil {
		t.Fatal(err)
	}
	in := newReview("tagged", route.ID)
	in.Scope = []store.NotificationSelector{{Kind: "harbormaster", TagID: authz.TagID(tag.ID)}}
	rv := mustReview(t, s, in)
	if err := s.DeleteTag(ctx, authz.TagID(tag.ID)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReview(ctx, rv.ID)
	if err != nil || len(got.Scope) != 1 || got.Scope[0].TagID != authz.TagID(tag.ID) {
		t.Fatalf("scope after the tag was deleted = %+v, %v", got.Scope, err)
	}
}

func TestDeleteReview_KeepsItsRuns(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := mustReview(t, s, newReview("gone", route.ID))
	longAgo(t, s, rv.ID)
	if n, err := s.RecordDueReviewRuns(ctx, []store.DueReview{dueFor(rv, "d:2026-11-24", revT0)}, revNow); err != nil || n != 1 {
		t.Fatalf("record = %d, %v", n, err)
	}
	if err := s.DeleteReview(ctx, rv.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteReview(ctx, rv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	var reviewID *int64
	var name, typ string
	if err := s.Pool.QueryRow(ctx, `select review_id, review_name, review_type from agent_review_runs`).Scan(&reviewID, &name, &typ); err != nil {
		t.Fatal(err)
	}
	if reviewID != nil || name != "gone" || typ != "cost" {
		t.Fatalf("run = %v %q %q, want the review id cleared and the snapshots kept", reviewID, name, typ)
	}
	if n := invCount(t, s, "agent_review_routes") + invCount(t, s, "agent_review_scopes"); n != 0 {
		t.Fatalf("%d dependent rows left", n)
	}
}

// An admin can always turn a review off (P3-R15): the update whose only change is enabled true to false keeps the
// stored routes and scope whatever they became, and counts no route.
func TestReview_TurningOffNeedsNoRoutes(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	good, pager := reviewRoute(t, s, "good"), reviewRoute(t, s, "soon-pager")
	invExec(t, s, `update notification_routes set destination = 'pagerduty' where id = $1`, pager.ID)
	a := mustReviewWithRoutesThenBreak(t, s, "a", good.ID, pager.ID)
	off := a
	off.Enabled = false
	got, err := s.UpdateReview(ctx, off, 0)
	if err != nil || got.Enabled || got.Version != 2 || len(got.RouteIDs) != 2 {
		t.Fatalf("turn off with a PagerDuty route stored = %+v, %v; want off, version 2, both routes kept", got, err)
	}

	// Not the exception: still on, or anything else changed, or a route added.
	b := mustReviewWithRoutesThenBreak(t, s, "b", good.ID, pager.ID)
	same := b
	if _, err := s.UpdateReview(ctx, same, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Errorf("saving it still on = %v, want ErrReviewInvalid", err)
	}
	renamed := b
	renamed.Enabled, renamed.Name = false, "b2"
	if _, err := s.UpdateReview(ctx, renamed, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Errorf("turning off and renaming = %v, want ErrReviewInvalid", err)
	}
	fewer := b
	fewer.Enabled, fewer.RouteIDs = false, []int64{pager.ID}
	if _, err := s.UpdateReview(ctx, fewer, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Errorf("turning off and dropping an allowed route = %v, want ErrReviewInvalid", err)
	}

	// Every route deleted: none left, and it can still be turned off, but not saved again off.
	c := mustReview(t, s, newReview("c", good.ID))
	if err := s.DeleteNotificationRoute(ctx, good.ID); err != nil {
		t.Fatal(err)
	}
	c.RouteIDs, c.Enabled = nil, false
	got, err = s.UpdateReview(ctx, c, 0)
	if err != nil || got.Enabled || len(got.RouteIDs) != 0 {
		t.Fatalf("turn off with no routes = %+v, %v", got, err)
	}
	if _, err := s.UpdateReview(ctx, got, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Errorf("saving an off review with no routes = %v, want ErrReviewInvalid", err)
	}
}

// mustReviewWithRoutesThenBreak creates an enabled review on the routes as they are allowed now; the caller has made
// one of them PagerDuty since, so the returned review is stored with a route it could not be saved with.
func mustReviewWithRoutesThenBreak(t *testing.T, s *store.Store, name string, ids ...int64) store.Review {
	t.Helper()
	invExec(t, s, `update notification_routes set destination = 'email' where id = any($1)`, ids)
	rv := mustReview(t, s, newReview(name, ids...))
	invExec(t, s, `update notification_routes set destination = 'pagerduty' where id = $1`, ids[len(ids)-1])
	return rv
}
