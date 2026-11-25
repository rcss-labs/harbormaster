package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ---- money ------------------------------------------------------------------

func TestJob_ReservesBeforeTheAgentIsCalled(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	var seen []runRow
	var runID *uuid.UUID
	f.agent.set(func(c *call) {
		seen, runID = f.runs(), f.inv(id).RunID
		c.send(goodRun("Fine.")...)
	})
	v := f.runTo(id, "done")
	if len(seen) != 1 || seen[0].state != "running" || seen[0].reserved != testReservation || seen[0].tokens != 1 ||
		seen[0].userID == nil || *seen[0].userID != f.identity.ID || runID == nil || *runID != seen[0].id {
		t.Fatalf("when the agent was called: runs %+v, investigation run %v", seen, runID)
	}
	body := f.agent.call(0)
	if body.RunID != seen[0].id || body.Role != "investigator" || body.Provider != testProvider || body.Model != testModel ||
		body.MaxCostMicroUSD != testReservation || len(body.Messages) != 0 || body.Task == nil {
		t.Fatalf("request = %+v", body)
	}
	if body.Task.Kind != "investigate" || body.Task.Subject.Kind != "service" || !slices.Equal(body.Task.Subject.ServiceIDs, []string{checkoutKey.ID()}) ||
		body.Task.Alert.Kind != "service_failing" || !body.Task.Alert.OpenedAt.Equal(v.AlertOpenedAt) {
		t.Errorf("task = %+v", body.Task)
	}
	f.settled("done 4200")
	if v.Summary != "Fine." || v.Attempts != 1 {
		t.Errorf("investigation = %+v", v)
	}
	if ev, err := f.st.ListInvestigationEvidence(context.Background(), id); err != nil || len(ev) != 1 || ev[0].ServiceName != "checkout-api" || ev[0].Action != "view" {
		t.Errorf("evidence = %+v, %v", ev, err)
	}
}

func TestJob_BudgetExhaustedSkipsWithoutCallingTheAgent(t *testing.T) {
	f := newFixture(t)
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = testReservation - 1 })
	id := f.request("checkout-api")
	f.want(f.runTo(id, "skipped"), "skipped", "budget")
	f.clock.advance(time.Hour)
	stop := f.start(f.runner)
	time.Sleep(200 * time.Millisecond)
	stop()
	if f.agent.count() != 0 || len(f.runs()) != 0 {
		t.Errorf("%d agent calls, %d runs, want none", f.agent.count(), len(f.runs()))
	}
	rows := f.audit()
	if len(rows) != 1 || rows[0].Details["outcome"] != "skipped_budget" || rows[0].ActorUsername != f.identity.Username ||
		rows[0].ActorUserID == nil || *rows[0].ActorUserID != f.identity.ID || rows[0].Details["investigation_id"] != float64(id) ||
		rows[0].Details["model"] != testModel || rows[0].Details["cost_micro_usd"] != float64(0) {
		t.Errorf("audit = %+v", rows)
	}
}

