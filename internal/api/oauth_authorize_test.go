package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/oauthsrv"
)

// oauthUser creates a user with the given kind, optionally with Logs (or only View) on a fresh tag, and signs it in.
func (e *oauthAPIEnv) oauthUser(t *testing.T, name string, admin, sso bool, actions authz.ActionSet) *apiClient {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.st.CreateUser(t.Context(), name, hash, admin, false)
	if err != nil {
		t.Fatal(err)
	}
	if actions != 0 {
		tag, err := e.st.CreateTag(t.Context(), "tag-"+name, "blue", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetPermission(t.Context(), user.ID, tag.ID, actions); err != nil {
			t.Fatal(err)
		}
	}
	if sso {
		if _, err := e.st.Pool.Exec(t.Context(),
			`update users set idp_entity_id = 'idp', external_id = $2, external_id_format = 'persistent' where id = $1`,
			user.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	return login(t, e.srv, name, testPassword)
}

func (e *oauthAPIEnv) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthorize_ValidateAnswersTheView(t *testing.T) {
	env := newOAuthAPIEnv(t)
	resp := env.omer.get(authorizePath(env.request()))
	wantStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var view struct {
		ClientName    string `json:"client_name"`
		ClientHost    string `json:"client_host"`
		RedirectHost  string `json:"redirect_host"`
		Scope         string `json:"scope"`
		ReadOnly      bool   `json:"read_only"`
		ExpiresInDays int    `json:"expires_in_days"`
		SeesLogs      bool   `json:"sees_logs"`
		Username      string `json:"username"`
	}
	decode(t, resp, &view)
	if view.ClientName != "Claude" || view.RedirectHost != "claude.ai" || view.Scope != "harbormaster:read" ||
		!view.ReadOnly || view.Username != "omer" || view.ClientHost == "" {
		t.Fatalf("view = %+v", view)
	}
}

func TestAuthorize_ViewReflectsTheUser(t *testing.T) {
	env := newOAuthAPIEnv(t)
	cases := []struct {
		name     string
		client   *apiClient
		days     int
		seesLogs bool
	}{
		{"sso linked user with logs", env.oauthUser(t, "ssouser", false, true, authz.ActionSet(authz.View|authz.Logs)), 7, true},
		{"admin with a password", env.sam, 7, true},
		{"password user with view only", env.oauthUser(t, "viewer", false, false, authz.ActionSet(authz.View)), 90, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.client.get(authorizePath(env.request()))
			wantStatus(t, resp, http.StatusOK)
			var view struct {
				ExpiresInDays int  `json:"expires_in_days"`
				SeesLogs      bool `json:"sees_logs"`
			}
			decode(t, resp, &view)
			if view.ExpiresInDays != tc.days || view.SeesLogs != tc.seesLogs {
				t.Fatalf("view = %+v, want %d days and sees_logs %v", view, tc.days, tc.seesLogs)
			}
		})
	}
}

func TestAuthorize_ValidateTable(t *testing.T) {
	env := newOAuthAPIEnv(t)
	cases := []struct {
		name         string
		mutate       func(*oauthsrv.AuthorizeRequest)
		code         string
		redirectable bool
	}{
		{"client off the allowlist", func(r *oauthsrv.AuthorizeRequest) { r.ClientID = "https://evil.example/c.json" }, "invalid_client", false},
		{"redirect not listed", func(r *oauthsrv.AuthorizeRequest) { r.RedirectURI = "https://evil.example/cb" }, "invalid_request", false},
		{"response type", func(r *oauthsrv.AuthorizeRequest) { r.ResponseType = "token" }, "unsupported_response_type", true},
		{"plain challenge method", func(r *oauthsrv.AuthorizeRequest) { r.CodeChallengeMethod = "plain" }, "invalid_request", true},
		{"scope", func(r *oauthsrv.AuthorizeRequest) { r.Scope = "everything" }, "invalid_scope", true},
		{"resource", func(r *oauthsrv.AuthorizeRequest) { r.Resource = "https://other.example/mcp" }, "invalid_target", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := env.request()
			tc.mutate(&req)
			resp := env.omer.get(authorizePath(req))
			wantStatus(t, resp, http.StatusBadRequest)
			code, redirectTo := oauthError(t, resp)
			if code != tc.code {
				t.Fatalf("error = %q, want %q", code, tc.code)
			}
			if tc.redirectable {
				parsed, err := url.Parse(redirectTo)
				if err != nil || parsed.Query().Get("error") != tc.code || parsed.Query().Get("state") != "st4te" ||
					parsed.Query().Get("iss") != oauthTestIssuer || parsed.Query().Get("code") != "" ||
					parsed.Scheme+"://"+parsed.Host+parsed.Path != oauthTestRedirect {
					t.Fatalf("redirect_to = %q", redirectTo)
				}
			} else if redirectTo != "" {
				t.Fatalf("a non-redirectable error carries redirect_to %q", redirectTo)
			}
		})
	}
}

func TestAuthorize_UnlistedRedirectIsAnErrorPageNotARedirect(t *testing.T) {
	env := newOAuthAPIEnv(t)
	evil := env.request()
	evil.RedirectURI = "https://attacker.example/steal"

	// Validation: an error with no redirect_to, which the page renders without an approve button.
	resp := env.omer.get(authorizePath(evil))
	wantStatus(t, resp, http.StatusBadRequest)
	raw := map[string]any{}
	decode(t, resp, &raw)
	if _, has := raw["redirect_to"]; has {
		t.Fatalf("the answer carries redirect_to: %v", raw)
	}

	// Approving and denying are refused the same way, and no code is stored.
	for _, decision := range []string{"approve", "deny"} {
		resp := env.decide(env.omer, evil, decision)
		wantStatus(t, resp, http.StatusBadRequest)
		body := map[string]any{}
		decode(t, resp, &body)
		if _, has := body["redirect_to"]; has {
			t.Fatalf("%s: the answer carries redirect_to: %v", decision, body)
		}
	}
	if n := env.countRows(t, `select count(*) from oauth_codes`); n != 0 {
		t.Fatalf("%d codes stored for an unlisted redirect", n)
	}
}

func TestAuthorize_ApproveIssuesACodeBoundToTheRequest(t *testing.T) {
	env := newOAuthAPIEnv(t)
	req := env.request()
	resp := env.decide(env.omer, req, "approve")
	wantStatus(t, resp, http.StatusOK)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	decode(t, resp, &out)
	parsed, err := url.Parse(out.RedirectTo)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	code := q.Get("code")
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != oauthTestRedirect || len(code) != 43 ||
		q.Get("state") != "st4te" || q.Get("iss") != oauthTestIssuer {
		t.Fatalf("redirect_to = %s", out.RedirectTo)
	}
	stored, err := env.st.ConsumeOAuthCode(t.Context(), apitoken.Hash(code))
	if err != nil {
		t.Fatal(err)
	}
	omer, _ := env.st.GetUserByUsername(t.Context(), "omer")
	if stored.UserID != omer.ID || stored.ClientID != req.ClientID || stored.RedirectURI != req.RedirectURI ||
		stored.CodeChallenge != req.CodeChallenge || stored.Resource != req.Resource || stored.AuthSource != "password" {
		t.Fatalf("stored code = %+v", stored)
	}
}

func TestAuthorize_DenyRedirectsWithAccessDenied(t *testing.T) {
	env := newOAuthAPIEnv(t)
	resp := env.decide(env.omer, env.request(), "deny")
	wantStatus(t, resp, http.StatusOK)
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	decode(t, resp, &out)
	parsed, err := url.Parse(out.RedirectTo)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("error") != "access_denied" || q.Get("state") != "st4te" || q.Get("iss") != oauthTestIssuer || q.Get("code") != "" {
		t.Fatalf("deny redirect = %s", out.RedirectTo)
	}
	if n := env.countRows(t, `select count(*) from oauth_codes`); n != 0 {
		t.Fatal("deny stored a code")
	}
	// An unknown decision is refused.
	wantStatus(t, env.decide(env.omer, env.request(), "maybe"), http.StatusUnprocessableEntity)
}

func TestAuthorize_NeedsASessionAndCSRF(t *testing.T) {
	env := newOAuthAPIEnv(t)
	anon := newAPIClient(t, env.srv)

	wantStatus(t, anon.get(authorizePath(env.request())), http.StatusUnauthorized)
	wantStatus(t, anon.post("/api/oauth/authorize", map[string]any{"request": env.request(), "decision": "approve"}), http.StatusUnauthorized)

	// A signed-in POST without the CSRF header is refused, and stores nothing.
	resp := env.omer.do(http.MethodPost, "/api/oauth/authorize",
		map[string]any{"request": env.request(), "decision": "approve"}, map[string]string{"X-CSRF-Token": ""})
	wantStatus(t, resp, http.StatusForbidden)
	resp = env.omer.do(http.MethodPost, "/api/oauth/authorize",
		map[string]any{"request": env.request(), "decision": "approve"}, map[string]string{"X-CSRF-Token": "wrong"})
	wantStatus(t, resp, http.StatusForbidden)
	if n := env.countRows(t, `select count(*) from oauth_codes`); n != 0 {
		t.Fatalf("%d codes stored without CSRF", n)
	}

	// An access token (personal or OAuth) is refused with token_not_allowed on both.
	var created CreateTokenResponse
	r := env.omer.post("/api/tokens", map[string]any{"name": "t", "expires_in_days": 1})
	wantStatus(t, r, http.StatusCreated)
	decode(t, r, &created)
	issued := env.connect(t, env.omer)
	for name, token := range map[string]string{"personal token": created.Secret, "oauth access token": issued.AccessToken} {
		for _, call := range []struct{ method, path string }{
			{http.MethodGet, authorizePath(env.request())}, {http.MethodPost, "/api/oauth/authorize"},
		} {
			resp := env.bearer(t, call.method, call.path, token)
			wantStatus(t, resp, http.StatusForbidden)
			if code, _ := errorCode(t, resp); code != "token_not_allowed" {
				t.Errorf("%s %s %s: code = %q, want token_not_allowed", name, call.method, call.path, code)
			}
		}
	}
}

func TestAuthorize_PendingPasswordChangeIsBlocked(t *testing.T) {
	env := newOAuthAPIEnv(t)
	omer, err := env.st.GetUserByUsername(t.Context(), "omer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.Pool.Exec(t.Context(), `update users set must_change_password = true where id = $1`, omer.ID); err != nil {
		t.Fatal(err)
	}
	for _, resp := range []*http.Response{
		env.omer.get(authorizePath(env.request())),
		env.decide(env.omer, env.request(), "approve"),
	} {
		wantStatus(t, resp, http.StatusForbidden)
		if code, _ := errorCode(t, resp); code != "password_change_required" {
			t.Fatalf("code = %q, want password_change_required", code)
		}
	}
	if n := env.countRows(t, `select count(*) from oauth_codes`); n != 0 {
		t.Fatal("a code was stored for a user who must change their password")
	}
}

func TestOAuthOff_RoutesAre404(t *testing.T) {
	env := newOAuthAPIEnvWith(t, false, nil)
	wantStatus(t, env.omer.get(authorizePath(env.request())), http.StatusNotFound)
	wantStatus(t, env.decide(env.omer, env.request(), "approve"), http.StatusNotFound)
	wantStatus(t, env.postToken(t, env.refreshForm("hmr_x")), http.StatusNotFound)
	wantStatus(t, env.omer.do(http.MethodDelete, "/api/oauth/grants/1", nil, nil), http.StatusNotFound)
	wantStatus(t, env.sam.do(http.MethodDelete, "/api/users/1/oauth-grants/1", nil, nil), http.StatusNotFound)
	// Token management still works, and lists no grants.
	resp := env.omer.get("/api/tokens")
	wantStatus(t, resp, http.StatusOK)
	var list struct {
		Grants []GrantDTO `json:"grants"`
	}
	decode(t, resp, &list)
	if list.Grants == nil || len(list.Grants) != 0 {
		t.Fatalf("grants = %#v, want an empty list", list.Grants)
	}
}

func TestOAuthOnWithoutTokensIsOff(t *testing.T) {
	env := newOAuthAPIEnvWith(t, true, func(s *Server) { s.Cfg.APITokens = false; s.OAuth = nil })
	wantStatus(t, env.omer.get(authorizePath(env.request())), http.StatusNotFound)
}

// A connector's consent link, with its whole query, survives an SSO sign-in; anything off the origin does not.
func TestSAMLLogin_ReturnPathKeepsTheQueryAndStaysSameOrigin(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	long := "/oauth/authorize?state=" + strings.Repeat("s", 600)
	consent := "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {"https://claude.ai/oauth/mcp-client.json"},
		"redirect_uri": {oauthTestRedirect}, "code_challenge": {oauthChallenge(oauthTestVerifier)},
		"code_challenge_method": {"S256"}, "state": {"a b&c=d"}, "resource": {oauthTestIssuer + "/mcp"},
	}.Encode()
	cases := []struct{ name, raw, want string }{
		{"the consent link with its query", consent, consent},
		{"a long state still fits", long, long},
		{"over 2048 characters is dropped", "/oauth/authorize?state=" + strings.Repeat("s", 2100), "/"},
		{"another origin", "https://evil.example" + consent, "/"},
		{"protocol relative", "//evil.example" + consent, "/"},
		{"backslash trick", `/\evil.example` + consent, "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBrowser(t, srv)
			resp := b.get("/api/auth/saml/login?return_to=" + url.QueryEscape(tc.raw))
			resp.Body.Close()
			loc, _ := url.Parse(resp.Header.Get("Location"))
			var got string
			if err := st.Pool.QueryRow(t.Context(), `select return_to from saml_requests where id = $1`, loc.Query().Get("RelayState")).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("return_to stored as %q, want %q", got, tc.want)
			}
		})
	}
}
