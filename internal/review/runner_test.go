package review

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ---- schedule -------------------------------------------------------------------------------------------------------

func TestJob_RunsOncePerPeriodAcrossTwoRunners(t *testing.T) {
	f := newFixture(t)
	f.clock.at = dueAt.Add(-2 * time.Second)
	rv := f.review("Daily spend")
	f.scheduleChanged(rv.ID, dueAt.Add(-time.Hour)) // yesterday's occurrence is not its own
	second := f.newRunner(f.agent)
	stop, stopSecond := f.start(f.runner), f.start(second)
	time.Sleep(200 * time.Millisecond)
	if runs := f.reviewRuns(rv.ID); len(runs) != 0 {
		t.Fatalf("before the scheduled minute: %+v", runs)
	}
	f.clock.advance(3 * time.Second)
	f.wait("the review to be done", func() bool { runs := f.reviewRuns(rv.ID); return len(runs) == 1 && runs[0].State == "done" })
	for range 5 {
		f.clock.advance(30 * time.Second)
		time.Sleep(60 * time.Millisecond)
	}
	stop()
	stopSecond()
	runs := f.reviewRuns(rv.ID)
	if len(runs) != 1 || runs[0].PeriodKey != "d:2026-11-25" || runs[0].PeriodLabel != "25 Nov 2026" || !runs[0].ScheduledFor.Equal(dueAt) ||
		runs[0].Manual || runs[0].Attempts != 1 {
		t.Errorf("runs = %+v", runs)
	}
	f.settled("done 4200")
	if f.agent.count() != 1 {
		t.Errorf("%d agent calls, want 1", f.agent.count())
	}
}

func TestJob_LateStartWithinTheLimitRuns(t *testing.T) {
	f := newFixture(t)
	f.clock.at = dueAt.Add(6 * time.Hour) // exactly the limit
	rv := f.review("Daily spend")
	stop := f.start(f.runner)
	f.wait("the review to be done", func() bool { runs := f.reviewRuns(rv.ID); return len(runs) == 1 && runs[0].State == "done" })
	stop()
	f.settled("done 4200")
}

func TestJob_PastTheLimitRecordsMissedWithoutARun(t *testing.T) {
	t.Run("before the row was recorded", func(t *testing.T) {
		f := newFixture(t)
		f.clock.at = dueAt.Add(6*time.Hour + time.Second)
		rv := f.review("Daily spend")
		stop := f.start(f.runner)
		f.wait("the record", func() bool { return len(f.reviewRuns(rv.ID)) == 1 })
		stop()
		run := f.reviewRuns(rv.ID)[0]
		f.want(run, "skipped", "missed")
		if run.ClaimSeq != 0 {
			t.Errorf("a row that came too late was claimed")
		}
		f.unspent()
	})
	t.Run("while the row waited", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		f.clock.at = dueAt.Add(6*time.Hour + time.Second)
		f.want(f.runTo(id, "skipped"), "skipped", "missed")
		f.unspent()
	})
	// A holder that died before it started a run used an attempt, not the limit.
	t.Run("after a crash before the start", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		if _, _, err := f.st.ClaimReviewRun(context.Background(), f.clock.now(), 6*time.Minute, f.clock.now()); err != nil {
			t.Fatal(err)
		}
		f.clock.at = dueAt.Add(6*time.Hour + time.Second)
		f.want(f.runTo(id, "skipped"), "skipped", "missed")
		f.unspent()
	})
}

func TestJob_LongOutageLeavesOneRecordPerReview(t *testing.T) {
	f := newFixture(t)
	f.clock.at = dueAt.Add(10*24*time.Hour + 7*time.Hour)
	daily, other := f.review("Daily spend"), f.review("Daily capacity", func(r *store.Review) { r.Type = "capacity" })
	f.ticks(f.runner)
	for _, rv := range []store.Review{daily, other} {
		runs := f.reviewRuns(rv.ID)
		if len(runs) != 1 {
			t.Fatalf("%s: %d records, want 1", rv.Name, len(runs))
		}
		f.want(runs[0], "skipped", "missed")
		if runs[0].PeriodKey != "d:2026-12-05" {
			t.Errorf("period = %s", runs[0].PeriodKey)
		}
	}
	f.unspent()
}

func TestJob_NewReviewFirstRunsAtItsNextOccurrence(t *testing.T) {
	f := newFixture(t)
	rv := f.review("Daily spend")
	f.scheduleChanged(rv.ID, f.clock.now().Add(-time.Minute)) // made after today's occurrence
	f.ticks(f.runner)
	if runs := f.reviewRuns(rv.ID); len(runs) != 0 {
		t.Fatalf("an occurrence before the review was made: %+v", runs)
	}
	f.clock.at = dueAt.Add(24*time.Hour + time.Second)
	stop := f.start(f.runner)
	f.wait("tomorrow's run", func() bool { runs := f.reviewRuns(rv.ID); return len(runs) == 1 && runs[0].State == "done" })
	stop()
	if runs := f.reviewRuns(rv.ID); runs[0].PeriodKey != "d:2026-11-26" {
		t.Errorf("period = %s", runs[0].PeriodKey)
	}
}

func TestJob_ScheduleMovedLaterTodayDoesNotRunTwice(t *testing.T) {
	f := newFixture(t)
	rv := f.review("Daily spend")
	stop := f.start(f.runner)
	f.wait("today's run", func() bool { runs := f.reviewRuns(rv.ID); return len(runs) == 1 && runs[0].State == "done" })
	stop()
	rv.AtMinute = 540 // 09:00
	if _, err := f.st.UpdateReview(context.Background(), rv, 0); err != nil {
		t.Fatal(err)
	}
	f.scheduleChanged(rv.ID, f.clock.now())
	f.clock.at = dueAt.Add(time.Hour + time.Minute)
	f.ticks(f.runner)
	if runs := f.reviewRuns(rv.ID); len(runs) != 1 {
		t.Errorf("%d runs of one day", len(runs))
	}
	f.settled("done 4200")
}

func TestJob_InvalidScheduleIsSkippedAndLogged(t *testing.T) {
	f := newFixture(t)
	broken, fine := f.review("Broken"), f.review("Fine")
	f.exec(`update agent_reviews set timezone = 'Mars/Olympus' where id = $1`, broken.ID)
	stop := f.start(f.runner)
	f.wait("the other review", func() bool { runs := f.reviewRuns(fine.ID); return len(runs) == 1 && runs[0].State == "done" })
	stop()
	if runs := f.reviewRuns(broken.ID); len(runs) != 0 {
		t.Errorf("a review with an invalid schedule ran: %+v", runs)
	}
	if !strings.Contains(f.logs.String(), fmt.Sprintf(`"review_id":%d`, broken.ID)) {
		t.Errorf("the invalid schedule was not logged:\n%s", f.logs.String())
	}
}