// Whatever becomes of a run, it is settled once at its last figures, its token is gone and it has one audit row.
func TestJob_SettlesExactlyOnceOnEveryPath(t *testing.T) {
	cases := map[string]struct {
		script  func(f *fixture, id int64, c *call)
		prepare func(f *fixture)
		stop    bool // cancel the runner once the agent has spoken
		state   string
		reason  string
		run     string
		outcome string
		retryIn time.Duration
	}{
		"done": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("Fine.")...) },
			state: "done", run: "done 4200", outcome: "done"},
		"agent error": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(700), agentError("rate_limited")) },
			state: "failed", reason: "error", run: "failed 700", outcome: "error"},
		"result then error": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine."}), agentError("rate_limited"))
		}, state: "failed", reason: "error", run: "failed 700", outcome: "error"},
		"vendor drop": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine."}), agentError("unavailable"))
		}, state: "requested", run: "failed 700", outcome: "error", retryIn: 5 * time.Minute},
		"no result": {script: func(_ *fixture, _ int64, c *call) { c.send(provisional(90_000), usage(800), agentError("no_result")) },
			state: "failed", reason: "no_result", run: "failed 800", outcome: "no_result"},
		"done without a result": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(900), map[string]any{"type": "notice", "notice": "limit_reached"}, doneEvent)
		}, state: "failed", reason: "limit_reached", run: "failed 900", outcome: "limit_reached"},
		"result after the limit notice": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(900), map[string]any{"type": "notice", "notice": "limit_reached"}, resultEvent(agentresult.Result{Summary: "Fine."}), doneEvent)
		}, state: "done", run: "done 900", outcome: "done"},
		"agent refuses": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusTooManyRequests) },
			state: "requested", run: "failed 0", outcome: "agent_busy", retryIn: 30 * time.Second},
		"agent unreachable": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusBadGateway) },
			state: "requested", run: "failed 0", outcome: "agent_unavailable", retryIn: 60 * time.Second},
		"unknown model": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusNotFound) },
			state: "failed", reason: "error", run: "failed 0", outcome: "error"},
		"stream breaks": {script: func(_ *fixture, _ int64, c *call) { c.send(provisional(90_000)) },
			state: "requested", run: "failed 90000", outcome: "agent_unavailable", retryIn: 60 * time.Second},
		"stream breaks after the result": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine."}))
		}, state: "requested", run: "failed 700", outcome: "agent_unavailable", retryIn: 60 * time.Second},
		"malformed result": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(700), map[string]any{"type": "result", "summary": 7}, doneEvent)
		}, state: "requested", run: "failed 700", outcome: "agent_unavailable", retryIn: 60 * time.Second},
		"cancel": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(600)); c.hold() }, stop: true,
			state: "requested", run: "failed 600", outcome: "interrupted"},
		"overdue": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(600)); c.hold() },
			prepare: func(f *fixture) { f.runner.slack = -59 * time.Second },
			state:   "failed", reason: "error", run: "failed 600", outcome: "error"},
		"panic in the event handler": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(500)); c.send(goodRun("Fine.")...) },
			prepare: func(f *fixture) {
				f.runner.onEvent = func(ev agentclient.Event) {
					if ev.Type == "tool_start" {
						panic("PANIC-SENTINEL")
					}
				}
			}, state: "failed", reason: "error", run: "failed 4200", outcome: "error"},
		"panic after the end": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("Fine.")...) },
			prepare: func(f *fixture) { f.runner.KickNotify = func() { panic("PANIC-SENTINEL") } },
			state:   "done", run: "done 4200", outcome: "done"},
		"store conflict": {script: func(f *fixture, id int64, c *call) {
			f.exec(`update agent_investigations set claim_seq = claim_seq + 1, claimed_until = $2 where id = $1`, id, f.clock.now().Add(time.Hour))
			c.send(goodRun("Fine.")...)
		}, state: "running", run: "done 4200", outcome: "done"},
		"store refuses the result": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("REFUSE-THIS")...) },
			prepare: func(f *fixture) {
				f.exec(`alter table agent_investigations add constraint refuse_this check (summary <> 'REFUSE-THIS')`)
			}, state: "failed", reason: "error", run: "done 4200", outcome: "done"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.request("checkout-api")
			spoke := make(chan struct{}, 8)
			f.agent.set(func(call *call) {
				defer func() { spoke <- struct{}{} }()
				if c.stop {
					spoke <- struct{}{}
				}
				c.script(f, id, call)
			})
			if c.prepare != nil {
				c.prepare(f)
			}
			stop := f.start(f.runner)
			select {
			case <-spoke:
			case <-time.After(10 * time.Second):
				t.Fatal("the agent was never called")
			}
			if c.stop {
				f.wait("the usage to be stored", func() bool { return f.runs()[0].cost > 0 })
				stop()
			}
			f.wait("the run to be settled", func() bool { return !f.anyRunning() && len(f.audit()) > 0 && f.inv(id).State == c.state })
			stop()
			v := f.inv(id)
			f.want(v, c.state, c.reason)
			f.settled(c.run)
			if f.agent.count() != 1 {
				t.Errorf("%d agent calls, want 1", f.agent.count())
			}
			rows := f.audit()
			if len(rows) != 1 || rows[0].Details["outcome"] != c.outcome {
				t.Fatalf("audit = %+v, want outcome %s", rows, c.outcome)
			}
			if n := strings.Count(f.logs.String(), `"msg":"agent run finished"`); n != 1 {
				t.Errorf("the run was ended %d times", n)
			}
			if c.retryIn > 0 && (v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(c.retryIn))) {
				t.Errorf("retry at %v, want in %v", v.ClaimedUntil, c.retryIn)
			}
			if v.State != "done" && v.Summary != "" {
				t.Errorf("a summary was stored: %q", v.Summary)
			}
		})
	}
}

// The first result stands; a later one neither replaces it nor is stored beside it.
func TestJob_TheFirstResultWins(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) {
		c.send(usage(700), resultEvent(agentresult.Result{Summary: "First."}), resultEvent(agentresult.Result{Summary: "Second."}), doneEvent)
	})
	if v := f.runTo(id, "done"); v.Summary != "First." {
		t.Errorf("summary = %q", v.Summary)
	}
	f.settled("done 700")
}

func TestJob_UsageRecordedPerEventProvisionalIncluded(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	var seen []int64
	f.agent.set(func(c *call) {
		for _, ev := range []map[string]any{provisional(90_000), usage(4000), provisional(80_000), usage(5500)} {
			c.send(ev)
			want := ev["cost_micro_usd"].(int64)
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if runs := f.runs(); len(runs) == 1 && runs[0].cost == want {
					seen = append(seen, want)
					break
				}
			}
		}
		c.send(resultEvent(agentresult.Result{Summary: "Fine."}), doneEvent)
	})
	f.runTo(id, "done")
	if !slices.Equal(seen, []int64{90_000, 4000, 80_000, 5500}) {
		t.Errorf("the run row showed %v", seen)
	}
	f.settled("done 5500")
}

