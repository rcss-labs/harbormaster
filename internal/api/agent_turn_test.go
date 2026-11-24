package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/limits"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// holdUntilGone keeps a fake agent run open until its caller hangs up, or a few seconds pass so that a turn that
// never lets go fails its test instead of hanging the suite.
func holdUntilGone(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(6 * time.Second):
	}
}

func execSQL(t *testing.T, st *store.Store, query string) {
	t.Helper()
	if _, err := st.Pool.Exec(t.Context(), query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func catalogueWithTimeout(seconds int) string {
	return strings.Replace(fakeCatalogue, `"run_timeout_seconds":300`, fmt.Sprintf(`"run_timeout_seconds":%d`, seconds), 1)
}

// runRow is the one agent_runs row of a test that made a single run.
func runRow(t *testing.T, st *store.Store) (state string, cost int64, tools []string) {
	t.Helper()
	if err := st.Pool.QueryRow(t.Context(), `select state, cost_micro_usd, tools from agent_runs`).Scan(&state, &cost, &tools); err != nil {
		t.Fatal(err)
	}
	return state, cost, tools
}

func auditDetails(t *testing.T, st *store.Store) map[string]any {
	t.Helper()
	entries, err := st.ListAudit(t.Context(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "agent_run" {
			return e.Details
		}
	}
	t.Fatal("no agent_run audit row")
	return nil
}

// failFirstUsageWrite makes the first write of a run's cost fail, as a database hiccup would.
func failFirstUsageWrite(t *testing.T, st *store.Store) {
	execSQL(t, st, `create sequence usage_fail_seq`)
	execSQL(t, st, `create function fail_first_usage() returns trigger language plpgsql as $$
		begin if nextval('usage_fail_seq') = 1 then raise exception 'usage write failed'; end if; return new; end $$`)
	execSQL(t, st, `create trigger fail_first_usage before update of cost_micro_usd on agent_runs for each row
		when (new.cost_micro_usd is distinct from old.cost_micro_usd) execute function fail_first_usage()`)
}

func TestChat_DrainInterruptsTheTurn(t *testing.T) {
	env := newChatEnv(t)
	var cancelled atomic.Bool
	entered, seen := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"partial"}`)
		once.Do(func() { close(entered) })
		holdUntilGone(r)
		cancelled.Store(true)
	})
	id := newConversation(t, env.sam)
	done := make(chan []sseFrame, 1)
	go func() {
		resp := send(env.sam, id, "hello")
		br := bufio.NewReader(resp.Body)
		for { // the first text event has been relayed, so the turn has it
			line, err := br.ReadString('\n')
			if err != nil || strings.HasPrefix(line, "data: ") {
				break
			}
		}
		close(seen)
		done <- readSSE(t, &http.Response{Body: io.NopCloser(br)})
	}()
	<-entered
	<-seen
	start := time.Now()
	env.api.BeginDrain()
	var frames []sseFrame
	select {
	case frames = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end on drain")
	}
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "interrupted" {
		t.Errorf("last frame = %+v, want an error event interrupted", last)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("drain took %v", time.Since(start))
	}
	waitFor(t, "the slot", func() bool { return slotsHeld(t, env, "sam") == 0 })
	if state, _, _ := runRow(t, env.st); state != "failed" || !cancelled.Load() {
		t.Errorf("run state %s, agent cancelled %v", state, cancelled.Load())
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 || msgs[1].Text != "partial" || !msgs[1].Interrupted {
		t.Errorf("messages = %+v", msgs)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
}

// A browser that stops reading must not hold the run open: the agent run is cancelled, the last figures seen are
// recorded, and the part answer is saved, all while the browser is still not reading.
func TestChat_StalledBrowserStopsTheRunAndKeepsLastFigures(t *testing.T) {
	env := newChatEnv(t, func(s *Server) { s.AgentWriteTimeout = 300 * time.Millisecond })
	var cancelled atomic.Bool
	chunk := strings.Repeat("a", 64<<10)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":100}`,
			`{"type":"usage","input_tokens":7,"output_tokens":9,"cost_micro_usd":999}`)
		line, _ := json.Marshal(map[string]string{"type": "text", "delta": chunk})
		for i := 0; i < 200; i++ {
			ndjson(w, string(line))
		}
		holdUntilGone(r)
		cancelled.Store(true)
	})
	id := newConversation(t, env.sam)
	resp := send(env.sam, id, "hello") // headers only: the body is never read
	defer resp.Body.Close()
	waitFor(t, "the slot to be released", func() bool {
		return slotsHeld(t, env, "sam") == 0 && agentCount(t, env.st, `select count(*) from agent_runs`) == 1
	})
	state, cost, _ := runRow(t, env.st)
	if state != "failed" || cost != 999 {
		t.Errorf("run = %s cost %d, want failed 999\nserver log:\n%s", state, cost, env.logs.String())
	}
	if d := auditDetails(t, env.st); d["cost_micro_usd"] != float64(999) || d["input_tokens"] != float64(7) || d["outcome"] != "interrupted" {
		t.Errorf("audit = %v", d)
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 || !msgs[1].Interrupted || len(msgs[1].Text) == 0 {
		t.Errorf("messages = %d, want the part answer saved", len(msgs))
	}
	waitFor(t, "the agent run to be cancelled", cancelled.Load)
}

