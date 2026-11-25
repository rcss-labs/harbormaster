package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const testSummary = "The api ran out of memory after the last deploy."

// newInvEnv is newEnv with the investigation step on.
func newInvEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.d.Investigate = true
	e.d.InvestigationWindow = 15 * time.Minute
	e.d.InvestigateTimeout = 2 * time.Second
	return e
}

// switched is a route with "Investigate with AI" on.
func (e *env) switched(name string, mutate ...func(*store.NotificationRoute)) store.NotificationRoute {
	e.t.Helper()
	return e.route(name, append([]func(*store.NotificationRoute){func(r *store.NotificationRoute) { r.Investigate = true }}, mutate...)...)
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(from string) int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(context.Background(), `select count(*) from `+from).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// finishInvestigations settles every requested investigation as done, the way the investigator would.
func (e *env) finishInvestigations(summary string) {
	e.t.Helper()
	for {
		inv, claim, err := e.st.ClaimInvestigation(context.Background(), e.clock, time.Minute)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			e.t.Fatal(err)
		}
		if err := e.st.FinishInvestigation(context.Background(), claim, "done", "", &store.InvestigationResult{Summary: summary}, e.clock); err != nil {
			e.t.Fatalf("finish investigation %d: %v", inv.ID, err)
		}
	}
}

// stageDiagnosis leaves the Opened delivered, the investigation done and the diagnosis row planned but unsent (the
// guard refused its send once).
func (e *env) stageDiagnosis() {
	e.t.Helper()
	e.cycle()
	e.finishInvestigations(testSummary)
	e.d.Guard = func(context.Context) error { return store.ErrLeaseLost }
	e.cycle()
	e.d.Guard = nil
	if row := e.diagnosisRow(); row == nil || row.Status != "pending" || row.Attempts != 0 {
		e.t.Fatalf("staged diagnosis row = %+v, want pending and unsent", row)
	}
}

func (e *env) diagnosisRow() *store.NotificationDelivery {
	e.t.Helper()
	for _, d := range e.deliveries() {
		if d.Event == "diagnosis" {
			d := d
			return &d
		}
	}
	return nil
}

// msgSender records whole messages and delegates to the env's recorder.
type msgSender struct {
	inner Sender
	mu    sync.Mutex
	msgs  []Message
}

func (m *msgSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, msg Message) Result {
	m.mu.Lock()
	m.msgs = append(m.msgs, msg)
	m.mu.Unlock()
	return m.inner.Send(ctx, r, s, msg)
}

func (m *msgSender) of(event Event) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Message
	for _, msg := range m.msgs {
		if msg.Event == event {
			out = append(out, msg)
		}
	}
	return out
}

func (e *env) recordMessages() *msgSender {
	m := &msgSender{inner: e.d.Senders}
	e.d.Senders = m
	return m
}

// cycleWithin runs one cycle and fails when it does not return in time; it returns the cycle's error.
func (e *env) cycleWithin(wait time.Duration) error {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel) // a cycle that is stuck must not hold the store's close
	return e.cycleWithCtx(ctx, wait)
}

// cycleWithCtx is cycleWithin under the caller's context.
func (e *env) cycleWithCtx(ctx context.Context, wait time.Duration) error {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.d.Cycle(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		e.t.Fatalf("a cycle did not return within %v", wait)
		return nil
	}
}

func (e *env) openedSent(route string) bool { return e.sender.count(route, EventOpened) > 0 }

// --- the anti-goal: the alert message is never delayed, lost, duplicated or changed because of the agent ---

func TestDispatcher_OpenedSentOnTimeWhenAgentIsDown(t *testing.T) {
	e := newInvEnv(t)
	e.d.InvestigateTimeout = 500 * time.Millisecond
	e.switched("one")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e.d.OnInvestigationRequested = func() { <-release; panic("agent down") } // blocks, then fails
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))

	start := time.Now()
	err := e.cycleWithin(5 * time.Second)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if took := time.Since(start); took > 4*e.d.InvestigateTimeout {
		t.Fatalf("a blocked job hook held the cycle for %v", took)
	}
	e.d.Wait()
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatalf("opened sent %d times, want once on the first cycle", e.sender.count("one", EventOpened))
	}
}

