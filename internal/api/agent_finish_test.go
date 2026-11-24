package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// holdLock takes a lock with the given statement in a transaction of its own and returns a function that gives it
// back. A finishing step that has to wait for it shows how long the step is allowed to wait.
func holdLock(t *testing.T, st *store.Store, statement string) (release func()) {
	t.Helper()
	tx, err := st.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), statement); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("%s: %v", statement, err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = tx.Rollback(context.Background()) }) }
	t.Cleanup(release)
	return release
}

// lockableRows are the rows a turn writes as it ends, and how another transaction holds them.
var lockableRows = map[string]string{
	"conversation": `select id from agent_conversations for update`,
	"run":          `select id from agent_runs for update`,
	"token":        `select id from api_tokens where kind = 'run' for update`,
	"audit":        `lock table audit_log in access exclusive mode`,
}

// runWithLock runs one turn whose script says "x", lets the test lock something, then reports usage 70 and done.
// The lock is given back after held. It returns the frames and how long the turn took from the lock on.
func runWithLock(t *testing.T, env *chatEnv, id int64, statement string, held time.Duration) (took time.Duration, frames []sseFrame) {
	t.Helper()
	entered, locked := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		once.Do(func() { close(entered) })
		select {
		case <-locked:
		case <-time.After(8 * time.Second):
		}
		ndjson(w, `{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":70}`, `{"type":"done"}`)
	})
	done := make(chan []sseFrame, 1)
	go func() { done <- readSSE(t, send(env.sam, id, "hello")) }()
	<-entered
	release := holdLock(t, env.st, statement)
	start := time.Now()
	time.AfterFunc(held, release)
	close(locked)
	select {
	case frames = <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("the turn never ended")
	}
	took = time.Since(start)
	if rest := held - took; rest > 0 {
		time.Sleep(rest + 200*time.Millisecond)
	}
	return took, frames
}

func reconcileSoon(t *testing.T, env *chatEnv, after time.Duration) {
	t.Helper()
	if _, err := env.st.ReconcileAgentRuns(t.Context(), time.Hour, time.Now().Add(after)); err != nil {
		t.Fatal(err)
	}
}

// wantEnded checks what must be true of every run once it is over: settled at its cost, no token, one audit row
// that agrees with the run row, the slot free.
func wantEnded(t *testing.T, env *chatEnv, state string, cost int64) {
	t.Helper()
	if st, c, _ := runRow(t, env.st); st != state || c != cost {
		t.Errorf("run = %s cost %d, want %s %d", st, c, state, cost)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	outcomes := auditOutcomes(t, env.st)
	if len(outcomes) != 1 {
		t.Fatalf("audit rows = %v, want exactly one", outcomes)
	}
	if (outcomes[0] == "done") != (state == "done") {
		t.Errorf("audit outcome %s for a run that is %s", outcomes[0], state)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}

// A row that somebody else holds for three seconds delays an ordinary turn and loses nothing.
func TestChat_RowLockedForThreeSecondsLosesNothing(t *testing.T) {
	for name, statement := range lockableRows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := newChatEnv(t)
			id := newConversation(t, env.sam)
			_, frames := runWithLock(t, env, id, statement, 3*time.Second)
			if last := frames[len(frames)-1]; last.Event != "done" {
				t.Errorf("last frame = %+v, want done", last)
			}
			if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 || msgs[1].Text != "x" || msgs[1].Interrupted {
				t.Errorf("messages = %+v, want the answer saved", msgs)
			}
			wantEnded(t, env, "done", 70)
		})
	}
}

// A row held for longer than any step waits: the turn still ends, and one reconcile pass does what it could not.
func TestChat_RowLockedForFourteenSecondsIsRepairedByOnePass(t *testing.T) {
	for name, statement := range lockableRows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := newChatEnv(t)
			took, frames := runWithLock(t, env, newConversation(t, env.sam), statement, 14*time.Second)
			if took > 35*time.Second {
				t.Errorf("the turn took %v", took)
			}
			wantState := "done"
			if name == "run" { // its usage could not be stored in ten seconds, so the run was stopped
				wantState = "failed"
			}
			if name == "conversation" {
				if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "internal" {
					t.Errorf("last frame = %+v, want an error event: the answer was not saved", last)
				}
			}
			reconcileSoon(t, env, 2*time.Minute)
			wantEnded(t, env, wantState, 70)
		})
	}
}

