package api

import (
	"context"
	"fmt"
	"net/http"
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

// slowStepEnv runs one turn whose script says "x", waits for the test to lock something, then ends the run with
// done. It returns how long the turn took after the lock was taken, and the frames.
func runWithLockedStep(t *testing.T, env *chatEnv, id int64, lock func() func(), settleFirst bool) (took time.Duration, frames []sseFrame, release func()) {
	t.Helper()
	entered, locked := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		if settleFirst {
			ndjson(w, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":50}`)
		}
		ndjson(w, `{"type":"text","delta":"x"}`)
		once.Do(func() { close(entered) })
		select {
		case <-locked:
		case <-time.After(8 * time.Second):
		}
		ndjson(w, `{"type":"done"}`)
	})
	done := make(chan []sseFrame, 1)
	go func() { done <- readSSE(t, send(env.sam, id, "hello")) }()
	<-entered
	if settleFirst {
		waitFor(t, "the usage to be recorded", func() bool {
			var c int64
			_ = env.st.Pool.QueryRow(t.Context(), `select cost_micro_usd from agent_runs`).Scan(&c)
			return c == 50
		})
	}
	release = lock()
	start := time.Now()
	close(locked)
	select {
	case frames = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the turn never ended")
	}
	return time.Since(start), frames, release
}

