package oauthsrv

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestToken_CodeExchangeIssuesTheRightTokens(t *testing.T) {
	e := newOAuthEnv(t)
	now := time.Now()
	resp, terr := e.exchange(e.approve(e.user, e.request()), now)
	if terr != nil {
		t.Fatalf("%+v", terr)
	}
	if resp.TokenType != "Bearer" || resp.ExpiresIn != 3600 || resp.Scope != Scope ||
		!strings.HasPrefix(resp.AccessToken, "hmo_") || !strings.HasPrefix(resp.RefreshToken, "hmr_") {
		t.Fatalf("response = %+v", resp)
	}
	// The rows are hashed, never the raw tokens.
	if e.count(`select count(*) from api_tokens where secret_hash in ($1, $2)`, resp.AccessToken, resp.RefreshToken) != 0 {
		t.Fatal("a raw token is stored")
	}
	grants, err := e.st.ListOAuthGrantsForUser(t.Context(), e.user.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants = %v, %v", grants, err)
	}
	g := grants[0]
	if g.ClientID != e.docs.clientID() || g.ClientName != "Claude" || g.Resource != testIssuer+"/mcp" || g.Scope != Scope ||
		g.AuthSource != "password" {
		t.Fatalf("grant = %+v", g)
	}
	if want := now.Add(90 * 24 * time.Hour); g.ExpiresAt.Sub(want).Abs() > time.Second {
		t.Fatalf("grant ends %v, want %v", g.ExpiresAt, want)
	}
}

