package investigate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentjob"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ---- the hourly cap and the maximum wait, for a request no run was started for ------------------------------------

// An outage uses up the first attempts of many requests without starting one; the cap still holds afterwards.
func TestJob_HourlyCapHoldsAfterAnOutage(t *testing.T) {
	f := newFixture(t)
	f.runner.PerHour = 1
	f.agent.catalogueStatus = http.StatusServiceUnavailable
	var ids []int64
	for range 6 {
		ids = append(ids, f.request("checkout-api"))
	}
	states := func(state string, attempts int) (n int) {
		for _, id := range ids {
			if v := f.inv(id); v.State == state && v.Attempts == attempts {
				n++
			}
		}
		return n
	}
	stop := f.start(f.runner)
	f.wait("the outage to use the first attempts", func() bool { return states("requested", 1) == 6 })
	stop()
	f.agent.mu.Lock()
	f.agent.catalogueStatus = http.StatusOK
	f.agent.mu.Unlock()
	f.clock.advance(61 * time.Second)
	stop = f.start(f.newRunner1(1))
	f.wait("every request to end", func() bool { return states("requested", 1)+states("running", 2) == 0 && !f.anyRunning() })
	stop()
	if f.agent.count() != 1 || states("done", 2) != 1 || states("skipped", 2) != 5 {
		t.Errorf("%d paid runs, %d done, %d skipped; want 1, 1 and 5", f.agent.count(), states("done", 2), states("skipped", 2))
	}
}

// newRunner1 is newRunner with an hourly cap.
func (f *fixture) newRunner1(perHour int) *Runner {
	r := f.newRunner(f.agent)
	r.PerHour = perHour
	return r
}

func TestJob_MaximumWaitHoldsUntilARunWasStarted(t *testing.T) {
	t.Run("catalogue failed", func(t *testing.T) {
		f := newFixture(t)
		f.agent.catalogueStatus = http.StatusServiceUnavailable
		id := f.request("checkout-api")
		f.runToRelease(id)
		f.agent.catalogueStatus = http.StatusOK
		f.clock.advance(15*time.Minute + time.Second)
		f.want(f.runTo(id, "skipped"), "skipped", "expired")
		if f.agent.count() != 0 {
			t.Errorf("%d agent calls", f.agent.count())
		}
	})
	t.Run("crash before the start", func(t *testing.T) {
		f := newFixture(t)
		id := f.request("checkout-api")
		if _, _, err := f.st.ClaimInvestigation(context.Background(), f.clock.now(), 6*time.Minute); err != nil {
			t.Fatal(err)
		}
		f.clock.advance(15*time.Minute + time.Second)
		f.want(f.runTo(id, "skipped"), "skipped", "expired")
		if f.agent.count() != 0 {
			t.Errorf("%d agent calls", f.agent.count())
		}
	})
	t.Run("crash after the start", func(t *testing.T) { // the run that was started was in time and under the cap
		f := newFixture(t)
		f.runner.PerHour = 1
		id := f.request("checkout-api")
		f.crashed(id, 100)
		f.clock.advance(16 * time.Minute)
		f.want(f.runTo(id, "done"), "done", "")
	})
}

// ---- the previous run must be over before another is reserved -----------------------------------------------------

