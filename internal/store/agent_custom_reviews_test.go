package store_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const customText = "Look at the cost of the largest services first.\nKeep the summary to three sentences."

func newCustomReview(name, text string, routeIDs ...int64) store.Review {
	r := newReview(name, routeIDs...)
	r.Type, r.Instructions = store.ReviewTypeCustom, text
	return r
}

// stepBackTo0222 turns a migrated schema into what a release before phase 4 left: the old type check and none of
// the three new columns. Dropping a column drops the checks that name it.
func stepBackTo0222(t *testing.T, s *store.Store) {
	t.Helper()
	invExec(t, s, `alter table agent_reviews drop constraint agent_reviews_type_check;
		alter table agent_reviews add constraint agent_reviews_type_check check (type in ('cost', 'security', 'reliability', 'capacity'));
		alter table agent_reviews drop column instructions;
		alter table agent_review_runs drop column instructions;
		alter table agent_review_runs drop column test`)
}

func apply0223(t *testing.T, s *store.Store) error {
	t.Helper()
	body, err := os.ReadFile("migrations/0223_agent_custom_reviews.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(context.Background(), string(body))
	return err
}

// --- migration ---

func TestMigrate_0223KeepsBuiltInRowsAndAdmitsCustom(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	stepBackTo0222(t, s)
	for _, typ := range []string{"cost", "security", "reliability", "capacity"} {
		invExec(t, s, `insert into agent_reviews (name, type, cadence, at_minute, timezone) values ($1, $1, 'daily', 480, 'UTC')`, typ)
		invExec(t, s, `insert into agent_review_runs (review_name, review_type, period_key, period_label, scheduled_for)
			values ($1, $1, $1, 'l', now())`, typ)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_reviews (name, type, cadence, at_minute, timezone) values ('c', 'custom', 'daily', 1, 'UTC')`); err == nil {
		t.Fatal("before the migration a custom type was accepted")
	}
	if err := apply0223(t, s); err != nil {
		t.Fatalf("apply 0223 over existing rows: %v", err)
	}
	var reviews, runs int
	if err := s.Pool.QueryRow(ctx, `select (select count(*) from agent_reviews where instructions = ''),
		(select count(*) from agent_review_runs where instructions = '' and not test)`).Scan(&reviews, &runs); err != nil {
		t.Fatal(err)
	}
	if reviews != 4 || runs != 4 {
		t.Fatalf("after the migration %d reviews and %d runs have the defaults, want 4 and 4", reviews, runs)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_reviews (name, type, cadence, at_minute, timezone, instructions) values ('c', 'custom', 'daily', 1, 'UTC', 'look')`); err != nil {
		t.Fatalf("a custom row with text was refused: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_reviews (name, type, cadence, at_minute, timezone, instructions) values ('b', 'bogus', 'daily', 1, 'UTC', '')`); err == nil {
		t.Fatal("an unknown type was accepted: the list must stay closed")
	}
}

func TestMigrate_0223RefusesCustomWithoutTextAndTextWithoutCustom(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	insertReview := `insert into agent_reviews (name, type, cadence, at_minute, timezone, instructions) values ($1, $2, 'daily', 1, 'UTC', $3)`
	insertRun := `insert into agent_review_runs (review_name, review_type, period_key, period_label, scheduled_for, manual, test, instructions)
		values ('n', 'cost', $1, 'l', now(), $2, $3, $4)`
	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"custom without text":         {insertReview, []any{"a", "custom", ""}},
		"built-in with text":          {insertReview, []any{"b", "cost", "text"}},
		"review text over 2000":       {insertReview, []any{"c", "custom", strings.Repeat("x", 2001)}},
		"run text over 2000":          {insertRun, []any{"d", true, false, strings.Repeat("x", 2001)}},
		"test run that is not manual": {insertRun, []any{"e", false, true, ""}},
	} {
		if _, err := s.Pool.Exec(ctx, c.sql, c.args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"custom with 2000": {insertReview, []any{"f", "custom", strings.Repeat("é", 2000)}},
		"manual test run":  {insertRun, []any{"g", true, true, ""}},
		"run with a copy":  {insertRun, []any{"h", false, false, "copy"}},
		"built-in no text": {insertReview, []any{"i", "cost", ""}},
	} {
		if _, err := s.Pool.Exec(ctx, c.sql, c.args...); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// A task of the release before this one writes with its own column lists and never names the new columns.
func TestMigrate_0223_OldInsertStatementsStillWork(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	var id int64
	if err := s.Pool.QueryRow(ctx, `insert into agent_reviews
		(name, type, enabled, cadence, at_minute, weekday, month_day, timezone, updated_by)
		values ('old', 'security', true, 'daily', 480, nullif(0, 0), nullif(0, 0), 'UTC', nullif(0, 0::bigint)) returning id`).Scan(&id); err != nil {
		t.Fatalf("phase 3 review insert: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_review_routes (review_id, route_id) values ($1, $2)`, id, route.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update agent_reviews set name = 'older', type = 'capacity', enabled = false, cadence = 'daily',
		at_minute = 5, timezone = 'UTC', version = version + 1 where id = $1`, id); err != nil {
		t.Fatalf("phase 3 review update: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_review_runs
		(review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, state, reason, requested_at, finished_at)
		values ($1, 'older', 'capacity', 'd:1', 'l', now(), false, 'requested', '', now(), null)`, id); err != nil {
		t.Fatalf("phase 3 due insert: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into agent_review_runs
		(review_id, review_name, review_type, period_key, period_label, scheduled_for, manual, requested_at)
		values ($1, 'older', 'capacity', 'x:1', 'l', now(), true, now())`, id); err != nil {
		t.Fatalf("phase 3 manual insert: %v", err)
	}
	var text, copied string
	var test bool
	if err := s.Pool.QueryRow(ctx, `select (select instructions from agent_reviews where id = $1),
		(select instructions from agent_review_runs where period_key = 'x:1'), (select test from agent_review_runs where period_key = 'x:1')`, id).Scan(&text, &copied, &test); err != nil {
		t.Fatal(err)
	}
	if text != "" || copied != "" || test {
		t.Fatalf("defaults = %q %q %v, want empty, empty, false", text, copied, test)
	}
}

// --- definitions ---

func TestCustomReview_RoundTripsItsInstructions(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	created := mustReview(t, s, newCustomReview("mine", customText, route.ID))
	if created.Type != store.ReviewTypeCustom || created.Instructions != customText {
		t.Fatalf("created = %q %q", created.Type, created.Instructions)
	}
	got, err := s.GetReview(ctx, created.ID)
	if err != nil || got.Instructions != customText {
		t.Fatalf("get = %q, %v", got.Instructions, err)
	}
	list, err := s.ListReviews(ctx)
	if err != nil || len(list) != 1 || list[0].Instructions != customText {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got.Instructions = "Only the cheapest ten."
	updated, err := s.UpdateReview(ctx, got, 0)
	if err != nil || updated.Instructions != "Only the cheapest ten." {
		t.Fatalf("update = %q, %v", updated.Instructions, err)
	}
	if again, _ := s.GetReview(ctx, created.ID); again.Instructions != "Only the cheapest ten." {
		t.Fatalf("stored = %q", again.Instructions)
	}
}

func TestCustomReview_NeedsInstructionsAndBuiltInRefusesThem(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	if _, err := s.CreateReview(ctx, newCustomReview("empty", "", route.ID), 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("custom without text = %v, want ErrReviewInvalid", err)
	}
	built := newReview("built", route.ID)
	built.Instructions = "text"
	if _, err := s.CreateReview(ctx, built, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("built-in with text = %v, want ErrReviewInvalid", err)
	}
	if n := invCount(t, s, "agent_reviews"); n != 0 {
		t.Fatalf("%d reviews written by refused creates", n)
	}
	custom := mustReview(t, s, newCustomReview("custom", customText, route.ID))
	plain := mustReview(t, s, newReview("plain", route.ID))
	custom.Instructions = ""
	if _, err := s.UpdateReview(ctx, custom, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("update to no text = %v, want ErrReviewInvalid", err)
	}
	plain.Instructions = "text"
	if _, err := s.UpdateReview(ctx, plain, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("update with text on a built-in = %v, want ErrReviewInvalid", err)
	}
	if got, _ := s.GetReview(ctx, custom.ID); got.Version != 1 || got.Instructions != customText {
		t.Fatalf("refused update changed the row: %+v", got)
	}
}

func TestCustomReview_LengthAndTextChecks(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	long := strings.Repeat("€", 2000) // 2000 characters, 6000 bytes
	rv := mustReview(t, s, newCustomReview("long", long, route.ID))
	if rv.Instructions != long {
		t.Fatal("2000 characters were not stored whole")
	}
	for name, text := range map[string]string{
		"2001 characters": strings.Repeat("a", 2001),
		"invalid UTF-8":   "look at \xff this",
		"NUL":             "look\x00 at this",
	} {
		if _, err := s.CreateReview(ctx, newCustomReview("bad "+name, text, route.ID), 0); !errors.Is(err, store.ErrReviewInvalid) {
			t.Errorf("create with %s = %v, want ErrReviewInvalid", name, err)
		}
		rv.Instructions = text
		if _, err := s.UpdateReview(ctx, rv, 0); !errors.Is(err, store.ErrReviewInvalid) {
			t.Errorf("update with %s = %v, want ErrReviewInvalid", name, err)
		}
	}
	if n := invCount(t, s, "agent_reviews"); n != 1 {
		t.Fatalf("%d reviews, want only the first", n)
	}
	if got, _ := s.GetReview(ctx, rv.ID); got.Version != 1 || got.Instructions != long {
		t.Fatalf("a refused update changed the row: version %d", got.Version)
	}
}

func TestCustomReview_TypeCannotChangeToOrFromCustom(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	plain := mustReview(t, s, newReview("plain", route.ID))
	custom := mustReview(t, s, newCustomReview("custom", customText, route.ID))

	toCustom := plain
	toCustom.Type, toCustom.Instructions = store.ReviewTypeCustom, customText
	if _, err := s.UpdateReview(ctx, toCustom, 0); !errors.Is(err, store.ErrReviewTypeFixed) {
		t.Fatalf("cost to custom = %v, want ErrReviewTypeFixed", err)
	}
	fromCustom := custom
	fromCustom.Type, fromCustom.Instructions = "security", ""
	if _, err := s.UpdateReview(ctx, fromCustom, 0); !errors.Is(err, store.ErrReviewTypeFixed) {
		t.Fatalf("custom to security = %v, want ErrReviewTypeFixed", err)
	}
	for _, id := range []int64{plain.ID, custom.ID} {
		if got, _ := s.GetReview(ctx, id); got.Version != 1 {
			t.Fatalf("review %d changed by a refused update: version %d", id, got.Version)
		}
	}
	// A change among the built-in types is still allowed.
	plain.Type = "security"
	if got, err := s.UpdateReview(ctx, plain, 0); err != nil || got.Type != "security" || got.Version != 2 {
		t.Fatalf("cost to security = %+v, %v", got, err)
	}
	// An unknown review is still not found, not a type error.
	ghost := toCustom
	ghost.ID = 424242
	if _, err := s.UpdateReview(ctx, ghost, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown review = %v, want ErrNotFound", err)
	}
}

func TestCustomReview_InstructionEditRaisesVersionAndKeepsTheSchedule(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := mustReview(t, s, newCustomReview("mine", customText, route.ID))
	longAgo(t, s, rv.ID)
	before, err := s.GetReview(ctx, rv.ID)
	if err != nil {
		t.Fatal(err)
	}
	before.Instructions = "A different request."
	after, err := s.UpdateReview(ctx, before, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version+1 || !after.ScheduleChangedAt.Equal(before.ScheduleChangedAt) {
		t.Fatalf("version %d -> %d, schedule %v -> %v; want the version raised and the schedule kept", before.Version, after.Version, before.ScheduleChangedAt, after.ScheduleChangedAt)
	}
}

func TestCustomReview_CountsTowardTheLimitOfTwenty(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	for i := 0; i < store.MaxAgentReviews/2; i++ {
		mustReview(t, s, newReview("built "+string(rune('a'+i)), route.ID))
		mustReview(t, s, newCustomReview("custom "+string(rune('a'+i)), customText, route.ID))
	}
	if _, err := s.CreateReview(ctx, newCustomReview("one more", customText, route.ID), 0); !errors.Is(err, store.ErrReviewLimit) {
		t.Fatalf("21st review (custom) = %v, want ErrReviewLimit", err)
	}
	if _, err := s.CreateReview(ctx, newReview("one more built-in", route.ID), 0); !errors.Is(err, store.ErrReviewLimit) {
		t.Fatalf("21st review (built-in) = %v, want ErrReviewLimit", err)
	}
}

// --- run rows ---

// startedCustomRun makes a manual run of a custom review and starts its first attempt with the given text.
func startedCustomRun(t *testing.T, s *store.Store, text string) (store.ReviewRun, store.Review) {
	t.Helper()
	ctx := context.Background()
	rv := mustReview(t, s, newCustomReview("mine", customText, reviewRoute(t, s, "r").ID))
	if _, err := s.RequestManualReviewRun(ctx, rv.ID, "by hand", false, revNow); err != nil {
		t.Fatal(err)
	}
	run := claimReview(t, s, revNow)
	if err := s.StartReviewRun(ctx, run.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", Instructions: text}, revNow); err != nil {
		t.Fatal(err)
	}
	return run, rv
}

func TestStartReview_CopiesTheInstructionsOnce(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, _ := startedCustomRun(t, s, "first text")
	if got := reviewRunState(t, s, run.ID); got.Instructions != "first text" {
		t.Fatalf("copy after the first start = %q", got.Instructions)
	}
	if err := s.ReleaseReviewRun(ctx, run.Claim(), revNow, true); err != nil {
		t.Fatal(err)
	}
	second := claimReview(t, s, revNow.Add(time.Minute))
	if second.Instructions != "first text" {
		t.Fatalf("claimed row carries %q, want the copy", second.Instructions)
	}
	if err := s.StartReviewRun(ctx, second.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", Instructions: "second text"}, revNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, run.ID); got.Instructions != "first text" || got.Attempts != 2 {
		t.Fatalf("after the second start: copy %q, attempts %d; want the first copy kept", got.Instructions, got.Attempts)
	}
}

func TestStartReview_BuiltInRunKeepsAnEmptyCopy(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "plain", reviewRoute(t, s, "r").ID)
	manual, err := s.RequestManualReviewRun(ctx, rv.ID, "by hand", false, revNow)
	if err != nil || manual.Instructions != "" {
		t.Fatalf("manual run = %+v, %v", manual, err)
	}
	run := claimReview(t, s, revNow)
	if err := s.StartReviewRun(ctx, run.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m"}, revNow); err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, run.ID); got.Instructions != "" {
		t.Fatalf("copy = %q, want empty", got.Instructions)
	}
	// A custom run is only copied when it starts, not when it is requested.
	custom := mustReview(t, s, newCustomReview("custom", customText, reviewRoute(t, s, "r2").ID))
	requested, err := s.RequestManualReviewRun(ctx, custom.ID, "by hand", false, revNow)
	if err != nil || requested.Instructions != "" {
		t.Fatalf("requested custom run = %+v, %v; want no copy yet", requested, err)
	}
}

func TestManual_TestRunIsMarkedPlannedWhenMade(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	test, err := s.RequestManualReviewRun(ctx, a.ID, "Test on x", true, revNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, test.ID); !got.Test || !got.Manual || got.State != "requested" || got.DeliveryPlannedAt == nil || !got.DeliveryPlannedAt.Equal(revNow) {
		t.Fatalf("test run = %+v, want test, manual, requested and planned at now", got)
	}
	live, err := s.RequestManualReviewRun(ctx, b.ID, "Run on x", false, revNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, live.ID); got.Test || got.DeliveryPlannedAt != nil {
		t.Fatalf("ordinary manual run = %+v, want not a test and not planned", got)
	}
}

func TestManual_TestRunRefusedWhileOneIsWaitingOrRunning(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
	rv.Enabled = false
	if _, err := s.UpdateReview(ctx, rv, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestManualReviewRun(ctx, rv.ID, "Test one", true, revNow); err != nil {
		t.Fatalf("a test run of a disabled review: %v", err)
	}
	for _, test := range []bool{true, false} {
		if _, err := s.RequestManualReviewRun(ctx, rv.ID, "again", test, revNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("test=%v while waiting = %v, want ErrConflict", test, err)
		}
	}
	claimReview(t, s, revNow)
	for _, test := range []bool{true, false} {
		if _, err := s.RequestManualReviewRun(ctx, rv.ID, "again", test, revNow); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("test=%v while running = %v, want ErrConflict", test, err)
		}
	}
}

// The planner (phase 3 Task 10) is not in this tree. Its shape is a finished row with no delivery_planned_at; a test
// run must never match it, and an ordinary run must, or the check proves nothing. Task 5 adds the planner's own query.
func TestReviewRuns_NoPlannerQuerySelectsATestRun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	test, err := s.RequestManualReviewRun(ctx, a.ID, "Test one", true, revNow)
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.RequestManualReviewRun(ctx, b.ID, "Run one", false, revNow)
	if err != nil {
		t.Fatal(err)
	}
	invExec(t, s, `update agent_review_runs set state = 'done', summary = 's', finished_at = $1, claimed_until = null`, revNow)
	rows, err := s.Pool.Query(ctx, `select id from agent_review_runs where state = 'done' and delivery_planned_at is null`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var unplanned []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		unplanned = append(unplanned, id)
	}
	if len(unplanned) != 1 || unplanned[0] != live.ID || unplanned[0] == test.ID {
		t.Fatalf("finished and unplanned = %v, want only the ordinary run %d", unplanned, live.ID)
	}
}

func TestSkipReview_InstructionsReason(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, _ := startedCustomRun(t, s, "text")
	if err := s.SkipReviewRun(ctx, run.Claim(), "instructions", revNow); err != nil {
		t.Fatalf("skip with the new reason: %v", err)
	}
	if got := reviewRunState(t, s, run.ID); got.State != "skipped" || got.Reason != "instructions" || got.Instructions != "text" {
		t.Fatalf("run = %+v", got)
	}
	if err := s.SkipReviewRun(ctx, run.Claim(), "no such reason", revNow); err == nil {
		t.Fatal("an unknown reason was accepted")
	}
}

func TestDeleteReview_KeepsTheCopiesOnItsRuns(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, rv := startedCustomRun(t, s, "the copy")
	if err := s.DeleteReview(ctx, rv.ID); err != nil {
		t.Fatal(err)
	}
	got := reviewRunState(t, s, run.ID)
	if got.ReviewID != nil || got.Instructions != "the copy" || got.ReviewType != store.ReviewTypeCustom {
		t.Fatalf("run after the delete = %+v, want the copy and the snapshots kept", got)
	}
}

func TestRetention_RemovesTheCopyWithTheRun(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	now := time.Now()
	old := insertReviewRunRow(t, s, "old", "done", hoursAgo(now, 100))
	young := insertReviewRunRow(t, s, "young", "done", hoursAgo(now, 60))
	invExec(t, s, `update agent_review_runs set instructions = 'a copy'`)
	if _, err := s.RunRetention(ctx, now, store.Retention{AgentRuns: 72 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if reviewRunExists(t, s, old) || !reviewRunExists(t, s, young) {
		t.Fatal("the old run should go and the young one stay")
	}
	if n := invCount(t, s, "agent_review_runs where instructions <> ''"); n != 1 {
		t.Fatalf("%d copies left, want only the young run's", n)
	}
	if got := reviewRunState(t, s, young); got.Instructions != "a copy" {
		t.Fatalf("young run's copy = %q", got.Instructions)
	}
}

// An older task's open read blocks the migration's ALTER, and every later statement queues behind it, so the
// migration must give up (and the next start retry it) well before the dispatcher's 5 second write timeout.
func TestMigrate_0223GivesUpOnALockInsteadOfQueueingEveryWriter(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	stepBackTo0222(t, s)
	reader, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			reader.Rollback(ctx) //nolint:errcheck // nothing was written
		}
	}
	defer release()
	if _, err := reader.Exec(ctx, `select count(*) from agent_reviews`); err != nil {
		t.Fatal(err)
	}
	migrated := make(chan error, 1)
	go func() { migrated <- apply0223(t, s) }()
	select {
	case err := <-migrated:
		if err == nil || !strings.Contains(err.Error(), "55P03") {
			t.Fatalf("migration under an open reader = %v, want a lock timeout (55P03)", err)
		}
	case <-time.After(4 * time.Second):
		release()
		<-migrated
		t.Fatal("the migration was still waiting after 4 seconds: it must time out on its lock within 2")
	}
	release()
	if err := apply0223(t, s); err != nil {
		t.Fatalf("retry once the reader is gone: %v", err)
	}
}

// Turning a review off is exempt from the route checks only when nothing else changes, the text included.
func TestCustomReview_TurningOffWhileEditingTheTextIsAFullSave(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	rv := mustReview(t, s, newCustomReview("mine", customText, route.ID))
	invExec(t, s, `update notification_routes set destination = 'pagerduty' where id = $1`, route.ID)
	stored := func() store.Review {
		t.Helper()
		got, err := s.GetReview(ctx, rv.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	off := stored()
	off.Enabled = false

	edited := off
	edited.Instructions = "A different request."
	if _, err := s.UpdateReview(ctx, edited, 0); !errors.Is(err, store.ErrReviewInvalid) {
		t.Fatalf("off and edited text with a PagerDuty route = %v, want ErrReviewInvalid", err)
	}
	if got := stored(); !got.Enabled || got.Version != 1 || got.Instructions != customText {
		t.Fatalf("a refused update changed the row: %+v", got)
	}
	for name, text := range map[string]string{"empty": "", "over-long": strings.Repeat("a", 2001)} {
		bad := off
		bad.Instructions = text
		if _, err := s.UpdateReview(ctx, bad, 0); !errors.Is(err, store.ErrReviewInvalid) {
			t.Errorf("off with %s text = %v, want ErrReviewInvalid", name, err)
		}
	}
	got, err := s.UpdateReview(ctx, off, 0)
	if err != nil {
		t.Fatalf("turning off alone: %v", err)
	}
	if got.Enabled || got.Instructions != customText || len(got.RouteIDs) != 1 || got.RouteIDs[0] != route.ID {
		t.Fatalf("after turning off = %+v, want off with the text and routes kept", got)
	}
}

// P4-R3: a test run is never shown to a reader of the review runs, which phase 3 code decides by covered tags.
func TestTestRun_StoresNoCoveredTags(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	start := func(review store.Review, test bool, at time.Time) store.ReviewRun {
		t.Helper()
		if _, err := s.RequestManualReviewRun(ctx, review.ID, "by hand", test, at); err != nil {
			t.Fatal(err)
		}
		run := claimReview(t, s, at)
		if err := s.StartReviewRun(ctx, run.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", CoveredTags: []int64{7}}, at); err != nil {
			t.Fatal(err)
		}
		return reviewRunState(t, s, run.ID)
	}
	test := start(a, true, revNow)
	if len(test.CoveredTags) != 0 {
		t.Fatalf("test run covered tags = %v, want none", test.CoveredTags)
	}
	invExec(t, s, `update agent_review_runs set state = 'done', finished_at = $1, summary = 's', claimed_until = null`, revNow)
	normal := start(b, false, revNow.Add(time.Minute))
	if len(normal.CoveredTags) != 1 || normal.CoveredTags[0] != 7 {
		t.Fatalf("normal run covered tags = %v, want [7]", normal.CoveredTags)
	}
	runs, err := s.ListReadableReviewRuns(ctx, store.ReadableReviewRunFilter{ViewableTags: []int64{7}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != normal.ID {
		t.Fatalf("readable runs for a reader with View on the tag = %+v, want only the normal run %d", runs, normal.ID)
	}
}

// The copy is made once even when another transaction wrote one first, and a free release does not undo it.
func TestStartReview_CopyHoldsAgainstAnUncommittedCopyAndAFreeRelease(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	run, _ := startedCustomRun(t, s, "first text")
	if err := s.ReleaseReviewRun(ctx, run.Claim(), time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, run.ID); got.Instructions != "first text" || got.Attempts != 0 {
		t.Fatalf("after a free release: copy %q, attempts %d; want the copy kept", got.Instructions, got.Attempts)
	}
	again := claimReview(t, s, revNow.Add(time.Minute))
	if again.Attempts != 1 {
		t.Fatalf("attempts = %d, want the first attempt again", again.Attempts)
	}
	if err := s.StartReviewRun(ctx, again.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", Instructions: "second text"}, revNow); err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s, run.ID); got.Instructions != "first text" {
		t.Fatalf("copy after a start on the re-claimed first attempt = %q, want the first", got.Instructions)
	}

	// Another transaction writes the copy and has not committed when the start arrives.
	s2 := storetest.Open(t)
	other, _ := startedCustomRun(t, s2, "")
	if got := reviewRunState(t, s2, other.ID); got.Instructions != "" {
		t.Fatalf("setup: copy = %q", got.Instructions)
	}
	tx, err := s2.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // already committed on the happy path
	if _, err := tx.Exec(ctx, `update agent_review_runs set instructions = 'other writer' where id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s2.StartReviewRun(ctx, other.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", Instructions: "late"}, revNow)
	}()
	time.Sleep(300 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := reviewRunState(t, s2, other.ID); got.Instructions != "other writer" {
		t.Fatalf("copy = %q, want the other transaction's copy kept", got.Instructions)
	}
}

// Phase 3's readers decide by tags and know no test run: the finish leaves a test run without any, as the start does.
func TestTestRun_StoresNoCoveredTagsAtTheFinish(t *testing.T) {
	for name, c := range map[string]struct {
		test          bool
		state, reason string
		also, want    []int64
	}{
		"a test run that is done":  {true, "done", "", []int64{7, 9}, []int64{}},
		"a test run that failed":   {true, "failed", "error", nil, []int64{}},
		"an ordinary run":          {false, "done", "", []int64{9}, []int64{7, 9}},
		"an ordinary run, no more": {false, "done", "", nil, []int64{7}},
	} {
		t.Run(name, func(t *testing.T) {
			s, ctx := storetest.Open(t), context.Background()
			rv := oldReview(t, s, "a", reviewRoute(t, s, "r").ID)
			if _, err := s.RequestManualReviewRun(ctx, rv.ID, "by hand", c.test, revNow); err != nil {
				t.Fatal(err)
			}
			run := claimReview(t, s, revNow)
			if err := s.StartReviewRun(ctx, run.Claim(), store.ReviewStart{RunID: uuid.New(), Provider: "p", Model: "m", CoveredTags: []int64{7}}, revNow); err != nil {
				t.Fatal(err)
			}
			var res *store.InvestigationResult
			if c.state == "done" {
				res = &store.InvestigationResult{Summary: "s"}
			}
			if err := s.FinishReviewRun(ctx, run.Claim(), c.state, c.reason, res, c.also, revNow); err != nil {
				t.Fatal(err)
			}
			if got := reviewRunState(t, s, run.ID).CoveredTags; !reflect.DeepEqual(got, c.want) {
				t.Errorf("covered = %v, want %v", got, c.want)
			}
		})
	}
}

// P4-R5: a task of phase 3 that claims a test row writes tags at its start; the check refuses that write.
func TestTestRun_RawSQLRefusesTagsOnATestRow(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	route := reviewRoute(t, s, "r")
	a, b := oldReview(t, s, "a", route.ID), oldReview(t, s, "b", route.ID)
	test, err := s.RequestManualReviewRun(ctx, a.ID, "Test one", true, revNow)
	if err != nil {
		t.Fatal(err)
	}
	normal, err := s.RequestManualReviewRun(ctx, b.ID, "Run one", false, revNow)
	if err != nil {
		t.Fatal(err)
	}
	var pgErr *pgconn.PgError
	if _, err := s.Pool.Exec(ctx, `update agent_review_runs set covered_tags = '{7}' where id = $1`, test.ID); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("tags on a test row = %v, want a check violation (23514)", err)
	}
	if _, err := s.Pool.Exec(ctx, `update agent_review_runs set covered_tags = '{}' where id = $1`, test.ID); err != nil {
		t.Fatalf("no tags on a test row: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `update agent_review_runs set covered_tags = '{7}' where id = $1`, normal.ID); err != nil {
		t.Fatalf("tags on a normal row: %v", err)
	}
}