func TestJob_SweptRunCancelsAtOnce(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	var closed atomic.Bool
	var events atomic.Int64
	f.runner.onEvent = func(agentclient.Event) { events.Add(1) }
	f.agent.set(func(c *call) {
		c.send(usage(500))
		f.wait("the usage to be stored", func() bool { return f.runs()[0].cost == 500 })
		f.exec(`update agent_runs set state = 'failed', finished_at = $1`, f.clock.now())
		c.send(usage(900), resultEvent(agentresult.Result{Summary: "Fine."}), doneEvent)
		closed.Store(c.hold())
	})
	v := f.runTo(id, "failed")
	f.want(v, "failed", "interrupted")
	f.settled("failed 900")
	f.wait("the stream to be closed", closed.Load)
	if v.Summary != "" || f.agent.count() != 1 {
		t.Errorf("summary %q after %d calls", v.Summary, f.agent.count())
	}
	if events.Load() != 2 {
		t.Errorf("%d events were handled, want the two up to the refusal", events.Load())
	}
}

func TestJob_AtMostTwoRunsPerInvestigation(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) { c.send(usage(700)) })
	f.runToRelease(id)
	f.clock.advance(61 * time.Second)
	f.want(f.runTo(id, "failed"), "failed", "agent_unavailable")
	f.clock.advance(time.Hour)
	stop := f.start(f.runner)
	time.Sleep(200 * time.Millisecond)
	stop()
	f.settled("failed 700", "failed 700")
	if f.agent.count() != 2 {
		t.Errorf("%d agent calls, want 2", f.agent.count())
	}
}

