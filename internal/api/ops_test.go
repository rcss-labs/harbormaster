package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func opsGet(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newOps(ping func(context.Context) error, healthy func() error) http.Handler {
	return OpsHandler(ping, healthy, "tok", telemetry.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOpsHandler_HealthzIsOKWhenEverythingIsHealthy(t *testing.T) {
	h := newOps(func(context.Context) error { return nil }, func() error { return nil })
	if rec := opsGet(h, "/healthz", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestOpsHandler_HealthzIs503WhenTheLeaseLoopIsStale(t *testing.T) {
	h := newOps(func(context.Context) error { return nil }, func() error { return errors.New("lease manager stalled") })
	rec := opsGet(h, "/healthz", "")
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "stalled") {
		t.Fatalf("healthz = %d %q, want 503 with a body that does not leak the reason", rec.Code, rec.Body.String())
	}
}

func TestOpsHandler_HealthzIs503WhenTheDatabaseIsDown(t *testing.T) {
	h := newOps(func(context.Context) error { return errors.New("connection refused") }, func() error { return nil })
	if rec := opsGet(h, "/healthz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d, want 503", rec.Code)
	}
}

func TestOpsHandler_MetricsNeedTheToken(t *testing.T) {
	h := newOps(func(context.Context) error { return nil }, func() error { return nil })
	if rec := opsGet(h, "/metrics", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("metrics without a token = %d, want 401", rec.Code)
	}
	rec := opsGet(h, "/metrics", "tok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "harbormaster_build_info") {
		t.Errorf("metrics with the token = %d", rec.Code)
	}
}

func TestOpsHandler_UnknownPathsAre404(t *testing.T) {
	h := newOps(func(context.Context) error { return nil }, func() error { return nil })
	for _, path := range []string{"/", "/api/me", "/fake-idp/metadata", "/fake-notify/x", "/index.html", "/healthz/x"} {
		rec := opsGet(h, path, "")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s = %d %q, want a JSON 404", path, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("POST /healthz = 200")
	}
}
