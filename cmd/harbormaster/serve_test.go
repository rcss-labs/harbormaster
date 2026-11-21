package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestServe_FakeAWSBootsAndPolls(t *testing.T) {
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	// A private schema keeps this test away from other packages' data.
	schemaURL := schemaScopedURL(t, dbURL)
	addr := freeAddr(t)
	env := map[string]string{
		"HM_DATABASE_URL": schemaURL, "HM_LISTEN": addr, "HM_COOKIE_SECURE": "false",
		"HM_FAKE_AWS": "1", "HM_POLL_JITTER": "1ms", "HM_LOG_LEVEL": "warn",
	}
	getenv := func(k string) string { return env[k] }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, nil, getenv, io.Discard) }()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	base := "http://" + addr

	// Fake mode migrates and seeds before it listens; a loaded host needs far more than a few seconds.
	deadline := time.Now().Add(60 * time.Second)
	loggedIn := false
	clusters := 0
	for time.Now().Before(deadline) && clusters != 6 {
		time.Sleep(150 * time.Millisecond)
		if !loggedIn {
			body, _ := json.Marshal(map[string]string{"username": "sam", "password": "harbormaster-e2e-1"})
			resp, err := client.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
			if err != nil {
				continue
			}
			resp.Body.Close()
			loggedIn = resp.StatusCode == http.StatusOK
			continue
		}
		resp, err := client.Get(base + "/api/clusters")
		if err != nil {
			continue
		}
		var out struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		clusters = len(out.Items)
	}
	if !loggedIn || clusters != 6 {
		t.Errorf("loggedIn=%v clusters=%d; want login ok and 6 clusters within 10s", loggedIn, clusters)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServe returned %v after cancel", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runServe did not shut down")
	}
}

// schemaScopedURL creates a throwaway schema and returns dbURL with search_path pointing at it.
func schemaScopedURL(t *testing.T, dbURL string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("t_serve_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
		conn.Close(context.Background())
	})
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// serveUnderTest starts runServe with env on a free port and waits until /healthz answers.
// The returned stop cancels the server and waits for it to shut down.
func serveUnderTest(t *testing.T, env map[string]string) (base string, stop func()) {
	t.Helper()
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	addr := freeAddr(t)
	env["HM_DATABASE_URL"] = schemaScopedURL(t, dbURL)
	env["HM_LISTEN"] = addr
	env["HM_BASE_URL"] = "http://" + addr
	env["HM_COOKIE_SECURE"] = "false"
	env["HM_POLL_JITTER"] = "1ms"
	env["HM_LOG_LEVEL"] = "warn"
	getenv := func(k string) string { return env[k] }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, nil, getenv, io.Discard) }()
	base = "http://" + addr
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if resp, err := client.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("server did not start within 60s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return base, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runServe returned %v after cancel", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("runServe did not shut down")
		}
	}
}

// getFakeIdPMetadata returns the status and whether the body is SAML metadata from the fake IdP.
func getFakeIdPMetadata(t *testing.T, base string) (int, bool) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/fake-idp/metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, bytes.Contains(body, []byte("EntityDescriptor"))
}

func TestServe_FakeAWSMountsFakeIdP(t *testing.T) {
	base, stop := serveUnderTest(t, map[string]string{"HM_FAKE_AWS": "1"})
	defer stop()
	status, isMetadata := getFakeIdPMetadata(t, base)
	if status != http.StatusOK || !isMetadata {
		t.Errorf("GET /fake-idp/metadata in fake mode: status %d, metadata=%v; want 200 with EntityDescriptor", status, isMetadata)
	}
}

// Production must never serve the fake IdP. Today the SPA catch-all answers 200 with index.html for
// this extensionless path; the assertion only forbids the fake IdP's metadata.
func TestServe_ProductionDoesNotMountFakeIdP(t *testing.T) {
	base, stop := serveUnderTest(t, map[string]string{})
	defer stop()
	status, isMetadata := getFakeIdPMetadata(t, base)
	if isMetadata {
		t.Errorf("GET /fake-idp/metadata without fake AWS returned fake IdP metadata (status %d)", status)
	}
}

