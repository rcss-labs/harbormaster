package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
	"github.com/rcss-labs/harbormaster/internal/limits"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// sharedLimits returns a constructor for the limiter one more "task" would build: its own per-task memory and its
// own handle on the one store, as two web tasks have.
func sharedLimits(client kv.Client, up bool) func() limits.Concurrency {
	return func() limits.Concurrency {
		health := &staticHealth{up: up}
		return limits.WithFallback(limits.NewKVConcurrency(client), limits.NewMemoryConcurrency(), health, telemetry.New())
	}
}

type staticHealth struct{ up bool }

func (h *staticHealth) Up() bool { return h.up }

// secondTask builds another API server over the same store, clients and configuration as api, with its own limiter,
// as a second web task would be.
func secondTask(t *testing.T, api *Server, lim limits.Concurrency, mutate ...func(*Server)) *httptest.Server {
	t.Helper()
	s := Server{
		Store: api.Store, Auth: api.Auth, Setup: api.Setup, Clients: api.Clients, Prober: api.Prober,
		Cfg: api.Cfg, Log: api.Log, Now: api.Now, Deploy: api.Deploy, LogSearch: api.LogSearch,
		ExecAllowInsecureStream: api.ExecAllowInsecureStream, Limits: lim,
	}
	for _, m := range mutate {
		m(&s)
	}
	b := New(s)
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// execTasks returns an exec rig whose first task uses limiter a, plus a second rig view on a second task using b.
func execTasks(t *testing.T, a, b limits.Concurrency) (first, second *execRig) {
	t.Helper()
	var captured Server
	first = newExecRig(t, func(s *Server) {
		s.Limits = a
		captured = *s
	})
	captured.Limits = b
	srv := httptest.NewServer(New(captured).Handler())
	t.Cleanup(srv.Close)
	second = &execRig{srv: srv, fk: first.fk, st: first.st, ssm: first.ssm}
	return first, second
}

func openTerminal(t *testing.T, r *execRig, c *apiClient, id string) *wsClient {
	t.Helper()
	w := r.open(t, c, id, "payments-api", "/bin/sh")
	w.sendText(startFrame(c.csrf))
	w.waitStatus("connected")
	return w
}

func TestExec_LimitIsEstateWideAcrossTwoServers(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		mk := sharedLimits(client, true)
		a, b := execTasks(t, mk(), mk())
		id := a.paymentsTask(t)
		c := login(t, a.srv, "sam", testPassword)
		for i := 0; i < 3; i++ {
			openTerminal(t, a, c, id)
		}
		e := b.expectRefusal(t, c, id, "payments-api", "/bin/sh", "session_limit", 4409)
		if e.Message != "You already have 3 open terminal sessions." {
			t.Errorf("message = %q", e.Message)
		}
	})
}

func TestExec_LimitFallsBackPerTaskWhenStoreDown(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		mk := sharedLimits(client, false)
		a, b := execTasks(t, mk(), mk())
		id := a.paymentsTask(t)
		c := login(t, a.srv, "sam", testPassword)
		for i := 0; i < 3; i++ {
			openTerminal(t, a, c, id)
			openTerminal(t, b, c, id) // each task counts for itself while the store is down
		}
		a.expectRefusal(t, c, id, "payments-api", "/bin/sh", "session_limit", 4409)
		b.expectRefusal(t, c, id, "payments-api", "/bin/sh", "session_limit", 4409)
	})
}

func TestExec_ReleaseOnCloseFreesTheSharedSlot(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		mk := sharedLimits(client, true)
		a, b := execTasks(t, mk(), mk())
		id := a.paymentsTask(t)
		c := login(t, a.srv, "sam", testPassword)
		var open []*wsClient
		for i := 0; i < 3; i++ {
			open = append(open, openTerminal(t, a, c, id))
		}
		b.expectRefusal(t, c, id, "payments-api", "/bin/sh", "session_limit", 4409)
		open[0].conn.Close(websocket.StatusNormalClosure, "")
		eventually(t, "the slot to free on the other task", func() bool {
			w := b.open(t, c, id, "payments-api", "/bin/sh")
			w.sendText(startFrame(c.csrf))
			for {
				typ, f, _, err := w.next()
				if err != nil {
					return false
				}
				if typ == websocket.MessageText && f.Type == "status" && f.State == "connected" {
					return true
				}
				if typ == websocket.MessageText && f.Type == "error" {
					w.conn.CloseNow()
					return false
				}
			}
		})
	})
}