func TestToken_CodeIsSingleUseAndBound(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TokenRequest)
	}{
		{"wrong verifier", func(r *TokenRequest) { r.CodeVerifier = strings.Repeat("z", 43) }},
		{"verifier too short", func(r *TokenRequest) { r.CodeVerifier = testVerifier[:42] }},
		{"verifier too long", func(r *TokenRequest) { r.CodeVerifier = testVerifier + strings.Repeat("a", 90) }},
		{"verifier with bad characters", func(r *TokenRequest) { r.CodeVerifier = strings.Repeat("!", 43) }},
		{"missing verifier", func(r *TokenRequest) { r.CodeVerifier = "" }},
		{"the challenge used as the verifier (plain)", func(r *TokenRequest) { r.CodeVerifier = challengeOf(testVerifier) }},
		{"wrong redirect uri", func(r *TokenRequest) { r.RedirectURI = "http://localhost/callback" }},
		{"missing redirect uri", func(r *TokenRequest) { r.RedirectURI = "" }},
		{"wrong client id", func(r *TokenRequest) { r.ClientID = "https://claude.ai/other.json" }},
		{"missing client id", func(r *TokenRequest) { r.ClientID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOAuthEnv(t)
			code := e.approve(e.user, e.request())
			req := TokenRequest{GrantType: "authorization_code", Code: code, RedirectURI: testRedirect,
				CodeVerifier: testVerifier, ClientID: e.docs.clientID()}
			tc.mutate(&req)
			if _, terr := e.svc.Exchange(t.Context(), req, time.Now()); terr == nil || terr.Code != "invalid_grant" {
				t.Fatalf("error = %+v, want invalid_grant", terr)
			}
			// The failed attempt spent the code: the right values no longer work.
			if _, terr := e.exchange(code, time.Now()); terr == nil || terr.Code != "invalid_grant" {
				t.Fatalf("code still works after a failed attempt: %+v", terr)
			}
			if e.count(`select count(*) from oauth_grants`) != 0 {
				t.Fatal("a grant was created")
			}
		})
	}

	t.Run("replay", func(t *testing.T) {
		e := newOAuthEnv(t)
		code := e.approve(e.user, e.request())
		if _, terr := e.exchange(code, time.Now()); terr != nil {
			t.Fatalf("first use: %+v", terr)
		}
		if _, terr := e.exchange(code, time.Now()); terr == nil || terr.Code != "invalid_grant" {
			t.Fatalf("replay = %+v, want invalid_grant", terr)
		}
		if n := e.count(`select count(*) from oauth_grants`); n != 1 {
			t.Fatalf("%d grants after a replay, want 1", n)
		}
	})

	t.Run("expired code", func(t *testing.T) {
		e := newOAuthEnv(t)
		code, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.CreateOAuthCode(t.Context(), store.OAuthCode{
			CodeHash: apitoken.Hash(code), UserID: e.user.ID, ClientID: e.docs.clientID(), ClientName: "Claude",
			RedirectURI: testRedirect, CodeChallenge: challengeOf(testVerifier), Scope: Scope, Resource: testIssuer + "/mcp",
			ExpiresAt: time.Now().Add(-time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		if _, terr := e.exchange(code, time.Now()); terr == nil || terr.Code != "invalid_grant" {
			t.Fatalf("error = %+v, want invalid_grant", terr)
		}
	})

	t.Run("resource mismatch", func(t *testing.T) {
		e := newOAuthEnv(t)
		code := e.approve(e.user, e.request())
		_, terr := e.svc.Exchange(t.Context(), TokenRequest{GrantType: "authorization_code", Code: code,
			RedirectURI: testRedirect, CodeVerifier: testVerifier, ClientID: e.docs.clientID(), Resource: "https://other.example/mcp"}, time.Now())
		if terr == nil || terr.Code != "invalid_target" {
			t.Fatalf("error = %+v, want invalid_target", terr)
		}
		if _, terr := e.exchange(code, time.Now()); terr == nil {
			t.Fatal("code survived a resource mismatch")
		}
	})

	t.Run("user deleted before the exchange", func(t *testing.T) {
		e := newOAuthEnv(t)
		code := e.approve(e.user, e.request())
		if _, err := e.st.Pool.Exec(t.Context(), `delete from users where id = $1`, e.user.ID); err != nil {
			t.Fatal(err)
		}
		if _, terr := e.exchange(code, time.Now()); terr == nil || terr.Code != "invalid_grant" {
			t.Fatalf("error = %+v, want invalid_grant", terr)
		}
	})
}

// TestToken_PKCEIsS256Only checks the verifier rule directly: a code is bound to a challenge, and only the S256
// transform of the verifier opens it.
func TestToken_PKCEIsS256Only(t *testing.T) {
	good := challengeOf(testVerifier)
	if !verifierMatches(testVerifier, good) {
		t.Fatal("the S256 verifier was refused")
	}
	if verifierMatches(testVerifier, testVerifier) {
		t.Fatal("a plain challenge (challenge == verifier) was accepted")
	}
	if verifierMatches(strings.Repeat("a", 43), good) {
		t.Fatal("a wrong verifier was accepted")
	}
	// A client that registered a plain challenge cannot get one stored: Validate refuses the method, so no code
	// carries it (TestValidate_Table "method plain"); and one that sends the challenge as the verifier is refused above.
}

type countingStore struct {
	Store
	calls atomic.Int32
}

func (c *countingStore) ConsumeOAuthCode(ctx context.Context, h string) (store.OAuthCode, error) {
	c.calls.Add(1)
	return c.Store.ConsumeOAuthCode(ctx, h)
}

func (c *countingStore) RotateOAuthRefresh(ctx context.Context, id, h string, a, r store.APIToken, now time.Time, g time.Duration) (store.OAuthGrant, error) {
	c.calls.Add(1)
	return c.Store.RotateOAuthRefresh(ctx, id, h, a, r, now, g)
}

func TestToken_MalformedCodeOrRefreshNeverReadsTheDatabase(t *testing.T) {
	e := newOAuthEnv(t)
	counter := &countingStore{Store: e.st}
	e.svc.Store = counter
	badCodes := []string{"", "short", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("+", 43), strings.Repeat("a", 42) + "=", "<script>"}
	for _, code := range badCodes {
		_, terr := e.svc.Exchange(t.Context(), TokenRequest{GrantType: "authorization_code", Code: code,
			RedirectURI: testRedirect, CodeVerifier: testVerifier, ClientID: e.docs.clientID()}, time.Now())
		if terr == nil || terr.Code != "invalid_grant" {
			t.Errorf("code %q: %+v, want invalid_grant", code, terr)
		}
	}
	rawPAT, _, _ := apitoken.Generate(apitoken.KindPAT)
	rawAccess, _, _ := apitoken.Generate(apitoken.KindOAuthAccess)
	for _, token := range []string{"", "hmr_", "hmr_short", "nonsense", rawPAT, rawAccess, strings.Repeat("a", 5000)} {
		_, terr := e.svc.Exchange(t.Context(), TokenRequest{GrantType: "refresh_token", RefreshToken: token}, time.Now())
		if terr == nil || terr.Code != "invalid_grant" {
			t.Errorf("refresh %.12q: %+v, want invalid_grant", token, terr)
		}
	}
	if n := counter.calls.Load(); n != 0 {
		t.Fatalf("%d database reads for malformed input, want 0", n)
	}
}

func TestToken_GrantTypes(t *testing.T) {
	e := newOAuthEnv(t)
	for _, tc := range []struct{ grantType, want string }{
		{"", "invalid_request"}, {"password", "unsupported_grant_type"}, {"client_credentials", "unsupported_grant_type"},
		{"Authorization_Code", "unsupported_grant_type"},
	} {
		_, terr := e.svc.Exchange(t.Context(), TokenRequest{GrantType: tc.grantType}, time.Now())
		if terr == nil || terr.Code != tc.want {
			t.Errorf("grant_type %q: %+v, want %s", tc.grantType, terr, tc.want)
		}
	}
}

func TestToken_RefreshRotates(t *testing.T) {
	e := newOAuthEnv(t)
	now := time.Now()
	first, terr := e.exchange(e.approve(e.user, e.request()), now)
	if terr != nil {
		t.Fatal(terr)
	}
	second, terr := e.refresh(first.RefreshToken, now.Add(30*time.Minute))
	if terr != nil {
		t.Fatalf("refresh: %+v", terr)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken || second.ExpiresIn != 3600 {
		t.Fatalf("rotation reused a token or changed the lifetime: %+v", second)
	}
	// The old access token is gone, the new pair is present.
	parse := func(raw string) string {
		p, ok := apitoken.Parse(raw)
		if !ok {
			t.Fatalf("%.8s is not a token", raw)
		}
		return apitoken.DisplayID(p)
	}
	if _, err := e.st.GetAPITokenByPublicID(t.Context(), parse(first.AccessToken)); err == nil {
		t.Fatal("the old access token still exists after rotation")
	}
	if _, err := e.st.GetAPITokenByPublicID(t.Context(), parse(second.AccessToken)); err != nil {
		t.Fatalf("new access token: %v", err)
	}
	// The old refresh token inside the 60 s grace is refused and the grant survives.
	if _, terr := e.refresh(first.RefreshToken, now.Add(30*time.Minute+10*time.Second)); terr == nil || terr.Code != "invalid_grant" {
		t.Fatalf("old refresh inside grace = %+v, want invalid_grant", terr)
	}
	if e.count(`select count(*) from oauth_grants`) != 1 {
		t.Fatal("a retry inside the grace window ended the grant")
	}
	if _, terr := e.refresh(second.RefreshToken, now.Add(31*time.Minute)); terr != nil {
		t.Fatalf("the new refresh token stopped working: %+v", terr)
	}
}

func TestToken_RefreshReuseAfterGraceRevokesTheGrant(t *testing.T) {
	e := newOAuthEnv(t)
	now := time.Now()
	first, _ := e.exchange(e.approve(e.user, e.request()), now)
	second, terr := e.refresh(first.RefreshToken, now.Add(time.Minute))
	if terr != nil {
		t.Fatal(terr)
	}
	// The first refresh token again, well after the grace window.
	if _, terr := e.refresh(first.RefreshToken, now.Add(5*time.Minute)); terr == nil || terr.Code != "invalid_grant" {
		t.Fatalf("reuse = %+v, want invalid_grant", terr)
	}
	if e.count(`select count(*) from oauth_grants`) != 0 || e.count(`select count(*) from api_tokens`) != 0 {
		t.Fatal("the grant and its tokens must all be gone after reuse")
	}
	// The access token the second refresh issued stops working: its row is gone.
	p, _ := apitoken.Parse(second.AccessToken)
	if _, err := e.st.GetAPITokenByPublicID(t.Context(), apitoken.DisplayID(p)); err == nil {
		t.Fatal("the access token issued by the rotation still authenticates after reuse")
	}
	if _, terr := e.refresh(second.RefreshToken, now.Add(6*time.Minute)); terr == nil {
		t.Fatal("the newest refresh token works after the grant was revoked")
	}
	// Audited with the reason.
	entries, err := e.st.ListAudit(t.Context(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range entries {
		if a.Action == "oauth_revoke" && a.Details["reason"] == "refresh_reuse" {
			found = true
		}
	}
	if !found {
		t.Fatal("no oauth_revoke audit entry with reason refresh_reuse")
	}
}

func TestToken_RefreshByAnotherClientRevokesTheGrant(t *testing.T) {
	e := newOAuthEnv(t)
	first, _ := e.exchange(e.approve(e.user, e.request()), time.Now())
	_, terr := e.svc.Exchange(t.Context(), TokenRequest{GrantType: "refresh_token", RefreshToken: first.RefreshToken,
		ClientID: "https://claude.ai/another-client.json"}, time.Now())
	if terr == nil || terr.Code != "invalid_grant" {
		t.Fatalf("error = %+v, want invalid_grant", terr)
	}
	if e.count(`select count(*) from oauth_grants`) != 0 {
		t.Fatal("a refresh token presented by another client left the grant alive")
	}
}

func TestToken_GrantLifetimeFollowsTheUserKind(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		name        string
		admin, sso  bool
		okAtDay     int
		refusedAt   int
		wantGrantTo time.Duration
	}{
		{"sso linked user", false, true, 6, 8, 7 * day},
		{"admin with a password", true, false, 6, 8, 7 * day},
		{"password user", false, false, 89, 91, 90 * day},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOAuthEnv(t)
			user := e.newUser("u"+strings.ReplaceAll(tc.name, " ", ""), tc.admin, tc.sso)
			start := time.Now()
			resp, terr := e.exchange(e.approve(user, e.request()), start)
			if terr != nil {
				t.Fatalf("%+v", terr)
			}
			g, err := e.st.ListOAuthGrantsForUser(t.Context(), user.ID)
			if err != nil || len(g) != 1 {
				t.Fatalf("grants = %v, %v", g, err)
			}
			if got := g[0].ExpiresAt.Sub(start); (got - tc.wantGrantTo).Abs() > time.Second {
				t.Fatalf("grant lifetime = %v, want %v", got, tc.wantGrantTo)
			}
			// No token passes the grant's end.
			if e.count(`select count(*) from api_tokens where grant_id = $1 and expires_at > $2`, g[0].ID, g[0].ExpiresAt) != 0 {
				t.Fatal("a token outlives its grant")
			}
			refreshToken := resp.RefreshToken
			// Rotate daily until the last good day: every new token stays inside the grant.
			var last TokenResponse
			for d := 1; d <= tc.okAtDay; d += max(1, (tc.okAtDay-1)/3) {
				last, terr = e.refresh(refreshToken, start.Add(time.Duration(d)*day))
				if terr != nil {
					t.Fatalf("refresh on day %d: %+v", d, terr)
				}
				refreshToken = last.RefreshToken
				if e.count(`select count(*) from api_tokens where grant_id = $1 and expires_at > $2`, g[0].ID, g[0].ExpiresAt) != 0 {
					t.Fatalf("day %d: a token outlives its grant", d)
				}
			}
			if _, terr := e.refresh(refreshToken, start.Add(time.Duration(tc.refusedAt)*day)); terr == nil || terr.Code != "invalid_grant" {
				t.Fatalf("refresh on day %d = %+v, want invalid_grant", tc.refusedAt, terr)
			}
		})
	}
}

func TestToken_AccessTokenNearTheGrantEndIsShortened(t *testing.T) {
	e := newOAuthEnv(t)
	sso := e.newUser("sso", false, true)
	start := time.Now()
	resp, terr := e.exchange(e.approve(sso, e.request()), start)
	if terr != nil {
		t.Fatal(terr)
	}
	// 30 minutes before the 7 day end, a 1 hour access token would pass the grant.
	nearEnd := start.Add(7*24*time.Hour - 30*time.Minute)
	resp, terr = e.refresh(resp.RefreshToken, nearEnd)
	if terr != nil {
		t.Fatalf("%+v", terr)
	}
	if resp.ExpiresIn > 30*60 || resp.ExpiresIn < 29*60 {
		t.Fatalf("expires_in = %d, want about 1800 (the grant's end)", resp.ExpiresIn)
	}
}

// capture is a slog sink.
type capture struct{ bytes.Buffer }

func TestToken_NothingSecretIsLoggedOrAudited(t *testing.T) {
	e := newOAuthEnv(t)
	var logs capture
	e.svc.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	code := e.approve(e.user, e.request())
	first, terr := e.exchange(code, time.Now())
	if terr != nil {
		t.Fatal(terr)
	}
	second, _ := e.refresh(first.RefreshToken, time.Now().Add(time.Minute))
	_, _ = e.refresh(first.RefreshToken, time.Now().Add(10*time.Minute)) // reuse: revokes and audits
	entries, err := e.st.ListAudit(t.Context(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var audited strings.Builder
	grantEntries := 0
	for _, a := range entries {
		if a.Action == "oauth_grant" {
			grantEntries++
		}
		audited.WriteString(a.Summary + a.Target + a.IP)
		for k, v := range a.Details {
			audited.WriteString(k)
			if s, ok := v.(string); ok {
				audited.WriteString(s)
			}
		}
	}
	if grantEntries != 1 {
		t.Fatalf("%d oauth_grant audit entries, want 1", grantEntries)
	}
	for name, secret := range map[string]string{
		"code": code, "access": first.AccessToken, "refresh": first.RefreshToken,
		"rotated access": second.AccessToken, "rotated refresh": second.RefreshToken, "verifier": testVerifier,
	} {
		raw := strings.TrimPrefix(strings.TrimPrefix(secret, "hmo_"), "hmr_")
		for sink, text := range map[string]string{"log": logs.String(), "audit": audited.String()} {
			if strings.Contains(text, secret) || (len(raw) > 20 && strings.Contains(text, raw[len(raw)-24:])) {
				t.Errorf("%s leaks into the %s", name, sink)
			}
		}
	}
}
