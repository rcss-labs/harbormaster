package api

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestToken_CodeExchangeHappyPath(t *testing.T) {
	env := newOAuthAPIEnv(t)
	resp := env.postToken(t, env.codeForm(env.approve(t, env.omer, env.request())))
	wantStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out tokenResponse
	decode(t, resp, &out)
	if out.TokenType != "Bearer" || out.ExpiresIn != 3600 || out.Scope != "harbormaster:read" ||
		!strings.HasPrefix(out.AccessToken, "hmo_") || !strings.HasPrefix(out.RefreshToken, "hmr_") {
		t.Fatalf("response = %+v", out)
	}
	// The access token authenticates, as omer, and says which client it is.
	who := env.bearer(t, http.MethodGet, "/api/tokens/current", out.AccessToken)
	wantStatus(t, who, http.StatusOK)
	var dto WhoAmIDTO
	decode(t, who, &dto)
	if dto.User.Username != "omer" || dto.Token.Kind != "oauth_access" || dto.Token.Name != "Claude" || dto.Token.Scope != "read" {
		t.Fatalf("whoami = %+v", dto)
	}
}

func TestToken_CodeIsSingleUseAndBound(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"wrong verifier", func(f url.Values) { f.Set("code_verifier", strings.Repeat("z", 43)) }},
		{"plain verifier (the challenge itself)", func(f url.Values) { f.Set("code_verifier", oauthChallenge(oauthTestVerifier)) }},
		{"wrong redirect uri", func(f url.Values) { f.Set("redirect_uri", "http://localhost/callback") }},
		{"wrong client id", func(f url.Values) { f.Set("client_id", "https://claude.ai/other.json") }},
		{"missing verifier", func(f url.Values) { f.Del("code_verifier") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newOAuthAPIEnv(t)
			code := env.approve(t, env.omer, env.request())
			form := env.codeForm(code)
			tc.mutate(form)
			resp := env.postToken(t, form)
			wantStatus(t, resp, http.StatusBadRequest)
			if got, _ := oauthError(t, resp); got != "invalid_grant" {
				t.Fatalf("error = %q, want invalid_grant", got)
			}
			// The correct values no longer work: the failed attempt spent the code.
			resp = env.postToken(t, env.codeForm(code))
			wantStatus(t, resp, http.StatusBadRequest)
			if got, _ := oauthError(t, resp); got != "invalid_grant" {
				t.Fatalf("after a failed attempt: error = %q, want invalid_grant", got)
			}
			if n := env.countRows(t, `select count(*) from oauth_grants`); n != 0 {
				t.Fatal("a grant was created")
			}
		})
	}
	t.Run("replay", func(t *testing.T) {
		env := newOAuthAPIEnv(t)
		code := env.approve(t, env.omer, env.request())
		wantStatus(t, env.postToken(t, env.codeForm(code)), http.StatusOK)
		resp := env.postToken(t, env.codeForm(code))
		wantStatus(t, resp, http.StatusBadRequest)
		if got, _ := oauthError(t, resp); got != "invalid_grant" {
			t.Fatalf("replay error = %q", got)
		}
		if n := env.countRows(t, `select count(*) from oauth_grants`); n != 1 {
			t.Fatalf("%d grants after a replay, want 1", n)
		}
	})
	t.Run("expired code", func(t *testing.T) {
		env := newOAuthAPIEnv(t)
		code := env.approve(t, env.omer, env.request())
		if _, err := env.st.Pool.Exec(t.Context(), `update oauth_codes set expires_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		resp := env.postToken(t, env.codeForm(code))
		wantStatus(t, resp, http.StatusBadRequest)
		if got, _ := oauthError(t, resp); got != "invalid_grant" {
			t.Fatalf("error = %q", got)
		}
	})
}

func TestToken_MalformedCodeOrRefreshNeverReadsTheDatabase(t *testing.T) {
	env := newOAuthAPIEnv(t)
	// With the pool closed, any database read would fail with a 500: a malformed credential still answers 400.
	env.st.Pool.Close()
	for _, form := range []url.Values{
		env.codeForm("short"),
		env.codeForm(strings.Repeat("a", 44)),
		env.codeForm(strings.Repeat("+", 43)),
		env.refreshForm("hmr_nonsense"),
		env.refreshForm("hmp_" + strings.Repeat("a", 60)),
		env.refreshForm(""),
	} {
		resp := env.postToken(t, form)
		wantStatus(t, resp, http.StatusBadRequest)
		if got, _ := oauthError(t, resp); got != "invalid_grant" {
			t.Fatalf("%v: error = %q, want invalid_grant", form, got)
		}
	}
}

func TestToken_RefreshRotates(t *testing.T) {
	env := newOAuthAPIEnv(t)
	first := env.connect(t, env.omer)
	resp := env.postToken(t, env.refreshForm(first.RefreshToken))
	wantStatus(t, resp, http.StatusOK)
	var second tokenResponse
	decode(t, resp, &second)
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("rotation reissued a token")
	}
	// The new pair works, the old access token is gone, the old refresh token inside 60 s is refused, grant survives.
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", second.AccessToken), http.StatusOK)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", first.AccessToken), http.StatusUnauthorized)
	retry := env.postToken(t, env.refreshForm(first.RefreshToken))
	wantStatus(t, retry, http.StatusBadRequest)
	if got, _ := oauthError(t, retry); got != "invalid_grant" {
		t.Fatalf("retry error = %q", got)
	}
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", second.AccessToken), http.StatusOK)
	wantStatus(t, env.postToken(t, env.refreshForm(second.RefreshToken)), http.StatusOK)
}

func TestToken_RefreshReuseAfterGraceRevokesTheGrant(t *testing.T) {
	env := newOAuthAPIEnv(t)
	first := env.connect(t, env.omer)
	wantStatus(t, env.postToken(t, env.refreshForm(first.RefreshToken)), http.StatusOK)
	// Age the rotation past the 60 s grace window.
	if _, err := env.st.Pool.Exec(t.Context(),
		`update api_tokens set rotated_at = now() - interval '5 minutes' where rotated_at is not null`); err != nil {
		t.Fatal(err)
	}
	// The rotation left one access token; after the reuse every token of the grant must be gone.
	if n := env.countRows(t, `select count(*) from api_tokens where kind = 'oauth_access'`); n != 1 {
		t.Fatalf("%d access tokens before reuse, want 1", n)
	}
	resp := env.postToken(t, env.refreshForm(first.RefreshToken))
	wantStatus(t, resp, http.StatusBadRequest)
	if got, _ := oauthError(t, resp); got != "invalid_grant" {
		t.Fatalf("reuse error = %q", got)
	}
	if env.countRows(t, `select count(*) from oauth_grants`) != 0 || env.countRows(t, `select count(*) from api_tokens`) != 0 {
		t.Fatal("reuse must delete the grant and all its tokens")
	}
	if n := env.countRows(t, `select count(*) from audit_log where action = 'oauth_revoke' and details->>'reason' = 'refresh_reuse'`); n != 1 {
		t.Fatalf("%d oauth_revoke refresh_reuse audit entries, want 1", n)
	}
}

func TestToken_ReuseStopsTheIssuedAccessToken(t *testing.T) {
	env := newOAuthAPIEnv(t)
	first := env.connect(t, env.omer)
	var second tokenResponse
	resp := env.postToken(t, env.refreshForm(first.RefreshToken))
	wantStatus(t, resp, http.StatusOK)
	decode(t, resp, &second)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", second.AccessToken), http.StatusOK)
	if _, err := env.st.Pool.Exec(t.Context(),
		`update api_tokens set rotated_at = now() - interval '5 minutes' where rotated_at is not null`); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, env.postToken(t, env.refreshForm(first.RefreshToken)), http.StatusBadRequest)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", second.AccessToken), http.StatusUnauthorized)
	wantStatus(t, env.postToken(t, env.refreshForm(second.RefreshToken)), http.StatusBadRequest)
}

func TestToken_GrantLifetimeFollowsTheUserKind(t *testing.T) {
	day := 24 * time.Hour
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	env := newOAuthAPIEnvWith(t, true, func(s *Server) { s.Now = clock })
	cases := []struct {
		name   string
		client *apiClient
		good   int
		bad    int
	}{
		{"sso linked user", env.oauthUser(t, "ssouser", false, true, 0), 6, 8},
		{"admin with a password", env.sam, 6, 8},
		{"password user who is not an admin", env.omer, 89, 91},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offset.Store(0)
			issued := env.connect(t, tc.client)
			refresh := issued.RefreshToken
			for _, d := range []int{tc.good / 2, tc.good} {
				offset.Store(int64(time.Duration(d) * day))
				resp := env.postToken(t, env.refreshForm(refresh))
				wantStatus(t, resp, http.StatusOK)
				var next tokenResponse
				decode(t, resp, &next)
				refresh = next.RefreshToken
				// No token outlives the grant.
				if n := env.countRows(t, `select count(*) from api_tokens t join oauth_grants g on g.id = t.grant_id where t.expires_at > g.expires_at`); n != 0 {
					t.Fatalf("day %d: %d tokens outlive their grant", d, n)
				}
			}
			offset.Store(int64(time.Duration(tc.bad) * day))
			resp := env.postToken(t, env.refreshForm(refresh))
			wantStatus(t, resp, http.StatusBadRequest)
			if got, _ := oauthError(t, resp); got != "invalid_grant" {
				t.Fatalf("day %d: error = %q, want invalid_grant", tc.bad, got)
			}
		})
	}
}

func TestToken_FormOnlyAndNoStore(t *testing.T) {
	env := newOAuthAPIEnv(t)
	check := func(name string, resp *http.Response, status int) {
		t.Helper()
		if resp.StatusCode != status {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: status %d (%s), want %d", name, resp.StatusCode, b, status)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, cc)
		}
		_ = resp.Body.Close()
	}
	// JSON is refused.
	check("json body", env.postRaw(t, "application/json", strings.NewReader(`{"grant_type":"refresh_token"}`)), http.StatusUnsupportedMediaType)
	check("no content type", env.postRaw(t, "", strings.NewReader("grant_type=refresh_token")), http.StatusUnsupportedMediaType)
	check("multipart", env.postRaw(t, "multipart/form-data; boundary=x", strings.NewReader("--x--")), http.StatusUnsupportedMediaType)
	// Form with a charset is fine.
	check("form with charset", env.postRaw(t, "application/x-www-form-urlencoded; charset=UTF-8",
		strings.NewReader(env.refreshForm("hmr_bad").Encode())), http.StatusBadRequest)
	// Success and error answers both carry no-store.
	code := env.approve(t, env.omer, env.request())
	check("success", env.postToken(t, env.codeForm(code)), http.StatusOK)
	check("error", env.postToken(t, env.codeForm(code)), http.StatusBadRequest)
	check("unsupported grant type", env.postToken(t, url.Values{"grant_type": {"password"}}), http.StatusBadRequest)
	// Oversized, repeated parameters and client secrets are refused.
	check("oversized", env.postRaw(t, "application/x-www-form-urlencoded", strings.NewReader("a="+strings.Repeat("x", 9<<10))), http.StatusRequestEntityTooLarge)
	check("repeated parameter", env.postRaw(t, "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=refresh_token&refresh_token=a&refresh_token=b")), http.StatusBadRequest)
	form := env.refreshForm("hmr_x")
	form.Set("client_secret", "s3cret")
	resp := env.postToken(t, form)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("client_secret: status %d, want 400", resp.StatusCode)
	}
	if got, _ := oauthError(t, resp); got != "invalid_client" {
		t.Fatalf("client_secret error = %q, want invalid_client", got)
	}
	// Only POST exists.
	get, err := http.Get(env.srv.URL + "/api/oauth/token")
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusMethodNotAllowed && get.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 405 or 404", get.StatusCode)
	}
	_ = get.Body.Close()
}

func TestToken_RateLimitedPerIP(t *testing.T) {
	env := newOAuthAPIEnv(t)
	form := env.refreshForm("hmr_nonsense")
	limited, firstLimited := 0, 0
	for i := 1; i <= 45; i++ {
		resp := env.postToken(t, form)
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
			if firstLimited == 0 {
				firstLimited = i
			}
			if resp.Header.Get("Retry-After") == "" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
				t.Errorf("a 429 lacks Retry-After or no-store: %v", resp.Header)
			}
		} else if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("request %d: status %d", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if limited == 0 || firstLimited <= 30 || firstLimited > 33 {
		t.Fatalf("first 429 at request %d (%d limited), want right after the burst of 30", firstLimited, limited)
	}
}

func TestToken_Audited(t *testing.T) {
	env := newOAuthAPIEnv(t)
	code := env.approve(t, env.omer, env.request())
	form := env.codeForm(code)
	resp := env.postToken(t, form)
	wantStatus(t, resp, http.StatusOK)
	var issued tokenResponse
	decode(t, resp, &issued)
	rows, err := env.st.ListAudit(t.Context(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	granted := 0
	var dump strings.Builder
	for _, a := range rows {
		dump.WriteString(a.Summary + a.Target + a.Action)
		for k, v := range a.Details {
			dump.WriteString(k)
			if s, ok := v.(string); ok {
				dump.WriteString(s)
			}
		}
		if a.Action == "oauth_grant" {
			granted++
			if a.ActorUsername != "omer" || a.Details["client_id"] != env.docs.clientID || a.Details["client_name"] != "Claude" {
				t.Errorf("audit entry = %+v", a)
			}
		}
	}
	if granted != 1 {
		t.Fatalf("%d oauth_grant entries, want 1", granted)
	}
	assertNoSecrets(t, "audit log", dump.String(), code, issued.AccessToken, issued.RefreshToken, oauthTestVerifier)
	assertNoSecrets(t, "server log", env.logs.String(), code, issued.AccessToken, issued.RefreshToken, oauthTestVerifier)
}

// assertNoSecrets fails when text contains any secret or the random part of an OAuth token.
func assertNoSecrets(t *testing.T, where, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		bare := strings.TrimPrefix(strings.TrimPrefix(secret, "hmo_"), "hmr_")
		if strings.Contains(text, secret) || (len(bare) > 20 && strings.Contains(text, bare[len(bare)-24:])) {
			t.Errorf("a secret (%.6s...) is in the %s", secret, where)
		}
	}
}

func TestToken_SecretsNeverReachTheServerLog(t *testing.T) {
	env := newOAuthAPIEnv(t)
	code := env.approve(t, env.omer, env.request())
	issued := tokenResponse{}
	resp := env.postToken(t, env.codeForm(code))
	decode(t, resp, &issued)
	replay := env.codeForm(code) // an error path
	wantStatus(t, env.postToken(t, replay), http.StatusBadRequest)
	refreshed := tokenResponse{}
	resp = env.postToken(t, env.refreshForm(issued.RefreshToken))
	decode(t, resp, &refreshed)
	wantStatus(t, env.postToken(t, env.refreshForm(issued.RefreshToken)), http.StatusBadRequest) // retry
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", refreshed.AccessToken), http.StatusOK)
	wantStatus(t, env.bearer(t, http.MethodGet, "/api/tokens/current", issued.AccessToken), http.StatusUnauthorized)
	wantStatus(t, env.omer.get(authorizePath(env.request())), http.StatusOK)
	assertNoSecrets(t, "server log", env.logs.String(), code, issued.AccessToken, issued.RefreshToken,
		refreshed.AccessToken, refreshed.RefreshToken, oauthTestVerifier)
}

func TestToken_RefreshTokenNeverAuthenticatesTheAPI(t *testing.T) {
	// A refresh token is not an API credential: it never authenticates, even on routes tokens may read.
	env := newOAuthAPIEnv(t)
	issued := env.connect(t, env.omer)
	for _, path := range []string{"/api/tokens/current", "/api/overview", "/api/me"} {
		wantStatus(t, env.bearer(t, http.MethodGet, path, issued.RefreshToken), http.StatusUnauthorized)
	}
}
