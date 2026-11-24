package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// issueToken stores a personal token for userID and returns its raw secret form.
func issueToken(t *testing.T, st *store.Store, userID int64, expires time.Time) (raw string, tok store.APIToken) {
	t.Helper()
	raw, parsed, err := apitoken.Generate(apitoken.KindPAT)
	if err != nil {
		t.Fatal(err)
	}
	tok, err = st.CreateAPIToken(context.Background(), store.APIToken{
		PublicID: apitoken.DisplayID(parsed), UserID: userID, Kind: "pat", Name: "t",
		SecretHash: apitoken.Hash(parsed.Secret), Scope: "read", AuthSource: "password", ExpiresAt: expires,
	}, 50)
	if err != nil {
		t.Fatal(err)
	}
	return raw, tok
}

func TestTokenMaxTTL_Table(t *testing.T) {
	cfg := config.Config{TokenMaxTTL: 90 * 24 * time.Hour, TokenMaxTTLSSO: 7 * 24 * time.Hour}
	linked := store.User{IdPEntityID: "e", ExternalID: "n"}
	if !linked.SSOLinked() {
		t.Fatal("setup: user should be SSO-linked")
	}
	tests := []struct {
		name  string
		user  store.User
		admin bool
		want  time.Duration
	}{
		{"password non-admin", store.User{}, false, 90 * 24 * time.Hour},
		{"sso-linked", linked, false, 7 * 24 * time.Hour},
		{"admin with a password", store.User{}, true, 7 * 24 * time.Hour},
		{"sso-linked admin", linked, true, 7 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenMaxTTL(cfg, tc.user, tc.admin); got != tc.want {
				t.Errorf("TokenMaxTTL = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthenticateToken_Table(t *testing.T) {
	svc, st := newTestService(t)
	svc.cfg.APITokens = true
	ctx := context.Background()
	u := mustUser(t, st, "alice", false)
	gone := mustUser(t, st, "gone", false)
	valid, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	expired, _ := issueToken(t, st, u.ID, time.Now().Add(-time.Hour))
	orphan, _ := issueToken(t, st, gone.ID, time.Now().Add(time.Hour))
	// Deleting the user cascades to the token, so the token no longer resolves.
	if err := storetest.DeleteUser(ctx, st, gone.ID); err != nil {
		t.Fatal(err)
	}
	// A real id with another token's secret.
	parsedValid, _ := apitoken.Parse(valid)
	_, otherParsed, _ := apitoken.Generate(apitoken.KindPAT)
	wrongSecret := "hmp_" + parsedValid.ID + "_" + otherParsed.Secret
	_, unknownParsed, _ := apitoken.Generate(apitoken.KindPAT)
	unknownRaw := "hmp_" + unknownParsed.ID + "_" + unknownParsed.Secret
	oauthRaw, _, _ := apitoken.Generate(apitoken.KindOAuthAccess)

	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", valid, true},
		{"unknown id", unknownRaw, false},
		{"wrong secret with a real id", wrongSecret, false},
		{"expired", expired, false},
		{"user deleted", orphan, false},
		{"malformed", "hmp_nope", false},
		{"empty", "", false},
		{"other token kind", oauthRaw, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok, user, err := svc.AuthenticateToken(ctx, tc.raw, "10.0.0.1")
			if tc.ok {
				if err != nil || user.ID != u.ID || tok.UserID != u.ID {
					t.Fatalf("got user %d, %v; want success for %d", user.ID, err, u.ID)
				}
				return
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("err = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestAuthenticateToken_TouchesAtMostOncePerMinute(t *testing.T) {
	svc, st := newTestService(t)
	svc.cfg.APITokens = true
	ctx := context.Background()
	u := mustUser(t, st, "alice", false)
	raw, tok := issueToken(t, st, u.ID, time.Now().Add(24*time.Hour))
	base := time.Now()

	at := func(offset time.Duration, ip string) store.APIToken {
		t.Helper()
		svc.now = func() time.Time { return base.Add(offset) }
		if _, _, err := svc.AuthenticateToken(ctx, raw, ip); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetAPITokenByPublicID(ctx, tok.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := at(0, "10.0.0.1")
	if first.LastUsedAt == nil || first.LastUsedIP != "10.0.0.1" {
		t.Fatalf("first use not recorded: %+v", first)
	}
	within := at(30*time.Second, "10.0.0.2")
	if !within.LastUsedAt.Equal(*first.LastUsedAt) || within.LastUsedIP != "10.0.0.1" {
		t.Fatalf("touched again inside a minute: %v -> %v (%s)", first.LastUsedAt, within.LastUsedAt, within.LastUsedIP)
	}
	after := at(2*time.Minute, "10.0.0.3")
	if !after.LastUsedAt.After(first.LastUsedAt.Add(time.Minute)) || after.LastUsedIP != "10.0.0.3" {
		t.Fatalf("not refreshed after 2m: %v -> %v", first.LastUsedAt, after.LastUsedAt)
	}
}

func TestAuthenticateToken_ExpiryUsesTheServiceClock(t *testing.T) {
	svc, st := newTestService(t)
	svc.cfg.APITokens = true
	u := mustUser(t, st, "alice", false)
	raw, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, _, err := svc.AuthenticateToken(context.Background(), raw, ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated past expiry", err)
	}
}

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { *called = true })
}

func TestMiddleware_BearerTakesTheTokenPathOnlyWhenEnabled(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "alice", false)
	raw, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))

	serve := func(header string) (code int, called bool, www string) {
		h := svc.Middleware(RequireAuth(okHandler(&called)))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/x", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		h.ServeHTTP(rec, req)
		return rec.Code, called, rec.Header().Get("WWW-Authenticate")
	}

	if code, called, _ := serve("Bearer " + raw); code != 401 || called {
		t.Fatalf("tokens off: code=%d called=%v, want the header ignored (401 anonymous)", code, called)
	}
	svc.cfg.APITokens = true
	if code, called, _ := serve("Bearer " + raw); code != 200 || !called {
		t.Fatalf("valid token: code=%d called=%v", code, called)
	}
	if code, called, _ := serve("bearer " + raw); code != 200 || !called {
		t.Fatalf("lowercase scheme: code=%d called=%v", code, called)
	}
	unknown, _, _ := apitoken.Generate(apitoken.KindPAT)
	if code, called, www := serve("Bearer " + unknown); code != 401 || called || www != "Bearer" {
		t.Fatalf("unknown token: code=%d called=%v www=%q", code, called, www)
	}
	// Anything that is not a well-formed token is not a token request: it proceeds as anonymous.
	for _, header := range []string{"Basic abc", "Bearer not-a-token", "Bearer " + raw + " ", "Bearer"} {
		if code, called, _ := serve(header); code != 401 || called {
			t.Errorf("header %q: code=%d called=%v, want anonymous 401", header, code, called)
		}
	}
}

func TestMiddleware_InvalidBearerNeverFallsBackToSession(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "alice", false)
	svc.cfg.APITokens = true
	res := mustLogin(t, svc, "alice", testPassword, "10.0.0.1", "ua")
	unknown, _, _ := apitoken.Generate(apitoken.KindPAT)

	called := false
	h := svc.Middleware(RequireAuth(okHandler(&called)))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Cookie", CookieName+"="+res.SessionID)
	req.Header.Set("Authorization", "Bearer "+unknown)
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || called {
		t.Fatalf("code=%d called=%v, want 401 and the handler not reached", rec.Code, called)
	}
	// Control: the same cookie without the bad header is served.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Cookie", CookieName+"="+res.SessionID)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !called {
		t.Fatalf("control: code=%d called=%v", rec.Code, called)
	}
}

func TestMiddleware_ValidBearerIgnoresTheCookie(t *testing.T) {
	svc, st := newTestService(t)
	mustUser(t, st, "alice", false)
	bob := mustUser(t, st, "bob", false)
	svc.cfg.APITokens = true
	res := mustLogin(t, svc, "alice", testPassword, "10.0.0.1", "ua")
	raw, _ := issueToken(t, st, bob.ID, time.Now().Add(time.Hour))

	var gotUser store.User
	var cred Credential
	var hasSession bool
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _ = UserFrom(r.Context())
		cred, _ = CredentialFrom(r.Context())
		_, hasSession = SessionFrom(r.Context())
	}))
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Cookie", CookieName+"="+res.SessionID)
	req.Header.Set("Authorization", "Bearer "+raw)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotUser.ID != bob.ID || cred.Kind != CredentialToken || hasSession {
		t.Fatalf("user=%d cred=%v session=%v; want bob's token, no session", gotUser.ID, cred.Kind, hasSession)
	}
	if strings.Contains(cred.TokenPublicID, "_"+strings.SplitN(raw, "_", 3)[2]) {
		t.Fatal("credential carries the secret")
	}
}