// A usage write that fails must stop the run, and the figures last seen are still recorded before the run settles.
func TestChat_FailedUsageWriteStopsRunAndLastFiguresAreRecordedAtFinish(t *testing.T) {
	env := newChatEnv(t)
	failFirstUsageWrite(t, env.st)
	var cancelled atomic.Bool
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"usage","input_tokens":10,"output_tokens":5,"cost_micro_usd":777}`)
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(5 * time.Second):
		}
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "interrupted" {
		t.Errorf("last frame = %+v", last)
	}
	if !cancelled.Load() {
		t.Error("a failed usage write did not stop the agent run")
	}
	state, cost, _ := runRow(t, env.st)
	if state != "failed" || cost != 777 {
		t.Errorf("run = %s cost %d, want failed 777", state, cost)
	}
	if d := auditDetails(t, env.st); d["cost_micro_usd"] != float64(777) || d["input_tokens"] != float64(10) {
		t.Errorf("audit = %v", d)
	}
}

func TestChat_PanicInUsageWriteKeepsLastFigures(t *testing.T) {
	env := newChatEnv(t)
	var armed atomic.Bool
	env.api.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			panic("boom")
		}
		return time.Now()
	}
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		time.Sleep(100 * time.Millisecond)
		armed.Store(true)
		ndjson(w, `{"type":"usage","input_tokens":3,"output_tokens":4,"cost_micro_usd":444}`)
		holdUntilGone(r)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" {
		t.Errorf("last frame = %+v", last)
	}
	state, cost, _ := runRow(t, env.st)
	if state != "failed" || cost != 444 {
		t.Errorf("run = %s cost %d, want failed 444", state, cost)
	}
	if d := auditDetails(t, env.st); d["cost_micro_usd"] != float64(444) {
		t.Errorf("audit = %v", d)
	}
}

// The request's own context may be gone when an event that was already read is handled; usage must still be written
// on a context of its own.
func TestChat_UsageIsRecordedAfterTheRequestContextIsGone(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	env := newChatEnv(t, func(s *Server) {
		s.agentEventHook = func(ctx context.Context, ev agentclient.Event) {
			if ev.Type != "text" {
				return
			}
			once.Do(func() { close(entered) })
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`, `{"type":"usage","input_tokens":3,"output_tokens":2,"cost_micro_usd":55}`, `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resp := chatCtxRequest(t, ctx, env.sam, id, "hello")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the event hook never saw an event")
	}
	cancel()
	resp.Body.Close()
	waitFor(t, "the slot", func() bool {
		return slotsHeld(t, env, "sam") == 0 && agentCount(t, env.st, `select count(*) from audit_log where action = 'agent_run'`) == 1
	})
	if _, cost, _ := runRow(t, env.st); cost != 55 {
		t.Errorf("cost = %d, want 55", cost)
	}
	if strings.Contains(env.logs.String(), "usage not recorded") {
		t.Errorf("usage was written on the request context: %s", env.logs.String())
	}
}

func TestChat_HistoryIsBoundedByCountAndStartsWithAUser(t *testing.T) {
	env := newChatEnv(t)
	sam, _ := env.st.GetUserByUsername(t.Context(), "sam")
	conv, _ := env.st.CreateAgentConversation(t.Context(), sam.ID, "")
	for i := 0; i < 450; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := env.st.AppendAgentMessage(t.Context(), sam.ID, store.AgentMessage{ConversationID: conv.ID, Role: role, Text: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	frames := readSSE(t, send(env.sam, conv.ID, "the new question"))
	got := env.agent.runs()[0].Req.Messages
	if len(got) > 400 || len(got) < 300 || got[0].Role != "user" || got[len(got)-1].Text != "the new question" {
		t.Fatalf("history = %d messages, first %+v, last %+v", len(got), got[0], got[len(got)-1])
	}
	if got[len(got)-2].Text != "m449" {
		t.Errorf("the newest messages were not kept: %+v", got[len(got)-2])
	}
	trimmed := false
	for _, f := range frames {
		trimmed = trimmed || (f.Event == "notice" && f.Data["kind"] == "context_trimmed")
	}
	if !trimmed {
		t.Error("no context_trimmed notice")
	}
}

func TestChat_HistoryIsBoundedBySize(t *testing.T) {
	env := newChatEnv(t)
	sam, _ := env.st.GetUserByUsername(t.Context(), "sam")
	conv, _ := env.st.CreateAgentConversation(t.Context(), sam.ID, "")
	big := strings.Repeat("a", 100<<10)
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := env.st.AppendAgentMessage(t.Context(), sam.ID, store.AgentMessage{ConversationID: conv.ID, Role: role, Text: big}); err != nil {
			t.Fatal(err)
		}
	}
	frames := readSSE(t, send(env.sam, conv.ID, "the new question"))
	got := env.agent.runs()[0].Req.Messages
	raw, _ := json.Marshal(got)
	if len(raw) > 700<<10 || len(got) < 3 || got[0].Role != "user" || got[len(got)-1].Text != "the new question" {
		t.Fatalf("history = %d messages, %d bytes, first role %q", len(got), len(raw), got[0].Role)
	}
	trimmed := false
	for _, f := range frames {
		trimmed = trimmed || (f.Event == "notice" && f.Data["kind"] == "context_trimmed")
	}
	if !trimmed {
		t.Error("no context_trimmed notice")
	}
}

// The question itself is bounded at input, so one message can never be too big for the history budget alone.
func TestChat_LargestAcceptedQuestionFitsTheHistoryBudget(t *testing.T) {
	worst := strings.Repeat("<", chatTextMaxChars) // each character escapes to six bytes in JSON
	raw, _ := json.Marshal(agentclient.Message{Role: "user", Text: worst})
	if len(raw) > historyMaxBytes/4 {
		t.Errorf("the largest question encodes to %d bytes, over a quarter of the history budget", len(raw))
	}
	env := newChatEnv(t)
	resp := send(env.sam, newConversation(t, env.sam), worst)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	readSSE(t, resp)
	if got := env.agent.runs()[0].Req.Messages; len(got) != 1 || got[0].Text != worst {
		t.Errorf("the question was not sent whole: %d messages", len(got))
	}
}

// An unflagged user who owns a conversation still reaches nothing: no agent call, no run, nothing deleted.
func TestChat_UnflaggedOwnerOfAConversationIsRefused(t *testing.T) {
	env := newChatEnv(t)
	id := newConversation(t, env.omer)
	omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
	if err := env.st.SetUserAIChat(t.Context(), omer.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", fmt.Sprintf("/api/agent/conversations/%d", id)},
		{"DELETE", fmt.Sprintf("/api/agent/conversations/%d", id)},
		{"POST", messagesPath(id)},
	} {
		resp := env.omer.do(r.method, r.path, map[string]any{"text": "hi"}, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if len(env.agent.runs()) != 0 || agentCount(t, env.st, `select count(*) from agent_runs`) != 0 {
		t.Error("the agent was called or a run reserved")
	}
	if n := agentCount(t, env.st, `select count(*) from agent_conversations`); n != 1 {
		t.Errorf("%d conversations, want the owner's untouched", n)
	}
	if n := agentCount(t, env.st, `select count(*) from agent_messages`); n != 0 {
		t.Errorf("%d messages saved", n)
	}
}

func TestStatus_TellsAUserWhoCannotChatNothing(t *testing.T) {
	env := newChatEnv(t)
	omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
	_ = env.st.SetUserAIChat(t.Context(), omer.ID, false)
	setChatSettings(t, env.st, 0, "fake", "fake-1", nil) // budget used up and a model chosen: both would show
	var got map[string]any
	decode(t, env.omer.get("/api/agent/status"), &got)
	for k, want := range map[string]any{"enabled": true, "can_chat": false, "model_configured": false, "budget_exhausted": false, "agent_reachable": false} {
		if got[k] != want {
			t.Errorf("status[%s] = %v, want %v", k, got[k], want)
		}
	}
}

func TestStatus_UnreachableAgentDoesNotUnconfigureTheModel(t *testing.T) {
	up := newChatEnv(t)
	var got map[string]any
	decode(t, up.sam.get("/api/agent/status"), &got)
	if got["agent_reachable"] != true || got["model_configured"] != true {
		t.Fatalf("status = %v", got)
	}
	down := newChatEnv(t)
	down.agent.srv.Close()
	decode(t, down.sam.get("/api/agent/status"), &got)
	if got["agent_reachable"] != false || got["model_configured"] != true {
		t.Errorf("status with the agent down = %v, want reachable false and model_configured true", got)
	}
}

func TestChat_ModelTextIsNeverLogged(t *testing.T) {
	env := newChatEnv(t)
	failFirstUsageWrite(t, env.st)
	scripts := []func(w http.ResponseWriter){
		func(w http.ResponseWriter) {
			ndjson(w, `{"type":"text","delta":"ANSWER-MARKER"}`, `{"type":"done"}`)
		},
		func(w http.ResponseWriter) {
			ndjson(w, `{"type":"text","delta":"ANSWER-MARKER"}`, `{"type":"error","code":"provider_error","message":"ERROR-MARKER"}`)
		},
		func(w http.ResponseWriter) {
			ndjson(w, `{"type":"text","delta":"ANSWER-MARKER"}`, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":5}`)
			time.Sleep(50 * time.Millisecond)
		},
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
	}
	for _, s := range scripts {
		env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) { s(w) })
		readSSE(t, send(env.sam, newConversation(t, env.sam), "QUESTION-MARKER"))
	}
	if strings.Contains(env.logs.String(), "MARKER") {
		t.Errorf("model or user text reached the log:\n%s", env.logs.String())
	}
	if !strings.Contains(env.logs.String(), "agent run failed") {
		t.Errorf("the agent's failure was not logged:\n%s", env.logs.String())
	}
}

