package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv/kvtest"
)

// startServeWithKV runs `serve --fake-aws` on a private schema with extra settings and returns its base URL. The
// process stops with the test. The kv metrics live in the process-wide registry; stopping the store clears them, so the tests
// do not depend on order.
func startServeWithKV(t *testing.T, extra map[string]string) string {
	t.Helper()
	return startServeLogged(t, extra, io.Discard)
}

// startServeLogged is startServeWithKV with the process's log lines written to logs.
func startServeLogged(t *testing.T, extra map[string]string, logs io.Writer) string {
	t.Helper()
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	addr := freeAddr(t)
	env := map[string]string{
		"HM_DATABASE_URL": schemaScopedURL(t, dbURL), "HM_LISTEN": addr, "HM_BASE_URL": "http://" + addr, "HM_COOKIE_SECURE": "false",
		"HM_FAKE_AWS": "1", "HM_POLL_JITTER": "1ms", "HM_LOG_LEVEL": "warn", "HM_METRICS_TOKEN": "kv-test-token",
	}
	for k, v := range extra {
		env[k] = v
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, nil, func(k string) string { return env[k] }, logs) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runServe returned %v after cancel", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("runServe did not shut down")
		}
	})
	base := "http://" + addr
	waitForStatus(t, base+"/healthz", http.StatusOK)
	return base
}

func waitForStatus(t *testing.T, url string, want int) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never answered %d", url, want)
}

func scrapeMetrics(t *testing.T, base string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer kv-test-token")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestServe_KVUnsetStartsWithoutStore(t *testing.T) {
	base := startServeWithKV(t, nil)
	if out := scrapeMetrics(t, base); strings.Contains(out, "harbormaster_kv_") {
		t.Fatalf("no store is configured, but /metrics has kv series:\n%s", out)
	}
}

func TestServe_KVErrorIsScrubbed(t *testing.T) {
	env := map[string]string{
		"HM_DATABASE_URL": "postgres://example", "HM_FAKE_AWS": "1",
		"HM_KV_URL": "redis://user:s3cret@cache.internal:notaport/0",
	}
	err := runServe(t.Context(), nil, func(k string) string { return env[k] }, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a malformed HM_KV_URL")
	}
	if !strings.Contains(err.Error(), "kv url") {
		t.Fatalf("the error should come from parsing HM_KV_URL, got: %v", err)
	}
	for _, leak := range []string{"s3cret", "cache.internal"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("the error leaks %q: %v", leak, err)
		}
	}
}

func TestServe_KVUnreachableStillStarts(t *testing.T) {
	base := startServeWithKV(t, map[string]string{"HM_KV_URL": "redis://127.0.0.1:1"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := scrapeMetrics(t, base)
		if strings.Contains(out, "harbormaster_kv_up 0") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected harbormaster_kv_up 0:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestServe_KVUpWithRealStore(t *testing.T) {
	urls := kvtest.URLs()
	if len(urls) == 0 {
		t.Skip(kvtest.EnvVar + " not set")
	}
	base := startServeWithKV(t, map[string]string{"HM_KV_URL": urls[0], "HM_KV_PREFIX": "serve-test"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := scrapeMetrics(t, base)
		if strings.Contains(out, "harbormaster_kv_up 1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected harbormaster_kv_up 1 within 10s:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