// A failed save is the browser's bad news alone: the run was done and paid for, and its row and audit say so.
func TestChat_FailedSaveIsInternalForTheBrowserAndDoneForTheRecord(t *testing.T) {
	env := newChatEnv(t)
	execSQL(t, env.st, `create function refuse_answer() returns trigger language plpgsql as $$ begin
		if new.role = 'assistant' then raise exception 'no room'; end if; return new; end $$`)
	execSQL(t, env.st, `create trigger refuse_answer before insert on agent_messages for each row execute function refuse_answer()`)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":50}`, `{"type":"done"}`)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "internal" || strings.Contains(fmt.Sprint(eventNames(frames)), "done") {
		t.Errorf("events = %v, last %+v; want an internal error and no done", eventNames(frames), last)
	}
	wantEnded(t, env, "done", 50)
	if d := auditDetails(t, env.st); d["saved"] != false {
		t.Errorf("audit = %v, want saved false", d)
	}
}

// A process that dies mid-turn leaves a running run and a live token. One pass after the stale age repairs both, and
// a turn that turns out to be alive after all adds nothing to the record.
func TestChat_AbandonedTurnIsRepairedOnce(t *testing.T) {
	stuck, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(_ context.Context, ev agentclient.Event) {
			if ev.Type == "text" {
				once.Do(func() { close(stuck) })
				<-resume
			}
		}
	})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":70}`, `{"type":"text","delta":"x"}`)
		holdUntilGone(r)
	})
	done := make(chan []sseFrame, 1)
	go func() { done <- readSSE(t, send(env.sam, newConversation(t, env.sam), "hello")) }()
	<-stuck
	reconcileSoon(t, env, 59*time.Minute)
	if state, _, _ := runRow(t, env.st); state != "running" || agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`) != 1 {
		t.Errorf("a live turn was touched before the stale age: run %s", state)
	}
	reconcileSoon(t, env, 61*time.Minute)
	if got := auditOutcomes(t, env.st); len(got) != 1 || got[0] != "failed" {
		t.Errorf("audit = %v", got)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	close(resume)
	<-done
	reconcileSoon(t, env, 3*time.Hour)
	wantEnded(t, env, "failed", 70)
}

func runState(t *testing.T, st *store.Store) string {
	t.Helper()
	var s string
	if err := st.Pool.QueryRow(t.Context(), `select state from agent_runs`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A burst of tiny deltas must arrive whole, with done, however fast the agent sends them.
func TestChat_BurstOfTinyDeltasArrivesCompleteWithDone(t *testing.T) {
	env := newChatEnv(t)
	const n = 20000
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(`{"type":"text","delta":"ab"}` + "\n")
		}
		b.WriteString(`{"type":"done"}` + "\n")
		fmt.Fprint(w, b.String())
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	var got strings.Builder
	for _, f := range frames {
		if f.Event == "text" {
			got.WriteString(f.Data["delta"].(string))
		}
	}
	if got.String() != strings.Repeat("ab", n) {
		t.Errorf("received %d characters of %d", got.Len(), 2*n)
	}
	if last := frames[len(frames)-1]; last.Event != "done" {
		t.Errorf("last frame = %+v, want done", last)
	}
}

// A browser that reads slowly but does read is never dropped, however long the rest takes: 4 MB at 800 KB/s.
func TestChat_SlowReaderGetsEverythingAndDone(t *testing.T) {
	env := newChatEnv(t, func(s *Server) { s.AgentWriteTimeout = time.Second })
	line := `{"type":"text","delta":"` + strings.Repeat("z", 1024) + `"}`
	const n = 4096
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		for i := 0; i < n; i++ {
			fmt.Fprintln(w, line)
		}
		ndjson(w, `{"type":"done"}`)
	})
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	slow := &pacedBody{ReadCloser: resp.Body, perSecond: 800 << 10}
	start := time.Now()
	frames := readSSE(t, &http.Response{Body: slow})
	total := 0
	for _, f := range frames {
		if f.Event == "text" {
			total += len(f.Data["delta"].(string))
		}
	}
	if total != n*1024 {
		t.Errorf("received %d bytes of %d", total, n*1024)
	}
	if last := frames[len(frames)-1]; last.Event != "done" {
		t.Errorf("last frame = %+v, want done", last)
	}
	if took := time.Since(start); took < 4*time.Second {
		t.Errorf("read in %v: the reader was not slow", took)
	}
}

// pacedBody reads at a steady pace.
type pacedBody struct {
	io.ReadCloser
	perSecond int
}

func (p *pacedBody) Read(b []byte) (int, error) {
	if len(b) > 16<<10 {
		b = b[:16<<10]
	}
	n, err := p.ReadCloser.Read(b)
	time.Sleep(time.Duration(n) * time.Second / time.Duration(p.perSecond))
	return n, err
}

// done that arrives with the shutdown still counts: the answer was complete.
func TestChat_DoneBeatsDrain(t *testing.T) {
	var api atomic.Pointer[Server]
	env := newChatEnv(t, func(s *Server) {
		var once sync.Once
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			if ev.Type == "done" {
				once.Do(func() {
					api.Load().BeginDrain()
					time.Sleep(100 * time.Millisecond) // let the watcher see it
				})
			}
		}
	})
	api.Store(env.api)
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if last := frames[len(frames)-1]; last.Event != "done" {
		t.Errorf("last frame = %+v, want done", last)
	}
	if st := runState(t, env.st); st != "done" {
		t.Errorf("run state = %s", st)
	}
}

// An answer that had been read in full when the shutdown began is complete, though it was still waiting to be handled.
func TestChat_DoneThatWasReadBeforeTheDrainCounts(t *testing.T) {
	for range 6 { // which of two things that are both there a select picks is random
		var api atomic.Pointer[Server]
		env := newChatEnv(t, func(s *Server) {
			s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
				if ev.Type == "text" {
					time.Sleep(100 * time.Millisecond) // done is read meanwhile, and waits
					api.Load().BeginDrain()
				}
			}
		})
		api.Store(env.api)
		frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
		if last := frames[len(frames)-1]; last.Event != "done" {
			t.Fatalf("last frame = %+v, want done", last)
		}
	}
}

// Our own code failing on one event ends the turn, but what the run costs is still read and recorded.
func TestChat_PanicOnAnEventStillCountsTheUsageAfterIt(t *testing.T) {
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(_ context.Context, ev agentclient.Event) {
			if ev.Type == "text" {
				panic("boom")
			}
		}
	})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"tool_start","tool":"get_service"}`, `{"type":"text","delta":"x"}`,
			`{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":333}`)
		holdUntilGone(r)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "agent_unavailable" {
		t.Errorf("last frame = %+v", last)
	}
	wantEnded(t, env, "failed", 333)
	if strings.Contains(env.logs.String(), "boom") {
		t.Error("the panic's value was logged")
	}
}