// A browser write that blocks must give up at its deadline and say the stream broke.
func TestBrowserWriter_AppliesTheWriteDeadlineAndReportsABreak(t *testing.T) {
	w := newBlockingWriter()
	defer w.release()
	broken := make(chan struct{})
	var once sync.Once
	bw := startBrowserWriter(w, 150*time.Millisecond, time.Hour, func() { once.Do(func() { close(broken) }) })
	start := time.Now()
	if !bw.event("text", map[string]string{"delta": "x"}) {
		t.Fatal("the first event was refused")
	}
	select {
	case <-broken:
	case <-time.After(3 * time.Second):
		t.Fatal("a blocked write never gave up: no write deadline was set")
	}
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Errorf("gave up after %v, before the deadline", took)
	}
	if bw.event("text", map[string]string{"delta": "y"}) {
		t.Error("an event was accepted on a broken stream")
	}
	bw.finish(0)
}

// blockingWriter is a ResponseWriter whose Write blocks until the deadline set through SetWriteDeadline.
type blockingWriter struct {
	header   http.Header
	mu       sync.Mutex
	deadline time.Time
	stop     chan struct{}
	once     sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{header: http.Header{}, stop: make(chan struct{})}
}
func (w *blockingWriter) Header() http.Header { return w.header }
func (w *blockingWriter) WriteHeader(int)     {}
func (w *blockingWriter) release()            { w.once.Do(func() { close(w.stop) }) }
func (w *blockingWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	w.deadline = t
	w.mu.Unlock()
	return nil
}
func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	d := w.deadline
	w.mu.Unlock()
	if d.IsZero() {
		<-w.stop
		return 0, os.ErrClosed
	}
	select {
	case <-time.After(time.Until(d)):
		return 0, os.ErrDeadlineExceeded
	case <-w.stop:
		return 0, os.ErrClosed
	}
}