func TestRequireTokenScope_DefaultDenyAndSessionPass(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "alice", false)
	svc.cfg.APITokens = true
	raw, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	res := mustLogin(t, svc, "alice", testPassword, "10.0.0.1", "ua")

	do := func(route Scope, header map[string]string) (int, bool) {
		called := false
		h := svc.Middleware(RequireAuth(RequireTokenScope(route)(okHandler(&called))))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/x", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(rec, req)
		if rec.Code == 403 && !strings.Contains(rec.Body.String(), "token_not_allowed") {
			t.Fatalf("403 without the token_not_allowed code: %s", rec.Body)
		}
		return rec.Code, called
	}
	bearer := map[string]string{"Authorization": "Bearer " + raw}
	if code, called := do("", bearer); code != 403 || called {
		t.Errorf("unmarked route: code=%d called=%v, want 403", code, called)
	}
	if code, called := do("write", bearer); code != 403 || called {
		t.Errorf("route marked with another scope: code=%d called=%v, want 403", code, called)
	}
	if code, called := do(ScopeRead, bearer); code != 200 || !called {
		t.Errorf("read-marked route: code=%d called=%v, want 200", code, called)
	}
	cookie := map[string]string{"Cookie": CookieName + "=" + res.SessionID}
	if code, called := do("", cookie); code != 200 || !called {
		t.Errorf("session on an unmarked route: code=%d called=%v, want 200", code, called)
	}
}