func TestJob_NoSecondRunWhileThePreviousOneIsNotSettled(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	old := f.crashed(id, 3300)
	f.clock.advance(6*time.Minute + time.Second)
	before := f.inv(id).ClaimSeq
	ctx := context.Background()
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select 1 from agent_runs where id = $1 for update`, old); err != nil {
		t.Fatal(err)
	}
	stop := f.start(f.runner)
	f.waitLong("the claim to be given back", func() bool {
		v := f.inv(id)
		return v.ClaimSeq > before && v.State == "requested" || f.agent.count() > 0
	})
	v := f.inv(id)
	if runs := f.runs(); len(runs) != 1 || runs[0].id != old || f.agent.count() != 0 || v.Attempts != 1 {
		t.Errorf("runs %+v, %d agent calls, attempts %d; want only the old run and its attempt", runs, f.agent.count(), v.Attempts)
	}
	stop()
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(2 * time.Minute)
	f.want(f.runTo(id, "done"), "done", "")
	f.settled("failed 3300", "done 4200")
}

// A run row that retention has deleted is no reason to hold the request back.
func TestJob_APreviousRunThatIsGoneDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	old := f.crashed(id, 3300)
	f.exec(`delete from api_tokens where run_id = $1`, old)
	f.exec(`delete from agent_runs where id = $1`, old)
	f.clock.advance(6*time.Minute + time.Second)
	f.want(f.runTo(id, "done"), "done", "")
}

// ---- fences -------------------------------------------------------------------------------------------------------

// unpaid asserts that the one run that was reserved was ended unpaid with the given outcome and the agent not called.
func (f *fixture) unpaid(outcome string) {
	f.t.Helper()
	if f.agent.count() != 0 {
		f.t.Errorf("%d agent calls, want none", f.agent.count())
	}
	f.settled("failed 0")
	if rows := f.audit(); len(rows) != 1 || rows[0].Details["outcome"] != outcome || rows[0].Details["saved"] != true {
		f.t.Errorf("audit = %+v, want outcome %s", rows, outcome)
	}
}

// Another holder took the request while the run was being reserved: the run is ended and the request left alone.
func TestJob_ClaimLostWhileReserving(t *testing.T) {
	f := newFixture(t)
	f.runner.Concurrent = 1
	id := f.request("checkout-api")
	var seq atomic.Int64
	f.runner.Guard = func(context.Context) error {
		if len(f.runs()) == 1 && seq.Load() == 0 {
			f.exec(`update agent_investigations set claim_seq = claim_seq + 1, claimed_until = $2 where id = $1`, id, f.clock.now().Add(time.Hour))
			seq.Store(f.inv(id).ClaimSeq)
		}
		return nil
	}
	stop := f.start(f.runner)
	f.wait("the run to be ended", func() bool { return seq.Load() > 0 && len(f.audit()) == 1 && !f.anyRunning() })
	stop()
	if v := f.inv(id); v.State != "running" || v.ClaimSeq != seq.Load() || v.RunID != nil {
		t.Errorf("investigation = %s, claim %d, run %v; want the other holder's", v.State, v.ClaimSeq, v.RunID)
	}
	f.unpaid("interrupted")
	if w := containsAny(f.logs.String(), "could not save"); w != "" {
		t.Error("a write was tried on a request that another holder has")
	}
}

// The claim lost between the start and the call of the agent.
func TestJob_ClaimLostBeforeTheRenewal(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.exec(`create function bump_claim() returns trigger language plpgsql as $$ begin
		update agent_investigations set claim_seq = claim_seq + 1, claimed_until = claimed_until + interval '1 hour' where id = ` + fmt.Sprint(id) + `; return new; end $$`)
	f.exec(`create trigger bump_claim after insert on api_tokens for each row execute function bump_claim()`)
	stop := f.start(f.runner)
	f.wait("the run to be ended", func() bool { return len(f.audit()) == 1 && !f.anyRunning() })
	stop()
	if v := f.inv(id); v.State != "running" || v.ClaimSeq != 2 {
		t.Errorf("investigation = %s, claim %d; want the other holder's", v.State, v.ClaimSeq)
	}
	f.unpaid("interrupted")
}

// A step between the reservation and the call that fails for another reason gives the attempt back once.
func TestJob_AStartThatCannotBeStoredCallsNobody(t *testing.T) {
	for name, constraint := range map[string]string{
		"start": `alter table agent_investigations add constraint no_run check (run_id is null)`,
		"mint":  `alter table api_tokens add constraint no_token check (kind <> 'run')`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.exec(constraint)
			id := f.request("checkout-api")
			if v := f.runToRelease(id); v.Attempts != 1 || v.ClaimedUntil == nil || !v.ClaimedUntil.Equal(f.clock.now().Add(time.Minute)) {
				t.Errorf("attempts %d, retry %v", v.Attempts, v.ClaimedUntil)
			}
			f.unpaid("error")
		})
	}
}

// A result that another holder was faster with is discarded once and quietly: no kick, no second write.
func TestJob_AResultOfALostClaimIsDiscarded(t *testing.T) {
	f := newFixture(t)
	id := f.request("checkout-api")
	f.agent.set(func(c *call) {
		f.exec(`update agent_investigations set claim_seq = claim_seq + 1, claimed_until = $2 where id = $1`, id, f.clock.now().Add(time.Hour))
		c.send(goodRun("Fine.")...)
	})
	stop := f.start(f.runner)
	f.wait("the run to be ended", func() bool { return len(f.audit()) == 1 && !f.anyRunning() })
	stop()
	if rows := f.audit(); rows[0].Details["saved"] != false || f.kicks.Load() != 0 {
		t.Errorf("saved %v, %d kicks; want false and none", rows[0].Details["saved"], f.kicks.Load())
	}
	if !strings.Contains(f.logs.String(), `"error":"`+store.ErrConflict.Error()+`"`) {
		t.Errorf("the discarded result was not reported as a conflict alone:\n%s", f.logs.String())
	}
}

// A service that cannot be looked up is not one that cannot be seen: nothing is decided on a failed read.
func TestJob_AFailedLookupIsNotInvisible(t *testing.T) {
	t.Run("before the run", func(t *testing.T) {
		f := newFixture(t)
		f.exec(`alter table services rename to services_gone`)
		id := f.request("checkout-api")
		v := f.runToRelease(id)
		if v.Attempts != 1 || len(f.runs()) != 0 {
			t.Errorf("attempts %d, runs %+v", v.Attempts, f.runs())
		}
		f.clock.advance(61 * time.Second)
		f.want(f.runTo(id, "failed"), "failed", "error")
	})
	t.Run("with the result", func(t *testing.T) {
		f := newFixture(t)
		id := f.request("checkout-api")
		f.agent.set(func(c *call) {
			f.exec(`alter table services rename to services_gone`)
			c.send(goodRun("Fine.")...)
		})
		f.want(f.runTo(id, "failed"), "failed", "error")
		f.settled("done 4200")
	})
}

// The identity is read-only whatever it may hold: the second, independent layer, shown on a subject that holds all.
func TestSight_IsReadOnly(t *testing.T) {
	f := newFixture(t)
	see, err := agentjob.SightOf(context.Background(), f.st, f.admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !see.Can(checkoutKey, authz.View) || !see.Can(checkoutKey, authz.Logs) || see.Can(checkoutKey, authz.Operate) || see.Can(checkoutKey, authz.Deploy) {
		t.Error("what the job may ask for is not View and Logs alone")
	}
}

// A run whose worst case is not a known, positive sum does not start.
func TestJob_AnUnknownWorstCaseIsNotStarted(t *testing.T) {
	for name, set := range map[string]func(a *fakeAgent){
		"no token cap":           func(a *fakeAgent) { a.maxRunTokens = 0 },
		"negative token cap":     func(a *fakeAgent) { a.maxRunTokens = -200_000 },
		"negative price":         func(a *fakeAgent) { a.outputPrice = -15_000_000 },
		"negative price and cap": func(a *fakeAgent) { a.outputPrice, a.maxRunTokens = -15_000_000, -200_000 },
		"free":                   func(a *fakeAgent) { a.outputPrice = 0 },
		"less than a micro":      func(a *fakeAgent) { a.outputPrice, a.maxRunTokens = 1, 200 },
		"wraps to a negative":    func(a *fakeAgent) { a.outputPrice = 1 << 46 },
		// The product is just over 2^64 and would be a reservation of 10 micro-dollars.
		"wraps to a small sum": func(a *fakeAgent) { a.outputPrice, a.maxRunTokens = 15_000_000, 1_229_782_938_248 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.settings(func(s *store.AgentSettings) { s.MonthlyBudgetMicroUSD = 1 << 60 })
			set(f.agent)
			id := f.request("checkout-api")
			f.want(f.runTo(id, "skipped"), "skipped", "not_configured")
			if f.agent.count() != 0 || len(f.runs()) != 0 {
				t.Errorf("%d agent calls, runs %+v", f.agent.count(), f.runs())
			}
		})
	}
}

// ---- storing the result -------------------------------------------------------------------------------------------

// One write that fails, or hangs, does not cost the paid result.
func TestJob_TheResultIsStoredAfterOneBadWrite(t *testing.T) {
	t.Run("an error", func(t *testing.T) {
		f := newFixture(t)
		f.exec(`create sequence bad_writes`)
		f.exec(`create function bad_write() returns trigger language plpgsql as $$ begin
			if new.state = 'done' and nextval('bad_writes') = 1 then raise exception 'bad write'; end if; return new; end $$`)
		f.exec(`create trigger bad_write before update on agent_investigations for each row execute function bad_write()`)
		id := f.request("checkout-api")
		f.want(f.runTo(id, "done"), "done", "")
		if rows := f.audit(); len(rows) != 1 || rows[0].Details["saved"] != true || f.kicks.Load() != 1 {
			t.Errorf("audit %+v, %d kicks", rows, f.kicks.Load())
		}
	})
	t.Run("a hanging write", func(t *testing.T) {
		f := newFixture(t)
		f.exec(`create sequence bad_writes`)
		f.exec(`create function bad_write() returns trigger language plpgsql as $$ begin
			if new.state = 'done' and nextval('bad_writes') = 1 then perform pg_sleep(20); end if; return new; end $$`)
		f.exec(`create trigger bad_write before update on agent_investigations for each row execute function bad_write()`)
		id := f.request("checkout-api")
		stop := f.start(f.runner)
		f.waitLong("the result", func() bool { return f.inv(id).State == "done" })
		stop()
		f.settled("done 4200")
	})
	t.Run("writes that hang for ever", func(t *testing.T) { // and the failure still has its own time
		f := newFixture(t)
		f.exec(`create function bad_write() returns trigger language plpgsql as $$ begin
			if new.state = 'done' then perform pg_sleep(30); end if; return new; end $$`)
		f.exec(`create trigger bad_write before update on agent_investigations for each row execute function bad_write()`)
		id := f.request("checkout-api")
		stop := f.start(f.runner)
		f.waitLong("the failure", func() bool { return f.inv(id).State == "failed" })
		stop()
		f.want(f.inv(id), "failed", "error")
	})
}

// ---- account entries ----------------------------------------------------------------------------------------------

func TestJob_AccountSubjects(t *testing.T) {
	other := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: checkoutKey.AccountID, Region: checkoutKey.Region, Cluster: "ledger-prod"}, Service: "ledger-batch"}
	for name, region := range map[string]string{"with a region": "us-east-1", "without a region": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.services(other)
			id := f.request("checkout-api")
			f.exec(`update agent_investigations set subject_kind = 'account', service_name = '', cluster_name = '', region = $2,
				subject_key = 'account/482109375512/' || $2::text, members = '[{"cluster": "payments-prod", "service": "checkout-api"},
				{"cluster": "ledger-prod", "service": "ledger-batch"}]' where id = $1`, id, region)
			if region == "" { // its members do not say which region they are in
				f.want(f.runTo(id, "skipped"), "skipped", "not_visible")
				if f.agent.count() != 0 || len(f.runs()) != 0 {
					t.Errorf("%d agent calls, runs %+v", f.agent.count(), f.runs())
				}
				return
			}
			f.want(f.runTo(id, "done"), "done", "")
			subject := f.agent.call(0).Task.Subject
			if want := []string{checkoutKey.ID(), other.ID()}; subject.Kind != "account" || subject.AccountID != checkoutKey.AccountID ||
				subject.Region != region || subject.ClusterID != "" || fmt.Sprint(subject.ServiceIDs) != fmt.Sprint(want) {
				t.Errorf("subject = %+v", subject)
			}
		})
	}
}
