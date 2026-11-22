package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
)

func TestBearer_ValidTokenIsAuthenticated(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "sam")
	resp := c.get("/api/overview")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
}

func TestBearer_InvalidTokenDoesNotFallBackToCookie(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := login(t, srv, "sam", testPassword)
	// Control: the session alone is served.
	if resp := c.get("/api/overview"); resp.StatusCode != http.StatusOK {
		t.Fatalf("control: status %d", resp.StatusCode)
	}
	_, parsed, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	c.bearer = "hmp_" + parsed.ID + "_" + parsed.Secret // well formed, never stored
	resp := c.get("/api/overview")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 (the cookie must not rescue a bad token)", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
	if code, _ := errorCode(t, resp); code != "unauthenticated" {
		t.Errorf("code = %q", code)
	}
	// An expired token of a user who also holds a valid session fails the same way.
	c.bearer = issueToken(t, st, "sam", time.Now().Add(-time.Minute))
	if resp := c.get("/api/overview"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token with cookie: status %d, want 401", resp.StatusCode)
	}
}

func TestBearer_InvalidTokenIs401OnPublicRoutesToo(t *testing.T) {
	srv, _, _ := newTokenServer(t)
	c := newAPIClient(t, srv)
	_, parsed, _ := apitoken.Generate(apitoken.KindPAT)
	c.bearer = "hmp_" + parsed.ID + "_" + parsed.Secret
	for _, path := range []string{"/api/auth/options", "/api/auth/setup"} {
		resp := c.get(path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a bad token: status %d, want 401, never anonymous", path, resp.StatusCode)
		}
	}
}

func TestBearer_PostNeedsNoCSRF(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "sam")
	resp := c.post("/api/log-search/estimate", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		code, _ := errorCode(t, resp)
		t.Fatalf("status %d code %q: the request should reach the handler without a CSRF header", resp.StatusCode, code)
	}
}

func TestBearer_MustChangePasswordBlocked(t *testing.T) {
	srv, _, st := newTokenServer(t)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "fresh", hash, false, true); err != nil {
		t.Fatal(err)
	}
	c := tokenClient(t, srv, st, "fresh")
	resp := c.get("/api/overview")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "password_change_required" {
		t.Errorf("code = %q, want password_change_required", code)
	}
}

func TestBearer_LockedUserStillWorks(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "omer")
	omer, err := st.GetUserByUsername(t.Context(), "omer")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LockUser(t.Context(), omer.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resp := c.get("/api/overview")
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("a locked user's token was refused: lockout must not affect tokens")
	}
}

func TestBearer_RateLimited(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "sam")
	other := tokenClient(t, srv, st, "omer")
	limitedAt := 0
	for i := 1; i <= 300; i++ {
		resp := c.get("/api/tokens/current")
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			if got := resp.Header.Get("Retry-After"); got != "1" {
				t.Errorf("Retry-After = %q, want 1", got)
			}
			limitedAt = i
			break
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("call %d: status %d", i, resp.StatusCode)
		}
	}
	// 120 are the burst; refill adds at most a few while the loop runs.
	if limitedAt < 121 || limitedAt > 140 {
		t.Fatalf("first 429 at call %d, want just after the 120-call burst", limitedAt)
	}
	resp := other.get("/api/tokens/current")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("another user was limited too: %d", resp.StatusCode)
	}
}

func TestBearer_SessionRequestsAreNotTokenRateLimited(t *testing.T) {
	srv, _, _ := newTokenServer(t)
	c := login(t, srv, "sam", testPassword)
	for i := 0; i < 130; i++ {
		resp := c.get("/api/me")
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("session request %d was rate limited by the token bucket", i)
		}
	}
}

func TestBearer_CallCountsAsActivity(t *testing.T) {
	srv, _, st := newTokenServer(t)
	c := tokenClient(t, srv, st, "omer")
	if _, ok, err := st.LastSessionActivity(t.Context(), time.Now()); err != nil || ok {
		t.Fatalf("before the call: ok=%v err=%v, want no activity", ok, err)
	}
	resp := c.get("/api/tokens/current")
	resp.Body.Close()
	last, ok, err := st.LastSessionActivity(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatalf("after the call: ok=%v err=%v", ok, err)
	}
	if age := time.Since(last); age > time.Minute || age < -time.Minute {
		t.Fatalf("activity is %v old, want within a minute", age)
	}
}