// Only the turn cancels the agent run: a browser that leaves is an interruption, not an agent that failed.
func TestChat_LeavingIsAlwaysAnInterruption(t *testing.T) {
	entered := make(chan struct{}, 1)
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			entered <- struct{}{}
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond) // whatever follows the request's end by itself has happened now
		}
	})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		holdUntilGone(r)
	})
	id := newConversation(t, env.sam)
	for i := range 6 { // which of two things that are both there a select picks is random
		ctx, cancel := context.WithCancel(t.Context())
		resp := chatCtxRequest(t, ctx, env.sam, id, "hello")
		<-entered
		cancel()
		resp.Body.Close()
		waitFor(t, "the audit row", func() bool { return len(auditOutcomes(t, env.st)) == i+1 })
	}
	for _, outcome := range auditOutcomes(t, env.st) {
		if outcome != "interrupted" {
			t.Errorf("outcome %s, want interrupted", outcome)
		}
	}
}

// Once the server shuts down no agent run is started, though its question was already accepted. A call that is started
// and cancelled at once seldom reaches the agent, so the test counts the calls that are started.
func TestChat_NotStartedWhileDraining(t *testing.T) {
	env := newChatEnv(t)
	var calls atomic.Int32
	handler := env.api.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(countingContext{r.Context(), &calls}))
	}))
	t.Cleanup(srv.Close)
	sam := login(t, srv, "sam", testPassword)
	readSSE(t, send(sam, newConversation(t, sam), "hello"))
	before := calls.Load()
	if before == 0 || len(env.agent.runs()) != 1 {
		t.Fatalf("%d calls counted for a run that reached the agent", before)
	}
	env.api.BeginDrain()
	frames := readSSE(t, send(sam, newConversation(t, sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "interrupted" {
		t.Errorf("last frame = %+v, want an error event interrupted", last)
	}
	if n := calls.Load() - before; n != 0 {
		t.Errorf("%d agent calls were started", n)
	}
	if n := agentCount(t, env.st, `select count(*) from agent_runs where state = 'failed' and cost_micro_usd = 0`); n != 1 {
		t.Errorf("%d runs settled at no cost, want one", n)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
}

// countingContext counts the lookups that an outgoing HTTP round trip makes before anything else.
type countingContext struct {
	context.Context
	calls *atomic.Int32
}

func (c countingContext) Value(key any) any {
	if fmt.Sprintf("%T", key) == "httptrace.clientEventContextKey" {
		c.calls.Add(1)
	}
	return c.Context.Value(key)
}

// A run that is found lost at the moment its time is up ends for the stronger reason.
func TestChat_RunLostAsItsTimeIsUpIsATimeout(t *testing.T) {
	var st atomic.Pointer[store.Store]
	env := newChatEnv(t, func(s *Server) {
		s.AgentRunSlack = 100 * time.Millisecond
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			if ev.Type == "usage" {
				_, _ = st.Load().Pool.Exec(ctx, `update agent_runs set state = 'failed', finished_at = now()`)
				time.Sleep(1300 * time.Millisecond) // the one-second run is overdue before the usage is recorded
			}
		}
	})
	st.Store(env.st)
	env.agent.catalogue = catalogueWithTimeout(1)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":70}`)
		holdUntilGone(r)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "timeout" {
		t.Errorf("last frame = %+v, want an error event timeout", last)
	}
	wantEnded(t, env, "failed", 70)
}

// A reservation whose answer never came may have been made: the run must not hold the allowance until it is stale.
func TestChat_ReservationThatMayHaveBeenMadeIsSettled(t *testing.T) {
	env := newChatEnv(t)
	// The commit outlives the client's patience, shrugs off its cancel request and lands a moment after it.
	execSQL(t, env.st, `create function slow_commit() returns trigger language plpgsql as $$ begin
		perform pg_sleep(20); return null; exception when query_canceled then perform pg_sleep(0.4); return null; end $$`)
	execSQL(t, env.st, `create constraint trigger slow_commit after insert on agent_runs deferrable initially deferred for each row execute function slow_commit()`)
	id := newConversation(t, env.sam)
	status := make(chan int, 1)
	go func() {
		resp := send(env.sam, id, "hello")
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitFor(t, "the reservation to commit", func() bool {
		return agentCount(t, env.st, `select count(*) from pg_stat_activity where wait_event = 'PgSleep' and datname = current_database()`) == 1
	})
	env.api.BeginDrain() // shortens the wait for the reservation to two seconds
	if got := <-status; got != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", got)
	}
	if n := len(env.agent.runs()); n != 0 {
		t.Errorf("the agent was called %d times", n)
	}
	if states := runStates(t, env.st); len(states) != 1 || states[0] != "failed" {
		t.Errorf("run states = %v, want the run settled", states)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}

// A panic outside every step (here in the log) must not leave the goroutine that writes the browser running.
func TestChat_PanicOutsideAStepStopsTheBrowserWriter(t *testing.T) {
	env := newChatEnv(t)
	env.api.Log = slog.New(panickingLog{env.api.Log.Handler()})
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	waitFor(t, "the browser writer to stop", func() bool {
		stacks := make([]byte, 1<<20)
		return !strings.Contains(string(stacks[:runtime.Stack(stacks, true)]), "(*browserWriter).loop")
	})
}

// panickingLog panics on the line that says a run has finished.
type panickingLog struct{ slog.Handler }

func (p panickingLog) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Message == "agent run finished" {
		panic("boom")
	}
	return p.Handler.Handle(ctx, rec)
}

// At shutdown a usage write that is already waiting for a lock may not hold up the end of the turn.
func TestChat_DrainWithAUsageWriteInFlightEndsInTime(t *testing.T) {
	env := newChatEnv(t)
	entered, locked := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		once.Do(func() { close(entered) })
		<-locked
		ndjson(w, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":9}`)
		holdUntilGone(r)
	})
	done := make(chan []sseFrame, 1)
	go func() { done <- readSSE(t, send(env.sam, newConversation(t, env.sam), "hello")) }()
	<-entered
	release := holdLock(t, env.st, `select id from agent_runs for update`)
	defer release()
	close(locked)
	waitFor(t, "the usage write to wait for the row", func() bool {
		return agentCount(t, env.st, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and datname = current_database()`) > 0
	})
	start := time.Now()
	env.api.BeginDrain()
	var frames []sseFrame
	select {
	case frames = <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("the turn never ended")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the turn took %v from the drain on", took)
	}
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "interrupted" {
		t.Errorf("last frame = %+v, want an error event interrupted", last)
	}
	release()
	reconcileSoon(t, env, 61*time.Minute)
	if got := auditOutcomes(t, env.st); len(got) != 1 {
		t.Errorf("audit = %v", got)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 || runState(t, env.st) != "failed" {
		t.Errorf("%d run tokens left, run %s", n, runState(t, env.st))
	}
}

// A model that asks for a tool that does not exist is told so by the agent, which names it unknown_tool and goes on.
// The turn goes on too, and everything after it counts, what it costs above all.
func TestChat_UnknownToolIsANameLikeAnyOther(t *testing.T) {
	env := newChatEnv(t)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"tool_start","tool":"unknown_tool"}`, `{"type":"tool_end","tool":"unknown_tool","ok":false}`,
			`{"type":"tool_start","tool":"made <b>up"}`, `{"type":"tool_end","tool":"made <b>up","ok":true}`,
			`{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":900}`,
			`{"type":"text","delta":"the answer"}`, `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if got := strings.Join(eventNames(frames), ","); got != "tool_start,tool_end,tool_start,tool_end,text,done" {
		t.Fatalf("events = %s", got)
	}
	for _, f := range frames[:4] {
		if f.Data["name"] != "unknown_tool" {
			t.Errorf("tool frame = %+v", f)
		}
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 || msgs[1].Text != "the answer" || msgs[1].Interrupted ||
		strings.Join(msgs[1].Tools, ",") != "unknown_tool,unknown_tool" {
		t.Errorf("messages = %+v", msgs)
	}
	wantEnded(t, env, "done", 900)
	if d := auditDetails(t, env.st); fmt.Sprint(d["tools"]) != "[unknown_tool unknown_tool]" || d["cost_micro_usd"] != float64(900) {
		t.Errorf("audit = %v", d)
	}
}

// Usage that was already sent when the turn ended is still charged.
func TestChat_UsageAfterTheEndIsStillRecorded(t *testing.T) {
	var api atomic.Pointer[Server]
	var once sync.Once
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			if ev.Type == "text" { // the drain ends the turn while the rest of the answer is still to be read
				once.Do(func() { api.Load().BeginDrain(); time.Sleep(100 * time.Millisecond) })
			}
		}
	})
	api.Store(env.api)
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, `{"type":"text","delta":"t%d "}`+"\n", i)
	}
	b.WriteString(`{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":333}` + "\n")
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		fmt.Fprint(w, b.String())
		w.(http.Flusher).Flush()
		holdUntilGone(r)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "interrupted" {
		t.Errorf("last frame = %+v", last)
	}
	wantEnded(t, env, "failed", 333)
}

// The reservation commits on a context of its own: a browser that leaves while it waits still gets its run
// reserved, and then settled.
func TestChat_ReservationSurvivesTheBrowserLeaving(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.sam)
	release := holdLock(t, env.st, `select * from agent_settings for update`)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := chatCtxRequestErr(ctx, env.sam, id, "hello")
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(500 * time.Millisecond) // the handler is waiting for the settings row
	cancel()
	<-done
	time.Sleep(200 * time.Millisecond)
	release()
	waitFor(t, "the reserved run to be settled", func() bool {
		var n int
		_ = env.st.Pool.QueryRow(t.Context(), `select count(*) from agent_runs where state = 'failed'`).Scan(&n)
		return n == 1
	})
	if n := agentCount(t, env.st, `select count(*) from agent_runs where state = 'running'`); n != 0 {
		t.Errorf("%d runs left running", n)
	}
}