// crashed leaves an investigation as a process that died in the middle of its paid run would.
func (f *fixture) crashed(id int64, cost int64) uuid.UUID {
	f.t.Helper()
	ctx := context.Background()
	v, claim, err := f.st.ClaimInvestigation(ctx, f.clock.now(), 6*time.Minute)
	if err != nil || v.ID != id {
		f.t.Fatalf("claim = %d, %v", v.ID, err)
	}
	run := store.AgentRun{ID: uuid.New(), Role: "investigator", UserID: &f.identity.ID, Provider: testProvider, Model: testModel, ReservedMicroUSD: testReservation}
	if err := f.st.ReserveAgentRun(ctx, run, 10*time.Minute, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.StartInvestigationRun(ctx, claim, run.ID, testProvider, testModel, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	if _, _, err := f.runner.Auth.MintAgentRunToken(ctx, run.ID, true); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.RecordAgentRunUsage(ctx, run.ID, 420, 42, cost, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	return run.ID
}

func TestJob_ReclaimSettlesThePreviousRunFirst(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	old := f.crashed(id, 3300)
	f.settings(func(s *store.AgentSettings) {
		s.RoleModels["investigator"] = store.AgentModelRef{Provider: testProvider, Model: "claude-opus-4-5-20251101"}
	})
	f.agent.models = append(f.agent.models, "claude-opus-4-5-20251101")
	var atCall []runRow
	f.agent.set(func(c *call) { atCall = f.runs(); c.send(goodRun("Fine.")...) })
	f.clock.advance(6*time.Minute + time.Second)
	f.runTo(id, "done")
	if len(atCall) != 2 || atCall[0].id != old || atCall[0].state != "failed" || atCall[0].cost != 3300 || atCall[0].tokens != 0 || atCall[0].audits != 1 {
		t.Fatalf("when the agent was called again: %+v", atCall)
	}
	f.settled("failed 3300", "done 4200")
	first := f.audit()[0]
	if first.Target != old.String() || first.ActorUsername != f.identity.Username || first.Details["outcome"] != "interrupted" ||
		first.Details["role"] != "investigator" || first.Details["model"] != testModel || first.Details["investigation_id"] != float64(id) {
		t.Errorf("audit of the crashed run = %+v", first)
	}
}

// The crashed run is ended even when the new attempt ends before it spends anything.
func TestJob_ReclaimSettlesThePreviousRunEvenWhenNothingIsStarted(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.crashed(id, 3300)
	f.grant(0)
	f.clock.advance(6*time.Minute + time.Second)
	f.want(f.runTo(id, "skipped"), "skipped", "not_visible")
	f.settled("failed 3300")
}

// Ending a run that nobody meters any more: figures kept, token gone, one audit row however often it is ended.
func TestJob_EndIsIdempotentOnTheStore(t *testing.T) {
	f := newFixture(t)
	old := f.crashed(f.request("checkout-api"), 3300)
	run := store.AgentRun{ID: old, Role: "investigator", UserID: &f.identity.ID, Provider: testProvider, Model: testModel}
	runs := &agentrun.Runner{Store: f.st, Now: f.clock.now, Log: f.runner.Log}
	for range 2 {
		runs.End(context.Background(), run, agentrun.Actor{Name: f.identity.Username}, "", agentrun.Ending{Outcome: "interrupted"}, map[string]any{"investigation_id": int64(7)})
	}
	f.settled("failed 3300")
	if rows := f.audit(); len(rows) != 1 || rows[0].Details["investigation_id"] != float64(7) || rows[0].ActorUserID == nil || *rows[0].ActorUserID != f.identity.ID {
		t.Errorf("audit = %+v", rows)
	}
}

// A run that the coordinator's repair ended is audited as the identity's, and the investigation moves on by its claim.
func TestJob_ReconciledRunIsTheIdentitysAndTheInvestigationMovesOn(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.crashed(id, 3300)
	f.clock.advance(61 * time.Minute)
	if res, err := f.st.ReconcileAgentRuns(context.Background(), time.Hour, f.clock.now()); err != nil || res.Settled != 1 || res.Audited != 1 {
		t.Fatalf("reconcile = %+v, %v", res, err)
	}
	rows := f.audit()
	if len(rows) != 1 || rows[0].ActorUsername != f.identity.Username || rows[0].ActorUserID == nil || *rows[0].ActorUserID != f.identity.ID || rows[0].Details["role"] != "investigator" {
		t.Fatalf("audit = %+v", rows)
	}
	f.want(f.runTo(id, "done"), "done", "")
	f.settled("failed 3300", "done 4200")
}

func TestJob_HourlyCapSkips(t *testing.T) {
	f := newFixture(t)
	f.runner.PerHour = 1
	old := f.request("ledger-writer")
	f.exec(`update agent_investigations set state = 'done', started_at = $2 where id = $1`, old, f.clock.now().Add(-61*time.Minute))
	first := f.request("checkout-api")
	f.want(f.runTo(first, "done"), "done", "")
	second := f.request("checkout-api")
	f.want(f.runTo(second, "skipped"), "skipped", "rate_limited")
	f.settled("done 4200")
	if f.agent.count() != 1 {
		t.Errorf("%d agent calls, want 1", f.agent.count())
	}
}

// Two workers that look at the cap at the same moment do not both start the last run it allows.
func TestJob_HourlyCapHoldsForConcurrentStarts(t *testing.T) {
	f := newFixture(t)
	f.runner.PerHour, f.runner.Concurrent = 1, 8
	for range 8 {
		f.request("checkout-api")
	}
	stop := f.start(f.runner)
	f.wait("every request to end", func() bool {
		var left int
		_ = f.st.Pool.QueryRow(context.Background(), `select count(*) from agent_investigations where state in ('requested', 'running')`).Scan(&left)
		return left == 0
	})
	stop()
	if f.agent.count() != 1 {
		t.Errorf("%d agent calls under a cap of 1", f.agent.count())
	}
}

// A cap of 0 turns the investigator off: what is left over is ended without a run, a crashed run included.
func TestJob_PerHourZeroRecordsAndRunsNothing(t *testing.T) {
	f := newFixture(t)
	f.runner.PerHour = 0
	crashed := f.request("checkout-api")
	f.crashed(crashed, 3300)
	f.clock.advance(6*time.Minute + time.Second)
	fresh := f.request("checkout-api")
	f.want(f.runTo(crashed, "skipped"), "skipped", "rate_limited")
	f.want(f.runTo(fresh, "skipped"), "skipped", "rate_limited")
	f.settled("failed 3300")
	if f.agent.count() != 0 {
		t.Errorf("%d agent calls", f.agent.count())
	}
}

func TestJob_ExpiredRequestIsSkippedBeforeAnySpend(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.clock.advance(15*time.Minute + time.Second)
	f.want(f.runTo(id, "skipped"), "skipped", "expired")
	if f.agent.count() != 0 || len(f.runs()) != 0 {
		t.Errorf("%d agent calls, %d runs", f.agent.count(), len(f.runs()))
	}
	// A second attempt is not judged by its age: the first one was in time.
	second := f.request("checkout-api")
	f.crashed(second, 100)
	f.clock.advance(30 * time.Minute)
	f.want(f.runTo(second, "done"), "done", "")
}

func TestJob_AgentBusyDoesNotUseAnAttempt(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	busy := func(c *call) { c.w.WriteHeader(http.StatusTooManyRequests) }
	f.agent.set(busy)
	for range 3 {
		v := f.runToRelease(id)
		if v.Attempts != 0 || v.StartedAt != nil {
			t.Fatalf("after a busy agent: attempts %d, started %v", v.Attempts, v.StartedAt)
		}
		f.clock.advance(31 * time.Second)
	}
	f.agent.set(func(c *call) { c.send(goodRun("Fine.")...) })
	if v := f.runTo(id, "done"); v.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", v.Attempts)
	}
	f.settled("failed 0", "failed 0", "failed 0", "done 4200")

	// The wait ends with the maximum wait, on a second attempt too.
	late := f.request("checkout-api")
	f.crashed(late, 100)
	f.clock.advance(6*time.Minute + time.Second)
	f.agent.set(busy)
	if v := f.runToRelease(late); v.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", v.Attempts)
	}
	f.clock.advance(15 * time.Minute)
	f.want(f.runTo(late, "failed"), "failed", "agent_unavailable")
}

func TestJob_ConcurrencyNeverAboveTheSetting(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.agent.set(func(c *call) { <-release; c.send(goodRun("Fine.")...) })
	for range 5 {
		f.request("checkout-api")
	}
	stop := f.start(f.runner)
	f.wait("two runs", func() bool { return f.agent.count() == 2 })
	for range 5 {
		f.runner.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	if n := f.agent.count(); n != 2 {
		t.Errorf("%d runs at once, want 2", n)
	}
	close(release)
	f.wait("all five", func() bool { return len(f.audit()) == 5 && !f.anyRunning() })
	stop()
	if f.agent.most != 2 {
		t.Errorf("at most %d runs at once, want 2", f.agent.most)
	}
}

// ---- failure modes ----------------------------------------------------------

func TestJob_AgentDownRetriesOnceThenFails(t *testing.T) {
	f := newFixture(t)
	f.agent.catalogueStatus = http.StatusServiceUnavailable
	id := f.request("checkout-api")
	v := f.runToRelease(id)
	if v.Attempts != 1 || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(time.Minute)) {
		t.Fatalf("after the first try: attempts %d, retry %v", v.Attempts, v.ClaimedUntil)
	}
	f.clock.advance(59 * time.Second)
	stop := f.start(f.runner)
	time.Sleep(150 * time.Millisecond)
	stop()
	if f.inv(id).ClaimSeq != v.ClaimSeq {
		t.Fatal("claimed again before the retry time")
	}
	f.clock.advance(2 * time.Second)
	f.want(f.runTo(id, "failed"), "failed", "agent_unavailable")
	if len(f.runs()) != 0 {
		t.Errorf("%d runs were reserved", len(f.runs()))
	}
}

// A model that could not be reached is tried once more, after five minutes; the last attempt fails.
func TestJob_VendorDropRetriesOnceThenFails(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) { c.send(usage(700), agentError("unavailable")) })
	if v := f.runToRelease(id); v.Attempts != 1 || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(5*time.Minute)) {
		t.Fatalf("after the first drop: attempts %d, retry %v", v.Attempts, v.ClaimedUntil)
	}
	f.clock.advance(5*time.Minute + time.Second)
	f.want(f.runTo(id, "failed"), "failed", "error")
	f.settled("failed 700", "failed 700")
}

