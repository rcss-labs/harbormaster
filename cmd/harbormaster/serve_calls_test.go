package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// In fake mode the SDK middleware is bypassed, so the fake feeds the AWS call counter itself; without it a local run
// and the e2e suite could not show how many AWS reads a page caused.
func TestServe_FakeModeCountsAWSCalls(t *testing.T) {
	dbURL := os.Getenv("HM_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	env := map[string]string{
		"HM_DATABASE_URL": schemaScopedURL(t, dbURL), "HM_LISTEN": freeAddr(t), "HM_COOKIE_SECURE": "false",
		"HM_FAKE_AWS": "1", "HM_POLL_JITTER": "1ms", "HM_LOG_LEVEL": "warn",
	}
	getenv := func(k string) string { return env[k] }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, nil, getenv, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	})

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for key, n := range telemetry.Default.Snapshot().AWSCalls {
			if n > 0 && strings.HasSuffix(key, "|DescribeServices|ok|tier") {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("no tier-labelled DescribeServices call was counted in fake mode: %v", telemetry.Default.Snapshot().AWSCalls)
}