func TestJob_DisabledReviewIsNotRecorded(t *testing.T) {
	f := newFixture(t)
	rv := f.review("Daily spend", func(r *store.Review) { r.Enabled = false })
	f.ticks(f.runner)
	if runs := f.reviewRuns(rv.ID); len(runs) != 0 {
		t.Errorf("runs = %+v", runs)
	}
}

func TestJob_PerDayZeroRecordsAndRunsNothing(t *testing.T) {
	f := newFixture(t)
	f.runner.PerDay = 0
	waiting := f.due("Waiting")
	rv := f.review("Daily spend")
	f.ticks(f.runner)
	if runs := f.reviewRuns(rv.ID); len(runs) != 0 {
		t.Errorf("recorded %+v", runs)
	}
	if v := f.row(waiting); v.State != "requested" || v.ClaimSeq != 0 {
		t.Errorf("the waiting run = %s, claims %d", v.State, v.ClaimSeq)
	}
	f.unspent()
}

// A kick wakes the loop; a run that ended lets the next one in without waiting for the interval.
func TestJob_KickAndAFreeSlotDoNotWaitForTheInterval(t *testing.T) {
	f := newFixture(t)
	f.runner.Interval = time.Hour
	stop := f.start(f.runner)
	time.Sleep(100 * time.Millisecond)
	first, second := f.due("First"), f.due("Second")
	f.runner.Kick()
	f.wait("both to be done", func() bool { return f.row(first).State == "done" && f.row(second).State == "done" })
	stop()
}

// ---- money ----------------------------------------------------------------------------------------------------------

func TestJob_ReservesBeforeTheAgentIsCalled(t *testing.T) {
	f := newFixture(t)
	payments := f.tag("payments")
	f.grant(f.dev, payments)
	f.tagService(checkoutKey, payments)
	id := f.due("Weekly spend", func(r *store.Review) {
		r.Cadence, r.Weekday = "weekly", 3
		r.Scope = []store.NotificationSelector{{Kind: "harbormaster", TagID: payments}}
	})
	f.exec(`update agent_review_runs set period_key = 'w:2026-W48', period_label = 'PERIOD <typed>' where id = $1`, id)
	var seen []runRow
	var row store.ReviewRun
	f.agent.set(func(c *call) {
		seen, row = f.runs(), f.row(id)
		c.send(goodRun("Spend rose by 12 percent.")...)
	})
	v := f.runTo(id, "done")
	if len(seen) != 1 || seen[0].state != "running" || seen[0].reserved != testReservation || seen[0].tokens != 1 ||
		seen[0].userID == nil || *seen[0].userID != f.identity.ID || row.RunID == nil || *row.RunID != seen[0].id || row.StartedAt == nil {
		t.Fatalf("when the agent was called: runs %+v, review run %+v", seen, row)
	}
	body := f.agent.call(0)
	if body.RunID != seen[0].id || body.Role != "reviewer" || body.Provider != testProvider || body.Model != testModel ||
		body.MaxCostMicroUSD != testReservation || len(body.Messages) != 0 || body.Task == nil || body.Task.Review == nil || body.Task.Scope == nil {
		t.Fatalf("request = %+v", body)
	}
	// The period text comes from the period key, never from the stored label.
	if want := (agentclient.TaskReview{Type: "cost", Range: "7d", Period: "Week 48, 2026"}); *body.Task.Review != want {
		t.Errorf("review = %+v, want %+v", *body.Task.Review, want)
	}
	if want := []agentclient.ScopeEntry{{Kind: "tag", TagID: fmt.Sprint(payments), Name: "payments"}}; !slices.Equal(body.Task.Scope.Entries, want) || body.Task.Scope.ServicesTotal != 1 {
		t.Errorf("scope = %+v", *body.Task.Scope)
	}
	f.settled("done 4200")
	if v.Summary != "Spend rose by 12 percent." || v.Attempts != 1 || v.Provider != testProvider || v.Model != testModel || v.Range != "7d" || v.ScopeLabel != "Tag: payments" {
		t.Errorf("review run = %+v", v)
	}
	ev, err := f.st.ListReviewRunEvidence(context.Background(), id)
	if err != nil || len(ev) != 1 || ev[0].ServiceName != checkoutKey.Service || ev[0].ClusterName != checkoutKey.Cluster ||
		ev[0].AccountID != checkoutKey.AccountID || ev[0].Region != checkoutKey.Region || ev[0].Action != "view" {
		t.Errorf("evidence = %+v, %v", ev, err)
	}
}

func TestJob_BudgetExhaustedSkipsWithoutCallingTheAgent(t *testing.T) {
	f := newFixture(t)
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = testReservation - 1 })
	id := f.due("Daily spend")
	f.want(f.runTo(id, "skipped"), "skipped", "budget")
	f.clock.advance(time.Hour)
	f.ticks(f.runner)
	f.unspent()
	rows := f.audit()
	if len(rows) != 1 || rows[0].Details["outcome"] != "skipped_budget" || rows[0].ActorUsername != f.identity.Username ||
		rows[0].ActorUserID == nil || *rows[0].ActorUserID != f.identity.ID || rows[0].Details["review_run_id"] != float64(id) ||
		rows[0].Details["role"] != "reviewer" || rows[0].Details["model"] != testModel || rows[0].Details["cost_micro_usd"] != float64(0) {
		t.Errorf("audit = %+v", rows)
	}
}

// A review leaves room for one chat run and one investigation, and takes the last sum that does.
func TestJob_SkippedWhenOnlyTheReviewItselfWouldFit(t *testing.T) {
	for name, c := range map[string]struct {
		budget int64
		change func(*store.AgentSettings)
		state  string
	}{
		"only the review":               {budget: testReservation, state: "skipped"},
		"the review and one run":        {budget: testReservation + testLeave/2, state: "skipped"},
		"one below":                     {budget: testReservation + testLeave - 1, state: "skipped"},
		"all three":                     {budget: testReservation + testLeave, state: "done"},
		"no chat or investigator model": {budget: testReservation, change: func(s *store.AgentSettings) { delete(s.RoleModels, "chat"); delete(s.RoleModels, "investigator") }, state: "done"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.settings(func(s *store.AgentSettings) {
				s.MonthlyBudgetMicroUSD = c.budget
				if c.change != nil {
					c.change(s)
				}
			})
			id := f.due("Daily spend")
			v := f.runTo(id, c.state)
			if c.state == "skipped" {
				f.want(v, "skipped", "budget")
				f.unspent()
				return
			}
			f.settled("done 4200")
		})
	}
}

