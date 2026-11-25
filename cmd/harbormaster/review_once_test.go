package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/agent"
	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
)

// startFakeAgent serves the real agent handler with its scripted model, as `harbormaster-agent serve` does with
// HM_AGENT_FAKE_PROVIDER, and returns its URL. upstream is the Harbormaster that run tokens are redeemed against.
func startFakeAgent(t *testing.T, upstream string) string {
	t.Helper()
	env := map[string]string{"HM_AGENT_UPSTREAM_URL": upstream, "HM_AGENT_FAKE_PROVIDER": "true", "HM_AGENT_TRUSTED_PROXY": "false"}
	cfg, err := agent.LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	up, err := agent.NewUpstream(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(agent.NewHandler(cfg, up, map[string]provider.Provider{"fake": fake.Demo()}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	return server.URL
}

type reviewRunRow struct {
	id            int64
	state, reason string
	attempts      int
	retryAt       *time.Time // a waiting row's earliest retry, set when a try gave it back
}

func reviewRunRows(t *testing.T, conn *pgx.Conn, reviewID int64) []reviewRunRow {
	t.Helper()
	rows, err := conn.Query(context.Background(), `select id, state, reason, attempts, claimed_until from agent_review_runs where review_id = $1 order by id`, reviewID)
	if err != nil {
		t.Fatalf("agent_review_runs: %v", err)
	}
	defer rows.Close()
	var out []reviewRunRow
	for rows.Next() {
		var r reviewRunRow
		if err := rows.Scan(&r.id, &r.state, &r.reason, &r.attempts, &r.retryAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// createReviewDueNow makes a daily review whose latest occurrence was five minutes ago and whose schedule is old
// enough for that occurrence to count, sent to the route. It returns the review's id.
func createReviewDueNow(t *testing.T, me *session, conn *pgx.Conn, name string, routeID int64) int64 {
	t.Helper()
	fiveMinutesAgo := time.Now().UTC().Add(-5 * time.Minute)
	status, body := me.send(http.MethodPost, "/api/agent/reviews", map[string]any{
		"name": name, "type": "cost", "enabled": true,
		"schedule": map[string]any{"cadence": "daily", "at_minute": fiveMinutesAgo.Hour()*60 + fiveMinutesAgo.Minute(), "weekday": nil, "month_day": nil, "timezone": "UTC"},
		"scope":    []any{}, "route_ids": []int64{routeID},
	})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create review = %d %s", status, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == 0 {
		t.Fatalf("create review answered %s", body)
	}
	if _, err := conn.Exec(context.Background(), `update agent_reviews set schedule_changed_at = now() - interval '2 days' where id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func createRoute(t *testing.T, me *session, base, name, path string) int64 {
	t.Helper()
	route := degradedRoute(base)
	route["name"], route["url"] = name, base+path
	status, body := me.send(http.MethodPost, "/api/notifications/routes", route)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create route = %d %s", status, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == 0 {
		t.Fatalf("create route answered %s", body)
	}
	return created.ID
}

func countEvents(events []string, want string) int {
	n := 0
	for _, event := range events {
		if event == want {
			n++
		}
	}
	return n
}

// Two processes share one database and one review is due. Whichever holds the coordinator lease makes the run: one
// run row, finished, and exactly one `review` message at the receiver.
func TestReview_RunsOnceAndIsDelivered(t *testing.T) {
	schema := requireDB(t)
	hmA, hmB := freeAddr(t), freeAddr(t)
	agentURL := startFakeAgent(t, "http://"+hmA)
	env := func(addr string) map[string]string {
		return map[string]string{
			"HM_DATABASE_URL": schema, "HM_SESSION_KEY": "review-once-session-key", "HM_AGENT_URL": agentURL,
			"HM_LISTEN": addr, "HM_BASE_URL": "http://" + addr,
			"HM_POLL_SERVICES": "2s", "HM_POLL_RUNNING_TASKS": "2s", "HM_POLL_IDLE_AFTER": "0",
			"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_FAKE_NOTIFY_DEMO": "false",
		}
	}
	base := startServeLogged(t, env(hmA), io.Discard)
	logsB := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("second process log:\n%s", logsB.String())
		}
	})
	startServeLogged(t, env(hmB), logsB)
	conn, err := pgx.Connect(context.Background(), schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	me := signIn(t, base)
	const path = "/fake-notify/webhook/reviewonce"
	routeID := createRoute(t, me, base, "review once sink", path)
	reviewID := createReviewDueNow(t, me, conn, "Cost, once", routeID)
	defer func() {
		if t.Failed() {
			t.Logf("runs: %+v", reviewRunRows(t, conn, reviewID))
		}
	}()

	within(t, 120*time.Second, "the review message at the receiver", func() bool {
		return countEvents(receivedEvents(t, me, path), "review") > 0
	})
	rows := reviewRunRows(t, conn, reviewID)
	if len(rows) != 1 || rows[0].state != "done" || rows[0].reason != "" {
		t.Fatalf("runs of the review = %+v, want exactly one, done", rows)
	}
	// A second review due now is a later tick that scans the first review again after its run is done: when its own
	// result has arrived, a duplicate of the first would have been recorded and sent by then.
	secondID := createReviewDueNow(t, me, conn, "Cost, again", routeID)
	within(t, 120*time.Second, "the second review's message at the receiver", func() bool {
		return countEvents(receivedEvents(t, me, path), "review") >= 2
	})
	if rows = reviewRunRows(t, conn, reviewID); len(rows) != 1 || rows[0].state != "done" {
		t.Fatalf("runs of the first review after a later tick = %+v, want the same one", rows)
	}
	if second := reviewRunRows(t, conn, secondID); len(second) != 1 || second[0].state != "done" {
		t.Fatalf("runs of the second review = %+v, want one, done", second)
	}
	if got := countEvents(receivedEvents(t, me, path), "review"); got != 2 {
		t.Fatalf("review messages at the receiver = %d, want 2 (one per review)", got)
	}
	var deliveries int
	if err := conn.QueryRow(context.Background(), `select count(*) from notification_deliveries where kind = 'review' and route_id = $1`, routeID).Scan(&deliveries); err != nil || deliveries != 2 {
		t.Fatalf("review deliveries = %d, %v; want 2", deliveries, err)
	}
}

// hangingAddr is a loopback address that accepts connections and never answers them, as an agent that is up but
// stuck does. Unlike a closed port it holds the review job's call for as long as the client allows.
func hangingAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	return l.Addr().String()
}

// The agent service is away, refusing at once or accepting and never answering. The review is claimed first; an
// alert on another route raised after that still arrives within seconds, and nothing is sent for the review: its
// row waits for the agent or ends skipped, never done.
func TestReview_AgentDownDelaysNoAlert(t *testing.T) {
	for name, agentAddr := range map[string]func(*testing.T) string{"port closed": closedAddr, "accepts and never answers": hangingAddr} {
		t.Run(name, func(t *testing.T) {
			schema := requireDB(t)
			logs := &syncBuffer{}
			base := startServeLogged(t, map[string]string{
				"HM_DATABASE_URL": schema, "HM_SESSION_KEY": "review-down-session-key",
				"HM_AGENT_URL":     "http://" + agentAddr(t),
				"HM_POLL_SERVICES": "2s", "HM_POLL_RUNNING_TASKS": "2s", "HM_POLL_IDLE_AFTER": "0",
				"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_FAKE_NOTIFY_DEMO": "false",
			}, logs)
			conn, err := pgx.Connect(context.Background(), schema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close(context.Background()) })

			me := signIn(t, base)
			const reviewPath, alertPath = "/fake-notify/webhook/reviewdown", "/fake-notify/webhook/alertdown"
			// The review's own route takes no alert, so the alert route made later is the only one an alert reaches.
			reviewRoute := degradedRoute(base)
			reviewRoute["name"], reviewRoute["url"], reviewRoute["kinds"], reviewRoute["min_severity"] = "review down sink", base+reviewPath, []string{"account_unreachable"}, "critical"
			status, body := me.send(http.MethodPost, "/api/notifications/routes", reviewRoute)
			var created struct {
				ID int64 `json:"id"`
			}
			if (status != http.StatusOK && status != http.StatusCreated) || json.Unmarshal([]byte(body), &created) != nil || created.ID == 0 {
				t.Fatalf("create route = %d %s", status, body)
			}
			// The API refuses a review while the agent cannot say which types it offers, so the row is written directly.
			var reviewID int64
			fiveMinutesAgo := time.Now().UTC().Add(-5 * time.Minute)
			if err := conn.QueryRow(context.Background(), `insert into agent_reviews (name, type, cadence, at_minute, timezone, schedule_changed_at)
				values ('Cost, agent down', 'cost', 'daily', $1, 'UTC', now() - interval '2 days') returning id`, fiveMinutesAgo.Hour()*60+fiveMinutesAgo.Minute()).Scan(&reviewID); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(context.Background(), `insert into agent_review_routes (review_id, route_id) values ($1, $2)`, reviewID, created.ID); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if t.Failed() {
					t.Logf("runs: %+v\nlog:\n%s", reviewRunRows(t, conn, reviewID), logs.String())
				}
			}()

			// The job has the row in hand (running) or has given it back (requested with a retry time) before the alert exists.
			within(t, 90*time.Second, "the review was claimed", func() bool {
				rows := reviewRunRows(t, conn, reviewID)
				return len(rows) == 1 && (rows[0].state != "requested" || rows[0].retryAt != nil)
			})
			routeAt := time.Now()
			if status, body := me.send(http.MethodPost, "/api/notifications/routes", func() map[string]any {
				r := degradedRoute(base)
				r["name"], r["url"] = "alert down sink", base+alertPath
				return r
			}()); status != http.StatusOK && status != http.StatusCreated {
				t.Fatalf("create alert route = %d %s", status, body)
			}
			within(t, 5*time.Second, "alert.raised on the alert route, with the review job stuck or failing", func() bool {
				return countEvents(receivedEvents(t, me, alertPath), "alert.raised") > 0
			})
			t.Logf("alert arrived %v after its route was made", time.Since(routeAt).Round(100*time.Millisecond))

			// Waiting for the agent is requested with a retry time; running means the call is still in flight.
			within(t, 90*time.Second, "the review waits for a retry or is skipped", func() bool {
				rows := reviewRunRows(t, conn, reviewID)
				return len(rows) == 1 && ((rows[0].state == "requested" && rows[0].retryAt != nil) || rows[0].state == "skipped")
			})
			for _, row := range reviewRunRows(t, conn, reviewID) {
				if row.state == "done" || row.state == "running" {
					t.Errorf("run %+v with no agent", row)
				}
			}
			if got := countEvents(receivedEvents(t, me, reviewPath), "review"); got != 0 {
				t.Errorf("review messages at the receiver = %d, want none", got)
			}
			if out := logs.String(); strings.Contains(out, "panic") {
				t.Errorf("the process logged a panic:\n%s", out)
			}
		})
	}
}
