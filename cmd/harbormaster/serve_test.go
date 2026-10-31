package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
	"time"
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
			body, _ := json.Marshal(map[string]string{"username": "pavel", "password": "harbormaster-e2e-1"})
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
