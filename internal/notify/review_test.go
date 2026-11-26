package notify

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const testReviewSummary = "Spend is flat. Two services hold idle capacity."

// newReviewEnv is newInvEnv with the review step on as well.
func newReviewEnv(t *testing.T) *env {
	t.Helper()
	e := newInvEnv(t)
	e.d.Reviews = true
	return e
}

func (e *env) review(name string, routes ...store.NotificationRoute) int64 {
	e.t.Helper()
	r := store.Review{Name: name, Type: "cost", Enabled: true, Cadence: "daily", AtMinute: 480, Timezone: "Europe/Berlin"}
	for _, route := range routes {
		r.RouteIDs = append(r.RouteIDs, route.ID)
	}
	created, err := e.st.CreateReview(context.Background(), r, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return created.ID
}

// finishReview records a done run of the review, the way the reviewer job leaves it.
func (e *env) finishReview(reviewID int64) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Pool.QueryRow(context.Background(), `insert into agent_review_runs
		(review_id, review_name, review_type, period_key, period_label, scheduled_for, state, summary, requested_at, finished_at)
		select id, name, type, 'x:' || gen_random_uuid(), '21 November 2026', $2, 'done', $3, $2, $2 from agent_reviews where id = $1
		returning id`, reviewID, e.clock, testReviewSummary).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) reviewRows() []store.NotificationDelivery {
	e.t.Helper()
	var out []store.NotificationDelivery
	for _, d := range e.deliveries() {
		if d.Event == "review" {
			out = append(out, d)
		}
	}
	return out
}

func (e *env) reviewRow() store.NotificationDelivery {
	e.t.Helper()
	rows := e.reviewRows()
	if len(rows) != 1 {
		e.t.Fatalf("review rows = %+v, want one", rows)
	}
	return rows[0]
}

// stageReview leaves a done run of a review on these routes whose rows are planned but unsent (the guard refused the
// sends once). It returns the review and run ids.
func (e *env) stageReview(routes ...store.NotificationRoute) (reviewID, runID int64) {
	e.t.Helper()
	reviewID = e.review("Monthly spend", routes...)
	runID = e.finishReview(reviewID)
	e.d.Guard = func(context.Context) error { return store.ErrLeaseLost }
	e.cycle()
	e.d.Guard = nil
	rows := e.reviewRows()
	if len(rows) != len(routes) {
		e.t.Fatalf("staged review rows = %+v, want one per route", rows)
	}
	for _, row := range rows {
		if row.Status != "pending" || row.Attempts != 0 || row.ClaimedUntil != nil {
			e.t.Fatalf("staged review row = %+v, want pending and unsent", row)
		}
	}
	return reviewID, runID
}

func (e *env) reviewsSent(route string) int { return e.sender.count(route, EventReview) }

// order is what one route received, "opened,diagnosis,review".
func (e *env) order(route string) string {
	var out []string
	for _, rec := range e.sender.records() {
		if rec.Route == route {
			out = append(out, string(rec.Event))
		}
	}
	return strings.Join(out, ",")
}

func isReviewPlan(sql string) bool {
	return strings.Contains(sql, "agent_review_runs") && strings.Contains(sql, "delivery_planned_at is null")
}

func isReviewClaim(sql string) bool {
	return isDiagnosisClaim(sql) && strings.Contains(sql, "event = 'review'")
}

func isReviewStatement(sql string) bool {
	return strings.Contains(sql, "agent_review") || isReviewClaim(sql)
}

// statementHook runs fn before every statement that match accepts; fn may wait on the statement's context.
type statementHook struct {
	match func(sql string) bool
	fn    func(ctx context.Context, sql string)
}

func (h *statementHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if h.match(d.SQL) {
		h.fn(ctx, d.SQL)
	}
	return ctx
}