// A write that fails must tell the turn, which cancels the agent run.
func TestBrowserWriter_ReportsAFailedWrite(t *testing.T) {
	w := &failingWriter{header: http.Header{}}
	broken := make(chan struct{}, 1)
	bw := startBrowserWriter(w, time.Second, time.Hour, func() { broken <- struct{}{} })
	bw.event("text", map[string]string{"delta": "x"})
	select {
	case <-broken:
	case <-time.After(3 * time.Second):
		t.Fatal("a failed write was not reported")
	}
	bw.finish(0)
}

type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestChat_RunsPastTheirDeadlineEndWithATimeoutError(t *testing.T) {
	env := newChatEnv(t, func(s *Server) { s.AgentRunSlack = 100 * time.Millisecond })
	env.agent.catalogue = catalogueWithTimeout(1)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"slow"}`)
		holdUntilGone(r)
	})
	frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" || last.Data["code"] != "timeout" {
		t.Fatalf("last frame = %+v, want an error event timeout", last)
	}
	if state, _, _ := runRow(t, env.st); state != "failed" {
		t.Errorf("run state = %s", state)
	}
}

// Stale runs are swept after twice the agent's run timeout, and never sooner than ten minutes.
func TestChat_StaleAfterIsTwiceTheRunTimeoutWithAFloor(t *testing.T) {
	for name, tc := range map[string]struct {
		timeoutSeconds    int
		keepAge, sweepAge time.Duration
	}{
		"floor":   {60, 5 * time.Minute, 11 * time.Minute},
		"doubled": {400, 12 * time.Minute, 14 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			env := newChatEnv(t)
			env.agent.catalogue = catalogueWithTimeout(tc.timeoutSeconds)
			omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
			keep, sweep := uuid.New(), uuid.New()
			for id, age := range map[uuid.UUID]time.Duration{keep: tc.keepAge, sweep: tc.sweepAge} {
				if _, err := env.st.Pool.Exec(t.Context(), `insert into agent_runs (id, role, user_id, provider, model, state, reserved_micro_usd, tools, started_at)
					values ($1, 'chat', $2, 'fake', 'fake-1', 'running', 1, '{}', now() - $3::interval)`, id, omer.ID, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
					t.Fatal(err)
				}
			}
			readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
			stateOf := func(id uuid.UUID) string {
				var s string
				if err := env.st.Pool.QueryRow(t.Context(), `select state from agent_runs where id = $1`, id).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			if got := stateOf(keep); got != "running" {
				t.Errorf("the %v old run was swept (state %s)", tc.keepAge, got)
			}
			if got := stateOf(sweep); got != "failed" {
				t.Errorf("the %v old run was not swept (state %s)", tc.sweepAge, got)
			}
		})
	}
}

func TestChat_RunTokenLifetimeShorterThanTheRunIsWarnedAbout(t *testing.T) {
	for name, tc := range map[string]struct {
		timeoutSeconds int
		wantWarning    bool
	}{
		"covered":   {300, false},
		"too short": {900, true}, // 15 minutes: the 15-minute token cannot cover it plus a minute
	} {
		t.Run(name, func(t *testing.T) {
			env := newChatEnv(t)
			env.agent.catalogue = catalogueWithTimeout(tc.timeoutSeconds)
			readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
			if got := strings.Contains(env.logs.String(), "run token lifetime"); got != tc.wantWarning {
				t.Errorf("warned = %v, want %v\n%s", got, tc.wantWarning, env.logs.String())
			}
		})
	}
}

func TestChat_MintFailureEndsTheTurnAsAnErrorAndSettles(t *testing.T) {
	env := newChatEnv(t)
	execSQL(t, env.st, `create function refuse_run_token() returns trigger language plpgsql as $$ begin raise exception 'no token'; end $$`)
	execSQL(t, env.st, `create trigger refuse_run_token before insert on api_tokens for each row execute function refuse_run_token()`)
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 before the stream starts", resp.StatusCode)
	}
	resp.Body.Close()
	if len(env.agent.runs()) != 0 {
		t.Error("the agent was called without a token")
	}
	if state, _, _ := runRow(t, env.st); state != "failed" {
		t.Errorf("run state = %s", state)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
	if d := auditDetails(t, env.st); d["outcome"] != "error" {
		t.Errorf("audit = %v", d)
	}
}

// A panic in one finishing step must not skip the ones after it.
func TestChat_APanickingFinishStepDoesNotSkipTheRest(t *testing.T) {
	env := newChatEnv(t)
	var armed atomic.Bool
	env.api.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			panic("boom")
		}
		return time.Now()
	}
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		time.Sleep(100 * time.Millisecond)
		armed.Store(true) // the next clock read is the settle step's
		ndjson(w, `{"type":"done"}`)
	})
	readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
	if armed.Load() {
		t.Fatal("the panic never fired")
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left after a panicking settle step", n)
	}
	if n := agentCount(t, env.st, `select count(*) from audit_log where action = 'agent_run'`); n != 1 {
		t.Errorf("%d audit rows", n)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}

func TestChat_ToolNamesAreSettledAndATurnOfOnlyToolsIsSaved(t *testing.T) {
	env := newChatEnv(t)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"tool_start","tool":"get_service"}`, `{"type":"tool_end","tool":"get_service","ok":true}`,
			`{"type":"error","code":"provider_error","message":"failed"}`)
	})
	id := newConversation(t, env.sam)
	readSSE(t, send(env.sam, id, "first"))
	if _, _, tools := runRow(t, env.st); strings.Join(tools, ",") != "get_service" {
		t.Errorf("run tools = %v", tools)
	}
	_, msgs := getMessages(t, env.sam, id)
	if len(msgs) != 2 || msgs[1].Text != "" || strings.Join(msgs[1].Tools, ",") != "get_service" || !msgs[1].Interrupted {
		t.Fatalf("messages = %+v, want the tools-only turn saved as interrupted", msgs)
	}
	env.agent.setScript(nil)
	readSSE(t, send(env.sam, id, "second"))
	got := env.agent.runs()[1].Req.Messages
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Errorf("history = %v, want the empty assistant message left out", got)
	}
}

