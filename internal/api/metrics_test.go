package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func metricsErrorCode(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body is not the standard JSON error: %q", body)
	}
	return out.Error.Code
}

func TestMetricsHandler_DisabledWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer anything")
	MetricsHandler("", telemetry.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := metricsErrorCode(t, rec.Body.String()); code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
}

func TestMetricsHandler_RejectsWrongToken(t *testing.T) {
	h := MetricsHandler("secret", telemetry.New())
	for name, header := range map[string]string{"wrong": "Bearer nope", "missing": "", "wrong scheme": "Basic secret"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if code := metricsErrorCode(t, rec.Body.String()); code != "unauthenticated" {
			t.Errorf("%s: code = %q, want unauthenticated", name, code)
		}
	}
}

func TestMetricsHandler_ServesText(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	MetricsHandler("secret", telemetry.New()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "harbormaster_poller_leader") {
		t.Errorf("body lacks harbormaster_poller_leader:\n%s", rec.Body.String())
	}
}