func TestTokensOff_BearerIgnoredAndRoutes404(t *testing.T) {
	srv, _, st := newTestServer(t) // HM_API_TOKENS off
	raw := issueToken(t, st, "sam", time.Now().Add(time.Hour))
	c := newAPIClient(t, srv)
	c.bearer = raw
	resp := c.get("/api/overview")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("valid token with tokens off: status %d, want 401", resp.StatusCode)
	}
	session := login(t, srv, "sam", testPassword)
	for _, path := range []string{"/api/tokens", "/api/tokens/current", "/api/users/1/tokens"} {
		resp := session.get(path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s with tokens off: status %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestTokensOff_BearerDoesNotOverrideASession(t *testing.T) {
	srv, _, st := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	c.bearer = issueToken(t, st, "omer", time.Now().Add(time.Hour))
	resp := c.get("/api/me")
	defer resp.Body.Close()
	var me struct {
		Username string `json:"username"`
	}
	decode(t, resp, &me)
	if resp.StatusCode != http.StatusOK || me.Username != "sam" {
		t.Fatalf("status %d, answered as %q; with tokens off the session must be used", resp.StatusCode, me.Username)
	}
}

func TestPublicRoutes_TokenRefusedSessionAndAnonymousUnchanged(t *testing.T) {
	srv, _, st := newTokenServer(t)
	login(t, srv, "sam", testPassword) // anonymous login works, as before

	token := tokenClient(t, srv, st, "sam")
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/auth/login"}, {"GET", "/api/auth/setup"}, {"POST", "/api/auth/setup"},
		{"GET", "/api/auth/options"}, {"GET", "/api/auth/saml/login"}, {"GET", "/api/auth/saml/metadata"},
	} {
		resp := token.do(tc.method, tc.path, map[string]string{"username": "sam", "password": testPassword}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a valid token: status %d, want 403", tc.method, tc.path, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		if code, _ := errorCode(t, resp); code != "token_not_allowed" {
			t.Errorf("%s %s: code %q, want token_not_allowed", tc.method, tc.path, code)
		}
	}

	// A session-authenticated client reaches the login handler as before.
	session := login(t, srv, "omer", testPassword)
	resp := session.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "omer", "password": testPassword}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login with an existing session: status %d, want 200", resp.StatusCode)
	}
	// Anonymous: options and setup status answer.
	anon := newAPIClient(t, srv)
	resp = anon.get("/api/auth/options")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anonymous options: status %d, want 200", resp.StatusCode)
	}
	resp = anon.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "sam", "password": "wrong-password-1"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous bad login: status %d, want 401", resp.StatusCode)
	}
}

func TestRequestLog_CarriesTokenIDNeverTheSecret(t *testing.T) {
	srv, _, st, logs := newServerCustom(t, true, withTokens(nil, nil))
	raw := issueToken(t, st, "sam", time.Now().Add(time.Hour))
	parsed, ok := apitoken.Parse(raw)
	if !ok {
		t.Fatal("issued token does not parse")
	}
	c := newAPIClient(t, srv)
	c.bearer = raw
	for _, path := range []string{"/api/overview", "/api/users", "/api/nope"} { // allowed, refused and unknown
		resp := c.get(path)
		resp.Body.Close()
	}
	_, unknown, _ := apitoken.Generate(apitoken.KindPAT)
	c.bearer = "hmp_" + unknown.ID + "_" + unknown.Secret
	c.get("/api/overview").Body.Close()

	out := logs.String()
	if !strings.Contains(out, "token_id="+apitoken.DisplayID(parsed)) {
		t.Errorf("request log lacks token_id=%s:\n%s", apitoken.DisplayID(parsed), out)
	}
	for name, secret := range map[string]string{"stored token secret": parsed.Secret, "unknown token secret": unknown.Secret, "whole token": raw} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains the %s", name)
		}
	}
	if strings.Contains(out, apitoken.Hash(parsed.Secret)) {
		t.Error("log contains the secret hash")
	}
}
