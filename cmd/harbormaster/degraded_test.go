package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

const (
	degradedPassword = "harbormaster-e2e-1" // the seeded users' password in fake mode
	// A staging service and its cluster in the fake estate (see internal/devseed); ids are base64url of
	// account|region|cluster[|service].
	degradedServiceID = "NzMxMDIyODQ5OTA2fHVzLWVhc3QtMXxzdGFnaW5nLXdlYnxhcGk"
	degradedClusterID = "NzMxMDIyODQ5OTA2fHVzLWVhc3QtMXxzdGFnaW5nLXdlYg"
	degradedWindow    = 15 * time.Second
)

// session is a signed-in API client of one running server.
type session struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func signIn(t *testing.T, base string) *session {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{t: t, base: base, http: &http.Client{Timeout: 5 * time.Second, Jar: jar}}
	status, body := s.send(http.MethodPost, "/api/auth/login", map[string]string{"username": "sam", "password": degradedPassword})
	if status != http.StatusOK {
		t.Fatalf("sign-in = %d %s", status, body)
	}
	var out struct {
		User struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.User.CSRFToken == "" {
		t.Fatalf("sign-in answered without a CSRF token: %s", body)
	}
	s.csrf = out.User.CSRFToken
	return s
}

// send makes one API request and returns the status and body. A transport error fails the test.
func (s *session) send(method, path string, payload any) (int, string) {
	s.t.Helper()
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, reader)
	if err != nil {
		s.t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.csrf != "" {
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, string(body)
}

type focusAnswer struct {
	State       string     `json:"state"`
	RefreshedAt *time.Time `json:"refreshed_at"`
}

func (s *session) focus(open bool) focusAnswer {
	s.t.Helper()
	status, body := s.send(http.MethodPost, "/api/focus", map[string]any{"kind": "service", "id": degradedServiceID, "open": open})
	if status != http.StatusOK {
		s.t.Fatalf("POST /api/focus = %d %s", status, body)
	}
	var out focusAnswer
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		s.t.Fatalf("focus answer: %v: %s", err, body)
	}
	return out
}

// pageWithoutClocks returns a page body without what changes on every poll or every read: the poll time and the
// time series that grow with each cycle. What is left is the estate as Postgres holds it.
func pageWithoutClocks(t *testing.T, body string) string {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("page is not JSON: %v", err)
	}
	for _, key := range []string{"polled_at", "counts_history_72h", "tasks_history_72h", "stops_per_hour"} {
		delete(page, key)
	}
	out, err := json.Marshal(page) // map keys are sorted, so equal pages encode equally
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// metricSum adds the samples of one series whose line contains every filter.
func metricSum(out, name string, filters ...string) float64 {
	var sum float64
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		matches := true
		for _, f := range filters {
			if !strings.Contains(line, f) {
				matches = false
			}
		}
		if !matches {
			continue
		}
		if v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64); err == nil {
			sum += v
		}
	}
	return sum
}