func runState(t *testing.T, st *store.Store) string {
	t.Helper()
	var s string
	if err := st.Pool.QueryRow(t.Context(), `select state from agent_runs`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

const slowStepLimit = 6 * time.Second // each step has at most 2.5 s; the unbounded wait is 10 s

// A save that cannot get the conversation must not starve the settlement: the run is settled with its cost, the
// token is gone, an audit row says the save failed, the slot is free, and the browser is not told it is done.
func TestChat_SlowSaveDoesNotStarveSettlement(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.sam)
	took, frames, release := runWithLockedStep(t, env, id, func() func() {
		return holdLock(t, env.st, `select id from agent_conversations for update`)
	}, true)
	defer release()
	if took > slowStepLimit {
		t.Errorf("the turn took %v with the conversation locked", took)
	}
	if st := runState(t, env.st); st == "running" {
		t.Error("the run was left running")
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	d := auditDetails(t, env.st)
	if d["cost_micro_usd"] != float64(50) || d["saved"] != false {
		t.Errorf("audit = %v, want cost 50 and saved false", d)
	}
	last := frames[len(frames)-1]
	if last.Event != "error" || last.Data["code"] != "internal" {
		t.Errorf("last frame = %+v, want an error event: the answer was not saved", last)
	}
	for _, f := range frames {
		if f.Event == "done" {
			t.Error("done was sent for an answer that was not saved")
		}
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}

func TestChat_SlowSettleDoesNotStarveTheRest(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.sam)
	took, _, release := runWithLockedStep(t, env, id, func() func() {
		return holdLock(t, env.st, `select id from agent_runs for update`)
	}, true)
	defer release()
	if took > slowStepLimit {
		t.Errorf("the turn took %v with the run row locked", took)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 {
		t.Errorf("messages = %+v, want the answer saved", msgs)
	}
	if n := agentCount(t, env.st, `select count(*) from audit_log where action = 'agent_run'`); n != 1 {
		t.Errorf("%d audit rows", n)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}

func TestChat_SlowTokenDeleteDoesNotStarveTheRest(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.sam)
	took, _, release := runWithLockedStep(t, env, id, func() func() {
		return holdLock(t, env.st, `select id from api_tokens where kind = 'run' for update`)
	}, false)
	defer release()
	if took > slowStepLimit {
		t.Errorf("the turn took %v with the token row locked", took)
	}
	if st := runState(t, env.st); st != "done" {
		t.Errorf("run state = %s, want done", st)
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 {
		t.Errorf("messages = %+v, want the answer saved", msgs)
	}
	if n := agentCount(t, env.st, `select count(*) from audit_log where action = 'agent_run'`); n != 1 {
		t.Errorf("%d audit rows", n)
	}
}

func TestChat_SlowAuditDoesNotHoldTheTurn(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.sam)
	took, frames, release := runWithLockedStep(t, env, id, func() func() {
		return holdLock(t, env.st, `lock table audit_log in access exclusive mode`)
	}, false)
	defer release()
	if took > slowStepLimit {
		t.Errorf("the turn took %v with the audit log locked", took)
	}
	if last := frames[len(frames)-1]; last.Event != "done" {
		t.Errorf("last frame = %+v, want done", last)
	}
	if st := runState(t, env.st); st != "done" {
		t.Errorf("run state = %s", st)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
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

// A browser that reads slowly but does read is never dropped: the text it has not read yet is merged, and done
// still arrives at the end.
func TestChat_SlowReaderStillGetsEverythingAndDone(t *testing.T) {
	env := newChatEnv(t)
	line := `{"type":"text","delta":"` + strings.Repeat("z", 1024) + `"}`
	const n = 3000
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		for i := 0; i < n; i++ {
			ndjson(w, line)
		}
		ndjson(w, `{"type":"done"}`)
	})
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	time.Sleep(700 * time.Millisecond) // the agent has long finished; nothing has been read yet
	frames := readSSE(t, resp)
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
}

func TestBrowserWriter_MergesPendingTextAndAlwaysTakesControlFrames(t *testing.T) {
	w := newBlockingWriter()
	defer w.release()
	bw := startBrowserWriter(w, time.Hour, time.Hour, func() {})
	bw.event("notice", map[string]string{"kind": "context_trimmed"}) // the writer picks this one up and blocks on it
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 5000; i++ {
		if !bw.text("abcd") {
			t.Fatal("a delta was refused")
		}
	}
	for _, name := range []string{"notice", "error", "done"} {
		if !bw.event(name, map[string]string{}) {
			t.Fatalf("%s was refused", name)
		}
	}
	if n := bw.pendingItems(); n > 5 {
		t.Errorf("%d items pending, want the deltas merged", n)
	}
}

func TestBrowserWriter_RecoversFromAPanickingWrite(t *testing.T) {
	broken := make(chan struct{}, 1)
	bw := startBrowserWriter(&panickingWriter{header: http.Header{}}, time.Second, time.Hour, func() { broken <- struct{}{} })
	bw.event("text", map[string]string{"delta": "x"})
	select {
	case <-broken:
	case <-time.After(3 * time.Second):
		t.Fatal("a panicking write was not reported")
	}
	bw.finish(0)
}

type panickingWriter struct{ header http.Header }

func (w *panickingWriter) Header() http.Header       { return w.header }
func (w *panickingWriter) WriteHeader(int)           {}
func (w *panickingWriter) Write([]byte) (int, error) { panic("write") }

func TestBrowserWriter_FinishCutsShortAStalledWrite(t *testing.T) {
	w := newBlockingWriter()
	defer w.release()
	bw := startBrowserWriter(w, time.Hour, time.Hour, func() {})
	bw.event("text", map[string]string{"delta": "x"})
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	bw.finish(200 * time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("finish took %v on a stalled write", took)
	}
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

// At shutdown the usage write may not wait the usual ten seconds.
func TestChat_UsageWriteAtDrainIsBoundedToTwoSeconds(t *testing.T) {
	var api atomic.Pointer[Server]
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			if ev.Type == "usage" {
				api.Load().BeginDrain()
				time.Sleep(100 * time.Millisecond)
			}
		}
	})
	api.Store(env.api)
	id := newConversation(t, env.sam)
	entered := make(chan struct{})
	locked := make(chan struct{})
	var once sync.Once
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		once.Do(func() { close(entered) })
		<-locked
		ndjson(w, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":9}`)
		holdUntilGone(r)
	})
	done := make(chan []sseFrame, 1)
	go func() { done <- readSSE(t, send(env.sam, id, "hello")) }()
	<-entered
	release := holdLock(t, env.st, `select id from agent_runs for update`)
	defer release()
	start := time.Now()
	close(locked)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the turn never ended")
	}
	if took := time.Since(start); took > 7*time.Second {
		t.Errorf("the turn took %v: the usage write waited too long", took)
	}
}

// A tool the agent calls that this version does not know ends the turn with its own code, in the browser and in the
// audit row, instead of being quietly dropped.
func TestChat_UnknownToolEndsTheTurnAsUnknownTool(t *testing.T) {
	env := newChatEnv(t)
	var cancelled atomic.Bool
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"before"}`, `{"type":"tool_start","tool":"made_up_tool"}`)
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(5 * time.Second):
		}
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "unknown_tool" {
		t.Fatalf("last frame = %+v, want an error event unknown_tool", last)
	}
	for _, f := range frames {
		if f.Event == "tool_start" {
			t.Error("the unknown tool was relayed")
		}
	}
	if !cancelled.Load() {
		t.Error("the agent run was not cancelled")
	}
	if d := auditDetails(t, env.st); d["outcome"] != "unknown_tool" {
		t.Errorf("audit = %v", d)
	}
	if st := runState(t, env.st); st != "failed" {
		t.Errorf("run state = %s", st)
	}
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
