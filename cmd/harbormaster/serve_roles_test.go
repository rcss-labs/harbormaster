package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

const rolesMetricsToken = "roles-metrics-token"

// roleProc is one serve process under test.
type roleProc struct {
	base string
	stop func()
}

// startRole runs `serve` for one role in this test process, on the schema schemaURL, and waits until /healthz answers
// 200. webBase is the web task's address (empty for the first task); every task shares one HM_BASE_URL so the demo's
// notification receiver, which lives on the web task, is where the coordinator delivers.
func startRole(t *testing.T, schemaURL, role string, flagStyle bool, extra map[string]string) *roleProc {
	t.Helper()
	addr := freeAddr(t)
	env := map[string]string{
		"HM_DATABASE_URL": schemaURL, "HM_LISTEN": addr, "HM_BASE_URL": "http://" + addr, "HM_COOKIE_SECURE": "false",
		"HM_FAKE_AWS": "1", "HM_POLL_JITTER": "1ms", "HM_LOG_LEVEL": "warn", "HM_METRICS_TOKEN": rolesMetricsToken,
	}
	// Split roles refuse to start without the shared store. Every task of one test shares a prefix (derived from its
	// schema) so they see the same focus registry and kicks, and tests do not see each other's.
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set: split roles need HM_KV_URL")
	}
	sum := sha1.Sum([]byte(schemaURL))
	env["HM_KV_URL"], env["HM_KV_PREFIX"] = urls[0], "roles-"+hex.EncodeToString(sum[:6])
	for k, v := range extra {
		env[k] = v
	}
	var args []string
	if flagStyle {
		args = []string{"--role=" + role}
	} else {
		env["HM_ROLE"] = role
	}
	getenv := func(k string) string { return env[k] }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, args, getenv, io.Discard) }()
	p := &roleProc{base: "http://" + addr}
	stopped := false
	p.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: runServe returned %v after cancel", role, err)
			}
		case <-time.After(30 * time.Second):
			t.Errorf("%s: runServe did not shut down", role)
		}
	}
	t.Cleanup(p.stop)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("%s: runServe exited early: %v", role, err)
		default:
		}
		if resp, err := client.Get(p.base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: /healthz did not answer 200 within 60s", role)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, string(body)
}

