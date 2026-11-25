package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// syncBuffer is a log sink the process writes to while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

type investigationRow struct {
	state, reason string
	attempts      int
}

func investigationRows(t *testing.T, conn *pgx.Conn) []investigationRow {
	t.Helper()
	rows, err := conn.Query(context.Background(), `select state, reason, attempts from agent_investigations order by id`)
	if err != nil {
		t.Fatalf("agent_investigations: %v", err)
	}
	defer rows.Close()
	var out []investigationRow
	for rows.Next() {
		var r investigationRow
		if err := rows.Scan(&r.state, &r.reason, &r.attempts); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func receivedEvents(t *testing.T, me *session, path string) []string {
	t.Helper()
	status, body := me.send(http.MethodGet, "/fake-notify/received", nil)
	if status != http.StatusOK {
		return nil
	}
	var received []struct {
		Path string `json:"path"`
		Body struct {
			Event string `json:"event"`
		} `json:"body"`
	}
	if json.Unmarshal([]byte(body), &received) != nil {
		return nil
	}
	var events []string
	for _, entry := range received {
		if entry.Path == path {
			events = append(events, entry.Body.Event)
		}
	}
	return events
}

// The agent service is down. The alert still reaches the switched route at once; the investigation is tried twice and
// then fails with agent_unavailable; no diagnosis is sent and nothing panics.
func TestAgentDown_OpenedStillDelivered(t *testing.T) {
	schema := requireDB(t)
	logs := &syncBuffer{}
	base := startServeLogged(t, map[string]string{
		"HM_DATABASE_URL": schema, "HM_SESSION_KEY": "agent-down-session-key",
		"HM_AGENT_URL":     "http://" + closedAddr(t),
		"HM_POLL_SERVICES": "2s", "HM_POLL_RUNNING_TASKS": "2s", "HM_POLL_IDLE_AFTER": "0",
		"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_FAKE_NOTIFY_DEMO": "false",
	}, logs)
	conn, err := pgx.Connect(context.Background(), schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	me := signIn(t, base)
	route := degradedRoute(base)
	route["name"], route["investigate"] = "agent down sink", true
	route["url"] = base + "/fake-notify/webhook/agentdown"
	if status, body := me.send(http.MethodPost, "/api/notifications/routes", route); status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create route = %d %s", status, body)
	}

	within(t, degradedWindow, "alert.raised on the switched route", func() bool {
		for _, event := range receivedEvents(t, me, "/fake-notify/webhook/agentdown") {
			if event == "alert.raised" {
				return true
			}
		}
		return false
	})
	defer func() {
		if t.Failed() {
			t.Logf("investigations: %+v\nlog:\n%s", investigationRows(t, conn), logs.String())
		}
	}()
	within(t, 90*time.Second, "every investigation failed after two attempts", func() bool {
		// A first attempt gives its row back to be tried in a minute; the test only skips that wait.
		if _, err := conn.Exec(context.Background(), `update agent_investigations set claimed_until = now() where state = 'requested' and attempts = 1`); err != nil {
			t.Fatal(err)
		}
		rows := investigationRows(t, conn)
		for _, r := range rows {
			if r.state != "failed" || r.reason != "agent_unavailable" || r.attempts != 2 {
				return false
			}
		}
		return len(rows) > 0
	})

	time.Sleep(3 * time.Second) // two more dispatcher cycles: a diagnosis must not follow
	for _, event := range receivedEvents(t, me, "/fake-notify/webhook/agentdown") {
		if event == "diagnosis" {
			t.Fatal("a diagnosis was delivered although no investigation finished")
		}
	}
	if out := logs.String(); strings.Contains(out, "panic") {
		t.Errorf("the process logged a panic:\n%s", out)
	}
}