func (h *statementHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// untilCancelled is a statement that never answers within its timeout. It holds no lock.
func untilCancelled(ctx context.Context, _ string) {
	select {
	case <-ctx.Done():
	case <-time.After(20 * time.Second):
	}
}

// pollFor waits in a hook, where failing the test is not allowed.
func pollFor(wait time.Duration, cond func() bool) {
	for deadline := time.Now().Add(wait); !cond() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
}

// --- the anti-goal ---

func TestDispatcher_AlertRowsAreClaimedBeforeAnyReviewStatement(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	e.finishReview(e.review("Monthly spend", r))
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tracer := &stmtTracer{}
	e.tracedStore(tracer, 0)
	e.cycle()

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	alertClaim, diagnosisClaim, firstReview, reviewClaim := -1, -1, -1, -1
	first := func(at *int, i int, match bool) {
		if *at < 0 && match {
			*at = i
		}
	}
	for i, sql := range tracer.sql {
		first(&alertClaim, i, strings.Contains(sql, "update notification_deliveries") && strings.Contains(sql, "next_attempt_at <= $1"))
		first(&diagnosisClaim, i, isDiagnosisClaim(sql) && !isReviewClaim(sql))
		first(&firstReview, i, isReviewStatement(sql))
		first(&reviewClaim, i, isReviewClaim(sql))
	}
	if alertClaim < 0 || diagnosisClaim < alertClaim || firstReview < diagnosisClaim || reviewClaim < firstReview {
		t.Fatalf("alert claim at %d, diagnosis claim at %d, first review statement at %d, review claim at %d, want all present and in this order",
			alertClaim, diagnosisClaim, firstReview, reviewClaim)
	}
}

func TestDispatcher_OpenedSentWhenReviewPlanningFails(t *testing.T) {
	e := newReviewEnv(t)
	e.route("one")
	e.exec(`alter table agent_review_runs rename to agent_review_runs_gone`)
	e.exec(`alter table agent_review_routes rename to agent_review_routes_gone`)
	e.exec(`alter table agent_reviews rename to agent_reviews_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.d.Cycle(context.Background()); err != nil {
		t.Fatalf("cycle returned the review error: %v", err)
	}
	e.d.Wait()
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatalf("opened sent %d times, want once", e.sender.count("one", EventOpened))
	}
	if got := e.d.InvestigationErrors(); got != 1 {
		t.Fatalf("errors = %d, want 1: the failed planning was tried, counted and kept out of the cycle", got)
	}
}

// hangSender never answers the given events, and delegates every other one.
type hangSender struct {
	inner   Sender
	release chan struct{}
	mu      sync.Mutex
	calls   map[Event]int // of the events that hang
	live    int
}

func (h *hangSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, m Message) Result {
	h.mu.Lock()
	_, hangs := h.calls[m.Event]
	if hangs {
		h.calls[m.Event]++
		h.live++
	}
	h.mu.Unlock()
	if !hangs {
		return h.inner.Send(ctx, r, s, m)
	}
	defer func() { h.mu.Lock(); h.live--; h.mu.Unlock() }()
	select {
	case <-ctx.Done():
	case <-h.release:
	case <-time.After(20 * time.Second): // a failing test must not hang on its own sender
	}
	return Result{Retryable: true, Note: "Network error"}
}

func (h *hangSender) started(event Event) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[event]
}

func (h *hangSender) running() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live
}

func (e *env) hang(events ...Event) *hangSender {
	h := &hangSender{inner: e.d.Senders, release: make(chan struct{}), calls: map[Event]int{}}
	for _, event := range events {
		h.calls[event] = 0
	}
	e.d.Senders = h
	e.t.Cleanup(func() { close(h.release); e.d.Wait() })
	return h
}

func TestDispatcher_OpenedSentWhileAReviewSendHangs(t *testing.T) {
	e := newReviewEnv(t)
	h := e.hang(EventReview)
	r := e.route("one")
	e.finishReview(e.review("Monthly spend", r))
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.eventually(5*time.Second, "the review send to start", func() bool { return h.started(EventReview) == 1 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.eventually(3*time.Second, "the opened message while a review send hangs on the same route", func() bool { return e.openedSent("one") })
	if h.running() != 1 {
		t.Fatal("the review send ended before the opened message was sent")
	}
}

func TestDispatcher_PanicInTheReviewStepDoesNotStopTheLoop(t *testing.T) {
	e := newReviewEnv(t)
	e.route("one")
	h := &panickingHandler{Handler: slog.NewTextHandler(&strings.Builder{}, nil)}
	h.armed.Store(true)
	e.d.Log = slog.New(h)
	// The warning about the failed planning is the first one of the cycle; the handler panics on it.
	e.exec(`alter table agent_review_runs rename to agent_review_runs_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.cycleWithin(10 * time.Second); err != nil {
		t.Fatalf("cycle returned the panic's effect: %v", err)
	}
	e.d.Wait()
	if h.armed.Load() {
		t.Fatal("the panic was never provoked")
	}
	if got := e.d.InvestigationErrors(); got != 2 {
		t.Fatalf("errors = %d, want 2: the failed planning and the panic", got)
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	if e.sender.count("one", EventOpened) != 2 {
		t.Fatalf("opened sent %d times after the panic, want 2: the loop must go on", e.sender.count("one", EventOpened))
	}
}

func TestDispatcher_WithReviewsOffNothingNewRuns(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	review, _ := e.stageReview(r) // a due row
	unplanned := e.finishReview(review)
	e.d.Reviews = false
	e.advance(time.Second)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tracer := &stmtTracer{}
	e.tracedStore(tracer, 0)
	e.cycle()
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if len(tracer.sql) == 0 {
		t.Fatal("the tracer saw no statement")
	}
	for _, sql := range tracer.sql {
		if isReviewStatement(sql) {
			t.Errorf("with reviews off a cycle ran %q", sql)
		}
	}
	if row := e.reviewRow(); row.Status != "pending" || row.Attempts != 0 || e.reviewsSent("one") != 0 {
		t.Fatalf("row = %+v, want it left alone", row)
	}
	if run, err := e.st.GetReviewRun(context.Background(), unplanned); err != nil || run.DeliveryPlannedAt != nil {
		t.Fatalf("run = %+v, %v, want it not planned", run, err)
	}
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatal("opened not sent")
	}
}

// Every statement of the step is slow. With and without the diagnosis part, the step ends inside the timeouts the
// cycle had before reviews: one for recording, one for everything after it.
func TestDispatcher_ReviewStepStaysInsideThePlanTimeout(t *testing.T) {
	const timeout = 2 * time.Second
	const slack = 800 * time.Millisecond
	isRecording := func(sql string) bool { return sql == recordingLock }
	isDiagnosisPlan := func(sql string) bool { return strings.Contains(sql, "join agent_investigation_incidents") }
	for name, tc := range map[string]struct {
		investigate bool
		slow        func(sql string) bool
		limit       time.Duration
		errors      int64
	}{
		"reviews alone": {false, func(sql string) bool { return isReviewPlan(sql) || isReviewClaim(sql) }, timeout, 2},
		"a fast diagnosis part": {true, func(sql string) bool {
			return isRecording(sql) || isReviewPlan(sql) || isReviewClaim(sql)
		}, 2 * timeout, 3},
		"a slow diagnosis plan": {true, func(sql string) bool {
			return isRecording(sql) || isDiagnosisPlan(sql) || isReviewPlan(sql) || isReviewClaim(sql)
		}, 2 * timeout, 4},
		"everything slow": {true, func(sql string) bool {
			return isRecording(sql) || isDiagnosisPlan(sql) || isDiagnosisClaim(sql) || isReviewPlan(sql)
		}, 2 * timeout, 4},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // the cases only wait
			e := newReviewEnv(t)
			e.d.Investigate = tc.investigate
			e.d.InvestigateTimeout = timeout
			r := e.switched("one")
			e.finishReview(e.review("Monthly spend", r))
			e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
			e.tracedStore(&statementHook{match: tc.slow, fn: untilCancelled}, 0)
			start := time.Now()
			if err := e.cycleWithin(tc.limit + slack); err != nil {
				t.Fatalf("cycle returned %v, want the step's errors kept out of it", err)
			}
			if took := time.Since(start); took < tc.limit/2 {
				t.Fatalf("the cycle took %v: the slow statements were never reached", took)
			}
			e.waitWithin(5*time.Second, "the alert send")
			if e.sender.count("one", EventOpened) != 1 {
				t.Fatalf("opened sent %d times, want once", e.sender.count("one", EventOpened))
			}
			if got := e.d.InvestigationErrors(); got != tc.errors {
				t.Fatalf("errors = %d, want %d, one per slow step", got, tc.errors)
			}
		})
	}
}