func TestJob_CoordinatorChangeMidRunRetriesOnce(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) {
		if c.n == 1 {
			c.send(usage(600))
			c.hold()
			return
		}
		c.send(goodRun("Second holder.")...)
	})
	stop := f.start(f.runner)
	f.wait("the first run to cost something", func() bool { r := f.runs(); return len(r) == 1 && r[0].cost == 600 })
	stop()
	if v := f.inv(id); v.State != "requested" || v.Attempts != 1 {
		t.Fatalf("after the first holder: %s, attempts %d", v.State, v.Attempts)
	}
	f.clock.advance(time.Second) // the runs are listed by their start
	v := f.runnerTo(f.newRunner(f.agent), id, "done")
	if v.Attempts != 2 || v.Summary != "Second holder." {
		t.Errorf("investigation = %+v", v)
	}
	f.settled("failed 600", "done 4200")
}

func TestJob_CrashAfterClaimIsReclaimedAfterTheLease(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	if _, _, err := f.st.ClaimInvestigation(context.Background(), f.clock.now(), 6*time.Minute); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(6*time.Minute - time.Second)
	stop := f.start(f.runner)
	time.Sleep(150 * time.Millisecond)
	stop()
	if f.agent.count() != 0 {
		t.Fatal("the lease of the crashed holder was not respected")
	}
	f.clock.advance(2 * time.Second)
	if v := f.runTo(id, "done"); v.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", v.Attempts)
	}
	f.settled("done 4200")
}

func TestJob_PausedHolderCannotOverwriteTheResult(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	resume := make(chan struct{})
	f.agent.set(func(c *call) {
		c.send(usage(600))
		<-resume
		c.send(resultEvent(agentresult.Result{Summary: "From the paused holder."}), doneEvent)
	})
	stop := f.start(f.runner)
	f.wait("the first run to cost something", func() bool { r := f.runs(); return len(r) == 1 && r[0].cost == 600 })
	// The lease is the run timeout, at least five minutes, and one minute more.
	f.clock.advance(6*time.Minute + time.Second)
	second := newFakeAgent(t)
	second.set(func(c *call) { c.send(goodRun("From the new holder.")...) })
	stopSecond := f.start(f.newRunner(second))
	f.wait("the new holder to finish", func() bool { return f.inv(id).State == "done" })
	stopSecond()
	close(resume)
	f.wait("the paused holder to end", func() bool { return strings.Count(f.logs.String(), `"outcome":"done"`) >= 2 })
	stop()
	if v := f.inv(id); v.Summary != "From the new holder." || v.State != "done" {
		t.Errorf("investigation = %s %q", v.State, v.Summary)
	}
	f.settled("failed 600", "done 4200")
}

func TestJob_LeaseNotConfirmedStartsNothing(t *testing.T) {
	// The guard fails from its nth check on: whichever check finds out, nothing is paid and no attempt is used.
	for n := 1; n <= 3; n++ {
		t.Run(fmt.Sprint("from check ", n), func(t *testing.T) {
			f := newFixture(t)
			f.runner.Concurrent = 1 // so that the checks come in one order
			id := f.request("checkout-api")
			var checks atomic.Int64
			f.runner.Guard = func(context.Context) error {
				if checks.Add(1) >= int64(n) {
					return errNoLease
				}
				return nil
			}
			stop := f.start(f.runner)
			f.wait("the guard to refuse", func() bool { return checks.Load() >= int64(n)+2 })
			stop()
			v := f.inv(id)
			if v.State != "requested" || v.Attempts != 0 || v.StartedAt != nil {
				t.Errorf("investigation = %s, attempts %d, started %v", v.State, v.Attempts, v.StartedAt)
			}
			// Only the last check comes after a reservation; nothing is held of the allowance before it.
			if runs := f.runs(); (len(runs) > 0) != (n == 3) {
				t.Errorf("runs = %+v", runs)
			}
			for _, r := range f.runs() {
				if r.state != "failed" || r.cost != 0 || r.tokens != 0 {
					t.Errorf("run = %+v", r)
				}
			}
			if f.agent.count() != 0 {
				t.Errorf("%d agent calls", f.agent.count())
			}
		})
	}
}