func TestDispatcher_OpenedSentWhenRequestRecordingFails(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.exec(`alter table agent_investigations rename to agent_investigations_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.d.Cycle(context.Background()); err != nil {
		t.Fatalf("cycle returned the investigation error: %v", err)
	}
	e.d.Wait()
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatalf("opened sent %d times, want once", e.sender.count("one", EventOpened))
	}
}

func TestDispatcher_OpenedSentWhileRecordingIsBlocked(t *testing.T) {
	e := newInvEnv(t)
	e.d.InvestigateTimeout = 800 * time.Millisecond
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tx, err := e.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) }) //nolint:errcheck
	if _, err := tx.Exec(context.Background(), `select pg_advisory_xact_lock(hashtext('agent_investigations'))`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- e.d.Cycle(context.Background()) }()
	e.eventually(e.d.InvestigateTimeout/2, "the opened message while the recording statement is blocked", func() bool { return e.openedSent("one") })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cycle: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the cycle did not return after the timeout")
	}
	e.d.Wait()
}

type panickingHandler struct {
	slog.Handler
	armed atomic.Bool
}

func (h *panickingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn && h.armed.CompareAndSwap(true, false) {
		panic("log handler failed")
	}
	return h.Handler.Handle(ctx, r)
}

func TestDispatcher_PanicInAfterSendDoesNotStopTheLoop(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	h := &panickingHandler{Handler: slog.NewTextHandler(&strings.Builder{}, nil)}
	h.armed.Store(true)
	e.d.Log = slog.New(h)
	// The warning about the failed recording is the first thing afterSend does; the handler panics on it.
	e.exec(`alter table agent_investigations rename to agent_investigations_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.d.Cycle(context.Background()); err != nil {
		t.Fatalf("cycle returned the panic's effect: %v", err)
	}
	e.d.Wait()
	if h.armed.Load() {
		t.Fatal("the panic was never provoked")
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	if e.sender.count("one", EventOpened) != 2 {
		t.Fatalf("opened sent %d times after the panic, want 2: the loop must go on", e.sender.count("one", EventOpened))
	}
}

func TestDispatcher_APanickingJobHookIsContained(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.d.OnInvestigationRequested = func() { panic("hook") }
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	e.d.Wait()
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatal("opened not sent")
	}
}

func TestDispatcher_WithInvestigateOffNothingNewRuns(t *testing.T) {
	e := newEnv(t) // Investigate false
	e.switched("one")
	called := false
	e.d.OnInvestigationRequested = func() { called = true }
	e.exec(`alter table agent_investigation_incidents rename to agent_investigation_incidents_gone`)
	e.exec(`alter table agent_investigations rename to agent_investigations_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(time.Minute)
	e.cycle()
	if called || e.sender.count("one", EventOpened) != 1 {
		t.Fatalf("hook called = %v, opened sent %d times; want no hook and one opened", called, e.sender.count("one", EventOpened))
	}
}

type stmtTracer struct {
	mu  sync.Mutex
	sql []string
}

func (s *stmtTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	s.sql = append(s.sql, d.SQL)
	s.mu.Unlock()
	return ctx
}

func (s *stmtTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCycle_AlertRowsAreClaimedBeforeAnyInvestigationStatement(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tracer := &stmtTracer{}
	cfg := e.st.Pool.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.d.Store = &store.Store{Pool: pool}
	e.cycle()

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	claim, firstInvestigation := -1, -1
	for i, sql := range tracer.sql {
		isInvestigation := strings.Contains(sql, "agent_investigation") || strings.Contains(sql, "diagnosis_due_at <=")
		if claim < 0 && !isInvestigation && strings.Contains(sql, "next_attempt_at <= $1") && strings.Contains(sql, "claimed_until") {
			claim = i
		}
		if firstInvestigation < 0 && isInvestigation {
			firstInvestigation = i
		}
	}
	if claim < 0 || firstInvestigation < 0 || claim > firstInvestigation {
		t.Fatalf("alert claim at statement %d, first investigation statement at %d, want the claim first and both present", claim, firstInvestigation)
	}
}

// --- request recording ---

func TestDispatcher_RecordsOneRequestForAnOpenedOnASwitchedRoute(t *testing.T) {
	e := newInvEnv(t)
	e.switched("on")
	e.route("off")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(time.Minute)
	e.cycle()
	if e.count("agent_investigations") != 1 || e.count("agent_investigation_incidents") != 1 {
		t.Fatalf("investigations = %d, links = %d, want 1 and 1", e.count("agent_investigations"), e.count("agent_investigation_incidents"))
	}
}

func TestDispatcher_NoRequestWhenNoRouteHasTheSwitch(t *testing.T) {
	e := newInvEnv(t)
	e.route("off")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if e.count("agent_investigations") != 0 {
		t.Fatal("a request was recorded for a route with the switch off")
	}
}

func TestDispatcher_NoRequestForRepeatOrUpdate(t *testing.T) {
	e := newInvEnv(t)
	r := e.route("one", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle() // opened sent with the switch off
	e.update(r, func(r *store.NotificationRoute) { r.Investigate = true })
	e.advance(31 * time.Minute) // the opened row is older than the window; the repeat is new
	e.cycle()
	if e.sender.count("one", EventRepeat) != 1 {
		t.Fatalf("repeats = %d, want 1", e.sender.count("one", EventRepeat))
	}
	if e.count("agent_investigations") != 0 {
		t.Fatal("a repeat or an old opened row produced a request")
	}
}

func TestDispatcher_RequestRecordedWhileOpenedIsHeld(t *testing.T) {
	e := newInvEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.switched("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle()
	if len(e.sender.records()) != 0 {
		t.Fatal("the warning was sent during quiet hours")
	}
	if e.count("agent_investigations") != 1 {
		t.Fatalf("investigations = %d, want the request recorded while the opened row is held", e.count("agent_investigations"))
	}
}

func TestDispatcher_KicksTheJobOnce(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	var kicks atomic.Int32
	e.d.OnInvestigationRequested = func() { kicks.Add(1) }
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger),
		alert(svcX, "restart_loop", model.AlertDanger))
	e.cycle()
	e.eventually(time.Second, "the hook", func() bool { return kicks.Load() >= 1 })
	e.advance(time.Minute)
	e.cycle()
	time.Sleep(50 * time.Millisecond)
	if kicks.Load() != 1 {
		t.Fatalf("hook called %d times for two investigations in one cycle and none after, want once", kicks.Load())
	}
}

// --- the diagnosis message ---

func TestDispatcher_DiagnosisFollowsOpenedToTheSameRoute(t *testing.T) {
	e := newInvEnv(t)
	msgs := e.recordMessages()
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if len(msgs.of(EventDiagnosis)) != 0 {
		t.Fatal("a diagnosis was sent before the investigation was done")
	}
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	e.cycle()

	got := msgs.of(EventDiagnosis)
	if len(got) != 1 || got[0].Diagnosis == nil || got[0].Diagnosis.Summary != testSummary || got[0].Diagnosis.InvestigationID == 0 ||
		got[0].Subject.Key != svcX || got[0].RouteName != "one" {
		t.Fatalf("diagnosis messages = %+v, want one with the stored summary unchanged", got)
	}
	if e.sender.count("one", EventOpened) != 1 {
		t.Fatalf("opened sent %d times, want it once and unchanged", e.sender.count("one", EventOpened))
	}
	opened := msgs.of(EventOpened)
	if len(opened) != 1 || !opened[0].Investigate || opened[0].IncidentID == 0 {
		t.Fatalf("opened message = %+v, want the switch and the incident id for the PagerDuty link", opened)
	}
	e.advance(time.Minute)
	e.cycle()
	if len(msgs.of(EventDiagnosis)) != 1 {
		t.Fatal("the diagnosis was sent again by a later cycle")
	}
}

func TestDispatcher_WebhookGetsDiagnosisOnlyWithTheSwitchOn(t *testing.T) {
	e := newInvEnv(t)
	e.switched("on")
	e.route("off")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	e.cycle()
	if e.sender.count("on", EventDiagnosis) != 1 || e.sender.count("off", EventDiagnosis) != 0 || e.sender.count("off", EventOpened) != 1 {
		t.Fatalf("on: %v, off: %v", e.sender.to("on"), e.sender.to("off"))
	}
}

func TestDispatcher_DiagnosisAfterQuietHoursFollowsTheHeldOpened(t *testing.T) {
	e := newInvEnv(t)
	e.clock = time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	e.switched("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle() // held; the investigation is requested at once
	e.finishInvestigations(testSummary)
	e.advance(time.Minute)
	e.cycle()
	if len(e.sender.records()) != 0 || e.diagnosisRow() != nil {
		t.Fatalf("sent %+v, diagnosis row %+v: a diagnosis must wait for its delivered opened", e.sender.records(), e.diagnosisRow())
	}
	e.clock = time.Date(2026, 11, 21, 8, 0, 1, 0, time.UTC)
	e.cycle() // the opened is released and delivered
	e.advance(time.Second)
	e.cycle()
	var order []string
	for _, rec := range e.sender.records() {
		order = append(order, string(rec.Event))
	}
	if got := strings.Join(order, ","); got != "opened,diagnosis" {
		t.Fatalf("sent %q, want the opened and then the diagnosis", got)
	}
}

func TestDispatcher_DiagnosisIsDueAtTheEndOfQuietHours(t *testing.T) {
	e := newInvEnv(t)
	e.clock = time.Date(2026, 11, 20, 21, 50, 0, 0, time.UTC)
	e.switched("night", func(r *store.NotificationRoute) { r.QuietEnabled = true })
	e.setAlerts(alert(svcX, "service_degraded", model.AlertWarning))
	e.cycle() // not quiet yet: the opened is sent
	e.advance(20 * time.Minute)
	e.finishInvestigations(testSummary)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "pending" || row.HeldUntil == nil || !row.HeldUntil.Equal(time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("diagnosis row = %+v, want it planned and held until 08:00", row)
	}
	if e.sender.count("night", EventDiagnosis) != 0 {
		t.Fatal("the diagnosis was sent during quiet hours")
	}
	e.clock = time.Date(2026, 11, 21, 8, 0, 1, 0, time.UTC)
	e.cycle()
	if e.sender.count("night", EventDiagnosis) != 1 {
		t.Fatal("the diagnosis was not sent after quiet hours")
	}
}

func TestDispatcher_ResolvedBeforeReadyStoresButSendsNothing(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle() // resolved
	e.finishInvestigations(testSummary)
	e.advance(time.Minute)
	e.cycle()
	if e.sender.count("one", EventDiagnosis) != 0 || e.diagnosisRow() != nil {
		t.Fatal("a diagnosis was planned or sent for a resolved alert")
	}
	var summary string
	if err := e.st.Pool.QueryRow(context.Background(), `select summary from agent_investigations`).Scan(&summary); err != nil || summary != testSummary {
		t.Fatalf("stored summary = %q, %v, want it kept", summary, err)
	}
}

func TestDispatcher_ResolvedAfterPlannedCancelsTheRow(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.d.Guard = func(context.Context) error { return store.ErrLeaseLost } // the row stays unsent until the resolve is processed
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle()
	e.d.Guard = nil
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "cancelled" || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("diagnosis row = %+v, sent %d, want it cancelled and not sent", row, e.sender.count("one", EventDiagnosis))
	}
}

// A resolve processed by an old coordinator leaves a due row: the send step itself must refuse it.
func TestDispatcher_DiagnosisOfAResolvedIncidentIsCancelledAtSend(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.exec(`update notification_incidents set resolved_at = $1`, e.clock)
	rows, err := e.st.ClaimDiagnosisDeliveries(context.Background(), e.clock, time.Minute, store.ClaimFilter{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("claim = %v, %v", rows, err)
	}
	ctx := context.Background()
	if err := e.d.deliver(ctx, ctx, rows[0], map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if row := e.diagnosisRow(); row.Status != "cancelled" || row.Note != noteResolvedBeforeSent || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, want cancelled with the resolved note and nothing sent", row)
	}
}

func TestDispatcher_RouteDeletedMidRunSendsNothing(t *testing.T) {
	e := newInvEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	if err := e.st.DeleteNotificationRoute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Second)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "cancelled" || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, sent %d, want cancelled and nothing sent", row, e.sender.count("one", EventDiagnosis))
	}
}

func TestDispatcher_SwitchTurnedOffAfterPlanningCancels(t *testing.T) {
	e := newInvEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.update(r, func(r *store.NotificationRoute) { r.Investigate = false })
	e.advance(time.Second)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "cancelled" || row.Note != "Investigate with AI is off." || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, want cancelled with the switch note", row)
	}
}

func TestDispatcher_RouteDisabledAfterPlanningCancels(t *testing.T) {
	e := newInvEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.update(r, func(r *store.NotificationRoute) { r.Enabled = false })
	e.advance(time.Second)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "cancelled" || row.Note != noteRouteOff || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, want cancelled with the route-off note", row)
	}
}

func TestDispatcher_DiagnosisToPagerDutyIsCancelled(t *testing.T) {
	e := newInvEnv(t)
	r := e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.exec(`update notification_routes set destination = 'pagerduty' where id = $1`, r.ID)
	e.advance(time.Second)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "cancelled" || row.Note != "PagerDuty has no diagnosis message." || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, want cancelled with the PagerDuty note", row)
	}
}

func TestDispatcher_InvestigationDeletedBeforeSendCancels(t *testing.T) {
	for name, change := range map[string]string{
		"deleted":   `delete from agent_investigations`,
		"not done":  `update agent_investigations set state = 'failed', reason = 'error'`,
		"no result": `update agent_investigations set summary = ''`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newInvEnv(t)
			e.switched("one")
			e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
			e.stageDiagnosis()
			e.exec(change)
			e.advance(time.Second)
			e.cycle()
			row := e.diagnosisRow()
			if row == nil || row.Status != "cancelled" || row.Note != "The investigation is no longer available." || e.sender.count("one", EventDiagnosis) != 0 {
				t.Fatalf("row = %+v, want cancelled with the investigation note", row)
			}
		})
	}
}

func TestDispatcher_DiagnosisRetriesThroughTheLog(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	var diagnosisCalls atomic.Int32
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventDiagnosis && diagnosisCalls.Add(1) == 1 {
			return Result{Retryable: true, StatusCode: 500, Note: "HTTP 500"}
		}
		return Result{Delivered: true, StatusCode: 200, Duration: 5 * time.Millisecond}
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	e.cycle()
	if row := e.diagnosisRow(); row == nil || row.Status != "pending" || row.Attempts != 1 {
		t.Fatalf("after the 500: %+v, want pending with one attempt", row)
	}
	e.advance(time.Minute)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "delivered" || row.Attempts != 2 || diagnosisCalls.Load() != 2 {
		t.Fatalf("after the retry: %+v, calls %d, want delivered with 2 attempts", row, diagnosisCalls.Load())
	}
}

func TestDispatcher_FailedDiagnosisCanBeRetriedByHand(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	var fail atomic.Bool
	fail.Store(true)
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventDiagnosis && fail.Load() {
			return Result{StatusCode: 400, Note: "HTTP 400"}
		}
		return Result{Delivered: true, StatusCode: 200}
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	e.cycle()
	row := e.diagnosisRow()
	if row == nil || row.Status != "failed" {
		t.Fatalf("row = %+v, want failed", row)
	}
	fail.Store(false)
	if _, err := e.st.RetryNotificationDelivery(context.Background(), row.ID, e.clock); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Second)
	e.cycle()
	if row := e.diagnosisRow(); row.Status != "delivered" {
		t.Fatalf("after the manual retry: %+v, want delivered", row)
	}
}

func TestDispatcher_DiagnosisDoesNotShiftRepeats(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.advance(25 * time.Minute)
	e.finishInvestigations(testSummary)
	e.cycle() // the diagnosis goes out at +25 min
	if e.sender.count("one", EventDiagnosis) != 1 {
		t.Fatal("no diagnosis")
	}
	e.advance(5*time.Minute + time.Second) // +30 min 1 s after the opened row, 5 min after the diagnosis
	e.cycle()
	if e.sender.count("one", EventRepeat) != 1 {
		t.Fatalf("repeats = %d, want 1: the interval counts from the alert message", e.sender.count("one", EventRepeat))
	}
}

func TestDispatcher_LostLeaseDoesNotSendADiagnosis(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis() // the guard refused it once
	if e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatal("a diagnosis was sent without the lease")
	}
	e.advance(time.Second)
	e.cycle()
	if e.sender.count("one", EventDiagnosis) != 1 {
		t.Fatalf("diagnosis sent %d times once the guard passed, want once", e.sender.count("one", EventDiagnosis))
	}
}

// --- a diagnosis send never delays an alert ---

type diagnosisHangSender struct {
	inner   Sender
	started chan struct{}
	release chan struct{}
	live    atomic.Int32
	peak    atomic.Int32
	calls   atomic.Int32
}

func (h *diagnosisHangSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, m Message) Result {
	if m.Event != EventDiagnosis {
		return h.inner.Send(ctx, r, s, m)
	}
	h.calls.Add(1)
	n := h.live.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer h.live.Add(-1)
	select {
	case h.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
	case <-h.release:
	case <-time.After(20 * time.Second): // a failing test must not hang on its own sender
	}
	return Result{Retryable: true, Note: "Network error"}
}

func (e *env) hangDiagnoses() *diagnosisHangSender {
	h := &diagnosisHangSender{inner: e.d.Senders, started: make(chan struct{}, 8), release: make(chan struct{})}
	e.d.Senders = h
	e.t.Cleanup(func() { close(h.release); e.d.Wait() })
	return h
}

func TestDispatcher_AHangingDiagnosisDoesNotDelayAnOpenedOnTheSameRoute(t *testing.T) {
	e := newInvEnv(t)
	h := e.hangDiagnoses()
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the diagnosis send never started")
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.eventually(3*time.Second, "the new opened message while a diagnosis send hangs on the same route", func() bool { return e.sender.count("one", EventOpened) == 2 })
}

func TestDispatcher_OneDiagnosisSendPerRouteAtATime(t *testing.T) {
	e := newInvEnv(t)
	h := e.hangDiagnoses()
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	for range 3 {
		if err := e.cycleWithin(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Second)
	}
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("no diagnosis send started")
	}
	if h.peak.Load() != 1 || h.calls.Load() != 1 {
		t.Fatalf("diagnosis sends started = %d, peak concurrent = %d, want one while the first still hangs", h.calls.Load(), h.peak.Load())
	}
}

// --- fix round 1: bounds, isolation and pins ---

// waitWithin waits for the send workers and fails when they do not end in time.
func (e *env) waitWithin(wait time.Duration, what string) {
	e.t.Helper()
	done := make(chan struct{})
	go func() { e.d.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
		e.t.Fatalf("%s: the workers did not end within %v", what, wait)
	}
}

// hold runs sql in a transaction of its own that stays open until the returned func (or the test's end).
func (e *env) hold(sql string) (release func()) {
	e.t.Helper()
	tx, err := e.st.Pool.Begin(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	release = func() { tx.Rollback(context.Background()) } //nolint:errcheck
	e.t.Cleanup(release)
	if _, err := tx.Exec(context.Background(), sql); err != nil {
		e.t.Fatal(err)
	}
	return release
}

const recordingLock = `select pg_advisory_xact_lock(hashtext('agent_investigations'))`

func isDiagnosisClaim(sql string) bool {
	return strings.Contains(sql, "update notification_deliveries") && strings.Contains(sql, "diagnosis_due_at <= $1")
}

type claimTracer struct {
	before, after func() // around every diagnosis claim statement
}

type claimKey struct{}

func (c *claimTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	claim := isDiagnosisClaim(d.SQL)
	if claim && c.before != nil {
		c.before()
	}
	return context.WithValue(ctx, claimKey{}, claim)
}

func (c *claimTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if claim, _ := ctx.Value(claimKey{}).(bool); claim && c.after != nil {
		c.after()
	}
}

// tracedStore gives the dispatcher a pool of its own with a tracer; maxConns 0 keeps the default size.
func (e *env) tracedStore(tracer pgx.QueryTracer, maxConns int32) {
	e.t.Helper()
	cfg := e.st.Pool.Config()
	cfg.ConnConfig.Tracer = tracer
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	e.d.Store = &store.Store{Pool: pool}
}

func TestDispatcher_OpenedIsUnchangedUnlessAgentAndSwitchAreOn(t *testing.T) {
	for name, tc := range map[string]struct{ agent, route, want bool }{
		"agent off, switch on": {false, true, false},
		"agent on, switch off": {true, false, false},
		"both on":              {true, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newInvEnv(t)
			e.d.Investigate = tc.agent
			msgs := e.recordMessages()
			e.route("pd", func(r *store.NotificationRoute) { r.Destination, r.Investigate = DestPagerDuty, tc.route })
			e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
			e.cycle()
			opened := msgs.of(EventOpened)
			if len(opened) != 1 {
				t.Fatalf("opened messages = %d, want 1", len(opened))
			}
			got, err := PagerDutyBody(opened[0], "fake-routing-key")
			if err != nil {
				t.Fatal(err)
			}
			base := opened[0]
			base.Investigate, base.IncidentID = false, 0
			want, err := PagerDutyBody(base, "fake-routing-key")
			if err != nil {
				t.Fatal(err)
			}
			if same := bytes.Equal(got, want); same == tc.want {
				t.Fatalf("body equals the one without the agent = %v, want %v\n%s", same, !tc.want, got)
			}
			if opened[0].Investigate != tc.want {
				t.Fatalf("Investigate = %v, want %v", opened[0].Investigate, tc.want)
			}
		})
	}
}

// Eight due diagnosis rows whose lookups all block must leave connections of a pool of four for a new alert.
func TestDispatcher_BlockedDiagnosisLookupsLeaveThePoolToAlerts(t *testing.T) {
	e := newInvEnv(t)
	for i := range 8 {
		e.switched(fmt.Sprintf("r%d", i))
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()

	e.d.InvestigateTimeout = time.Minute // the cap alone must protect the pool
	lock, err := e.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	e.tracedStore(&claimTracer{before: func() {
		once.Do(func() {
			if _, err := lock.Exec(context.Background(), `lock table agent_investigations in access exclusive mode`); err != nil {
				t.Error(err)
			}
		})
	}}, 4)
	var cycleB sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		lock.Rollback(context.Background()) //nolint:errcheck
		cycleB.Wait()
		e.d.Wait()
	})
	e.advance(time.Second)
	if err := e.cycleWithCtx(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	blocked := func() bool {
		return e.count(`pg_locks where relation = 'agent_investigations'::regclass and not granted`) >= 2
	}
	deadline := time.Now().Add(5 * time.Second)
	for !blocked() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	cycleB.Go(func() { e.d.Cycle(ctx) }) //nolint:errcheck
	e.eventually(5*time.Second, "the new opened message while diagnosis lookups block", func() bool { return e.sender.count("r0", EventOpened) == 2 })
}

func TestDispatcher_AtMostTwoDiagnosisSendsAcrossCycles(t *testing.T) {
	e := newInvEnv(t)
	for i := range 6 {
		e.switched(fmt.Sprintf("r%d", i))
	}
	h := e.hangDiagnoses()
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	for range 4 {
		e.advance(time.Second)
		if err := e.cycleWithin(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	e.eventually(5*time.Second, "two diagnosis sends", func() bool { return h.calls.Load() >= 2 })
	time.Sleep(200 * time.Millisecond)
	if h.calls.Load() != 2 || h.peak.Load() != 2 {
		t.Fatalf("diagnosis sends started = %d, peak = %d, want 2 across every cycle", h.calls.Load(), h.peak.Load())
	}
	if e.sender.count("r0", EventOpened) != 1 || e.sender.count("r5", EventOpened) != 1 {
		t.Fatal("an opened message is missing")
	}
}

// A row whose route is gone holds a connection too, so it counts towards the same cap.
func TestDispatcher_RoutelessDiagnosisRowsCountTowardsTheCap(t *testing.T) {
	e := newInvEnv(t)
	for i := range 4 {
		e.switched(fmt.Sprintf("r%d", i))
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.exec(`update notification_deliveries set route_id = null where event = 'diagnosis'`)

	// The claimed rows are locked right after their claim, so their workers stay busy writing.
	lock, err := e.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	e.tracedStore(&claimTracer{after: func() {
		once.Do(func() {
			if _, err := lock.Exec(context.Background(), `select id from notification_deliveries
				where event = 'diagnosis' and claimed_until is not null for update`); err != nil {
				t.Error(err)
			}
		})
	}}, 0)
	t.Cleanup(func() { lock.Rollback(context.Background()); e.d.Wait() }) //nolint:errcheck
	for range 2 {
		e.advance(time.Second)
		if err := e.cycleWithin(4 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`notification_deliveries where event = 'diagnosis' and claimed_until is not null`); n != 2 {
		t.Fatalf("%d routeless rows claimed by two cycles, want 2 while the first two are busy", n)
	}
}

func TestDispatcher_InvestigationLookupHasItsOwnTimeout(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.d.InvestigateTimeout = 400 * time.Millisecond
	release := e.hold(`lock table agent_investigations in access exclusive mode`)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.waitWithin(5*time.Second, "a blocked investigation lookup")
	if row := e.diagnosisRow(); row.Status != "pending" || row.Attempts != 0 || row.ClaimedUntil != nil || e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatalf("row = %+v, want it given back unsent with no attempt counted", row)
	}
	release()
	e.advance(time.Second)
	e.cycle()
	if e.sender.count("one", EventDiagnosis) != 1 {
		t.Fatalf("diagnosis sent %d times after the lookup worked again, want once", e.sender.count("one", EventDiagnosis))
	}
}

func TestDispatcher_AStuckHookStartsOnceAndHoldsNoCycle(t *testing.T) {
	e := newInvEnv(t)
	e.d.InvestigateTimeout = 10 * time.Second
	e.switched("one")
	var calls atomic.Int32
	release := make(chan struct{})
	closeOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(closeOnce)
	e.d.OnInvestigationRequested = func() { calls.Add(1); <-release }
	var alerts []model.Alert
	newAlert := func() {
		alerts = append(alerts, alert(key(acctA, "us-east-1", "core", fmt.Sprintf("s%d", len(alerts))), "service_failing", model.AlertDanger))
		e.setAlerts(alerts...)
		e.advance(time.Second)
	}
	const cycles = 5
	for range cycles {
		newAlert()
		if err := e.cycleWithin(5 * time.Second); err != nil { // well under one InvestigateTimeout
			t.Fatal(err)
		}
		e.waitWithin(5*time.Second, "alert sends")
	}
	if calls.Load() != 1 || e.sender.count("one", EventOpened) != cycles {
		t.Fatalf("hook started %d times, opened sent %d; want 1 and %d", calls.Load(), e.sender.count("one", EventOpened), cycles)
	}
	if got := e.d.InvestigationErrors(); got != cycles-1 {
		t.Fatalf("investigation errors = %d, want %d, one per skipped call", got, cycles-1)
	}
	closeOnce()
	e.eventually(5*time.Second, "the hook to be called again once it returned", func() bool {
		newAlert()
		e.cycle()
		return calls.Load() > 1
	})
}

func TestDispatcher_APanickedHookIsCalledAgain(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	var calls atomic.Int32
	e.d.OnInvestigationRequested = func() { calls.Add(1); panic("hook") }
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.eventually(2*time.Second, "the panic to be counted", func() bool { return e.d.InvestigationErrors() == 1 })
	time.Sleep(20 * time.Millisecond) // the flag is cleared right after the panic is counted
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	e.eventually(2*time.Second, "the second call", func() bool { return calls.Load() == 2 })
}

// Every statement of the step blocks: recording on its advisory lock, planning and claiming on the table.
func TestDispatcher_AfterSendIsBoundedByTwoTimeouts(t *testing.T) {
	e := newInvEnv(t)
	e.d.InvestigateTimeout = 1500 * time.Millisecond
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	tx, err := e.st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); tx.Rollback(context.Background()) }) //nolint:errcheck
	if _, err := tx.Exec(ctx, recordingLock); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- e.d.Cycle(ctx) }()
	e.eventually(time.Second, "the opened message to be recorded as delivered", func() bool {
		return e.count(`notification_deliveries where event = 'opened' and status = 'delivered'`) == 1
	})
	if _, err := tx.Exec(ctx, `lock table notification_deliveries in access exclusive mode`); err != nil {
		t.Fatal(err)
	}
	limit := 2*e.d.InvestigateTimeout + 800*time.Millisecond
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cycle returned %v, want the step's errors kept out of it", err)
		}
	case <-time.After(limit - time.Since(start)):
		t.Fatalf("the cycle was held for more than %v, want at most two timeouts", limit)
	}
	if got := e.d.InvestigationErrors(); got != 3 {
		t.Fatalf("investigation errors = %d, want 3: recording, planning and claiming each timed out", got)
	}
}

func TestDispatcher_ASlowPlanDoesNotStopDueDiagnoses(t *testing.T) {
	e := newInvEnv(t)
	e.d.InvestigateTimeout = time.Second
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	e.hold(`lock table agent_investigation_incidents in access exclusive mode`)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.waitWithin(5*time.Second, "the diagnosis send")
	if e.sender.count("one", EventDiagnosis) != 1 {
		t.Fatalf("diagnosis sent %d times while planning is blocked, want the due row sent", e.sender.count("one", EventDiagnosis))
	}
}

func TestDispatcher_ARecordErrorDoesNotSkipTheLaterSteps(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.d.InvestigateTimeout = 800 * time.Millisecond
	e.hold(recordingLock)
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.waitWithin(5*time.Second, "the diagnosis send")
	if e.sender.count("one", EventDiagnosis) != 1 || e.d.InvestigationErrors() != 1 {
		t.Fatalf("diagnosis sent %d times, errors %d; want it planned and sent although recording failed", e.sender.count("one", EventDiagnosis), e.d.InvestigationErrors())
	}
}

func TestDispatcher_InvestigateOffRunsNoInvestigationStatement(t *testing.T) {
	e := newEnv(t) // Investigate false
	e.switched("one")
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
		if strings.Contains(sql, "agent_investigation") || strings.Contains(sql, "diagnosis_due_at <=") {
			t.Errorf("with the agent off a cycle ran %q", sql)
		}
	}
}

type panickingDiagnosisSender struct {
	inner Sender
	calls atomic.Int32
	first func()
}

func (p *panickingDiagnosisSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, m Message) Result {
	if m.Event == EventDiagnosis {
		p.calls.Add(1)
		if p.first != nil {
			p.first()
		}
		panic("render")
	}
	return p.inner.Send(ctx, r, s, m)
}

func TestDispatcher_APanickingDiagnosisSendFailsTheRowOnce(t *testing.T) {
	e := newInvEnv(t)
	p := &panickingDiagnosisSender{inner: e.d.Senders}
	e.d.Senders = p
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	for range 4 {
		e.advance(3 * time.Minute)
		e.cycle()
	}
	row := e.diagnosisRow()
	if p.calls.Load() != 1 || row.Status != "failed" || row.Attempts != 1 || row.Note == "" || row.ClaimedUntil != nil {
		t.Fatalf("sends = %d, row = %+v, want one send and a failed row with a note", p.calls.Load(), row)
	}
	if e.d.InvestigationErrors() != 1 {
		t.Fatalf("investigation errors = %d, want 1", e.d.InvestigationErrors())
	}
	if _, err := e.st.RetryNotificationDelivery(context.Background(), row.ID, e.clock); err != nil {
		t.Fatalf("the failed row cannot be retried by hand: %v", err)
	}
}

func TestDispatcher_APanicDuringShutdownStillFailsTheRow(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.d.Senders = &panickingDiagnosisSender{inner: e.d.Senders, first: cancel}
	e.advance(time.Second)
	if err := e.cycleWithCtx(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	e.waitWithin(5*time.Second, "the panicked worker")
	if row := e.diagnosisRow(); row == nil || row.Status != "failed" {
		t.Fatalf("row = %+v, want failed although the cycle's context ended", row)
	}
}

type openedHangSender struct {
	inner   Sender
	release chan struct{}
}

func (h *openedHangSender) Send(ctx context.Context, r store.NotificationRoute, s Secret, m Message) Result {
	if m.Event == EventOpened {
		select {
		case <-h.release:
		case <-ctx.Done():
		case <-time.After(20 * time.Second):
		}
	}
	return h.inner.Send(ctx, r, s, m)
}

func TestDispatcher_ADiagnosisWaitsForTheRoutesAlertSend(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.stageDiagnosis()
	h := &openedHangSender{inner: e.d.Senders, release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(h.release) })
	t.Cleanup(func() { release(); e.d.Wait() })
	e.d.Senders = h
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.advance(time.Second)
	if err := e.cycleWithin(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if e.sender.count("one", EventDiagnosis) != 0 {
		t.Fatal("a diagnosis was sent while an alert send of the same route was running")
	}
	release()
	e.waitWithin(5*time.Second, "the alert send")
	e.advance(time.Second)
	e.cycle()
	if e.sender.count("one", EventDiagnosis) != 1 || e.sender.count("one", EventOpened) != 2 {
		t.Fatalf("sent %v, want both opened messages and then the diagnosis", e.sender.to("one"))
	}
}

func TestDispatcher_InvestigationErrorsAreCountedAndWarned(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	var log bytes.Buffer
	e.d.Log = slog.New(slog.NewTextHandler(&log, nil))
	e.exec(`alter table agent_investigations rename to agent_investigations_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if got := e.d.InvestigationErrors(); got != 2 {
		t.Fatalf("investigation errors = %d, want 2: recording and planning", got)
	}
	for _, want := range []string{`level=WARN`, `step="record requests"`, `step="plan diagnosis"`, `count=1`, `count=2`} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log has no %s:\n%s", want, log.String())
		}
	}
}