// Review lookups that all block must leave connections of a pool of four for a new alert.
func TestDispatcher_BlockedReviewLookupsLeaveThePoolToAlerts(t *testing.T) {
	e := newReviewEnv(t)
	var routes []store.NotificationRoute
	for i := range 5 {
		routes = append(routes, e.route(fmt.Sprintf("r%d", i)))
	}
	e.stageReview(routes...)
	e.d.InvestigateTimeout = time.Minute // the cap alone must protect the pool
	var lookups atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	e.tracedStore(&statementHook{
		match: func(sql string) bool {
			return strings.Contains(sql, "agent_review_runs where id = $1") && !isReviewPlan(sql)
		},
		fn: func(ctx context.Context, sql string) {
			lookups.Add(1)
			untilCancelled(ctx, sql)
		},
	}, 4)
	t.Cleanup(func() { cancel(); e.d.Wait() })
	e.advance(time.Second)
	for range 3 {
		if err := e.cycleWithCtx(ctx, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	e.eventually(5*time.Second, "two blocked lookups", func() bool { return lookups.Load() >= 2 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.cycleWithCtx(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	e.eventually(3*time.Second, "the new opened message while review lookups block", func() bool { return e.openedSent("r0") })
	if lookups.Load() != 2 {
		t.Fatalf("review lookups at once = %d, want 2", lookups.Load())
	}
}

// Diagnosis and review sends share one cap, whatever mix of them hangs.
func TestDispatcher_AtMostTwoFollowUpSendsAcrossCycles(t *testing.T) {
	e := newReviewEnv(t)
	h := e.hang(EventReview, EventDiagnosis)
	e.switched("diagnosed")
	var routes []store.NotificationRoute
	for i := range 4 {
		routes = append(routes, e.route(fmt.Sprintf("r%d", i)))
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.finishReview(e.review("Monthly spend", routes...))
	for range 4 {
		e.advance(time.Second)
		if err := e.cycleWithin(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	e.eventually(5*time.Second, "two sends", func() bool { return h.started(EventDiagnosis)+h.started(EventReview) >= 2 })
	time.Sleep(200 * time.Millisecond)
	if h.started(EventDiagnosis) != 1 || h.started(EventReview) != 1 {
		t.Fatalf("diagnosis sends = %d, review sends = %d, want one of each: two follow-up sends over every cycle", h.started(EventDiagnosis), h.started(EventReview))
	}
	if rows := e.reviewRows(); len(rows) != 4 {
		t.Fatalf("review rows = %d, want 4: planning needs no free worker", len(rows))
	}
}

func TestDispatcher_FullFollowUpLaneStillPlansReviews(t *testing.T) {
	e := newReviewEnv(t)
	h := e.hang(EventDiagnosis)
	e.switched("a")
	e.switched("b")
	r := e.route("reviewed")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.eventually(5*time.Second, "both workers to be busy", func() bool { return h.started(EventDiagnosis) == 2 })
	e.finishReview(e.review("Monthly spend", r))
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if row := e.reviewRow(); row.Status != "pending" || row.ClaimedUntil != nil {
		t.Fatalf("row = %+v, want it planned and not claimed while no worker is free", row)
	}
}

func TestDispatcher_OneReviewSendPerRouteAtATime(t *testing.T) {
	e := newReviewEnv(t)
	h := e.hang(EventReview)
	r := e.route("one")
	e.finishReview(e.review("Monthly spend", r))
	e.finishReview(e.review("Weekly risks", r))
	for range 3 {
		if err := e.cycleWithin(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Second)
	}
	e.eventually(5*time.Second, "a review send", func() bool { return h.started(EventReview) >= 1 })
	time.Sleep(200 * time.Millisecond)
	if h.started(EventReview) != 1 {
		t.Fatalf("review sends started = %d, want one while the first still hangs", h.started(EventReview))
	}
}

func TestDispatcher_AReviewWaitsForTheRoutesAlertSend(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	e.stageReview(r)
	h := &openedHangSender{inner: e.d.Senders, release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(h.release) })
	t.Cleanup(func() { release(); e.d.Wait() })
	e.d.Senders = h
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if e.reviewsSent("one") != 0 {
		t.Fatal("a review was sent while an alert send of the same route was running")
	}
	release()
	e.waitWithin(5*time.Second, "the alert send")
	e.advance(time.Second)
	e.cycle()
	if got := e.order("one"); got != "opened,review" {
		t.Fatalf("sent %q, want the opened message and then the review", got)
	}
}

// The plan budget is split, so a review plan that is slow leaves the review claim its own time.
func TestDispatcher_ASlowReviewPlanDoesNotStopDueMessages(t *testing.T) {
	e := newReviewEnv(t)
	e.d.InvestigateTimeout = time.Second
	e.switched("diagnosed")
	reviewed := e.route("reviewed")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.stageReview(reviewed)
	e.tracedStore(&statementHook{match: isReviewPlan, fn: untilCancelled}, 0)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.waitWithin(5*time.Second, "the sends")
	if e.sender.count("diagnosed", EventDiagnosis) != 1 || e.reviewsSent("reviewed") != 1 {
		t.Fatalf("diagnoses = %d, reviews = %d while review planning is slow, want both due rows sent",
			e.sender.count("diagnosed", EventDiagnosis), e.reviewsSent("reviewed"))
	}
}

func TestDispatcher_ReviewsDoNotNeedTheInvestigator(t *testing.T) {
	e := newReviewEnv(t)
	e.d.Investigate = false
	r := e.switched("one", func(r *store.NotificationRoute) { r.Destination = DestWebhook })
	msgs := e.recordMessages()
	e.finishReview(e.review("Monthly spend", r))
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tracer := &stmtTracer{}
	e.tracedStore(tracer, 0)
	e.cycle()
	e.advance(time.Second)
	e.cycle()
	if got := e.order("one"); got != "opened,review" {
		t.Fatalf("sent %q, want the opened message and then the review", got)
	}
	if opened := msgs.of(EventOpened); len(opened) != 1 || opened[0].Investigate || opened[0].IncidentID != 0 || opened[0].Review != nil {
		t.Fatalf("opened = %+v, want it as it is without the agent", opened)
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	for _, sql := range tracer.sql {
		if strings.Contains(sql, "agent_investigation") || isDiagnosisClaim(sql) && !isReviewClaim(sql) {
			t.Errorf("with the investigator off a cycle ran %q", sql)
		}
	}
}

func TestDispatcher_ReviewErrorsAreCountedAndWarned(t *testing.T) {
	e := newReviewEnv(t)
	e.route("one")
	var log bytes.Buffer
	e.d.Log = slog.New(slog.NewTextHandler(&log, nil))
	broken := func(sql string) bool { return isReviewPlan(sql) || isReviewClaim(sql) }
	e.d.InvestigateTimeout = 600 * time.Millisecond
	e.tracedStore(&statementHook{match: broken, fn: untilCancelled}, 0)
	e.cycle()
	if got := e.d.InvestigationErrors(); got != 2 {
		t.Fatalf("errors = %d, want 2: planning and claiming", got)
	}
	for _, want := range []string{`level=WARN`, `step="plan reviews"`, `step="claim reviews"`, `count=1`, `count=2`} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log has no %s:\n%s", want, log.String())
		}
	}
}

// A shutdown during either review statement is not an error.
func TestDispatcher_ACancelledReviewStepReportsNoError(t *testing.T) {
	for name, match := range map[string]func(string) bool{"plan": isReviewPlan, "claim": isReviewClaim} {
		t.Run(name, func(t *testing.T) {
			e := newReviewEnv(t)
			e.d.InvestigateTimeout = time.Minute
			e.route("one")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reached atomic.Bool
			e.tracedStore(&statementHook{match: match, fn: func(context.Context, string) { reached.Store(true); cancel() }}, 0)
			e.cycleWithCtx(ctx, 10*time.Second) //nolint:errcheck // a cancelled cycle reports its own errors
			if !reached.Load() || e.d.InvestigationErrors() != 0 {
				t.Fatalf("statement reached = %v, errors = %d, want it reached and 0 errors for a shutdown", reached.Load(), e.d.InvestigationErrors())
			}
		})
	}
}

type panickingReviewSender struct {
	inner Sender
	calls atomic.Int32
}

func (p *panickingReviewSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, m Message) Result {
	if m.Event == EventReview {
		p.calls.Add(1)
		panic("render")
	}
	return p.inner.Send(ctx, r, s, m)
}

func TestDispatcher_APanickingReviewSendFailsTheRowOnce(t *testing.T) {
	e := newReviewEnv(t)
	p := &panickingReviewSender{inner: e.d.Senders}
	e.d.Senders = p
	r := e.route("one")
	e.finishReview(e.review("Monthly spend", r))
	for range 3 {
		e.cycle()
		e.advance(3 * time.Minute)
	}
	if row := e.reviewRow(); p.calls.Load() != 1 || row.Status != "failed" || row.Attempts != 1 || row.Note == "" || row.ClaimedUntil != nil {
		t.Fatalf("sends = %d, row = %+v, want one send and a failed row with a note", p.calls.Load(), row)
	}
}

func TestDispatcher_ReviewResultsAreNotInTheAlertSeries(t *testing.T) {
	e := newReviewEnv(t)
	e.d.Telemetry = telemetry.New()
	r := e.route("one")
	replies := []Result{{Retryable: true, StatusCode: 500, Note: "HTTP 500"}, {Delivered: true, StatusCode: 200}, {StatusCode: 400, Note: "HTTP 400"}}
	var n atomic.Int32
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventReview {
			return replies[(n.Add(1)-1)%3]
		}
		return Result{Delivered: true, StatusCode: 200}
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.finishReview(e.review("Monthly spend", r))
	e.finishReview(e.review("Weekly risks", r))
	for range 4 {
		e.cycle()
		e.advance(time.Minute)
	}
	if n.Load() != 3 {
		t.Fatalf("review sends = %d, want 3: a retry, a delivery and a failure", n.Load())
	}
	e.exec(`update notification_deliveries set status = 'pending', attempts = 0, finished_at = null, diagnosis_due_at = $1 where event = 'review'`, e.clock)
	e.exec(`update notification_routes set secret_enc = '\x00'`)
	e.cycle() // fails before the send: the destination's secret cannot be read
	var out bytes.Buffer
	if err := e.d.Telemetry.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `harbormaster_notification_deliveries_total{destination="webhook",result="delivered"} 1`) {
		t.Errorf("the opened message is not counted exactly:\n%s", grepLines(out.String(), "notification_deliveries_total{"))
	}
	for _, result := range []string{"failed", "retried"} {
		if strings.Contains(out.String(), `harbormaster_notification_deliveries_total{destination="webhook",result="`+result+`"}`) {
			t.Errorf("a review was counted as %s in the alert series", result)
		}
	}
}

func TestDispatcher_ReviewLookupHasItsOwnTimeout(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	e.stageReview(r)
	e.d.InvestigateTimeout = 400 * time.Millisecond
	for name, match := range map[string]func(string) bool{
		"run": func(sql string) bool {
			return strings.Contains(sql, "agent_review_runs where id = $1") && !isReviewPlan(sql)
		},
		"route": func(sql string) bool {
			return strings.Contains(sql, "exists") && strings.Contains(sql, "agent_review_routes")
		},
	} {
		base := e.d.Store
		e.tracedStore(&statementHook{match: match, fn: untilCancelled}, 0)
		e.advance(time.Second)
		if err := e.cycleWithin(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		e.waitWithin(5*time.Second, "a blocked "+name+" lookup")
		if row := e.reviewRow(); row.Status != "pending" || row.Attempts != 0 || row.ClaimedUntil != nil || e.reviewsSent("one") != 0 {
			t.Fatalf("%s: row = %+v, want it given back unsent with no attempt counted", name, row)
		}
		e.d.Store = base
	}
	e.advance(time.Second)
	e.cycle()
	if e.reviewsSent("one") != 1 {
		t.Fatalf("review sent %d times after the lookups worked again, want once", e.reviewsSent("one"))
	}
}

// --- the review message ---

func TestDispatcher_ReviewReachesEveryRouteOfTheReview(t *testing.T) {
	e := newReviewEnv(t)
	msgs := e.recordMessages()
	a, b, c := e.route("a"), e.route("b", func(r *store.NotificationRoute) {
		// The alert filters of a route do not apply to a review.
		r.MinSeverity, r.Kinds, r.Scope = "critical", []string{"restart_loop"}, []store.NotificationSelector{serviceSel(svcX)}
	}), e.route("c")
	e.route("not-on-it")
	run := e.finishReview(e.review("Monthly spend", a, b, c))
	for range 3 {
		e.cycle()
		e.advance(time.Second)
	}
	for _, name := range []string{"a", "b", "c"} {
		if e.reviewsSent(name) != 1 {
			t.Errorf("%s got %d reviews, want one", name, e.reviewsSent(name))
		}
	}
	got := msgs.of(EventReview)
	if len(got) != 3 {
		t.Fatalf("review messages = %d, want 3", len(got))
	}
	for _, m := range got {
		if m.Review == nil || len(m.Review.NameIDs) != 0 || m.Review.RunID != run || m.Review.Name != "Monthly spend" || m.Review.Type != "cost" ||
			m.Review.PeriodLabel != "21 November 2026" || m.Review.Summary != testReviewSummary {
			t.Errorf("review = %+v, want the stored run", m.Review)
		}
		if m.Subject.Text != "" || m.Subject.Key != (model.ServiceKey{}) || m.Subject.Severity != "info" || m.Diagnosis != nil || m.Investigate || m.IncidentID != 0 {
			t.Errorf("message = %+v, want no alert text, no key and no investigation", m)
		}
		if m.DeliveryID == 0 || m.BaseURL != "https://hm.example.com" || m.RouteName == "" {
			t.Errorf("message = %+v, want the delivery, base URL and route", m)
		}
		if _, err := WebhookBody(m); err != nil {
			t.Errorf("the message does not render: %v", err)
		}
	}
	for _, row := range e.reviewRows() {
		if row.Status != "delivered" || row.Attempts != 1 {
			t.Errorf("row = %+v, want it delivered", row)
		}
	}
}

func TestDispatcher_TheStoredReviewSummaryIsPassedUncleaned(t *testing.T) {
	const raw = "Spend is flat, see https://elsewhere.example/page for more."
	if CleanReviewSummary(raw, "https://hm.example.com", secretshape.Names{}) == raw {
		t.Fatal("the fixture is not changed by cleaning")
	}
	e := newReviewEnv(t)
	msgs := e.recordMessages()
	r := e.route("one")
	e.finishReview(e.review("Monthly spend", r))
	e.exec(`update agent_review_runs set summary = $1`, raw)
	e.cycle()
	if got := msgs.of(EventReview); len(got) != 1 || got[0].Review == nil || got[0].Review.Summary != raw {
		t.Fatalf("review messages = %+v, want the stored summary as it is", got)
	}
}

func TestDispatcher_ReviewWaitsForQuietHoursToEnd(t *testing.T) {
	e := newReviewEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	night, day := e.route("night", func(r *store.NotificationRoute) { r.QuietEnabled = true }), e.route("day")
	e.finishReview(e.review("Monthly spend", night, day))
	e.cycle()
	if e.reviewsSent("day") != 1 || e.reviewsSent("night") != 0 {
		t.Fatalf("day = %d, night = %d during quiet hours, want 1 and 0", e.reviewsSent("day"), e.reviewsSent("night"))
	}
	e.clock = time.Date(2026, 11, 21, 7, 59, 59, 0, time.UTC)
	e.cycle()
	if e.reviewsSent("night") != 0 {
		t.Fatal("the review was sent before quiet hours ended")
	}
	e.clock = time.Date(2026, 11, 21, 8, 0, 1, 0, time.UTC)
	e.cycle()
	if e.reviewsSent("night") != 1 || e.reviewsSent("day") != 1 {
		t.Fatalf("night = %d, day = %d after quiet hours, want one each", e.reviewsSent("night"), e.reviewsSent("day"))
	}
}

// cancelled runs a cycle and fails unless the one review row was cancelled with the note and nothing was sent.
func (e *env) cancelledReview(note string) {
	e.t.Helper()
	e.advance(time.Second)
	e.cycle()
	if row := e.reviewRow(); row.Status != "cancelled" || row.Note != note || row.Attempts != 1 || e.reviewsSent("one") != 0 {
		e.t.Fatalf("row = %+v, sent %d, want it cancelled with %q and nothing sent", row, e.reviewsSent("one"), note)
	}
}

func TestDispatcher_RouteDeletedBeforeSendCancels(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	e.stageReview(r)
	if err := e.st.DeleteNotificationRoute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	e.cancelledReview("The route was deleted.")
}

func TestDispatcher_RouteDisabledBeforeSendCancels(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	review, _ := e.stageReview(r)
	e.exec(`delete from agent_reviews where id = $1`, review) // the route is checked first
	e.update(r, func(r *store.NotificationRoute) { r.Enabled = false })
	e.cancelledReview("The route is off.")
}

func TestDispatcher_ReviewToPagerDutyIsCancelled(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	review, _ := e.stageReview(r)
	e.exec(`delete from agent_reviews where id = $1`, review) // the destination is checked before the review
	e.exec(`delete from agent_review_runs`)
	e.exec(`update notification_routes set destination = 'pagerduty' where id = $1`, r.ID)
	e.cancelledReview("PagerDuty has no review message.")
}

func TestDispatcher_RouteTakenOffTheReviewCancels(t *testing.T) {
	e := newReviewEnv(t)
	r, other := e.route("one"), e.route("other")
	review, _ := e.stageReview(r)
	e.exec(`insert into agent_review_routes (review_id, route_id) values ($1, $2)`, review, other.ID)
	e.exec(`delete from agent_review_routes where route_id = $1`, r.ID)
	// The route is on another review: that does not count.
	e.exec(`insert into agent_review_routes (review_id, route_id) values ($1, $2)`, e.review("Another", other), r.ID)
	e.cancelledReview("This route is no longer on the review.")
}

func TestDispatcher_ReviewDeletedBeforeSendCancels(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	review, _ := e.stageReview(r)
	if err := e.st.DeleteReview(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	e.cancelledReview("This route is no longer on the review.")
}

func TestDispatcher_ReviewTurnedOffBeforeSendCancels(t *testing.T) {
	e := newReviewEnv(t)
	review, _ := e.stageReview(e.route("one"))
	e.review("Another", e.route("two")) // it stays on: that does not count
	e.exec(`update agent_reviews set enabled = false where id = $1`, review)
	e.cancelledReview("The review is off.")
}

// customReviewRun leaves a done run of type custom, the way the reviewer job does while the operator switch is on.
func (e *env) customReviewRun(manual bool) int64 {
	e.t.Helper()
	review := e.review("Custom", e.route("one"))
	run := e.finishReview(review)
	e.exec(`update agent_review_runs set review_type = 'custom', manual = $2 where id = $1`, run, manual)
	return run
}

// The operator switch for custom reviews is checked at plan and at send; results stay readable, built-in types
// are unaffected, and a manual run is no exception.
func TestDispatcher_CustomRunIsNotPlannedWhileTheSwitchIsOff(t *testing.T) {
	for _, manual := range []bool{false, true} {
		e := newReviewEnv(t)
		run := e.customReviewRun(manual)
		builtIn := e.finishReview(e.review("Cost", e.route("two")))
		e.cycle()
		rows := e.reviewRows()
		if len(rows) != 1 || rows[0].RouteName != "two" {
			t.Fatalf("manual %v: rows %+v, want only the built-in run's", manual, rows)
		}
		for _, id := range []int64{run, builtIn} {
			if got, err := e.st.GetReviewRun(context.Background(), id); err != nil || got.DeliveryPlannedAt == nil || got.Summary != testReviewSummary {
				t.Fatalf("manual %v: run %d = %+v, %v, want it marked and still readable", manual, id, got, err)
			}
		}
		e.d.CustomReviews = true
		e.cycle()
		if n := len(e.reviewRows()); n != 1 {
			t.Fatalf("manual %v: %d rows after the switch went on, want the marked run still unplanned", manual, n)
		}
	}
}

func TestDispatcher_CustomRunIsPlannedAndSentWhileTheSwitchIsOn(t *testing.T) {
	e := newReviewEnv(t)
	e.d.CustomReviews = true
	e.customReviewRun(false)
	e.cycle()
	if row := e.reviewRow(); row.Status != "delivered" || e.reviewsSent("one") != 1 {
		t.Fatalf("row = %+v, sent %d, want it delivered", row, e.reviewsSent("one"))
	}
}

func TestDispatcher_CustomRunTurnedOffBeforeSendCancels(t *testing.T) {
	for _, manual := range []bool{false, true} {
		e := newReviewEnv(t)
		e.d.CustomReviews = true
		e.customReviewRun(manual)
		e.d.Guard = func(context.Context) error { return store.ErrLeaseLost }
		e.cycle() // planned, not sent
		e.d.Guard = nil
		e.d.CustomReviews = false
		e.cancelledReview("Custom reviews are off.")
	}
}

// "Run now" works on a review that is off, and its result is sent.
func TestDispatcher_ManualRunOfAReviewThatIsOffIsSent(t *testing.T) {
	e := newReviewEnv(t)
	review := e.review("Monthly spend", e.route("one"))
	run := e.finishReview(review)
	e.exec(`update agent_review_runs set manual = true where id = $1`, run)
	e.exec(`update agent_reviews set enabled = false where id = $1`, review)
	e.cycle()
	if row := e.reviewRow(); row.Status != "delivered" || e.reviewsSent("one") != 1 {
		t.Fatalf("row = %+v, sent %d, want it delivered", row, e.reviewsSent("one"))
	}
}

// A manual run is excused from the switch only: the route must still be on the review.
func TestDispatcher_ManualRunOfADeletedReviewIsCancelled(t *testing.T) {
	e := newReviewEnv(t)
	review, run := e.stageReview(e.route("one"))
	e.exec(`update agent_review_runs set manual = true where id = $1`, run)
	if err := e.st.DeleteReview(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	e.cancelledReview("This route is no longer on the review.")
}

func TestDispatcher_RunDeletedBeforeSendCancels(t *testing.T) {
	for name, change := range map[string]string{
		"deleted":    `delete from agent_review_runs`,
		"not done":   `update agent_review_runs set state = 'failed', reason = 'error'`,
		"no summary": `update agent_review_runs set summary = ''`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newReviewEnv(t)
			r := e.route("one")
			e.stageReview(r)
			e.exec(change)
			e.exec(`delete from agent_review_routes`) // the run is checked before the review
			e.cancelledReview("The review result is no longer available.")
		})
	}
}

// A row that waited while reviews were off (or after a rollback) is not sent as if it were news.
func TestDispatcher_AnOldReviewResultIsCancelled(t *testing.T) {
	const note = "The review result is too old to send."
	for name, age := range map[string]time.Duration{"just past the bound": 48*time.Hour + time.Second, "ten days": 240 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			e := newReviewEnv(t)
			e.stageReview(e.route("one"))
			e.advance(age - time.Second) // cancelledReview adds the second
			e.cancelledReview(note)
		})
	}
	t.Run("no finish time", func(t *testing.T) {
		e := newReviewEnv(t)
		e.stageReview(e.route("one"))
		e.exec(`update agent_review_runs set finished_at = null`)
		e.cancelledReview(note)
	})
}

func TestDispatcher_AReviewResultAtTheAgeBoundIsSent(t *testing.T) {
	e := newReviewEnv(t)
	e.stageReview(e.route("one"))
	e.advance(48 * time.Hour)
	e.cycle()
	if row := e.reviewRow(); row.Status != "delivered" || e.reviewsSent("one") != 1 {
		t.Fatalf("row = %+v, sent %d, want it delivered 48 hours after the run finished", row, e.reviewsSent("one"))
	}
}

// The oldest row the dispatcher makes itself: planned 24 hours after the run finished, then held through the longest
// quiet hours there are (the clocks of Antarctica/Troll go back two hours in that night), and then tried three times.
func TestDispatcher_AReviewHeldThroughTheLongestQuietHoursIsSent(t *testing.T) {
	e := newReviewEnv(t)
	var fails atomic.Int32
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventReview && fails.Add(1) < maxAttempts {
			return Result{Retryable: true, StatusCode: 500, Note: "HTTP 500"}
		}
		return Result{Delivered: true, StatusCode: 200}
	}
	night := e.route("night", func(r *store.NotificationRoute) { r.QuietEnabled, r.QuietTimezone = true, "Antarctica/Troll" })
	planned := time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC) // 22:00 local
	e.clock = planned.Add(-24 * time.Hour)
	e.finishReview(e.review("Monthly spend", night))
	e.clock = planned
	e.cycle()
	if row := e.reviewRow(); row.HeldUntil == nil || row.HeldUntil.Sub(planned) != 12*time.Hour {
		t.Fatalf("row = %+v, want it held for 12 hours", row)
	}
	e.clock = planned.Add(12 * time.Hour)
	for range maxAttempts {
		e.cycle()
		e.advance(3 * time.Minute)
	}
	if row := e.reviewRow(); row.Status != "delivered" || row.Attempts != maxAttempts {
		t.Fatalf("row = %+v, want it delivered by the last attempt", row)
	}
}

func TestDispatcher_AnOldDiagnosisIsStillSent(t *testing.T) {
	e := newReviewEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.exec(`update agent_investigations set finished_at = $1`, e.clock.Add(-240*time.Hour))
	e.advance(time.Second)
	e.cycle()
	if row := e.diagnosisRow(); row == nil || row.Status != "delivered" {
		t.Fatalf("diagnosis row = %+v, want it delivered: the age bound is for reviews", row)
	}
}

func TestDispatcher_ReviewRetriesThroughTheLog(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	var calls atomic.Int32
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventReview && calls.Add(1) == 1 {
			return Result{Retryable: true, StatusCode: 500, Note: "HTTP 500"}
		}
		return Result{Delivered: true, StatusCode: 200, Duration: 5 * time.Millisecond}
	}
	e.finishReview(e.review("Monthly spend", r))
	e.cycle()
	if row := e.reviewRow(); row.Status != "pending" || row.Attempts != 1 {
		t.Fatalf("after the 500: %+v, want pending with one attempt", row)
	}
	e.advance(10 * time.Second)
	e.cycle()
	if calls.Load() != 1 {
		t.Fatal("the retry did not wait for its time")
	}
	e.advance(time.Minute)
	e.cycle()
	if row := e.reviewRow(); row.Status != "delivered" || row.Attempts != 2 || calls.Load() != 2 {
		t.Fatalf("after the retry: %+v, calls %d, want one delivered message and 2 attempts", row, calls.Load())
	}
}

func TestDispatcher_FailedReviewCanBeRetriedByHand(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	var fail atomic.Bool
	fail.Store(true)
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventReview && fail.Load() {
			return Result{StatusCode: 400, Note: "HTTP 400"}
		}
		return Result{Delivered: true, StatusCode: 200}
	}
	e.finishReview(e.review("Monthly spend", r))
	e.cycle()
	row := e.reviewRow()
	if row.Status != "failed" {
		t.Fatalf("row = %+v, want failed", row)
	}
	fail.Store(false)
	if _, err := e.st.RetryNotificationDelivery(context.Background(), row.ID, e.clock); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Second)
	e.cycle()
	if row := e.reviewRow(); row.Status != "delivered" || row.Retries != 1 {
		t.Fatalf("after the manual retry: %+v, want delivered", row)
	}
}