// The lease lost while the reservation was being made: the run exists by then and is ended unpaid.
func TestJob_LeaseLostWhileReserving(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.runner.Guard = func(context.Context) error {
		if len(f.runs()) > 0 {
			return errNoLease
		}
		return nil
	}
	v := f.runToRelease(id)
	if v.Attempts != 0 || f.agent.count() != 0 {
		t.Errorf("attempts %d, %d agent calls", v.Attempts, f.agent.count())
	}
	f.settled("failed 0")
}

// Stopped between the claim and the call of the agent: the claim is given back, though the context has ended by then.
func TestJob_StoppedDuringSetUpGivesTheClaimBack(t *testing.T) {
	for name, stopNow := range map[string]func(f *fixture, check int64) bool{
		"before the reservation": func(_ *fixture, check int64) bool { return check == 2 },
		"after the reservation":  func(f *fixture, _ int64) bool { return len(f.runs()) > 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.runner.Concurrent = 1 // so that the checks come in one order
			id := f.request("checkout-api")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var checks atomic.Int64
			f.runner.Guard = func(ctx context.Context) error {
				if stopNow(f, checks.Add(1)) {
					cancel()
				}
				if len(f.runs()) == 0 {
					return ctx.Err()
				}
				return nil // the lease was confirmed and the job stopped right after
			}
			f.runner.Run(ctx)
			if reserved := len(f.runs()) > 0; reserved != (name == "after the reservation") {
				t.Errorf("a run was reserved: %v", reserved)
			}
			if v := f.inv(id); v.State != "requested" || v.Attempts != 0 || v.ClaimSeq != 1 {
				t.Errorf("investigation = %s, attempts %d, claims %d", v.State, v.Attempts, v.ClaimSeq)
			}
			for _, r := range f.runs() {
				if r.state != "failed" || r.cost != 0 || r.tokens != 0 || r.audits != 1 {
					t.Errorf("run = %+v", r)
				}
			}
			if f.agent.count() != 0 {
				t.Errorf("%d agent calls", f.agent.count())
			}
		})
	}
}

func TestJob_TheClaimCoversTheWholeRun(t *testing.T) {
	for timeout, want := range map[time.Duration]time.Duration{time.Minute: 6 * time.Minute, 20 * time.Minute: 21 * time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			f := newFixture(t)
			f.agent.runTimeout = timeout
			id := f.request("checkout-api")
			var until *time.Time
			f.agent.set(func(c *call) { until = f.inv(id).ClaimedUntil; c.send(goodRun("Fine.")...) })
			f.runTo(id, "done")
			if until == nil || !until.Equal(f.clock.now().Add(want)) {
				t.Errorf("claimed until %v, want in %v", until, want)
			}
		})
	}
}

func TestJob_NoModelConfiguredSkips(t *testing.T) {
	for name, change := range map[string]func(*store.AgentSettings){
		"none chosen": func(s *store.AgentSettings) { s.RoleModels = nil },
		"not offered any more": func(s *store.AgentSettings) {
			s.RoleModels["investigator"] = store.AgentModelRef{Provider: testProvider, Model: "claude-haiku-4-5-20251001"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.settings(change)
			id := f.request("checkout-api")
			f.want(f.runTo(id, "skipped"), "skipped", "not_configured")
			if f.agent.count() != 0 || len(f.runs()) != 0 {
				t.Errorf("%d agent calls, %d runs", f.agent.count(), len(f.runs()))
			}
		})
	}
}

func TestJob_OldAgentWithoutTheRoleSkips(t *testing.T) {
	for name, roles := range map[string][]string{"no roles listed": nil, "chat only": {"chat", "test"}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.agent.roles = roles
			id := f.request("checkout-api")
			f.want(f.runTo(id, "skipped"), "skipped", "not_configured")
			if f.agent.count() != 0 || len(f.runs()) != 0 {
				t.Errorf("%d agent calls, %d runs", f.agent.count(), len(f.runs()))
			}
		})
	}
}

func TestJob_IdentityCannotSeeTheServiceSkipsWithoutARun(t *testing.T) {
	for name, prepare := range map[string]func(f *fixture) string{
		"no grant": func(f *fixture) string { f.grant(0); return "checkout-api" },
		"tag moved": func(f *fixture) string {
			tag, err := f.st.CreateTag(context.Background(), "finance", "red", false)
			if err != nil {
				f.t.Fatal(err)
			}
			if err := f.st.SetServiceTag(context.Background(), checkoutKey, &tag.ID); err != nil {
				f.t.Fatal(err)
			}
			return "checkout-api"
		},
		"service gone": func(*fixture) string { return "deleted-api" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.request(prepare(f))
			f.want(f.runTo(id, "skipped"), "skipped", "not_visible")
			if f.agent.count() != 0 || len(f.runs()) != 0 {
				t.Errorf("%d agent calls, %d runs", f.agent.count(), len(f.runs()))
			}
		})
	}
}