func TestChat_ModelOutputIsCleanedAndBounded(t *testing.T) {
	env := newChatEnv(t)
	longMessage := strings.Repeat("m", 1000)
	secret := "AKIA" + "IOSFODNN7EXAMPLE"
	cases := map[string]struct {
		script   string
		wantCode string
	}{
		"bad code":     {`{"type":"error","code":"Bad Code!","message":"x"}`, "agent_unavailable"},
		"long message": {`{"type":"error","code":"provider_error","message":"` + longMessage + `"}`, "provider_error"},
		"secret":       {`{"type":"error","code":"provider_error","message":"key ` + secret + ` rejected"}`, "provider_error"},
	}
	for name, tc := range cases {
		env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) { ndjson(w, tc.script) })
		frames := readSSE(t, send(env.sam, newConversation(t, env.sam), "hello"))
		last := frames[len(frames)-1]
		msg, _ := last.Data["message"].(string)
		if last.Data["code"] != tc.wantCode || len([]rune(msg)) > maxErrorMessageLen || strings.Contains(msg, secret) {
			t.Errorf("%s: code %v message %q", name, last.Data["code"], msg)
		}
	}

	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"a\u0000b\u0001c\td\n"}`,
			`{"type":"tool_start","tool":"not_a_tool"}`, `{"type":"tool_start","tool":"get_service"}`,
			`{"type":"notice","notice":"made_up"}`, `{"type":"notice","notice":"limit_reached"}`, `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if got := strings.Join(eventNames(frames), ","); got != "text,tool_start,notice,done" {
		t.Errorf("events = %s, want an unknown tool and an unknown notice dropped", got)
	}
	if frames[0].Data["delta"] != "ab"+"c\td\n" {
		t.Errorf("delta = %q", frames[0].Data["delta"])
	}
	if _, msgs := getMessages(t, env.sam, id); len(msgs) != 2 || msgs[1].Text != "abc\td\n" || strings.Join(msgs[1].Tools, ",") != "get_service" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestChat_RejectsAQuestionWithANUL(t *testing.T) {
	env := newChatEnv(t)
	resp := send(env.sam, newConversation(t, env.sam), "hello\x00world")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	if agentCount(t, env.st, `select count(*) from agent_runs`) != 0 {
		t.Error("a run was reserved")
	}
}

