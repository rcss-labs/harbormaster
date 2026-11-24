package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChat_TextAfterDoneIsIgnored(t *testing.T) {
	env := newChatEnv(t)
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"kept"}`, `{"type":"done"}`, `{"type":"text","delta":"LATE-MARKER"}`, `{"type":"tool_start","tool":"late_tool"}`)
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if got := eventNames(frames); strings.Join(got, ",") != "text,done" {
		t.Errorf("events = %v", got)
	}
	_, msgs := getMessages(t, env.sam, id)
	if len(msgs) != 2 || msgs[1].Text != "kept" || len(msgs[1].Tools) != 0 {
		t.Errorf("messages = %+v", msgs)
	}
}

// Model text goes out as one JSON data line, so it can neither add a line nor forge an event.
func TestChat_StreamCannotBeBrokenOutOf(t *testing.T) {
	env := newChatEnv(t)
	hostile := "a\nevent: error\ndata: {\"code\":\"forged\"}\n\r\n done"
	line, _ := json.Marshal(map[string]string{"type": "text", "delta": hostile})
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, string(line), `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	resp := send(env.sam, id, "hello")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	frames := readSSE(t, &http.Response{Body: io.NopCloser(strings.NewReader(string(raw)))})
	if got := eventNames(frames); strings.Join(got, ",") != "text,done" {
		t.Fatalf("events = %v in %q", got, raw)
	}
	for _, f := range frames {
		if len(f.Lines) != 2 {
			t.Errorf("frame has %d lines: %q", len(f.Lines), f.Lines)
		}
	}
	if want := strings.ReplaceAll(hostile, "\r", ""); frames[0].Data["delta"] != want { // control characters are stripped
		t.Errorf("delta = %q", frames[0].Data["delta"])
	}
	if strings.Contains(string(raw), "\r") {
		t.Errorf("a carriage return reached the stream: %q", raw)
	}
}

func TestChat_StreamHasKeepaliveComments(t *testing.T) {
	env := newChatEnv(t, func(s *Server) { s.AgentHeartbeat = 20 * time.Millisecond })
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"x"}`)
		time.Sleep(200 * time.Millisecond)
		ndjson(w, `{"type":"done"}`)
	})
	resp := send(env.sam, newConversation(t, env.sam), "hello")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "\n: ") && !strings.HasPrefix(string(raw), ": ") {
		t.Errorf("no comment line in %q", raw)
	}
}

// A panic mid-turn still settles the run, deletes its token, saves what was said and frees the slot.
func TestChat_PanicSettlesOnce(t *testing.T) {
	env := newChatEnv(t)
	var armed atomic.Bool
	env.api.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			panic("boom")
		}
		return time.Now()
	}
	env.agent.setScript(func(w http.ResponseWriter, r *http.Request, _ capturedRun) {
		ndjson(w, `{"type":"text","delta":"partial"}`)
		time.Sleep(100 * time.Millisecond)
		armed.Store(true)
		ndjson(w, `{"type":"usage","input_tokens":1,"output_tokens":1,"cost_micro_usd":1}`, `{"type":"done"}`)
	})
	id := newConversation(t, env.sam)
	frames := readSSE(t, send(env.sam, id, "hello"))
	if last := frames[len(frames)-1]; last.Event != "error" {
		t.Errorf("last frame = %+v, want an error event", last)
	}
	if armed.Load() {
		t.Fatal("the panic never fired")
	}
	_, msgs := getMessages(t, env.sam, id)
	if len(msgs) != 2 || msgs[1].Text != "partial" || !msgs[1].Interrupted {
		t.Errorf("messages = %+v", msgs)
	}
	if states := runStates(t, env.st); len(states) != 1 || states[0] != "failed" {
		t.Errorf("run states = %v", states)
	}
	if n := agentCount(t, env.st, `select count(*) from api_tokens where kind = 'run'`); n != 0 {
		t.Errorf("%d run tokens left", n)
	}
	if got := auditOutcomes(t, env.st); len(got) != 1 {
		t.Errorf("audit rows = %v, want exactly one", got)
	}
	if slotsHeld(t, env, "sam") != 0 {
		t.Error("slot still held")
	}
}