func TestJob_GroupKeepsOnlyVisibleMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag, err := f.st.CreateTag(ctx, "finance", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	var members []store.InvestigationMember
	var want []string
	var keys []model.ServiceKey
	for i := range 25 {
		k := model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: fmt.Sprintf("worker-%02d", i)}
		keys = append(keys, k)
		members = append(members, store.InvestigationMember{Cluster: k.Cluster, Service: k.Service})
		if i >= 2 && len(want) < 20 {
			want = append(want, k.ID())
		}
	}
	f.services(keys...)
	for _, k := range keys[:2] {
		if err := f.st.SetServiceTag(ctx, k, &tag.ID); err != nil {
			t.Fatal(err)
		}
	}
	members = append(members[:2:2], append([]store.InvestigationMember{{Cluster: checkoutKey.Cluster, Service: "deleted-api"}, members[2]}, members[2:]...)...)
	group := func(members []store.InvestigationMember) int64 {
		raw, err := json.Marshal(members)
		if err != nil {
			t.Fatal(err)
		}
		id := f.request("checkout-api")
		f.exec(`update agent_investigations set subject_kind = 'cluster', service_name = '', members = $2,
			subject_key = 'cluster/482109375512/us-east-1/payments-prod' where id = $1`, id, raw)
		return id
	}
	f.runTo(group(members), "done")
	subject := f.agent.call(0).Task.Subject
	if subject.Kind != "cluster" || subject.ClusterID != checkoutKey.ClusterKey.ID() || !slices.Equal(subject.ServiceIDs, want) {
		t.Errorf("subject = %+v, want the 20 visible members once each", subject)
	}
	hidden := group(members[:3])
	f.want(f.runTo(hidden, "skipped"), "skipped", "not_visible")
	none := group([]store.InvestigationMember{})
	f.want(f.runTo(none, "skipped"), "skipped", "not_visible")
	if f.agent.count() != 1 {
		t.Errorf("%d agent calls, want 1", f.agent.count())
	}
}

func TestJob_IncidentResolvedMidRunStillStoresTheResult(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.exec(`insert into notification_incidents (account_id, region, cluster_name, service_name, kind, severity, title, text, opened_at)
		values ('482109375512', 'us-east-1', 'payments-prod', 'checkout-api', 'service_failing', 'danger', 't', 't', now())`)
	f.exec(`insert into agent_investigation_incidents (investigation_id, incident_id) select $1, id from notification_incidents`, id)
	f.agent.set(func(c *call) {
		f.exec(`update notification_incidents set resolved_at = now()`)
		c.send(goodRun("Fine.")...)
	})
	if v := f.runTo(id, "done"); v.Summary != "Fine." {
		t.Errorf("summary = %q", v.Summary)
	}
}

func TestJob_DoneKicksNotify(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.runTo(id, "done")
	if f.kicks.Load() != 1 {
		t.Errorf("%d kicks after a done investigation, want 1", f.kicks.Load())
	}
	f.agent.set(func(c *call) { c.send(agentError("no_result")) })
	f.runTo(f.request("checkout-api"), "failed")
	if f.kicks.Load() != 1 {
		t.Errorf("%d kicks after a failed one, want still 1", f.kicks.Load())
	}
}

// A request does not wait for the interval when the job is kicked, nor does the next one when a slot is free again.
func TestJob_KickAndAFreeSlotDoNotWaitForTheInterval(t *testing.T) {
	f := newFixture(t)
	f.runner.Interval, f.runner.Concurrent = time.Hour, 1
	stop := f.start(f.runner)
	time.Sleep(100 * time.Millisecond)
	first, second := f.request("checkout-api"), f.request("checkout-api")
	f.runner.Kick()
	f.wait("both to be done", func() bool { return f.inv(first).State == "done" && f.inv(second).State == "done" })
	stop()
}

// ---- labels -----------------------------------------------------------------

// What the identity may read is asked when the result arrives, and a service that does not exist is read by nobody.
func TestJob_EvidenceIsCheckedAgainstTheIdentityNow(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) {
		f.grant(authz.ActionSet(authz.View)) // Logs taken away during the run
		gone := viewItem("x")
		gone.ServiceID = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "invented-api"}.ID()
		c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine.", Evidence: []agentresult.Evidence{logsItem("refused"), viewItem("desired 2"), gone}}), doneEvent)
	})
	v := f.runTo(id, "done")
	ev, err := f.st.ListInvestigationEvidence(context.Background(), id)
	if err != nil || len(ev) != 1 || ev[0].Action != "view" || v.EvidenceDropped != 2 {
		t.Errorf("evidence = %+v, dropped %d, %v", ev, v.EvidenceDropped, err)
	}
}

// ---- content ----------------------------------------------------------------

