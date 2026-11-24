package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/hmtools/hmtoolstest"
)

func newTestRunner(t *testing.T, handler http.HandlerFunc) (ToolRunner, *hmtoolstest.RecordingServer) {
	t.Helper()
	server := hmtoolstest.NewRecordingServer(t, handler)
	up, err := hmtools.NewUpstream(server.URL, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewUpstreamToolRunner(up, 100_000), server
}

func TestUpstreamToolRunner_OneCallOneRequest(t *testing.T) {
	runner, server := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusters":2}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", map[string]any{"region": "eu-west-1"})
	if isError || text != `{"clusters":2}` {
		t.Fatalf("Run = %q, %v", text, isError)
	}
	seen := server.Seen()
	if len(seen) != 1 {
		t.Fatalf("%d upstream requests, want 1", len(seen))
	}
	if seen[0].Method != "GET" || seen[0].EscapedPath != "/api/overview" || seen[0].Query != "region=eu-west-1" {
		t.Errorf("request = %+v", seen[0])
	}
	if got := seen[0].Header.Get("Authorization"); got != "Bearer run-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestUpstreamToolRunner_NothingUnknownOrInvalidReachesHarbormaster(t *testing.T) {
	runner, server := newTestRunner(t, nil)
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"unknown tool", "delete_service", map[string]any{"service_id": "x"}},
		{"unknown argument", "get_overview", map[string]any{"path": "/api/admin"}},
		{"missing argument", "get_service_timeline", map[string]any{}},
	}
	for _, tc := range cases {
		if text, isError := runner.Run(context.Background(), "run-token", tc.tool, tc.args); !isError || text == "" {
			t.Errorf("%s: Run = %q, %v, want a tool error", tc.name, text, isError)
		}
	}
	if n := len(server.Seen()); n != 0 {
		t.Errorf("%d upstream requests, want 0", n)
	}
}

func TestUpstreamToolRunner_429CarriesRetryAfter(t *testing.T) {
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"Slow down."}}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if !isError || !strings.Contains(text, "Slow down.") || !strings.Contains(text, "7") {
		t.Errorf("Run = %q, %v, want the upstream message and the retry-after", text, isError)
	}
}

func TestUpstreamToolRunner_ServerErrorsHideTheBody(t *testing.T) {
	runner, _ := newTestRunner(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"boom","message":"panic at db.internal:5432"}}`))
	})
	text, isError := runner.Run(context.Background(), "run-token", "get_overview", nil)
	if !isError || strings.Contains(text, "db.internal") {
		t.Errorf("Run = %q, %v, want a generic unavailable error", text, isError)
	}
}