func TestDispatcher_APanicInAfterSendIsCounted(t *testing.T) {
	e := newInvEnv(t)
	e.switched("one")
	h := &panickingHandler{Handler: slog.NewTextHandler(&strings.Builder{}, nil)}
	h.armed.Store(true)
	e.d.Log = slog.New(h)
	e.exec(`alter table agent_investigations rename to agent_investigations_gone`)
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	if got := e.d.InvestigationErrors(); got != 2 {
		t.Fatalf("investigation errors = %d, want 2: the failed recording and the panic", got)
	}
}

func TestDispatcher_DiagnosisResultsAreNotInTheAlertSeries(t *testing.T) {
	e := newInvEnv(t)
	e.d.Telemetry = telemetry.New()
	e.switched("one")
	replies := []Result{{Retryable: true, StatusCode: 500, Note: "HTTP 500"}, {Delivered: true, StatusCode: 200}, {StatusCode: 400, Note: "HTTP 400"}}
	var n atomic.Int32
	e.sender.reply = func(_ string, event Event, _ int) Result {
		if event == EventDiagnosis {
			return replies[(n.Add(1)-1)%3]
		}
		return Result{Delivered: true, StatusCode: 200}
	}
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger), alert(svcY, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	for range 4 {
		e.advance(time.Minute)
		e.cycle()
	}
	if n.Load() != 3 {
		t.Fatalf("diagnosis sends = %d, want 3: a retry, a delivery and a failure", n.Load())
	}
	e.exec(`update notification_deliveries set status = 'pending', attempts = 0, finished_at = null, diagnosis_due_at = $1 where event = 'diagnosis'`, e.clock)
	e.exec(`update notification_routes set secret_enc = '\x00'`)
	e.cycle() // fails before the send: the destination's secret cannot be read
	var out bytes.Buffer
	if err := e.d.Telemetry.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `harbormaster_notification_deliveries_total{destination="webhook",result="delivered"} 2`) {
		t.Errorf("the two opened messages are not counted exactly:\n%s", grepLines(out.String(), "notification_deliveries_total{"))
	}
	for _, result := range []string{"failed", "retried"} {
		if strings.Contains(out.String(), `harbormaster_notification_deliveries_total{destination="webhook",result="`+result+`"}`) {
			t.Errorf("a diagnosis was counted as %s in the alert series", result)
		}
	}
}