// Fake mode turns image scanning on with no HM_SCAN_* variables, so the demo and the e2e suite show the feature.
func TestServe_FakeAWSHasImageScanningOn(t *testing.T) {
	base, stop := serveUnderTest(t, map[string]string{"HM_FAKE_AWS": "1"})
	defer stop()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	body, _ := json.Marshal(map[string]string{"username": "sam", "password": "harbormaster-e2e-1"})
	resp, err := client.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %v, %v", resp, err)
	}
	resp.Body.Close()
	resp, err = client.Get(base + "/api/security")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Enabled {
		t.Fatalf("GET /api/security enabled = %v, err = %v; want true in fake mode", out.Enabled, err)
	}
}

// A single process (role=all) holds every slice lease and the coordinator lease, and the jobs behind the coordinator
// lease still do their work: the demo's failing services produce a delivered notification, a requested scan produces
// a run, and every account x region has a poll row and an owner.
func TestServe_SingleProcessStillSendsNotificationsAndScans(t *testing.T) {
	env := map[string]string{"HM_FAKE_AWS": "1", "HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_SESSION_KEY": "test-session-key", "HM_SCAN_INTERVAL": "1s"}
	_, stop := serveUnderTest(t, env)
	defer stop()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, env["HM_DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	count := func(query string) int {
		var n int
		if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
			return -1
		}
		return n
	}
	waitUntil := func(what string, cond func() bool) {
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

	waitUntil("every slice to be owned and polled", func() bool {
		slices := count(`select count(*) from poll_leases where kind = 'slice'`)
		return slices > 0 && count(`select count(*) from poll_leases where kind = 'slice' and owner_id is not null`) == slices &&
			count(`select count(*) from poll_loops where tier = 'services' and last_ok_at is not null`) >= slices
	})
	waitUntil("the coordinator lease", func() bool {
		return count(`select count(*) from poll_leases where kind = 'coordinator' and owner_id is not null`) == 1
	})
	waitUntil("a delivered notification", func() bool {
		return count(`select count(*) from notification_deliveries where status = 'delivered'`) > 0
	})

	// Ask for a scan of an image of the demo's homelab account; the coordinator's scan runner launches it.
	var account string
	if err := conn.QueryRow(ctx, `select account_id from aws_accounts where enabled order by id limit 1`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, env["HM_DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	digest := "sha256:" + fmt.Sprintf("%064x", 42)
	if _, _, err := st.UpsertRequestedScanImage(ctx, store.ScanImage{Digest: digest, ImageRef: account + ".dkr.ecr.us-east-1.amazonaws.com/edge-nginx",
		Tag: "1.0", RegistryKind: scan.KindECR, RegistryAccountID: account, RegistryRegion: "us-east-1", Repository: "edge-nginx"}, time.Now()); err != nil {
		t.Fatalf("request a scan: %v", err)
	}
	waitUntil("a scan run", func() bool { return count(`select count(*) from scan_runs`) > 0 })
}

// Shutdown must not cancel in-flight requests: other handlers (deploys, writes, exec) keep the full drain.
// Only the OnShutdown hook (server.BeginDrain) signals, and that reaches log searches alone.
func TestHTTPServer_ShutdownDrainsInFlightRequestsAndRunsOnShutdown(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	ctxErr := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		ctxErr <- r.Context().Err()
	})}
	hookRan := make(chan struct{})
	srv.RegisterOnShutdown(func() { close(hookRan) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()
	go func() {
		if resp, err := http.Get("http://" + listener.Addr().String()); err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()
	select {
	case <-hookRan:
	case <-time.After(2 * time.Second):
		t.Fatal("RegisterOnShutdown hook did not run when Shutdown began")
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-ctxErr:
		t.Fatalf("in-flight request context ended at Shutdown (err %v); it must keep draining", err)
	default:
	}
	close(finish)
	if err := <-ctxErr; err != nil {
		t.Errorf("request context error = %v, want nil", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
