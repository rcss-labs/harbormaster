package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz = %d %q, want 200 ok", resp.StatusCode, body)
	}
}

func TestSecurityHeaders_Present(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "same-origin",
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'",
		"Cache-Control":           "no-store",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestUnknownAPIPath_IsJSON404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "not_found" {
		t.Fatalf("code = %q, want not_found", code)
	}
}

func TestSPA_ServedForDeepLink(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/clusters/abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("deep link = %d (%d bytes), want 200 with index.html", resp.StatusCode, len(body))
	}
}

func TestETag_304OnMatch(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	first := c.get("/api/me")
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if first.StatusCode != http.StatusOK || etag == "" || etag[0] != '"' || len(etag) != 34 {
		t.Fatalf("first: %d etag=%q, want 200 with a quoted 32-hex etag", first.StatusCode, etag)
	}
	second := c.do(http.MethodGet, "/api/me", nil, map[string]string{"If-None-Match": etag})
	defer second.Body.Close()
	body, _ := io.ReadAll(second.Body)
	if second.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("second: %d (%d bytes), want 304 empty", second.StatusCode, len(body))
	}
	if got := second.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on 304 = %q, want no-store", got)
	}
}

func TestCSRF_RequiredOnPost(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	resp := c.do(http.MethodPost, "/api/auth/logout", nil, map[string]string{"X-CSRF-Token": ""})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "csrf" {
		t.Fatalf("code = %q, want csrf", code)
	}
}

func TestPasswordChangeRequired_BlocksOtherRoutes(t *testing.T) {
	srv, _, st := newTestServer(t)
	hash, err := authHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "temp", hash, false, true); err != nil {
		t.Fatal(err)
	}
	c := login(t, srv, "temp", testPassword)

	blocked := c.get("/api/clusters")
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("/api/clusters = %d, want 403", blocked.StatusCode)
	}
	if code, _ := errorCode(t, blocked); code != "password_change_required" {
		t.Fatalf("code = %q, want password_change_required", code)
	}
	me := c.get("/api/me")
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("/api/me = %d, want 200", me.StatusCode)
	}
}

func TestPageFrom_ClampsAndRoundTripsCursor(t *testing.T) {
	cfg := testConfig()
	req := httptest.NewRequest(http.MethodGet, "/x?limit=9999", nil)
	limit, offset, err := pageFrom(req, cfg)
	if err != nil || limit != cfg.PageSizeMax || offset != 0 {
		t.Fatalf("clamp: %d %d %v", limit, offset, err)
	}
	next := nextCursor(2, 4, 2)
	if next == nil {
		t.Fatal("full page must produce a cursor")
	}
	req = httptest.NewRequest(http.MethodGet, "/x?limit=2&cursor="+*next, nil)
	if _, offset, err = pageFrom(req, cfg); err != nil || offset != 6 {
		t.Fatalf("cursor offset = %d err=%v, want 6", offset, err)
	}
	if nextCursor(2, 6, 1) != nil {
		t.Fatal("short page must have no next cursor")
	}
	for _, bad := range []string{"limit=0", "limit=x", "cursor=!!!"} {
		if _, _, err := pageFrom(httptest.NewRequest(http.MethodGet, "/x?"+bad, nil), cfg); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestClientIP_UsesRightmostForwardedHop(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.9:5555"
	if got := clientIP(req); got != "10.0.0.9" {
		t.Fatalf("peer = %q", got)
	}
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.7 ")
	if got := clientIP(req); got != "10.0.0.7" {
		t.Fatalf("xff = %q", got)
	}
}