// dbCounter returns a function that runs a count query on the shared schema.
func dbCounter(t *testing.T, schemaURL string) func(query string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return func(query string) int {
		var n int
		if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
			return -1
		}
		return n
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func requireDB(t *testing.T) string {
	t.Helper()
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	return schemaScopedURL(t, dbURL)
}

func TestServe_RoleFlagOverridesEnv(t *testing.T) {
	schema := requireDB(t)
	p := startRole(t, schema, "web", true, map[string]string{"HM_ROLE": "all"})
	// The flag says web, the variable says all: a web task builds no lease manager, so no member row exists.
	_, metrics := get(t, p.base+"/metrics", rolesMetricsToken)
	if !strings.Contains(metrics, `harbormaster_role_info{role="web",`) {
		t.Fatalf("role_info does not say web:\n%s", grepLines(metrics, "role_info"))
	}
	if n := dbCounter(t, schema)(`select count(*) from poll_members`); n != 0 {
		t.Fatalf("poll_members has %d rows; --role=web must override HM_ROLE=all", n)
	}
}

func grepLines(text, needle string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, needle) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// A split role without a shared store would run on per-task memory: focus heartbeats posted to a web task never reach
// a poller, yet the API would still report a page as live. Start must refuse instead.
func TestServe_SplitRolesRequireKV(t *testing.T) {
	for _, role := range []string{"web", "poller", "coordinator"} {
		t.Run(role, func(t *testing.T) {
			env := map[string]string{"HM_DATABASE_URL": "postgres://example/none", "HM_ROLE": role, "HM_FAKE_AWS": "1"}
			err := runServe(t.Context(), nil, func(k string) string { return env[k] }, io.Discard)
			var usage errUsage
			if !errors.As(err, &usage) || !strings.Contains(usage.msg, "HM_KV_URL") {
				t.Fatalf("err = %v, want a usage error naming HM_KV_URL", err)
			}
		})
	}
}

func TestServe_UnknownRoleIsAUsageError(t *testing.T) {
	getenv := func(k string) string {
		if k == "HM_DATABASE_URL" {
			return "postgres://example/none"
		}
		return ""
	}
	err := runServe(t.Context(), []string{"--role=worker"}, getenv, io.Discard)
	var usage errUsage
	if !errors.As(err, &usage) || !strings.Contains(usage.msg, "--role") {
		t.Fatalf("err = %v, want a usage error naming --role", err)
	}
}

// A web task shows the security feature (HM_SCAN_ENABLED) but never launches scans, so it needs none of the launch
// settings; a task that coordinates does.
func TestServe_WebRoleDoesNotNeedScanLaunchSettings(t *testing.T) {
	schema := requireDB(t)
	extra := map[string]string{"HM_SCAN_ENABLED": "true", "HM_FAKE_AWS": ""}
	web := startRole(t, schema, "web", true, extra)
	web.stop()

	env := map[string]string{"HM_DATABASE_URL": schema, "HM_LISTEN": freeAddr(t), "HM_SCAN_ENABLED": "true", "HM_ROLE": "coordinator"}
	err := runServe(t.Context(), nil, func(k string) string { return env[k] }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HM_SCAN") {
		t.Fatalf("coordinator with scanning on and no launch settings: err = %v, want today's launch-setting message", err)
	}
}

func TestServe_WebRoleRunsNoPollerAndNoJobs(t *testing.T) {
	schema := requireDB(t)
	web := startRole(t, schema, "web", true, nil)
	if status, _ := get(t, web.base+"/api/me", ""); status != http.StatusUnauthorized {
		t.Fatalf("/api/me = %d, want 401: the API is served", status)
	}
	time.Sleep(3 * time.Second)
	count := dbCounter(t, schema)
	if n := count(`select count(*) from poll_leases where owner_id is not null`); n != 0 {
		t.Errorf("%d leases are owned; a web task must own none", n)
	}
	if n := count(`select count(*) from poll_members where 'poller' = any(roles) or 'coordinator' = any(roles)`); n != 0 {
		t.Errorf("%d poller or coordinator members; a web task joins neither", n)
	}
	_, metrics := get(t, web.base+"/metrics", rolesMetricsToken)
	for _, want := range []string{"harbormaster_lease_slices_owned 0", "harbormaster_coordinator_active 0"} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestServe_PollerRoleServesOnlyHealthAndMetrics(t *testing.T) {
	schema := requireDB(t)
	startRole(t, schema, "web", true, nil) // seeds the demo accounts
	poller := startRole(t, schema, "poller", true, nil)

	if status, _ := get(t, poller.base+"/metrics", rolesMetricsToken); status != http.StatusOK {
		t.Errorf("/metrics with the token = %d, want 200", status)
	}
	for _, path := range []string{"/api/me", "/", "/fake-idp/metadata", "/fake-notify/x"} {
		if status, _ := get(t, poller.base+path, ""); status != http.StatusNotFound {
			t.Errorf("poller %s = %d, want 404", path, status)
		}
	}
	count := dbCounter(t, schema)
	waitUntil(t, "every slice to have an owner", func() bool {
		slices := count(`select count(*) from poll_leases where kind = 'slice'`)
		return slices > 0 && count(`select count(*) from poll_leases where kind = 'slice' and owner_id is not null`) == slices
	})
	if n := count(`select count(*) from poll_leases where kind = 'coordinator' and owner_id is not null`); n != 0 {
		t.Errorf("a poller holds the coordinator lease (%d)", n)
	}
}

func TestServe_CoordinatorRoleServesOnlyHealthAndMetrics(t *testing.T) {
	schema := requireDB(t)
	startRole(t, schema, "web", true, nil)
	coord := startRole(t, schema, "coordinator", true, nil)

	if status, _ := get(t, coord.base+"/metrics", rolesMetricsToken); status != http.StatusOK {
		t.Errorf("/metrics with the token = %d, want 200", status)
	}
	for _, path := range []string{"/api/me", "/", "/fake-idp/metadata"} {
		if status, _ := get(t, coord.base+path, ""); status != http.StatusNotFound {
			t.Errorf("coordinator %s = %d, want 404", path, status)
		}
	}
	count := dbCounter(t, schema)
	waitUntil(t, "the coordinator lease", func() bool {
		return count(`select count(*) from poll_leases where kind = 'coordinator' and owner_id is not null`) == 1
	})
	time.Sleep(2 * time.Second)
	if n := count(`select count(*) from poll_leases where kind = 'slice' and owner_id is not null`); n != 0 {
		t.Errorf("%d slices owned by a coordinator", n)
	}
}

// One poller and one coordinator do what role all did: every slice is polled, and the demo's failing services produce a
// delivered notification (the dispatcher runs only on the coordinator).
func TestServe_PollerAndCoordinatorTogetherCoverWhatAllDid(t *testing.T) {
	schema := requireDB(t)
	extra := map[string]string{"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_SESSION_KEY": "test-session-key"}
	web := startRole(t, schema, "web", true, extra)
	// One base URL for every task: the coordinator delivers to the receiver that the web task serves.
	shared := map[string]string{"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_SESSION_KEY": "test-session-key", "HM_BASE_URL": web.base}
	startRole(t, schema, "poller", true, shared)
	startRole(t, schema, "coordinator", true, shared)

	count := dbCounter(t, schema)
	waitUntil(t, "every slice polled and owned", func() bool {
		slices := count(`select count(*) from poll_leases where kind = 'slice'`)
		return slices > 0 && count(`select count(*) from poll_leases where kind = 'slice' and owner_id is not null`) == slices &&
			count(`select count(*) from poll_loops where tier = 'services' and last_ok_at is not null`) >= slices
	})
	waitUntil(t, "the coordinator lease", func() bool {
		return count(`select count(*) from poll_leases where kind = 'coordinator' and owner_id is not null`) == 1
	})
	waitUntil(t, "a delivered notification", func() bool {
		return count(`select count(*) from notification_deliveries where status = 'delivered'`) > 0
	})
}

// A manager that has not finished its first tick yet counts from process start: healthy shortly after it, stale after.
func TestLeaseLoopHealthy_NoTickYetIsHealthyShortlyAfterStart(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	noTick := func() time.Time { return time.Time{} }
	soon := leaseLoopHealthy(noTick, now.Add(-5*time.Second), func() time.Time { return now }, leaseLoopStaleAfter)
	if err := soon(); err != nil {
		t.Errorf("5 s after start with no tick: %v", err)
	}
	late := leaseLoopHealthy(noTick, now.Add(-2*leaseLoopStaleAfter), func() time.Time { return now }, leaseLoopStaleAfter)
	if err := late(); err == nil {
		t.Error("two limits after start with no tick must be stale")
	}
}

// ECS replaces a poller after this long without a tick: the value is part of the contract, so it is pinned.
func TestLeaseLoopStaleAfter_IsSixtySeconds(t *testing.T) {
	if leaseLoopStaleAfter != 60*time.Second {
		t.Fatalf("leaseLoopStaleAfter = %v, want 60s", leaseLoopStaleAfter)
	}
}

func TestLeaseLoopHealthy_GoesStaleAfterTheLimit(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	var last time.Time
	check := leaseLoopHealthy(func() time.Time { return last }, started, func() time.Time { return now }, time.Minute)
	if err := check(); err == nil {
		t.Error("no tick since a start 10 minutes ago must be stale")
	}
	last = now.Add(-59 * time.Second)
	if err := check(); err != nil {
		t.Errorf("a tick 59 s ago: %v", err)
	}
	last = now.Add(-61 * time.Second)
	if err := check(); err == nil {
		t.Error("a tick 61 s ago must be stale")
	}
	if err := leaseLoopHealthy(nil, started, func() time.Time { return now }, time.Minute)(); err != nil {
		t.Errorf("a role without the check: %v", err)
	}
}