func grepLines(text, part string) string {
	var out []string
	for line := range strings.Lines(text) {
		if strings.Contains(line, part) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}

func TestDispatcher_ACancelledCycleReportsNoInvestigationError(t *testing.T) {
	t.Run("before the step", func(t *testing.T) {
		e := newInvEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e.d.Cycle(ctx) //nolint:errcheck // a cancelled cycle reports its own errors
		if got := e.d.InvestigationErrors(); got != 0 {
			t.Fatalf("investigation errors = %d, want 0", got)
		}
	})
	t.Run("during the step", func(t *testing.T) {
		e := newInvEnv(t)
		e.d.InvestigateTimeout = time.Minute
		e.switched("one")
		e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
		e.hold(recordingLock)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); e.d.Cycle(ctx) }() //nolint:errcheck
		e.eventually(5*time.Second, "the opened message", func() bool { return e.openedSent("one") })
		time.Sleep(200 * time.Millisecond) // recording is now waiting for the lock
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the cycle did not stop")
		}
		if got := e.d.InvestigationErrors(); got != 0 {
			t.Fatalf("investigation errors = %d, want 0 for a shutdown", got)
		}
	})
}

func TestDispatcher_OnlyOpenedCarriesInvestigate(t *testing.T) {
	e := newInvEnv(t)
	msgs := e.recordMessages()
	e.switched("one", func(r *store.NotificationRoute) { r.RepeatSeconds = 1800 })
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.advance(time.Second)
	e.cycle() // diagnosis
	e.advance(31 * time.Minute)
	e.cycle() // repeat
	e.setAlerts()
	e.advance(30 * time.Second)
	e.cycle()
	e.advance(3 * time.Minute)
	e.cycle() // resolved
	for _, event := range []Event{EventDiagnosis, EventRepeat, EventResolved} {
		got := msgs.of(event)
		if len(got) != 1 || got[0].Investigate || got[0].IncidentID != 0 {
			t.Errorf("%s messages = %+v, want one without the investigation fields", event, got)
		}
	}
	if got := msgs.of(EventOpened); len(got) != 1 || !got[0].Investigate || got[0].IncidentID == 0 {
		t.Errorf("opened messages = %+v, want one with the investigation fields", got)
	}
}

func TestDispatcher_TheStoredSummaryIsPassedUncleaned(t *testing.T) {
	const raw = "The api is down, see https://elsewhere.example/page for more."
	if CleanSummary(raw, "https://hm.example.com") == raw {
		t.Fatal("the fixture is not changed by cleaning")
	}
	e := newInvEnv(t)
	msgs := e.recordMessages()
	e.switched("one")
	e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
	e.cycle()
	e.finishInvestigations(testSummary)
	e.exec(`update agent_investigations set summary = $1`, raw)
	e.advance(time.Second)
	e.cycle()
	if got := msgs.of(EventDiagnosis); len(got) != 1 || got[0].Diagnosis.Summary != raw {
		t.Fatalf("diagnosis messages = %+v, want the stored summary as it is", got)
	}
}
