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

	deadline := time.Now().Add(10 * time.Second)
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
			t.Fatal("server did not start within 10s")
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