func TestChat_AnswerOverTheCapIsCutWithAMarker(t *testing.T) {
	env := newChatEnv(t)
	chunk := strings.Repeat("a", 1024)
	line, _ := json.Marshal(map[string]string{"type": "text", "delta": chunk})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		for i := 0; i < 300; i++ {
			ndjson(w, string(line))
		}
		ndjson(w, `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if len(frames) != 301 {
		t.Errorf("%d frames, want every delta relayed", len(frames))
	}
	_, msgs := getMessages(t, env.sam, id)
	text := msgs[1].Text
	if len(text) > maxSavedAnswerBytes+len(answerCapMarker) || !strings.HasSuffix(text, answerCapMarker) || len(text) < maxSavedAnswerBytes-2048 {
		t.Errorf("saved %d bytes, suffix ok %v", len(text), strings.HasSuffix(text, answerCapMarker))
	}
}

// The slot is free by the time the browser hears the answer is done, so its next question is not turned away.
func TestChat_SlotIsReleasedBeforeDoneIsSent(t *testing.T) {
	env := newChatEnv(t, func(s *Server) { s.Limits = slowRelease{limits.NewMemoryConcurrency()} })
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: done" {
			if n := slotsHeld(t, env, "sam"); n != 0 {
				t.Errorf("slot still held when done arrived: %d", n)
			}
			return
		}
	}
	t.Fatal("no done event")
}

// slowRelease makes giving the slot back take a moment, so a turn that sends done first is caught.
type slowRelease struct{ limits.Concurrency }

func (s slowRelease) Acquire(ctx context.Context, name string, userID int64, max int) (func(), bool, error) {
	rel, ok, err := s.Concurrency.Acquire(ctx, name, userID, max)
	return func() { time.Sleep(300 * time.Millisecond); rel() }, ok, err
}