func TestJob_AChatRunFitsWhileAReviewIsRunning(t *testing.T) {
	f := newFixture(t)
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = testReservation + testLeave })
	id := f.due("Daily spend")
	reserve := func(role string) error {
		return f.st.ReserveAgentRun(context.Background(), store.AgentRun{ID: uuid.New(), Role: role, UserID: &f.admin.ID, Provider: testProvider,
			Model: testModel, ReservedMicroUSD: testReservation}, 10*time.Minute, f.clock.now())
	}
	var chat, investigation, third error
	f.agent.set(func(c *call) {
		chat, investigation, third = reserve("chat"), reserve("investigator"), reserve("chat")
		c.send(goodRun("Fine.")...)
	})
	stop := f.start(f.runner)
	f.wait("the review", func() bool { return f.row(id).State == "done" })
	stop()
	if chat != nil || investigation != nil || third == nil {
		t.Errorf("while a review ran: chat %v, investigation %v, a third run %v; want room for two", chat, investigation, third)
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
		freed   bool // the attempt was given back
	}{
		"done": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("Fine.")...) },
			state: "done", run: "done 4200", outcome: "done"},
		"agent error": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(700), agentError("rate_limited")) },
			state: "failed", reason: "error", run: "failed 700", outcome: "error"},
		"result then error": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine."}), agentError("rate_limited"))
		}, state: "failed", reason: "error", run: "failed 700", outcome: "error"},
		"vendor drop": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(700), agentError("unavailable")) },
			state: "failed", reason: "error", run: "failed 700", outcome: "error"},
		"no result": {script: func(_ *fixture, _ int64, c *call) { c.send(provisional(90_000), usage(800), agentError("no_result")) },
			state: "failed", reason: "no_result", run: "failed 800", outcome: "no_result"},
		"done without a result": {script: func(_ *fixture, _ int64, c *call) {
			c.send(usage(900), map[string]any{"type": "notice", "notice": "limit_reached"}, doneEvent)
		}, state: "failed", reason: "limit_reached", run: "failed 900", outcome: "limit_reached"},
		"agent refuses": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusTooManyRequests) },
			state: "requested", run: "failed 0", outcome: "agent_busy", retryIn: time.Minute, freed: true},
		"agent unreachable": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusBadGateway) },
			state: "requested", run: "failed 0", outcome: "agent_unavailable", retryIn: 5 * time.Minute},
		"bad request": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusBadRequest) },
			state: "failed", reason: "error", run: "failed 0", outcome: "error"},
		"unknown model": {script: func(_ *fixture, _ int64, c *call) { c.w.WriteHeader(http.StatusNotFound) },
			state: "failed", reason: "error", run: "failed 0", outcome: "error"},
		"stream breaks": {script: func(_ *fixture, _ int64, c *call) { c.send(provisional(90_000)) },
			state: "requested", run: "failed 90000", outcome: "agent_unavailable", retryIn: 5 * time.Minute},
		"swept": {script: func(f *fixture, _ int64, c *call) {
			c.send(usage(500))
			f.wait("the usage to be stored", func() bool { return f.runs()[0].cost == 500 })
			f.exec(`update agent_runs set state = 'failed', finished_at = $1`, f.clock.now())
			c.send(usage(900), resultEvent(agentresult.Result{Summary: "Fine."}), doneEvent)
			c.hold()
		}, state: "failed", reason: "interrupted", run: "failed 900", outcome: "interrupted"},
		"cancel": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(600)); c.hold() }, stop: true,
			state: "requested", run: "failed 600", outcome: "interrupted"},
		"overdue": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(600)); c.hold() },
			prepare: func(f *fixture) { f.runner.Driver.Slack = -59 * time.Second },
			state:   "failed", reason: "error", run: "failed 600", outcome: "error"},
		"panic in the event handler": {script: func(_ *fixture, _ int64, c *call) { c.send(usage(500)); c.send(goodRun("Fine.")...) },
			prepare: func(f *fixture) {
				f.runner.Driver.OnEvent = func(ev agentclient.Event) {
					if ev.Type == "tool_start" {
						panic("PANIC-SENTINEL")
					}
				}
			}, state: "failed", reason: "error", run: "failed 4200", outcome: "error"},
		"panic after the end": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("Fine.")...) },
			prepare: func(f *fixture) { f.runner.KickNotify = func() { panic("PANIC-SENTINEL") } },
			state:   "done", run: "done 4200", outcome: "done"},
		"store conflict": {script: func(f *fixture, id int64, c *call) {
			f.exec(`update agent_review_runs set claim_seq = claim_seq + 1, claimed_until = $2 where id = $1`, id, f.clock.now().Add(time.Hour))
			c.send(goodRun("Fine.")...)
		}, state: "running", run: "done 4200", outcome: "done"},
		"store refuses the result": {script: func(_ *fixture, _ int64, c *call) { c.send(goodRun("REFUSE-THIS")...) },
			prepare: func(f *fixture) {
				f.exec(`alter table agent_review_runs add constraint refuse_this check (summary <> 'REFUSE-THIS')`)
			}, state: "failed", reason: "error", run: "done 4200", outcome: "done"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
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
			f.wait("the run to be settled", func() bool { return !f.anyRunning() && len(f.audit()) > 0 && f.row(id).State == c.state })
			stop()
			v := f.row(id)
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
			if want := map[bool]int{true: 0, false: 1}[c.freed]; v.Attempts != want {
				t.Errorf("attempts = %d, want %d", v.Attempts, want)
			}
			if v.State != "done" && v.Summary != "" {
				t.Errorf("a summary was stored: %q", v.Summary)
			}
		})
	}
}

func TestJob_AtMostTwoRunsPerRow(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	f.agent.set(func(c *call) { c.send(usage(700)) })
	f.runToRelease(id)
	f.clock.advance(5*time.Minute + time.Second)
	v := f.runTo(id, "failed")
	f.want(v, "failed", "agent_unavailable")
	f.clock.advance(time.Hour)
	f.ticks(f.runner)
	f.settled("failed 700", "failed 700")
	if f.agent.count() != 2 || v.PrevRunID == nil || v.RunID == nil || *v.PrevRunID == *v.RunID {
		t.Errorf("%d agent calls, runs %v and %v; want two", f.agent.count(), v.PrevRunID, v.RunID)
	}
}