func TestDispatcher_ReviewDoesNotShiftRepeats(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	review := e.review("Monthly spend", r)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(25 * time.Minute)
	e.finishReview(review)
	e.cycle() // the review goes out at +25 min
	if e.reviewsSent("one") != 1 {
		t.Fatal("no review")
	}
	e.advance(5*time.Minute + time.Second) // +30 min 1 s after the opened row, 5 min after the review
	e.cycle()
	if e.sender.count("one", EventRepeat) != 1 {
		t.Fatalf("repeats = %d, want 1: the interval counts from the alert message", e.sender.count("one", EventRepeat))
	}
}

func TestDispatcher_LostLeaseDoesNotSendAReview(t *testing.T) {
	e := newReviewEnv(t)
	r := e.route("one")
	e.stageReview(r) // the guard refused it once
	if e.reviewsSent("one") != 0 {
		t.Fatal("a review was sent without the lease")
	}
	e.advance(time.Second)
	e.cycle()
	if e.reviewsSent("one") != 1 {
		t.Fatalf("review sent %d times once the guard passed, want once", e.reviewsSent("one"))
	}
}

// --- a review never goes ahead of a diagnosis ---

func TestDispatcher_DiagnosisGoesBeforeAReviewOnTheSameRoute(t *testing.T) {
	e := newReviewEnv(t)
	r := e.switched("one")
	review := e.review("Monthly spend", r)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishReview(review) // due before the diagnosis exists
	e.advance(time.Second)
	e.finishInvestigations(testSummary)
	for range 3 {
		e.advance(time.Second)
		e.cycle()
	}
	if got := e.order("one"); got != "opened,diagnosis,review" {
		t.Fatalf("sent %q, want the diagnosis before the review", got)
	}
}

