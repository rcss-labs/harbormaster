package oauthsrv

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/apitoken"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func TestValidate_Table(t *testing.T) {
	e := newOAuthEnv(t)
	cases := []struct {
		name         string
		mutate       func(*AuthorizeRequest)
		code         string // "" means valid
		redirectable bool
	}{
		{"valid", func(*AuthorizeRequest) {}, "", false},
		{"scope omitted is fine", func(r *AuthorizeRequest) { r.Scope = "" }, "", false},
		{"state omitted is fine", func(r *AuthorizeRequest) { r.State = "" }, "", false},
		{"client off the allowlist", func(r *AuthorizeRequest) { r.ClientID = "https://evil.example/c.json" }, "invalid_client", false},
		{"client not https", func(r *AuthorizeRequest) { r.ClientID = "http://127.0.0.1/c.json" }, "invalid_client", false},
		{"client empty", func(r *AuthorizeRequest) { r.ClientID = "" }, "invalid_client", false},
		{"redirect not listed", func(r *AuthorizeRequest) { r.RedirectURI = "https://evil.example/cb" }, "invalid_request", false},
		{"redirect lookalike", func(r *AuthorizeRequest) { r.RedirectURI = "https://claude.ai.evil.example/api/mcp/auth_callback" }, "invalid_request", false},
		{"redirect empty", func(r *AuthorizeRequest) { r.RedirectURI = "" }, "invalid_request", false},
		{"response type token", func(r *AuthorizeRequest) { r.ResponseType = "token" }, "unsupported_response_type", true},
		{"response type missing", func(r *AuthorizeRequest) { r.ResponseType = "" }, "unsupported_response_type", true},
		{"method plain", func(r *AuthorizeRequest) { r.CodeChallengeMethod = "plain" }, "invalid_request", true},
		{"method missing", func(r *AuthorizeRequest) { r.CodeChallengeMethod = "" }, "invalid_request", true},
		{"method lower case", func(r *AuthorizeRequest) { r.CodeChallengeMethod = "s256" }, "invalid_request", true},
		{"challenge missing", func(r *AuthorizeRequest) { r.CodeChallenge = "" }, "invalid_request", true},
		{"challenge too short", func(r *AuthorizeRequest) { r.CodeChallenge = r.CodeChallenge[:42] }, "invalid_request", true},
		{"challenge too long", func(r *AuthorizeRequest) { r.CodeChallenge += "a" }, "invalid_request", true},
		{"challenge not base64url", func(r *AuthorizeRequest) { r.CodeChallenge = strings.Repeat("+", 43) }, "invalid_request", true},
		{"scope other", func(r *AuthorizeRequest) { r.Scope = "harbormaster:write" }, "invalid_scope", true},
		{"scope extra", func(r *AuthorizeRequest) { r.Scope = "harbormaster:read other" }, "invalid_scope", true},
		{"resource missing", func(r *AuthorizeRequest) { r.Resource = "" }, "invalid_target", true},
		{"resource other", func(r *AuthorizeRequest) { r.Resource = "https://other.example/mcp" }, "invalid_target", true},
		{"resource without /mcp", func(r *AuthorizeRequest) { r.Resource = testIssuer }, "invalid_target", true},
		{"state at the limit", func(r *AuthorizeRequest) { r.State = strings.Repeat("s", 512) }, "", false},
		{"state too long", func(r *AuthorizeRequest) { r.State = strings.Repeat("s", 513) }, "invalid_request", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := e.request()
			tc.mutate(&req)
			view, aerr := e.svc.Validate(t.Context(), req, e.user, e.subject)
			if tc.code == "" {
				if aerr != nil {
					t.Fatalf("unexpected error %+v", aerr)
				}
				if view.ClientName != "Claude" || view.Scope != Scope || !view.ReadOnly || view.RedirectHost != "claude.ai" ||
					view.ClientHost != strings.TrimPrefix(e.docs.URL, "https://") {
					t.Fatalf("view = %+v", view)
				}
				return
			}
			if aerr == nil {
				t.Fatal("request accepted, want " + tc.code)
			}
			if aerr.Code != tc.code || aerr.Redirectable != tc.redirectable {
				t.Fatalf("error = %+v, want code %s redirectable %v", aerr, tc.code, tc.redirectable)
			}
			if aerr.Description == "" {
				t.Fatal("error has no description to show the user")
			}
		})
	}
}

func TestValidate_ClientDocumentProblemsAreNeverRedirectable(t *testing.T) {
	e := newOAuthEnv(t)
	other := newDocServer(t, func(w http.ResponseWriter, r *http.Request, _ string) { w.WriteHeader(500) })
	req := e.request()
	req.ClientID = other.clientID()
	svc := NewService(e.st, testConfig())
	svc.Clients = other.resolver()
	_, aerr := svc.Validate(t.Context(), req, e.user, e.subject)
	if aerr == nil || aerr.Redirectable || aerr.Code != "temporarily_unavailable" {
		t.Fatalf("error = %+v, want a shown temporarily_unavailable", aerr)
	}
}

func TestValidate_ExpiresInDaysFollowsTheUserKind(t *testing.T) {
	e := newOAuthEnv(t)
	sso := e.newUser("sso", false, true)
	admin := e.newUser("boss", true, false)
	cases := []struct {
		name    string
		user    store.User
		subject authz.Subject
		days    int
	}{
		{"password user", e.user, e.subject, 90},
		{"sso user", sso, authz.Subject{UserID: sso.ID}, 7},
		{"admin with a password", admin, authz.Subject{UserID: admin.ID, Admin: true}, 7},
	}
	for _, tc := range cases {
		view, aerr := e.svc.Validate(t.Context(), e.request(), tc.user, tc.subject)
		if aerr != nil || view.ExpiresInDays != tc.days {
			t.Errorf("%s: days = %d (%v), want %d", tc.name, view.ExpiresInDays, aerr, tc.days)
		}
	}
}