// crashed leaves a review run as a process that died in the middle of its paid run would.
func (f *fixture) crashed(id int64, cost int64) uuid.UUID {
	f.t.Helper()
	ctx := context.Background()
	v, claim, err := f.st.ClaimReviewRun(ctx, f.clock.now(), 6*time.Minute, f.clock.now())
	if err != nil || v.ID != id {
		f.t.Fatalf("claim = %d, %v", v.ID, err)
	}
	run := store.AgentRun{ID: uuid.New(), Role: "reviewer", UserID: &f.identity.ID, Provider: testProvider, Model: testModel, ReservedMicroUSD: testReservation}
	if err := f.st.ReserveAgentRun(ctx, run, 10*time.Minute, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.StartReviewRun(ctx, claim, store.ReviewStart{RunID: run.ID, Provider: testProvider, Model: testModel, Range: "24h"}, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	if _, _, err := f.runner.Driver.Auth.MintAgentRunToken(ctx, run.ID, true); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.RecordAgentRunUsage(ctx, run.ID, 420, 42, cost, f.clock.now()); err != nil {
		f.t.Fatal(err)
	}
	return run.ID
}

func TestJob_ReclaimSettlesThePreviousRunFirst(t *testing.T) {
	const newModel = "claude-opus-4-5-20251101"
	t.Run("before the next run", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		old := f.crashed(id, 3300)
		f.settings(func(s *store.AgentSettings) {
			s.RoleModels["reviewer"] = store.AgentModelRef{Provider: testProvider, Model: newModel}
		})
		f.agent.models = append(f.agent.models, newModel)
		var atCall []runRow
		f.agent.set(func(c *call) { atCall = f.runs(); c.send(goodRun("Fine.")...) })
		f.clock.advance(6*time.Minute + time.Second)
		v := f.runTo(id, "done")
		if len(atCall) != 2 || atCall[0].id != old || atCall[0].state != "failed" || atCall[0].cost != 3300 || atCall[0].tokens != 0 || atCall[0].audits != 1 {
			t.Fatalf("when the agent was called again: %+v", atCall)
		}
		f.settled("failed 3300", "done 4200")
		first := f.audit()[0]
		if first.Target != old.String() || first.ActorUsername != f.identity.Username || first.Details["outcome"] != "interrupted" ||
			first.Details["role"] != "reviewer" || first.Details["model"] != testModel || first.Details["review_run_id"] != float64(id) {
			t.Errorf("audit of the crashed run = %+v", first)
		}
		if v.PrevRunID == nil || *v.PrevRunID != old || v.Model != newModel {
			t.Errorf("review run = %+v", v)
		}
	})
	t.Run("when nothing is started", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		f.crashed(id, 3300)
		f.grant()
		f.clock.advance(6*time.Minute + time.Second)
		f.want(f.runTo(id, "skipped"), "skipped", "not_visible")
		f.settled("failed 3300")
	})
	// While the previous run cannot be settled no second one is made beside it, and no attempt is used.
	t.Run("not when it cannot be settled", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		old := f.crashed(id, 3300)
		f.exec(`create function no_settle() returns trigger language plpgsql as $$ begin raise exception 'not now'; end $$`)
		f.exec(`create trigger no_settle before update on agent_runs for each row execute function no_settle()`)
		f.clock.advance(6*time.Minute + time.Second)
		before := f.row(id).ClaimSeq
		stop := f.start(f.runner)
		f.waitLong("the claim to be given back", func() bool {
			v := f.row(id)
			return v.State == "requested" && v.ClaimSeq > before || f.agent.count() > 0
		})
		stop()
		if runs := f.runs(); len(runs) != 1 || runs[0].id != old || f.agent.count() != 0 || f.row(id).Attempts != 1 {
			t.Errorf("runs %+v, %d agent calls, attempts %d", runs, f.agent.count(), f.row(id).Attempts)
		}
		f.exec(`drop trigger no_settle on agent_runs`)
		f.clock.advance(6 * time.Minute)
		f.want(f.runTo(id, "done"), "done", "")
		f.settled("failed 3300", "done 4200")
	})
}

func TestJob_DailyCapSkips(t *testing.T) {
	f := newFixture(t)
	f.runner.PerDay = 1
	old := f.due("Yesterday's")
	f.exec(`update agent_review_runs set state = 'done', started_at = $2 where id = $1`, old, f.clock.now().Add(-24*time.Hour-time.Second))
	first := f.due("First")
	f.want(f.runTo(first, "done"), "done", "")
	f.clock.advance(time.Minute)
	second := f.due("Second")
	f.want(f.runTo(second, "skipped"), "skipped", "rate_limited")
	f.settled("done 4200")

	// A row whose run was started once is under the cap already.
	f.runner.PerDay = 2
	third := f.due("Third")
	f.crashed(third, 100)
	f.clock.advance(6*time.Minute + time.Second)
	f.want(f.runTo(third, "done"), "done", "")
}

func TestJob_OneReviewAtATimeAcrossTwoRunners(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.agent.set(func(c *call) { <-release; c.send(goodRun("Fine.")...) })
	var ids []int64
	for i := range 4 {
		ids = append(ids, f.due(fmt.Sprintf("Review %d", i)))
	}
	second := f.newRunner(f.agent)
	stop, stopSecond := f.start(f.runner), f.start(second)
	f.wait("one run", func() bool { return f.agent.count() == 1 })
	for range 5 {
		f.runner.Kick()
		second.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	if n := f.agent.count(); n != 1 {
		t.Errorf("%d runs at once, want 1", n)
	}
	close(release)
	f.wait("all of them", func() bool {
		return !slices.ContainsFunc(ids, func(id int64) bool { return f.row(id).State != "done" }) && !f.anyRunning()
	})
	stop()
	stopSecond()
	if f.agent.most != 1 || f.agent.count() != 4 {
		t.Errorf("at most %d at once in %d calls; want 1 in 4", f.agent.most, f.agent.count())
	}
}

func TestJob_TheClaimCoversTheWholeRun(t *testing.T) {
	for timeout, want := range map[time.Duration]time.Duration{time.Minute: 6 * time.Minute, 20 * time.Minute: 21 * time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			f := newFixture(t)
			f.agent.runTimeout = timeout
			id := f.due("Daily spend")
			var until *time.Time
			f.agent.set(func(c *call) { until = f.row(id).ClaimedUntil; c.send(goodRun("Fine.")...) })
			f.runTo(id, "done")
			if until == nil || !until.Equal(f.clock.now().Add(want)) {
				t.Errorf("claimed until %v, want in %v", until, want)
			}
		})
	}
}

// ---- yielding -------------------------------------------------------------------------------------------------------

func TestJob_WaitsWhileAnInvestigationIsWaiting(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	waiting := f.investigation(time.Minute)
	f.ticks(f.runner)
	if v := f.row(id); v.State != "requested" || v.ClaimSeq != 0 {
		t.Fatalf("with an investigation waiting: %s, claims %d", v.State, v.ClaimSeq)
	}
	f.exec(`update agent_investigations set state = 'running', claimed_until = now() where id = $1`, waiting)
	f.want(f.runTo(id, "done"), "done", "")

	// One that waits longer than investigations may wait holds no review up.
	f.clock.advance(time.Minute)
	f.investigation(15*time.Minute + time.Second)
	f.want(f.runTo(f.due("Another"), "done"), "done", "")
}

