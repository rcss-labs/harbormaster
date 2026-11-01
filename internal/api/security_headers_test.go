package api

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

var wantSecurityHeaders = map[string]string{
	"Content-Security-Policy":   "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'",
	"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	"X-Frame-Options":           "DENY",
	"X-Content-Type-Options":    "nosniff",
	"Referrer-Policy":           "same-origin",
}

func assertSecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for k, v := range wantSecurityHeaders {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSecurityHeaders_SetOnEveryResponse(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assertSecurityHeaders(t, rec.Header())
	if got := rec.Header().Get("Content-Security-Policy"); got != CSPValue {
		t.Errorf("CSPValue = %q, header = %q", CSPValue, got)
	}
}

func TestSecurityHeaders_OnJSONError(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	assertSecurityHeaders(t, rec.Header())
}

type hijackableRecorder struct {
	*httptest.ResponseRecorder
}

func (hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

func TestSecurityHeaders_DoesNotBreakHijack(t *testing.T) {
	var hijacker bool
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hijacker = w.(http.Hijacker)
	}))
	h.ServeHTTP(hijackableRecorder{httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/", nil))
	if !hijacker {
		t.Fatal("wrapped ResponseWriter no longer implements http.Hijacker")
	}
}