// within polls cond every 200 ms until it holds, and fails with what when it does not within the window.
func within(t *testing.T, window time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("within %v: %s did not hold", window, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func lastOK(t *testing.T, conn *pgx.Conn, tier string) time.Time {
	t.Helper()
	var at *time.Time
	if err := conn.QueryRow(context.Background(), `select max(last_ok_at) from poll_loops where tier = $1`, tier).Scan(&at); err != nil {
		t.Fatalf("poll_loops: %v", err)
	}
	if at == nil {
		return time.Time{}
	}
	return *at
}

// The store is cut under a running process and restored. Everything that matters to people keeps working from
// Postgres and the in-memory limits; only freshness is lost, and it comes back by itself.
func TestDegraded_StoreCutMidRun(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	if os.Getenv("HM_TEST_DATABASE_URL") == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	for _, raw := range urls {
		raw := raw
		t.Run(degradedLabel(t, raw), func(t *testing.T) {
			schema := requireDB(t) // a private schema per engine
			proxied, cut, restore := kvtest.Proxy(t, raw)
			base := startServeWithKV(t, map[string]string{
				"HM_DATABASE_URL": schema, "HM_KV_URL": proxied, "HM_KV_PREFIX": "degraded-" + strconv.FormatInt(time.Now().UnixNano(), 36),
				"HM_POLL_SERVICES": "2s", "HM_POLL_RUNNING_TASKS": "2s", "HM_POLL_IDLE_AFTER": "0",
				"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_FAKE_NOTIFY_DEMO": "false",
				"HM_SESSION_KEY": "degraded-session-key",
			})
			conn, err := pgx.Connect(context.Background(), schema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close(context.Background()) })

			me := signIn(t, base)
			within(t, 60*time.Second, "harbormaster_kv_up 1", func() bool {
				return metricSum(scrapeMetrics(t, base), "harbormaster_kv_up") == 1
			})
			me.focus(true)
			var before focusAnswer
			within(t, 30*time.Second, "a live focus with a read time", func() bool {
				before = me.focus(false)
				return before.State == "live" && before.RefreshedAt != nil
			})
			within(t, 30*time.Second, "a polled service page", func() bool {
				status, _ := me.send(http.MethodGet, "/api/services/"+degradedServiceID, nil)
				return status == http.StatusOK
			})
			_, serviceBefore := me.send(http.MethodGet, "/api/services/"+degradedServiceID, nil)
			_, clusterBefore := me.send(http.MethodGet, "/api/clusters/"+degradedClusterID, nil)
			servicesTierBefore := lastOK(t, conn, "services")

			cut()

			within(t, degradedWindow, "harbormaster_kv_up 0 after the cut", func() bool {
				return metricSum(scrapeMetrics(t, base), "harbormaster_kv_up") == 0
			})
			t.Run("pages read Postgres and are unchanged", func(t *testing.T) {
				status, service := me.send(http.MethodGet, "/api/services/"+degradedServiceID, nil)
				if status != http.StatusOK {
					t.Fatalf("service page = %d %s", status, service)
				}
				if got, want := pageWithoutClocks(t, service), pageWithoutClocks(t, serviceBefore); got != want {
					t.Errorf("service page changed with the store down:\n got %s\nwant %s", got, want)
				}
				status, cluster := me.send(http.MethodGet, "/api/clusters/"+degradedClusterID, nil)
				if status != http.StatusOK {
					t.Fatalf("cluster page = %d %s", status, cluster)
				}
				if got, want := pageWithoutClocks(t, cluster), pageWithoutClocks(t, clusterBefore); got != want {
					t.Errorf("cluster page changed with the store down:\n got %s\nwant %s", got, want)
				}
			})
			t.Run("focus answers unavailable", func(t *testing.T) {
				if got := me.focus(true); got.State != "unavailable" {
					t.Errorf("focus state = %q, want unavailable", got.State)
				}
			})
			t.Run("limits fall back to memory", func(t *testing.T) {
				memory := func() float64 {
					return metricSum(scrapeMetrics(t, base), "harbormaster_limit_decisions_total", `backend="memory"`, `result="allowed"`)
				}
				start := memory()
				// The log search takes the same per-user concurrency slot as a terminal and a rate token.
				status, body := me.send(http.MethodPost, "/api/log-search", map[string]any{
					"service_ids": []string{degradedServiceID}, "range": "1h", "query": "fields @message | limit 5",
				})
				if status != http.StatusOK {
					t.Fatalf("log search with the store down = %d %s", status, body)
				}
				if got := memory(); got <= start {
					t.Errorf("memory-backend limit decisions = %v, want more than %v", got, start)
				}
			})
			t.Run("polling keeps advancing", func(t *testing.T) {
				// Whether a cycle already finished depends on how long the subtests above took, so wait for it rather than
				// assume it: under load the first check raced the tier and failed on a healthy poller.
				var duringCut time.Time
				within(t, degradedWindow, "the services tier to cycle after the last read before the cut", func() bool {
					duringCut = lastOK(t, conn, "services")
					return duringCut.After(servicesTierBefore)
				})
				within(t, degradedWindow, "the services tier to complete another cycle while the store is down", func() bool {
					return lastOK(t, conn, "services").After(duringCut)
				})
			})
			t.Run("notifications are still delivered", func(t *testing.T) {
				// A route made now sends the failing demo services' alerts: the dispatcher needs neither the store nor
				// the user to be live.
				status, body := me.send(http.MethodPost, "/api/notifications/routes", degradedRoute(base))
				if status != http.StatusOK && status != http.StatusCreated {
					t.Fatalf("create route = %d %s", status, body)
				}
				within(t, degradedWindow, "an alert.raised delivery on the fake sink", func() bool {
					status, body := me.send(http.MethodGet, "/fake-notify/received", nil)
					if status != http.StatusOK {
						return false
					}
					var received []struct {
						Path string `json:"path"`
						Body struct {
							Event string `json:"event"`
						} `json:"body"`
					}
					if json.Unmarshal([]byte(body), &received) != nil {
						return false
					}
					for _, entry := range received {
						if entry.Path == "/fake-notify/webhook/degraded" && entry.Body.Event == "alert.raised" {
							return true
						}
					}
					return false
				})
			})
			t.Run("sign-out and sign-in work", func(t *testing.T) {
				if status, body := me.send(http.MethodPost, "/api/auth/logout", nil); status != http.StatusNoContent && status != http.StatusOK {
					t.Fatalf("sign-out = %d %s", status, body)
				}
				again := signIn(t, base)
				if status, _ := again.send(http.MethodGet, "/api/me", nil); status != http.StatusOK {
					t.Fatalf("/api/me after signing in again = %d", status)
				}
				me = again
			})

			restore()

			within(t, degradedWindow, "harbormaster_kv_up 1 after the restore", func() bool {
				return metricSum(scrapeMetrics(t, base), "harbormaster_kv_up") == 1
			})
			hitsAtRestore := metricSum(scrapeMetrics(t, base), "harbormaster_snapshot_cache_requests_total", `result="hit"`)
			within(t, degradedWindow, "a live focus heartbeat with a newer read time", func() bool {
				got := me.focus(false)
				return got.State == "live" && got.RefreshedAt != nil && got.RefreshedAt.After(*before.RefreshedAt)
			})
			within(t, degradedWindow, "a snapshot cache hit counted again", func() bool {
				me.send(http.MethodGet, "/api/services/"+degradedServiceID, nil)
				me.send(http.MethodGet, "/api/clusters/"+degradedClusterID, nil)
				return metricSum(scrapeMetrics(t, base), "harbormaster_snapshot_cache_requests_total", `result="hit"`) > hitsAtRestore
			})
		})
	}
}

func degradedLabel(t *testing.T, raw string) string {
	t.Helper()
	o, err := kv.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return "store-" + o.Address[strings.LastIndexByte(o.Address, ':')+1:]
}

func degradedRoute(base string) map[string]any {
	kinds := []string{"service_failing", "service_degraded", "restart_loop", "unhealthy_task", "deployment_failed", "cannot_place", "account_unreachable"}
	return map[string]any{
		"name": "degraded sink", "enabled": true, "destination": "webhook", "label": "", "recipients": []string{},
		"pagerduty_region": "", "chat_id": "", "thread_id": nil, "url": fmt.Sprintf("%s/fake-notify/webhook/degraded", base),
		"min_severity": "warning", "kinds": kinds, "repeat_seconds": 0, "quiet_enabled": false, "quiet_timezone": "UTC",
		"notify_resolved": false, "scope": []any{}, "fallback": false,
	}
}