var sentinels = []string{"SUMMARY-SENTINEL", "NOTE-SENTINEL", "EXCERPT-SENTINEL", "ALERT-SENTINEL", "TITLE-SENTINEL", "MESSAGE-SENTINEL", "PANIC-SENTINEL", "DELTA-SENTINEL", "CODE_SENTINEL"}

func sentinelRun(end ...map[string]any) []map[string]any {
	item := logsItem("EXCERPT-SENTINEL refused")
	item.Note = "NOTE-SENTINEL"
	return append([]map[string]any{usage(700), {"type": "text", "delta": "DELTA-SENTINEL"}, toolStart("EXCERPT-SENTINEL"),
		resultEvent(agentresult.Result{Summary: "SUMMARY-SENTINEL", Evidence: []agentresult.Evidence{item}})}, end...)
}

func (f *fixture) runSentinels() {
	f.t.Helper()
	f.agent.set(func(c *call) { c.send(sentinelRun(doneEvent)...) })
	f.runTo(f.request("checkout-api"), "done")
	f.agent.set(func(c *call) { c.send(sentinelRun(agentError("CODE_SENTINEL"))...) })
	f.runTo(f.request("checkout-api"), "failed")
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = 1 })
	f.runTo(f.request("checkout-api"), "skipped")
	f.settings(func(*store.AgentSettings) {})
	f.runner.onEvent = func(agentclient.Event) { panic("PANIC-SENTINEL") }
	f.runTo(f.request("checkout-api"), "failed")
}

func TestJob_AuditRowHasNoContent(t *testing.T) {
	f := newFixture(t)
	f.runSentinels()
	rows := f.audit()
	if len(rows) != 4 {
		t.Fatalf("%d audit rows, want 4", len(rows))
	}
	for _, e := range rows {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if w := containsAny(string(raw), sentinels...); w != "" {
			t.Errorf("audit row holds %s: %s", w, raw)
		}
		for _, k := range []string{"run_id", "investigation_id", "role", "provider", "model", "tools", "input_tokens", "output_tokens", "cost_micro_usd", "outcome", "subject_kind"} {
			if _, ok := e.Details[k]; !ok {
				t.Errorf("audit row lacks %s: %s", k, raw)
			}
		}
		if e.Details["subject_kind"] != "service" || e.Details["role"] != "investigator" || e.Target != e.Details["run_id"] {
			t.Errorf("audit row = %s", raw)
		}
	}
	// What is stored of a run that failed is a fixed code.
	var reasons []string
	dbRows, err := f.st.Pool.Query(context.Background(), `select reason from agent_investigations where state <> 'done'`)
	if err != nil {
		t.Fatal(err)
	}
	for dbRows.Next() {
		var reason string
		if err := dbRows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, reason)
	}
	if w := containsAny(strings.Join(reasons, " "), sentinels...); w != "" || len(reasons) != 3 {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestJob_LogsHaveNoContent(t *testing.T) {
	f := newFixture(t)
	f.runSentinels()
	logs := f.logs.String()
	if w := containsAny(logs, append(slices.Clone(sentinels), "hma_")...); w != "" {
		t.Errorf("the log holds %s:\n%s", w, logs)
	}
	if !strings.Contains(logs, "agent run finished") {
		t.Error("nothing was logged")
	}
}

func TestJob_RedactEnvFollowsTheProviderSetting(t *testing.T) {
	for _, unredacted := range [][]string{nil, {"openai"}, {testProvider}} {
		t.Run(fmt.Sprint(unredacted), func(t *testing.T) {
			f := newFixture(t)
			f.settings(func(s *store.AgentSettings) { s.UnredactedProviders = unredacted })
			var redact bool
			var scope, user string
			f.agent.set(func(c *call) {
				if err := f.st.Pool.QueryRow(context.Background(), `select t.redact_env, t.scope, u.username from api_tokens t join users u on u.id = t.user_id
					where t.run_id = $1`, c.body.RunID).Scan(&redact, &scope, &user); err != nil {
					t.Error(err)
				}
				if !strings.HasPrefix(c.r.Header.Get("Authorization"), "Bearer hma_") {
					t.Error("the agent was not given the run token")
				}
				c.send(goodRun("Fine.")...)
			})
			f.runTo(f.request("checkout-api"), "done")
			if want := !slices.Contains(unredacted, testProvider); redact != want || scope != "read" || user != f.identity.Username {
				t.Errorf("token: redact %v (want %v), scope %s, user %s", redact, want, scope, user)
			}
		})
	}
}

// The alert's text goes to the agent scrubbed and within what it accepts.
func TestJob_TaskTextIsScrubbedAndWithinTheAgentsLimits(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.exec(`update agent_investigations set alert_title = $2, alert_text = $3 where id = $1`, id,
		strings.Repeat("é", 1500), strings.Repeat("x ", 743)+fakeSecret+strings.Repeat(" y", 2000))
	f.runTo(id, "done")
	alert := f.agent.call(0).Task.Alert
	if len(alert.Title) > 2000 || len(alert.Text) > 2000 || alert.Title == "" || strings.Contains(alert.Title, "\ufffd") {
		t.Errorf("title of %d bytes, text of %d", len(alert.Title), len(alert.Text))
	}
	if strings.Contains(alert.Text, fakeSecret[:6]) {
		t.Error("the text holds a secret")
	}
}