func TestJob_AgentBusyDoesNotUseAnAttempt(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	busy := func(c *call) { c.w.WriteHeader(http.StatusTooManyRequests) }
	f.agent.set(busy)
	for range 3 {
		v := f.runToRelease(id)
		if v.Attempts != 0 || v.StartedAt != nil || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(time.Minute)) {
			t.Fatalf("after a busy agent: %+v", v)
		}
		f.clock.advance(time.Minute + time.Second)
	}
	f.agent.set(func(c *call) { c.send(goodRun("Fine.")...) })
	if v := f.runTo(id, "done"); v.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", v.Attempts)
	}
	f.settled("failed 0", "failed 0", "failed 0", "done 4200")

	// The wait ends with the late limit: before the first run as missed, after it as a failure.
	f.agent.set(busy)
	late := f.due("Never run")
	f.runToRelease(late)
	f.clock.at = dueAt.Add(6*time.Hour + time.Second)
	f.want(f.runTo(late, "skipped"), "skipped", "missed")
	f.clock.at = testNow.Add(time.Hour)
	second := f.due("Run once")
	f.crashed(second, 100)
	f.clock.advance(6*time.Minute + time.Second)
	if v := f.runToRelease(second); v.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", v.Attempts)
	}
	f.clock.at = dueAt.Add(6*time.Hour + time.Second)
	f.want(f.runTo(second, "failed"), "failed", "agent_unavailable")
}

// An agent older than the role, than the type or without the model waits, without an attempt, until the late limit.
func TestJob_OldAgentWithoutTheRoleWaitsThenIsMissed(t *testing.T) {
	for name, set := range map[string]func(a *fakeAgent){
		"no roles listed":   func(a *fakeAgent) { a.roles = nil },
		"role not offered":  func(a *fakeAgent) { a.roles = []string{"chat", "investigator", "test"} },
		"no types listed":   func(a *fakeAgent) { a.reviewTypes = nil },
		"type not offered":  func(a *fakeAgent) { a.reviewTypes = []string{"security"} },
		"no catalogue":      func(a *fakeAgent) { a.catalogueStatus = http.StatusServiceUnavailable },
		"model not offered": func(a *fakeAgent) { a.models = []string{"claude-haiku-4-5-20251001"} },
		"no price":          func(a *fakeAgent) { a.outputPrice = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			set(f.agent)
			id := f.due("Daily spend")
			for range 2 {
				v := f.runToRelease(id)
				if v.Attempts != 0 || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(5*time.Minute)) {
					t.Fatalf("after a look at the agent: %+v", v)
				}
				f.clock.advance(5*time.Minute + time.Second)
			}
			f.clock.at = dueAt.Add(6*time.Hour + time.Second)
			f.want(f.runTo(id, "skipped"), "skipped", "missed")
			f.unspent()
		})
	}
	// A row that had its first run ends too.
	t.Run("after a first run", func(t *testing.T) {
		f := newFixture(t)
		id := f.due("Daily spend")
		f.crashed(id, 100)
		f.agent.catalogueStatus = http.StatusServiceUnavailable
		f.clock.at = dueAt.Add(6*time.Hour + time.Second)
		f.want(f.runTo(id, "failed"), "failed", "agent_unavailable")
		f.settled("failed 100")
	})
}

func TestJob_AgentDownRetriesOnceThenFails(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	f.agent.set(func(c *call) { c.w.WriteHeader(http.StatusBadGateway) })
	v := f.runToRelease(id)
	if v.Attempts != 1 || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(5*time.Minute)) {
		t.Fatalf("after the first try: attempts %d, retry %v", v.Attempts, v.ClaimedUntil)
	}
	f.clock.advance(5*time.Minute - time.Second)
	f.ticks(f.runner)
	if f.row(id).ClaimSeq != v.ClaimSeq {
		t.Fatal("claimed again before the retry time")
	}
	f.clock.advance(2 * time.Second)
	f.want(f.runTo(id, "failed"), "failed", "agent_unavailable")
	f.settled("failed 0", "failed 0")
}

func TestJob_NoModelConfiguredSkips(t *testing.T) {
	f := newFixture(t)
	f.settings(func(s *store.AgentSettings) { delete(s.RoleModels, "reviewer") })
	f.want(f.runTo(f.due("Daily spend"), "skipped"), "skipped", "not_configured")
	f.unspent()
}

// A type this code does not know, as a later release would leave it, is never run.
func TestJob_UnknownTypeIsNeverRun(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	f.exec(`update agent_review_runs set review_type = 'custom' where id = $1`, id)
	f.agent.reviewTypes = append(f.agent.reviewTypes, "custom")
	f.want(f.runTo(id, "skipped"), "skipped", "not_configured")
	f.unspent()
}

// What fails before the agent is called costs no attempt: the row waits, within the late limit.
func TestJob_ASetUpThatFailsCostsNoAttempt(t *testing.T) {
	for name, c := range map[string]struct {
		break_, mend string
		runs         int
	}{
		"settings": {break_: `alter table agent_settings rename to agent_settings_gone`, mend: `alter table agent_settings_gone rename to agent_settings`},
		"services": {break_: `alter table services rename to services_gone`, mend: `alter table services_gone rename to services`},
		"start":    {break_: `alter table agent_review_runs add constraint no_run check (run_id is null)`, mend: `alter table agent_review_runs drop constraint no_run`, runs: 1},
		"mint":     {break_: `alter table api_tokens add constraint no_token check (kind <> 'run')`, mend: `alter table api_tokens drop constraint no_token`, runs: 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
			f.exec(c.break_)
			v := f.runToRelease(id)
			f.exec(c.mend)
			if v.Attempts != 0 || v.StartedAt != nil || f.agent.count() != 0 || len(f.runs()) != c.runs {
				t.Errorf("attempts %d, started %v, %d agent calls, runs %+v", v.Attempts, v.StartedAt, f.agent.count(), f.runs())
			}
			if v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(5*time.Minute)) {
				t.Errorf("retry at %v", v.ClaimedUntil)
			}
			f.clock.advance(5*time.Minute + time.Second)
			f.want(f.runTo(id, "done"), "done", "")
		})
	}
}

// A start that the store refuses for its text would be refused again.
func TestJob_AStartRefusedForItsTextFails(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("m", 201)
	f.agent.models = []string{long}
	f.settings(func(s *store.AgentSettings) {
		s.RoleModels["reviewer"] = store.AgentModelRef{Provider: testProvider, Model: long}
	})
	id := f.due("Daily spend")
	f.want(f.runTo(id, "failed"), "failed", "error")
	f.settled("failed 0")
	if f.agent.count() != 0 {
		t.Errorf("%d agent calls", f.agent.count())
	}
}

// ---- scope ----------------------------------------------------------------------------------------------------------