func TestValidate_SeesLogs(t *testing.T) {
	e := newOAuthEnv(t)
	cases := []struct {
		name    string
		subject authz.Subject
		want    bool
	}{
		{"view only", authz.Subject{Perms: map[authz.TagID]authz.ActionSet{1: authz.ActionSet(authz.View)}}, false},
		{"logs on a tag", authz.Subject{Perms: map[authz.TagID]authz.ActionSet{1: authz.ActionSet(authz.View | authz.Logs)}}, true},
		{"logs without view", authz.Subject{Perms: map[authz.TagID]authz.ActionSet{1: authz.ActionSet(authz.Logs)}}, false},
		{"admin", authz.Subject{Admin: true}, true},
		{"nothing", authz.Subject{}, false},
	}
	for _, tc := range cases {
		view, aerr := e.svc.Validate(t.Context(), e.request(), e.user, tc.subject)
		if aerr != nil || view.SeesLogs != tc.want {
			t.Errorf("%s: SeesLogs = %v (%v), want %v", tc.name, view.SeesLogs, aerr, tc.want)
		}
	}
}

func TestApprove_BindsTheCodeToTheRequest(t *testing.T) {
	e := newOAuthEnv(t)
	req := e.request()
	to, aerr := e.svc.Approve(t.Context(), req, e.user, "saml")
	if aerr != nil {
		t.Fatalf("%+v", aerr)
	}
	parsed, err := url.Parse(to)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != testRedirect {
		t.Fatalf("redirect = %s", to)
	}
	q := parsed.Query()
	code := q.Get("code")
	if len(code) != 43 || q.Get("state") != "st4te" || q.Get("iss") != testIssuer {
		t.Fatalf("redirect query = %v", q)
	}
	// Only the hash is stored.
	if e.count(`select count(*) from oauth_codes where code_hash = $1`, code) != 0 {
		t.Fatal("the code itself is stored")
	}
	got, err := e.st.ConsumeOAuthCode(t.Context(), apitoken.Hash(code))
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != e.user.ID || got.ClientID != req.ClientID || got.RedirectURI != req.RedirectURI ||
		got.CodeChallenge != req.CodeChallenge || got.Resource != req.Resource || got.AuthSource != "saml" ||
		got.ClientName != "Claude" || got.Scope != Scope {
		t.Fatalf("stored code = %+v", got)
	}
	if lifetime := time.Until(got.ExpiresAt); lifetime > 61*time.Second || lifetime < 50*time.Second {
		t.Fatalf("code lifetime = %v, want about 60s", lifetime)
	}
}

func TestApprove_CodesAreUnique(t *testing.T) {
	e := newOAuthEnv(t)
	seen := map[string]bool{}
	for range 20 {
		code := e.approve(e.user, e.request())
		if seen[code] {
			t.Fatal("a code was issued twice")
		}
		seen[code] = true
	}
}

func TestApprove_RevalidatesEverything(t *testing.T) {
	e := newOAuthEnv(t)
	bad := e.request()
	bad.RedirectURI = "https://evil.example/cb"
	if to, aerr := e.svc.Approve(t.Context(), bad, e.user, "password"); aerr == nil || to != "" || aerr.Redirectable {
		t.Fatalf("approve with an unlisted redirect = %q, %+v", to, aerr)
	}
	plain := e.request()
	plain.CodeChallengeMethod = "plain"
	to, aerr := e.svc.Approve(t.Context(), plain, e.user, "password")
	if aerr == nil || !aerr.Redirectable {
		t.Fatalf("approve with plain = %q, %+v", to, aerr)
	}
	if e.count(`select count(*) from oauth_codes`) != 0 {
		t.Fatal("a refused approval stored a code")
	}
	// The redirectable error carries the error and iss, never a code.
	if ErrorRedirect := e.svc.ErrorRedirect(plain, aerr); !strings.Contains(ErrorRedirect, "error=invalid_request") ||
		strings.Contains(ErrorRedirect, "code=") || !strings.Contains(ErrorRedirect, "iss=") {
		t.Fatalf("error redirect = %s", ErrorRedirect)
	}
	if got := e.svc.ErrorRedirect(bad, &AuthorizeError{Code: "x"}); got != "" {
		t.Fatalf("a non-redirectable error has a redirect: %s", got)
	}
}

func TestDeny_RedirectsWithAccessDenied(t *testing.T) {
	e := newOAuthEnv(t)
	to, aerr := e.svc.Deny(t.Context(), e.request())
	if aerr != nil {
		t.Fatalf("%+v", aerr)
	}
	parsed, _ := url.Parse(to)
	q := parsed.Query()
	if q.Get("error") != "access_denied" || q.Get("state") != "st4te" || q.Get("iss") != testIssuer || q.Get("code") != "" {
		t.Fatalf("deny redirect = %s", to)
	}
	if e.count(`select count(*) from oauth_codes`) != 0 {
		t.Fatal("deny stored a code")
	}
	bad := e.request()
	bad.RedirectURI = "https://evil.example/cb"
	if to, aerr := e.svc.Deny(t.Context(), bad); aerr == nil || to != "" {
		t.Fatalf("deny to an unlisted redirect = %q, %+v: it must not redirect", to, aerr)
	}
}