// idleBeforeReviews waits, between the diagnosis sends and the review part of the same cycle, until ready.
func (e *env) idleBeforeReviews(ready func() bool) {
	e.tracedStore(&statementHook{match: isReviewPlan, fn: func(context.Context, string) { pollFor(3*time.Second, ready) }}, 0)
}

func (e *env) noFollowUpRuns() bool { return e.d.runningFollowUps() == 0 }

// The route's first diagnosis is sent before the review claim: the second one is still ahead of the review.
func TestDispatcher_AReviewNeverOvertakesTheRoutesNextDiagnosis(t *testing.T) {
	e := newReviewEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.finishReview(e.review("Monthly spend", r))
	e.idleBeforeReviews(e.noFollowUpRuns)
	for range 3 {
		e.advance(time.Second)
		e.cycle()
	}
	if got := e.order("one"); got != "opened,opened,diagnosis,diagnosis,review" {
		t.Fatalf("sent %q, want both diagnoses before the review", got)
	}
}

// Three diagnoses are due and two workers are free: a worker freed within the cycle is not given to a review.
func TestDispatcher_AReviewNeverTakesTheSlotOfAWaitingDiagnosis(t *testing.T) {
	e := newReviewEnv(t)
	var routes []store.NotificationRoute
	for i := range 3 {
		routes = append(routes, e.switched(fmt.Sprintf("r%d", i)))
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.finishReview(e.review("Monthly spend", routes...))
	e.idleBeforeReviews(e.noFollowUpRuns)
	e.advance(time.Second)
	e.cycle()
	sent := func(event Event) (n int) {
		for i := range 3 {
			n += e.sender.count(fmt.Sprintf("r%d", i), event)
		}
		return n
	}
	if sent(EventDiagnosis) != 2 || sent(EventReview) != 0 {
		t.Fatalf("first cycle sent %d diagnoses and %d reviews, want 2 and 0: a third diagnosis was waiting", sent(EventDiagnosis), sent(EventReview))
	}
	for range 4 {
		e.advance(time.Second)
		e.cycle()
	}
	for i := range 3 {
		if got := e.order(fmt.Sprintf("r%d", i)); got != "opened,diagnosis,review" {
			t.Errorf("r%d got %q, want the diagnosis before the review", i, got)
		}
	}
}

// A diagnosis skipped because its route had an alert send running is not overtaken when that send ends in between.
func TestDispatcher_AReviewNeverOvertakesADiagnosisHeldByAnAlertSend(t *testing.T) {
	e := newReviewEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.finishReview(e.review("Monthly spend", r))
	h := &openedHangSender{inner: e.d.Senders, release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(h.release) })
	t.Cleanup(func() { release(); e.d.Wait() })
	e.d.Senders = h
	e.idleBeforeReviews(func() bool { release(); return len(e.d.busyRoutes()) == 0 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	for range 3 {
		e.advance(time.Second)
		if err := e.cycleWithin(10 * time.Second); err != nil {
			t.Fatal(err)
		}
		e.waitWithin(5*time.Second, "the sends")
	}
	if got := e.order("one"); got != "opened,opened,diagnosis,review" {
		t.Fatalf("sent %q, want the diagnosis before the review", got)
	}
}

// When the diagnosis claim fails, nobody knows what is waiting: no review is claimed in that cycle.
func TestDispatcher_AFailedDiagnosisClaimHoldsReviewsBack(t *testing.T) {
	e := newReviewEnv(t)
	e.d.InvestigateTimeout = 1500 * time.Millisecond
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.finishReview(e.review("Monthly spend", r))
	base := e.d.Store
	e.tracedStore(&statementHook{match: func(sql string) bool { return isDiagnosisClaim(sql) && !isReviewClaim(sql) }, fn: untilCancelled}, 0)
	e.advance(time.Second)
	e.cycle()
	if e.reviewsSent("one") != 0 {
		t.Fatal("a review was sent although the diagnosis claim failed")
	}
	if rows := e.reviewRows(); len(rows) != 1 {
		t.Fatalf("review rows = %d, want the review still planned", len(rows))
	}
	e.d.Store = base
	for range 2 {
		e.advance(time.Second)
		e.cycle()
	}
	if got := e.order("one"); got != "opened,diagnosis,review" {
		t.Fatalf("sent %q, want the diagnosis before the review", got)
	}
}