func TestJob_NothingVisibleSkipsWithoutARun(t *testing.T) {
	for name, prepare := range map[string]func(f *fixture) []store.NotificationSelector{
		"no grant": func(f *fixture) []store.NotificationSelector { f.grant(); return nil },
		"tag moved": func(f *fixture) []store.NotificationSelector {
			f.tagService(checkoutKey, f.tag("finance"))
			return []store.NotificationSelector{{Kind: "cluster", AccountID: checkoutKey.AccountID, Region: checkoutKey.Region, Cluster: checkoutKey.Cluster}}
		},
		"nothing there": func(*fixture) []store.NotificationSelector {
			return []store.NotificationSelector{{Kind: "account", AccountID: "999999999999"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			scope := prepare(f)
			id := f.due("Daily spend", func(r *store.Review) { r.Scope = scope })
			f.want(f.runTo(id, "skipped"), "skipped", "not_visible")
			f.unspent()
		})
	}
}

// The covered tags are every tag the identity may View, whatever the scope (P3-R1).
func TestJob_CoveredTagsAndCountAreStored(t *testing.T) {
	f := newFixture(t)
	payments, unused, hidden := f.tag("payments"), f.tag("unused"), f.tag("finance")
	f.grant(f.dev, payments, unused)
	f.tagService(checkoutKey, payments)
	f.services(batchKey)
	f.tagService(batchKey, hidden)
	id := f.due("Daily spend", func(r *store.Review) { r.Scope = []store.NotificationSelector{{Kind: "harbormaster", TagID: payments}} })
	var atCall store.ReviewRun
	f.agent.set(func(c *call) { atCall = f.row(id); c.send(goodRun("Fine.")...) })
	v := f.runTo(id, "done")
	want := []int64{int64(f.dev), int64(payments), int64(unused)}
	slices.Sort(want)
	for name, row := range map[string]store.ReviewRun{"when the agent was called": atCall, "when done": v} {
		got := slices.Sorted(slices.Values(row.CoveredTags))
		if !slices.Equal(got, want) || row.ServicesTotal != 1 || row.ScopeLabel != "Tag: payments" || row.Range != "24h" {
			t.Errorf("%s: tags %v (want %v), services %d, label %q, range %q", name, got, want, row.ServicesTotal, row.ScopeLabel, row.Range)
		}
	}
}

// ---- lifecycle ------------------------------------------------------------------------------------------------------

func TestJob_ReviewDisabledWhileWaitingIsSkipped(t *testing.T) {
	for name, c := range map[string]struct {
		sql   string
		first bool // only before the first run
	}{
		"disabled": {sql: `update agent_reviews set enabled = false`, first: true},
		"deleted":  {sql: `delete from agent_reviews`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
			f.exec(c.sql)
			f.want(f.runTo(id, "skipped"), "skipped", "disabled")
			f.unspent()
		})
		t.Run(name+" after a first run", func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
			f.crashed(id, 100)
			f.exec(c.sql)
			f.clock.advance(6*time.Minute + time.Second)
			if c.first {
				f.want(f.runTo(id, "done"), "done", "")
				return
			}
			f.want(f.runTo(id, "skipped"), "skipped", "disabled") // its scope is gone with it
			f.settled("failed 100")
		})
	}
}

func TestJob_ReviewDeletedMidRunStillStoresTheResult(t *testing.T) {
	f := newFixture(t)
	rv := f.due("Daily spend")
	f.agent.set(func(c *call) {
		if err := f.st.DeleteReview(context.Background(), *f.row(rv).ReviewID); err != nil {
			t.Error(err)
		}
		c.send(goodRun("Fine.")...)
	})
	v := f.runTo(rv, "done")
	if v.Summary != "Fine." || v.ReviewID != nil || v.ReviewName != "Daily spend" {
		t.Errorf("review run = %+v", v)
	}
	f.settled("done 4200")
}