func TestRefuseToken_RefusesTokensOnly(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "alice", false)
	svc.cfg.APITokens = true
	raw, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	res := mustLogin(t, svc, "alice", testPassword, "10.0.0.1", "ua")

	do := func(header map[string]string) (int, bool) {
		called := false
		h := svc.Middleware(RefuseToken(okHandler(&called)))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/login", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(rec, req)
		return rec.Code, called
	}
	if code, called := do(map[string]string{"Authorization": "Bearer " + raw}); code != 403 || called {
		t.Errorf("token: code=%d called=%v, want 403", code, called)
	}
	if code, called := do(map[string]string{"Cookie": CookieName + "=" + res.SessionID}); code != 200 || !called {
		t.Errorf("session: code=%d called=%v, want pass", code, called)
	}
	if code, called := do(nil); code != 200 || !called {
		t.Errorf("anonymous: code=%d called=%v, want pass", code, called)
	}
}

func TestRequireCSRF_TokenCredentialNeedsNone(t *testing.T) {
	svc, st := newTestService(t)
	u := mustUser(t, st, "alice", false)
	svc.cfg.APITokens = true
	raw, _ := issueToken(t, st, u.ID, time.Now().Add(time.Hour))
	if _, called := authedRequest(t, svc, st, "POST", "/api/x", map[string]string{"Authorization": "Bearer " + raw}); !called {
		t.Fatal("token POST without CSRF header was refused")
	}
}

func TestClientIP_GatewayHop(t *testing.T) {
	gateway := []netip.Prefix{netip.MustParsePrefix("10.0.8.0/22"), netip.MustParsePrefix("10.1.0.0/24")}
	req := func(xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.1.1:4000"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	// The real chain is "client, gateway task": the gateway sends the one client entry and the internal load
	// balancer appends the gateway. Rows with more entries pin that client-supplied entries are never read.
	rows := []struct {
		name     string
		xff      string
		trusted  bool
		gateways []netip.Prefix
		want     string
	}{
		{"peer in cidr: next entry to the left", "203.0.113.7, 10.0.9.20", true, gateway, "203.0.113.7"},
		{"peer in second cidr", "203.0.113.7, 10.1.0.9", true, gateway, "203.0.113.7"},
		{"peer outside cidr: unchanged", "203.0.113.7, 10.0.20.20", true, gateway, "10.0.20.20"},
		{"spoofed extra entries further left never used", "6.6.6.6, 7.7.7.7, 203.0.113.7, 10.0.9.20", true, gateway, "203.0.113.7"},
		{"one extra hop only even if that entry is also a gateway address", "6.6.6.6, 10.0.9.21, 10.0.9.20", true, gateway, "10.0.9.21"},
		{"setting empty: unchanged", "203.0.113.7, 10.0.9.20", true, nil, "10.0.9.20"},
		{"proxy not trusted: header ignored", "203.0.113.7, 10.0.9.20", false, gateway, "10.0.1.1"},
		{"no entry to the left: the peer", "10.0.9.20", true, gateway, "10.0.9.20"},
		{"left entry not an address: the peer", "garbage, 10.0.9.20", true, gateway, "10.0.9.20"},
		{"empty left entry: the peer", ", 10.0.9.20", true, gateway, "10.0.9.20"},
	}
	for _, row := range rows {
		if got := ClientIP(req(row.xff), row.trusted, row.gateways); got != row.want {
			t.Errorf("%s: %q, want %q", row.name, got, row.want)
		}
	}
}