func TestTail_LimitIsEstateWideAcrossTwoServers(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		mk := sharedLimits(client, true)
		srvA, apiA, _ := tailFixture(t, 5*time.Second, func(s *Server) { s.Limits = mk() })
		srvB := secondTask(t, apiA, mk())
		c := login(t, srvA, "oncall", testPassword)
		for range 2 {
			conn := startTail(t, srvA, c, paymentsID(), "")
			if ready, err := readFrame(conn, 3*time.Second); err != nil || ready["type"] != "ready" {
				t.Fatalf("ready = %v, %v", ready, err)
			}
		}
		third := startTail(t, srvB, c, paymentsID(), "")
		code, reason := expectClose(t, third)
		if code != 4409 || reason != "You already have 2 live tails open." {
			t.Errorf("close = %d %q, want 4409 and the exact limit message", code, reason)
		}
	})
}

func TestLogSearch_OneRunningPerUserAcrossTwoServers(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		mk := sharedLimits(client, true)
		env := newLogSearchEnv(t, nil, nil...)
		env.api.Limits = mk()
		srvB := secondTask(t, env.api, mk())
		env.fk.InsightsAlwaysRunning = true

		first := login(t, env.srv, "sam", testPassword)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if resp, err := postWithContext(ctx, first, searchReq(paymentsID())); err == nil {
				resp.Body.Close()
			}
		}()
		defer func() { cancel(); <-done }()
		waitFor(t, "the first search to start", func() bool { return env.fk.Calls()["StartQuery"] >= 1 })

		second := &apiClient{t: t, http: first.http, base: srvB.URL, csrf: first.csrf}
		// Bounded: if the limit were per task, this search would start and run until stopped.
		secondCtx, secondCancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer secondCancel()
		resp, err := postWithContext(secondCtx, second, searchReq(paymentsID()))
		if err != nil {
			t.Fatalf("second search on the other task was not refused (it ran until the timeout): %v", err)
		}
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("second search on the other task: status %d, want 409", resp.StatusCode)
		}
		if code, msg := errorCode(t, resp); code != "conflict" || msg != "You already have a log search running." {
			t.Errorf("code %q message %q", code, msg)
		}
	})
}

func TestLogSearch_RateLimitIsSharedAcrossTwoServers(t *testing.T) {
	kvtest.Each(t, func(t *testing.T, client kv.Client) {
		env := newLogSearchEnv(t, nil)
		env.api.Rates = limits.NewKVRate(client)
		srvB := secondTask(t, env.api, limits.NewMemoryConcurrency(), func(s *Server) { s.Rates = limits.NewKVRate(client) })
		c := login(t, env.srv, "sam", testPassword)
		for i := 0; i < 6; i++ {
			resp := c.post("/api/log-search", searchReq(paymentsID()))
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("search %d on the first task: status %d", i+1, resp.StatusCode)
			}
		}
		other := &apiClient{t: t, http: c.http, base: srvB.URL, csrf: c.csrf}
		resp := other.post("/api/log-search", searchReq(paymentsID()))
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("seventh search on the other task: status %d, want 429", resp.StatusCode)
		}
	})
}

// brokenRate is a Rate whose store is unreachable.
type brokenRate struct{}

func (brokenRate) Allow(context.Context, string, string, int, float64) (bool, error) {
	return false, errors.New("store unreachable")
}

func (brokenRate) Reserve(context.Context, string, string, time.Duration) (time.Duration, error) {
	return 0, errors.New("store unreachable")
}

func TestSAMLLogin_StoreErrorNeverBlocksSignIn(t *testing.T) {
	srv, _, _, _ := newServerWithFakeIdPCustom(t, func(s *Server) { s.Rates = brokenRate{} })
	b := newBrowser(t, srv)
	resp := b.get("/api/auth/saml/login")
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login start with a failing limiter: status %d, want the redirect to the IdP", resp.StatusCode)
	}
}