func TestJob_ManualRunOfADisabledReviewRuns(t *testing.T) {
	f := newFixture(t)
	rv := f.review("Monthly spend", func(r *store.Review) { r.Enabled, r.Cadence, r.MonthDay = false, "monthly", 3 })
	run, err := f.st.RequestManualReviewRun(context.Background(), rv.ID, "Run on 25 Nov 2026, 07:45 UTC", f.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	f.want(f.runTo(run.ID, "done"), "done", "")
	if got := f.agent.call(0).Task.Review; got.Period != "Run on 25 Nov 2026, 07:45 UTC" || got.Range != "30d" {
		t.Errorf("review = %+v", got)
	}
}

func TestJob_CoordinatorChangeMidRunRetriesOnce(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
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
	if v := f.row(id); v.State != "requested" || v.Attempts != 1 {
		t.Fatalf("after the first holder: %s, attempts %d", v.State, v.Attempts)
	}
	f.clock.advance(time.Second) // the runs are listed by their start
	v := f.runnerTo(f.newRunner(f.agent), id, "done")
	if v.Attempts != 2 || v.Summary != "Second holder." {
		t.Errorf("review run = %+v", v)
	}
	f.settled("failed 600", "done 4200")
}

func TestJob_PausedHolderCannotOverwriteTheResult(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
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
	f.wait("the new holder to finish", func() bool { return f.row(id).State == "done" })
	stopSecond()
	kicks := f.kicks.Load()
	close(resume)
	f.wait("the paused holder to end", func() bool { return strings.Count(f.logs.String(), `"outcome":"done"`) >= 2 })
	stop()
	if v := f.row(id); v.Summary != "From the new holder." || v.State != "done" || f.kicks.Load() != kicks {
		t.Errorf("review run = %s %q, %d kicks after %d", v.State, v.Summary, f.kicks.Load(), kicks)
	}
	f.settled("failed 600", "done 4200")
}

func TestJob_LeaseNotConfirmedStartsNothing(t *testing.T) {
	// The guard fails from its nth check on: whichever check finds out, nothing is paid and no attempt is used.
	for n := 1; n <= 3; n++ {
		t.Run(fmt.Sprint("from check ", n), func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
			fresh := f.review("Not recorded without the lease")
			f.runner.Interval = time.Hour // no look at the schedules comes between the job's checks
			var checks atomic.Int64
			f.runner.Guard = func(context.Context) error {
				if checks.Add(1) >= int64(n) {
					return errNoLease
				}
				return nil
			}
			stop := f.start(f.runner)
			f.wait("the guard to refuse", func() bool {
				if checks.Load() >= int64(n) {
					f.runner.Kick()
				}
				return checks.Load() >= int64(n)+2
			})
			stop()
			v := f.row(id)
			if v.State != "requested" || v.Attempts != 0 || v.StartedAt != nil {
				t.Errorf("review run = %s, attempts %d, started %v", v.State, v.Attempts, v.StartedAt)
			}
			if runs := f.reviewRuns(fresh.ID); (len(runs) > 0) != (n > 1) {
				t.Errorf("recorded %+v with the guard refusing from check %d", runs, n)
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

// The guard that counts is the one of the term the run was claimed in, not one the shared driver was built with.
func TestJob_TheDriverIsCopiedForEachTerm(t *testing.T) {
	f := newFixture(t)
	f.runner.Driver.Guard = func(context.Context) error { return errNoLease }
	f.want(f.runTo(f.due("Daily spend"), "done"), "done", "")
	if f.runner.Driver.Runs != nil || f.runner.Driver.Log != nil {
		t.Error("the shared driver was changed by a run")
	}
}

func TestJob_StoppedDuringSetUpGivesTheClaimBack(t *testing.T) {
	for name, stopNow := range map[string]func(f *fixture, check int64) bool{
		"before the reservation": func(_ *fixture, check int64) bool { return check == 2 },
		"after the reservation":  func(f *fixture, _ int64) bool { return len(f.runs()) > 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
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
			done := make(chan struct{})
			go func() { defer close(done); f.runner.Run(ctx) }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the job was never stopped by its own checks")
			}
			if reserved := len(f.runs()) > 0; reserved != (name == "after the reservation") {
				t.Errorf("a run was reserved: %v", reserved)
			}
			if v := f.row(id); v.State != "requested" || v.Attempts != 0 || v.ClaimSeq != 1 {
				t.Errorf("review run = %s, attempts %d, claims %d", v.State, v.Attempts, v.ClaimSeq)
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

// Another holder took the row while the run was being reserved: the run is ended and the row left alone.
func TestJob_ClaimLostWhileReserving(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	var seq atomic.Int64
	f.runner.Guard = func(context.Context) error {
		if len(f.runs()) == 1 && seq.Load() == 0 {
			f.exec(`update agent_review_runs set claim_seq = claim_seq + 1, claimed_until = $2 where id = $1`, id, f.clock.now().Add(time.Hour))
			seq.Store(f.row(id).ClaimSeq)
		}
		return nil
	}
	stop := f.start(f.runner)
	f.wait("the run to be ended", func() bool { return seq.Load() > 0 && len(f.audit()) == 1 && !f.anyRunning() })
	stop()
	if v := f.row(id); v.State != "running" || v.ClaimSeq != seq.Load() || v.RunID != nil {
		t.Errorf("review run = %s, claim %d, run %v; want the other holder's", v.State, v.ClaimSeq, v.RunID)
	}
	f.settled("failed 0")
	if f.agent.count() != 0 {
		t.Errorf("%d agent calls", f.agent.count())
	}
}

func TestJob_DoneKicksNotify(t *testing.T) {
	f := newFixture(t)
	f.runTo(f.due("Daily spend"), "done")
	if f.kicks.Load() != 1 {
		t.Errorf("%d kicks after a done review, want 1", f.kicks.Load())
	}
	f.agent.set(func(c *call) { c.send(agentError("no_result")) })
	f.clock.advance(time.Second)
	f.runTo(f.due("Other"), "failed")
	if f.kicks.Load() != 1 {
		t.Errorf("%d kicks after a failed one, want still 1", f.kicks.Load())
	}
}

// One write that fails does not cost the paid result.
func TestJob_TheResultIsStoredAfterOneBadWrite(t *testing.T) {
	f := newFixture(t)
	f.exec(`create sequence bad_writes`)
	f.exec(`create function bad_write() returns trigger language plpgsql as $$ begin
		if new.state = 'done' and nextval('bad_writes') = 1 then raise exception 'bad write'; end if; return new; end $$`)
	f.exec(`create trigger bad_write before update on agent_review_runs for each row execute function bad_write()`)
	f.want(f.runTo(f.due("Daily spend"), "done"), "done", "")
	if rows := f.audit(); len(rows) != 1 || rows[0].Details["saved"] != true || f.kicks.Load() != 1 {
		t.Errorf("audit %+v, %d kicks", rows, f.kicks.Load())
	}
}

// panicIn makes the runner's clock panic once, when the function with that suffix asks it for the time.
func (f *fixture) panicIn(suffix string) (fired *atomic.Bool) {
	fired = &atomic.Bool{}
	f.runner.Now = func() time.Time {
		pcs := make([]uintptr, 16)
		frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
		for fr, more := frames.Next(); ; fr, more = frames.Next() {
			if strings.HasSuffix(fr.Function, suffix) && fired.CompareAndSwap(false, true) {
				panic("PANIC-SENTINEL")
			}
			if !more {
				break
			}
		}
		return f.clock.now()
	}
	return fired
}

// Whatever panics once a run was reserved, it is settled once and the row ends: it is not left running for a second
// paid run. A panic in Save is one more attempt's to make good; one in the mapping of the outcome ends as failed.
func TestJob_APanicAfterTheRunEndsTheRowFailed(t *testing.T) {
	for name, c := range map[string]struct {
		in     string
		script func(c *call)
		run    string
		saved  bool
	}{
		"in Save":     {in: "(*job).settle", script: func(c *call) { c.send(goodRun("Fine.")...) }, run: "done 4200"},
		"in Conclude": {in: "(*job).giveBack", script: func(c *call) { c.w.WriteHeader(http.StatusTooManyRequests) }, run: "failed 0", saved: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.due("Daily spend")
			if c.in == "(*job).giveBack" { // it looks at the clock for a row that had a run before
				f.exec(`update agent_review_runs set started_at = $2 where id = $1`, id, f.clock.now())
			}
			fired := f.panicIn(c.in)
			f.agent.set(c.script)
			f.want(f.runTo(id, "failed"), "failed", "error")
			f.clock.advance(time.Hour)
			f.ticks(f.runner)
			f.settled(c.run)
			rows := f.audit()
			if f.agent.count() != 1 || !fired.Load() || len(rows) != 1 || rows[0].Details["saved"] != c.saved {
				t.Errorf("%d agent calls, panicked %v, audit %+v; want one call and saved %v", f.agent.count(), fired.Load(), rows, c.saved)
			}
			if f.kicks.Load() != 0 || strings.Contains(f.logs.String(), "PANIC-SENTINEL") {
				t.Errorf("%d kicks, or the panic's value in the log", f.kicks.Load())
			}
		})
	}
}

// A save that fails altogether still ends the row, and a conflict is left to the holder the row is with.
func TestJob_AnUnsavedRunEndsTheRowFailed(t *testing.T) {
	f := newFixture(t)
	f.exec(`create sequence bad_writes`)
	f.exec(`create function bad_write() returns trigger language plpgsql as $$ begin
		if new.state in ('done', 'failed') and nextval('bad_writes') <= 3 then raise exception 'bad write'; end if; return new; end $$`)
	f.exec(`create trigger bad_write before update on agent_review_runs for each row execute function bad_write()`)
	id := f.due("Daily spend")
	f.want(f.runTo(id, "failed"), "failed", "error")
	f.settled("done 4200")
	if f.agent.count() != 1 || f.kicks.Load() != 0 {
		t.Errorf("%d agent calls, %d kicks", f.agent.count(), f.kicks.Load())
	}
}

// ---- labels ---------------------------------------------------------------------------------------------------------

// A logs item is dropped although the identity holds Logs, what the identity may View is asked when the result arrives,
// and a service that does not exist is seen by nobody.
func TestJob_OnlyViewEvidenceTheIdentityMayReadNowIsStored(t *testing.T) {
	f := newFixture(t)
	finance := f.tag("finance")
	id := f.due("Daily spend")
	f.agent.set(func(c *call) {
		f.tagService(ledgerKey, finance) // out of sight during the run
		gone, moved, spelled := viewItem("x"), viewItem("y"), viewItem("desired 2")
		gone.ServiceID = model.ServiceKey{ClusterKey: checkoutKey.ClusterKey, Service: "invented-api"}.ID()
		moved.ServiceID = ledgerKey.ID()
		spelled.ServiceID = checkoutKey.ID()[:8] + "\n" + checkoutKey.ID()[8:] // decodes to the service, and is stored as it
		c.send(usage(700), resultEvent(agentresult.Result{Summary: "Fine.",
			Evidence: []agentresult.Evidence{logsItem("refused"), spelled, gone, moved}}), doneEvent)
	})
	v := f.runTo(id, "done")
	ev, err := f.st.ListReviewRunEvidence(context.Background(), id)
	if err != nil || len(ev) != 1 || ev[0].Action != "view" || ev[0].ServiceName != checkoutKey.Service || ev[0].ClusterName != checkoutKey.Cluster || v.EvidenceDropped != 3 {
		t.Errorf("evidence = %+v, dropped %d, %v", ev, v.EvidenceDropped, err)
	}
}

func TestJob_AReplacedSummaryIsTheReviewSentence(t *testing.T) {
	f := newFixture(t)
	id := f.due("Daily spend")
	f.agent.set(func(c *call) {
		c.send(usage(700), resultEvent(agentresult.Result{Summary: "It uses AKIA" + "IOSFODNN7EXAMPLE to sign."}), doneEvent)
	})
	if v := f.runTo(id, "done"); v.Summary != agentresult.ReplacementReviewSummary || !v.SummaryReplaced {
		t.Errorf("summary = %q, replaced %v", v.Summary, v.SummaryReplaced)
	}
}

// The identity is read-only whatever it may hold, shown on a subject that holds all.
func TestSight_IsReadOnly(t *testing.T) {
	f := newFixture(t)
	see, err := sightOf(context.Background(), f.st, f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !see.can(checkoutKey, authz.View) || see.can(checkoutKey, authz.Operate) || see.can(checkoutKey, authz.Deploy) {
		t.Error("what the job may ask for is not reading alone")
	}
}

// ---- content --------------------------------------------------------------------------------------------------------

var sentinels = []string{"SUMMARY-SENTINEL", "NOTE-SENTINEL", "EXCERPT-SENTINEL", "NAME-SENTINEL", "SCOPE-SENTINEL", "MESSAGE-SENTINEL", "PANIC-SENTINEL", "DELTA-SENTINEL", "CODE_SENTINEL"}

func sentinelRun(end ...map[string]any) []map[string]any {
	item := viewItem("EXCERPT-SENTINEL refused")
	item.Note = "NOTE-SENTINEL"
	return append([]map[string]any{usage(700), {"type": "text", "delta": "DELTA-SENTINEL"}, toolStart("EXCERPT-SENTINEL"),
		resultEvent(agentresult.Result{Summary: "SUMMARY-SENTINEL", Evidence: []agentresult.Evidence{item}})}, end...)
}

func (f *fixture) runSentinels() {
	f.t.Helper()
	tag := f.tag("SCOPE-SENTINEL")
	f.grant(f.dev, tag)
	f.tagService(checkoutKey, tag)
	n := 0
	next := func() int64 {
		n++
		f.clock.advance(time.Second)
		return f.due(fmt.Sprintf("NAME-SENTINEL %d", n), func(r *store.Review) {
			r.Scope = []store.NotificationSelector{{Kind: "harbormaster", TagID: tag}}
		})
	}
	f.agent.set(func(c *call) { c.send(sentinelRun(doneEvent)...) })
	if v := f.runTo(next(), "done"); v.Summary != "SUMMARY-SENTINEL" || v.ScopeLabel != "Tag: SCOPE-SENTINEL" {
		f.t.Errorf("review run = %+v", v)
	}
	f.agent.set(func(c *call) { c.send(sentinelRun(agentError("CODE_SENTINEL"))...) })
	f.runTo(next(), "failed")
	f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = 1 })
	f.runTo(next(), "skipped")
	f.settings(func(*store.AgentSettings) {})
	f.runner.Driver.OnEvent = func(agentclient.Event) { panic("PANIC-SENTINEL") }
	f.runTo(next(), "failed")
}

func TestJob_AuditRowHasIdsAndNoContent(t *testing.T) {
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
		for _, k := range []string{"run_id", "review_id", "review_run_id", "review_type", "role", "provider", "model", "tools", "input_tokens", "output_tokens", "cost_micro_usd", "outcome"} {
			if _, ok := e.Details[k]; !ok {
				t.Errorf("audit row lacks %s: %s", k, raw)
			}
		}
		if e.Details["review_type"] != "cost" || e.Details["role"] != "reviewer" || e.Target != e.Details["run_id"] {
			t.Errorf("audit row = %s", raw)
		}
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
		t.Fatal("nothing was logged")
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if (strings.Contains(line, "agentjob:") || strings.Contains(line, "agent run")) && !strings.Contains(line, `"review_run_id":`) {
			t.Errorf("a line of the driver has no review run id: %s", line)
		}
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
			f.runTo(f.due("Daily spend"), "done")
			if want := !slices.Contains(unredacted, testProvider); redact != want || scope != "read" || user != f.identity.Username {
				t.Errorf("token: redact %v (want %v), scope %s, user %s", redact, want, scope, user)
			}
		})
	}
}

// ---- the period text ------------------------------------------------------------------------------------------------

func TestPeriodText_ComesFromTheKey(t *testing.T) {
	at := time.Date(2026, 11, 25, 7, 45, 0, 0, time.UTC)
	for key, want := range map[string]string{
		"d:2026-11-05": "5 Nov 2026", "w:2026-W48": "Week 48, 2026", "w:2027-W01": "Week 1, 2027", "m:2026-11": "November 2026",
		"x:5f0c3c8e-8a55-4a4c-9f1d-2d0f6f1f6a11": "Run on 25 Nov 2026, 07:45 UTC",
		"":                                       "25 Nov 2026", "w:soon": "25 Nov 2026", "d:<b>": "25 Nov 2026", "q:2026-4": "25 Nov 2026", "w:2026-W99": "25 Nov 2026", "w:2026-W48x": "25 Nov 2026",
	} {
		if got := periodText(store.ReviewRun{PeriodKey: key, PeriodLabel: "IGNORE <this>", ScheduledFor: at}); got != want {
			t.Errorf("%q: %q, want %q", key, got, want)
		}
	}
}
