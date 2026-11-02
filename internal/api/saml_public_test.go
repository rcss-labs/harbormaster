package api

import (
	"encoding/base64"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	crewjam "github.com/crewjam/saml"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const fakeIdPUserEmailDomain = "@example.test"

func TestAuthOptions_NullWhenUnconfigured_EnabledWhenOn(t *testing.T) {
	plain, _, _ := newTestServer(t)
	var none struct {
		SSO           *struct{} `json:"sso"`
		PasswordLogin string    `json:"password_login"`
	}
	decode(t, newAPIClient(t, plain).get("/api/auth/options"), &none)
	if none.SSO != nil || none.PasswordLogin != "all" {
		t.Fatalf("unconfigured options = %+v", none)
	}

	srv, _, st := newServerWithFakeIdP(t)
	type options struct {
		SSO *struct {
			Enabled bool   `json:"enabled"`
			Label   string `json:"label"`
		} `json:"sso"`
		PasswordLogin string `json:"password_login"`
	}
	var on options
	decode(t, newAPIClient(t, srv).get("/api/auth/options"), &on)
	if on.SSO == nil || !on.SSO.Enabled || on.SSO.Label != "Sign in with SSO" || on.PasswordLogin != "break_glass" {
		t.Fatalf("enabled options = %+v", on)
	}
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	var off options
	decode(t, newAPIClient(t, srv).get("/api/auth/options"), &off)
	if off.SSO == nil || off.SSO.Enabled || off.PasswordLogin != "all" {
		t.Fatalf("disabled options = %+v", off)
	}
}

func TestSAMLLogin_RedirectsToIdPAndSetsBindingCookie(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	b := newBrowser(t, srv)
	resp := b.get("/api/auth/saml/login?return_to=/services/x")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Host != strings.TrimPrefix(srv.URL, "http://") || loc.Path != "/fake-idp/sso" || !strings.HasPrefix(idp.EntityID(), srv.URL) {
		t.Fatalf("Location = %s", loc)
	}
	relay := loc.Query().Get("RelayState")
	if loc.Query().Get("SAMLRequest") == "" || !strings.HasPrefix(relay, "_") {
		t.Fatalf("Location query = %v", loc.Query())
	}
	cookie := cookieNamed(resp, "hm_saml_req")
	if cookie == nil || cookie.Path != "/api/auth/saml/acs" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Secure || cookie.MaxAge != 600 || cookie.Value == "" {
		t.Fatalf("binding cookie = %+v", cookie)
	}
	var returnTo, bindingHash, purpose string
	err = st.Pool.QueryRow(t.Context(), `select return_to, binding_hash, purpose from saml_requests where id = $1`, relay).
		Scan(&returnTo, &bindingHash, &purpose)
	if err != nil {
		t.Fatalf("request row: %v", err)
	}
	if returnTo != "/services/x" || purpose != "login" || bindingHash != sha256Hex(cookie.Value) {
		t.Fatalf("row = %q %q %q", returnTo, purpose, bindingHash)
	}
}

func TestSAMLLogin_BindingCookieIsSecureNoneWhenCookieSecure(t *testing.T) {
	srv, _, _, _ := newServerWithFakeIdPCustom(t, func(s *Server) { s.Cfg.CookieSecure = true })
	resp := newBrowser(t, srv).get("/api/auth/saml/login")
	defer resp.Body.Close()
	cookie := cookieNamed(resp, "hm_saml_req")
	if cookie == nil || !cookie.Secure || cookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("binding cookie = %+v", cookie)
	}
}

func TestSAMLLogin_404WhenDisabledOrUnconfigured(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	resp := newBrowser(t, srv).get("/api/auth/saml/login")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if n := countRows(t, st, `select count(*) from saml_requests`); n != 0 {
		t.Fatalf("rows = %d", n)
	}

	plain, _, _ := newTestServer(t)
	resp = newBrowser(t, plain).get("/api/auth/saml/login")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSAMLLogin_TestRequiresAdminButWorksWhileDisabled(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	anon := newBrowser(t, srv).get("/api/auth/saml/login?test=1")
	anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous test start = %d", anon.StatusCode)
	}
	omer := login(t, srv, "omer", testPassword)
	b := browserWithJar(t, srv, omer.http.Jar)
	forbidden := b.get("/api/auth/saml/login?test=1")
	forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin test start = %d", forbidden.StatusCode)
	}
	if n := countRows(t, st, `select count(*) from saml_requests`); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	pavel := login(t, srv, "pavel", testPassword)
	admin := browserWithJar(t, srv, pavel.http.Jar)
	resp := admin.get("/api/auth/saml/login?test=1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("admin test start = %d", resp.StatusCode)
	}
	if n := countRows(t, st, `select count(*) from saml_requests where purpose = 'test'`); n != 1 {
		t.Fatalf("test rows = %d", n)
	}
}

func TestSAMLLogin_ReturnToIsRelativeOnly(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	cases := []struct{ raw, want string }{
		{"/services/x", "/services/x"},
		{"/services/x?tab=logs", "/services/x?tab=logs"},
		{"//evil.example", "/"},
		{"https://evil.example", "/"},
		{`/\evil.example`, "/"},
		{"javascript:alert(1)", "/"},
		{"/\n/evil.example", "/"},
		{"/\t/evil.example", "/"},
		{"evil", "/"},
		{"", "/"},
	}
	for _, tc := range cases {
		b := newBrowser(t, srv)
		resp := b.get("/api/auth/saml/login?return_to=" + url.QueryEscape(tc.raw))
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		var got string
		if err := st.Pool.QueryRow(t.Context(), `select return_to from saml_requests where id = $1`, loc.Query().Get("RelayState")).Scan(&got); err != nil {
			t.Fatalf("%q: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("return_to %q stored as %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSAMLLogin_RateLimited(t *testing.T) {
	frozen := time.Now()
	srv, _, _, _ := newServerWithFakeIdPCustom(t, func(s *Server) { s.Now = func() time.Time { return frozen } })
	b := newBrowser(t, srv)
	for i := 0; i < 60; i++ {
		resp := b.get("/api/auth/saml/login")
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("request %d: %d", i+1, resp.StatusCode)
		}
	}
	resp := b.get("/api/auth/saml/login")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("61st request: %d", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "rate_limited" {
		t.Fatalf("code = %q", code)
	}
}

func TestACS_HappyPath_SignsInAndRedirects(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	b := newBrowser(t, srv)
	resp := b.signIn("/api/auth/saml/login?return_to=/services/x", "alex")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/services/x" {
		t.Fatalf("ACS = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if cookieNamed(resp, "hm_session") == nil {
		t.Fatal("no session cookie")
	}
	cleared := cookieNamed(resp, "hm_saml_req")
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Path != "/api/auth/saml/acs" {
		t.Fatalf("binding cookie not cleared: %+v", cleared)
	}
	if got := meUsername(t, b); got != "alex" {
		t.Fatalf("/api/me username = %q", got)
	}
	if n := countRows(t, st, `select count(*) from sessions where auth_source = 'saml'`); n != 1 {
		t.Fatalf("saml sessions = %d", n)
	}
	if n := countRows(t, st, `select count(*) from saml_requests`); n != 0 {
		t.Fatalf("request row left behind: %d", n)
	}
}

func TestACS_RemembersAssertionAndResponseIDs(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	b := newBrowser(t, srv)
	b.signIn("/api/auth/saml/login", "alex").Body.Close()
	if n := countRows(t, st, `select count(*) from saml_assertion_ids where expires_at > now() and expires_at <= now() + interval '24 hours'`); n != 2 {
		t.Fatalf("remembered ids = %d, want 2 (assertion and response)", n)
	}
}

func TestACS_UnknownRequestRedirectsWithError(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	b := newBrowser(t, srv)
	resp := b.postForm("/api/auth/saml/acs", url.Values{"SAMLResponse": {"AAAA"}, "RelayState": {"_nosuchrequest"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?error=sso_failed" {
		t.Fatalf("ACS = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := lastFailureReason(t, st); got != saml.ReasonUnknownRequest {
		t.Fatalf("reason = %q", got)
	}
	if cleared := cookieNamed(resp, "hm_saml_req"); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("binding cookie not cleared on failure: %+v", cleared)
	}
}

func TestACS_ReplayRejected(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	b := newBrowser(t, srv)
	loc := b.startLogin("/api/auth/saml/login?return_to=/services/x")
	acs, form := b.idpResponse(loc, "alex")
	first := b.postForm(acs, form)
	first.Body.Close()
	if first.Header.Get("Location") != "/services/x" {
		t.Fatalf("first = %q", first.Header.Get("Location"))
	}

	second := b.postForm(acs, form)
	second.Body.Close()
	if second.Header.Get("Location") != "/login?error=sso_failed" || lastFailureReason(t, st) != saml.ReasonUnknownRequest {
		t.Fatalf("second = %q reason %q", second.Header.Get("Location"), lastFailureReason(t, st))
	}

	// A fresh request row under the same ID (so InResponseTo still matches) must hit the assertion cache.
	cookie := "replay-cookie-value"
	err := st.CreateSAMLRequest(t.Context(), store.SAMLRequest{
		ID: form.Get("RelayState"), Purpose: "login", ReturnTo: "/", BindingHash: sha256Hex(cookie),
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "hm_saml_req", Value: cookie})
	third, err := b.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	third.Body.Close()
	if third.Header.Get("Location") != "/login?error=sso_failed" || lastFailureReason(t, st) != saml.ReasonReplay {
		t.Fatalf("third = %q reason %q", third.Header.Get("Location"), lastFailureReason(t, st))
	}
	if n := countRows(t, st, `select count(*) from sessions where auth_source = 'saml'`); n != 1 {
		t.Fatalf("saml sessions = %d, want exactly the first", n)
	}
}

func TestACS_BindingCookieRequired(t *testing.T) {
	cases := map[string]func(*testing.T, *browser) *http.Response{
		"no cookie": func(t *testing.T, b *browser) *http.Response {
			acs, form := b.idpResponse(b.startLogin("/api/auth/saml/login"), "alex")
			return newBrowserAt(t, b.base).postForm(acs, form)
		},
		"wrong cookie": func(t *testing.T, b *browser) *http.Response {
			acs, form := b.idpResponse(b.startLogin("/api/auth/saml/login"), "alex")
			req, _ := http.NewRequest(http.MethodPost, acs, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: "hm_saml_req", Value: "somebody-elses-cookie"})
			resp, err := b.http.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			return resp
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _, st := newServerWithFakeIdP(t)
			b := newBrowser(t, srv)
			// Jar-less poster for "no cookie" must not share b's jar; the "wrong cookie" case overrides the header.
			resp := run(t, b)
			resp.Body.Close()
			if resp.Header.Get("Location") != "/login?error=sso_failed" || lastFailureReason(t, st) != saml.ReasonBindingMismatch {
				t.Fatalf("ACS = %q reason %q", resp.Header.Get("Location"), lastFailureReason(t, st))
			}
			if cookieNamed(resp, "hm_session") != nil {
				t.Fatal("a session was started without the binding cookie")
			}
			if n := countRows(t, st, `select count(*) from users where username = 'alex'`); n != 0 {
				t.Fatal("user was created without the binding cookie")
			}
		})
	}
}

func TestACS_SSOOffRefusesLoginPurposeOnly(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	const cookie = "c"
	if err := st.CreateSAMLRequest(t.Context(), store.SAMLRequest{
		ID: "_offlogin", Purpose: "login", ReturnTo: "/", BindingHash: sha256Hex(cookie), ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/saml/acs", strings.NewReader(url.Values{"SAMLResponse": {"AAAA"}, "RelayState": {"_offlogin"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "hm_saml_req", Value: cookie})
	resp, err := newBrowser(t, srv).http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Location") != "/login?error=sso_off" {
		t.Fatalf("Location = %q", resp.Header.Get("Location"))
	}
	if got := lastFailureReason(t, st); got != saml.ReasonSSOOff {
		t.Fatalf("reason = %q", got)
	}
}

func TestACS_NoCSRFHeaderNeeded_ButSessionCookieIgnored(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	pavel := login(t, srv, "pavel", testPassword)
	var oldSession string
	for _, c := range pavel.http.Jar.Cookies(mustURL(t, srv.URL)) {
		if c.Name == "hm_session" {
			oldSession = c.Value
		}
	}
	b := browserWithJar(t, srv, pavel.http.Jar) // the browser still carries pavel's hm_session
	resp := b.signIn("/api/auth/saml/login", "alex")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("ACS = %d %q (no CSRF header was sent)", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := meUsername(t, b); got != "alex" {
		t.Fatalf("new session belongs to %q, want the asserted identity", got)
	}
	// pavel's own session row is untouched and still works.
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(mustURL(t, srv.URL), []*http.Cookie{{Name: "hm_session", Value: oldSession, Path: "/"}})
	if got := meUsername(t, browserWithJar(t, srv, jar)); got != "pavel" {
		t.Fatalf("presented session now resolves to %q", got)
	}
	if n := countRows(t, st, `select count(*) from sessions`); n != 2 {
		t.Fatalf("sessions = %d, want pavel's plus alex's", n)
	}
}

func TestACS_FailureCountsTowardIPLimit(t *testing.T) {
	var verifyCalls atomic.Int32
	srv, _, st, _ := newServerWithFakeIdPCustom(t, func(s *Server) {
		s.SAML = &samlService{verify: func(v *saml.Verifier, in saml.VerifyInput) (saml.Identity, error) {
			verifyCalls.Add(1)
			return v.Verify(in)
		}}
	})
	const cookie = "c"
	post := func(id string) *http.Response {
		if err := st.CreateSAMLRequest(t.Context(), store.SAMLRequest{
			ID: id, Purpose: "login", ReturnTo: "/", BindingHash: sha256Hex(cookie), ExpiresAt: time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		body := url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte("<not-saml/>"))}, "RelayState": {id}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/saml/acs", strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "hm_saml_req", Value: cookie})
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for i := 0; i < testConfig().LoginMaxPerIP; i++ {
		resp := post("_fail" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		if resp.Header.Get("Location") != "/login?error=sso_failed" {
			t.Fatalf("post %d: %q", i, resp.Header.Get("Location"))
		}
	}
	if got := len(auditByAction(t, st, "sso_sign_in_failed")); got != testConfig().LoginMaxPerIP {
		t.Fatalf("audited failures = %d", got)
	}
	if lastFailureReason(t, st) != saml.ReasonMalformed {
		t.Fatalf("reason = %q", lastFailureReason(t, st))
	}
	if n := countRows(t, st, `select count(*) from login_attempts where username like 'sso:%' and ok = false`); n != testConfig().LoginMaxPerIP {
		t.Fatalf("login_attempts = %d", n)
	}
	callsBefore := verifyCalls.Load()

	resp := post("_blocked")
	if resp.Header.Get("Location") != "/login?error=sso_failed" {
		t.Fatalf("31st post: %q", resp.Header.Get("Location"))
	}
	if verifyCalls.Load() != callsBefore {
		t.Fatal("the blocked POST reached the verifier")
	}
	if n := countRows(t, st, `select count(*) from saml_assertion_ids`); n != 0 {
		t.Fatalf("assertion ids = %d", n)
	}
	if n := countRows(t, st, `select count(*) from saml_requests where id = '_blocked'`); n != 1 {
		t.Fatal("the precheck must run before the request row is consumed")
	}
}

func TestACS_VerifierGetsServerLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var seen *slog.Logger
	srv, _, _, _ := newServerWithFakeIdPCustom(t, func(s *Server) {
		s.Log = logger
		s.SAML = &samlService{verify: func(v *saml.Verifier, in saml.VerifyInput) (saml.Identity, error) {
			seen = v.Log
			return v.Verify(in)
		}}
	})
	newBrowser(t, srv).signIn("/api/auth/saml/login", "alex").Body.Close()
	if seen != logger {
		t.Fatal("Verifier.Log must be the server logger so validator_disagreement is logged at error level")
	}
}

func TestACS_TwoServersOneStore_OnlyOneSessionWins(t *testing.T) {
	first, idp, st := newServerWithFakeIdP(t)
	second := newSecondServer(t, first, st, idp)
	b := newBrowser(t, first)
	acs, form := b.idpResponse(b.startLogin("/api/auth/saml/login?return_to=/services/x"), "alex")
	var cookieValue string
	for _, c := range b.http.Jar.Cookies(mustURL(t, acs)) {
		if c.Name == "hm_saml_req" {
			cookieValue = c.Value
		}
	}
	targets := []string{first.URL, second.URL}
	locations := make([]string, len(targets))
	var wg sync.WaitGroup
	for i, base := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, base+"/api/auth/saml/acs", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: "hm_saml_req", Value: cookieValue})
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			locations[i] = resp.Header.Get("Location")
		}()
	}
	wg.Wait()
	wins, losses := 0, 0
	for _, l := range locations {
		switch {
		case l == "/services/x":
			wins++
		case strings.HasPrefix(l, "/login?error="):
			losses++
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("locations = %v", locations)
	}
	if n := countRows(t, st, `select count(*) from sessions where auth_source = 'saml'`); n != 1 {
		t.Fatalf("saml sessions = %d", n)
	}
}

func TestACS_TestFlowWritesResultNoSession(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	pavel := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, pavel.http.Jar)
	loc := b.startLogin("/api/auth/saml/login?test=1")
	id, _ := url.Parse(loc)
	requestID := id.Query().Get("RelayState")
	acs, form := b.idpResponse(loc, "alex")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/sso?test="+requestID {
		t.Fatalf("ACS = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := countRows(t, st, `select count(*) from sessions`); n != 1 {
		t.Fatalf("sessions = %d, want only pavel's", n)
	}
	if n := countRows(t, st, `select count(*) from users where username = 'alex'`); n != 0 {
		t.Fatal("test sign-in created a user")
	}
	raw, err := st.TakeSAMLTestResult(t.Context(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK       bool   `json:"ok"`
		Reason   string `json:"reason"`
		NameID   string `json:"name_id"`
		Username string `json:"username"`
		Outcome  string `json:"outcome"`
	}
	mustJSON(t, raw, &result)
	if !result.OK || result.NameID != "alex"+fakeIdPUserEmailDomain || result.Username != "alex" || result.Outcome != "would_create" {
		t.Fatalf("result = %+v", result)
	}
	if n := countRows(t, st, `select count(*) from saml_assertion_ids`); n != 2 {
		t.Fatalf("assertion ids = %d: a test response must not be replayable as a real sign-in", n)
	}
}

func TestACS_TestFlowFailureIsStoredAsResult(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	pavel := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, pavel.http.Jar)
	idp.SetTamper(fakeIdPUnsigned())
	loc := b.startLogin("/api/auth/saml/login?test=1")
	requestID := mustURL(t, loc).Query().Get("RelayState")
	acs, form := b.idpResponse(loc, "alex")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.Header.Get("Location") != "/settings/sso?test="+requestID {
		t.Fatalf("Location = %q", resp.Header.Get("Location"))
	}
	raw, err := st.TakeSAMLTestResult(t.Context(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK      bool   `json:"ok"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	mustJSON(t, raw, &result)
	if result.OK || result.Reason != saml.ReasonUnsigned || result.Message == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestACS_LoginFailureUserLocked_AuditedAndCounted(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	alex, err := st.CreateSAMLUser(t.Context(), store.NewSAMLUser{
		Username: "alex", IdPEntityID: idp.EntityID(), ExternalID: "alex" + fakeIdPUserEmailDomain,
		ExternalIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LockUser(t.Context(), alex.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, srv)
	resp := b.signIn("/api/auth/saml/login", "alex")
	resp.Body.Close()
	if resp.Header.Get("Location") != "/login?error=sso_failed" || cookieNamed(resp, "hm_session") != nil {
		t.Fatalf("ACS = %q", resp.Header.Get("Location"))
	}
	entries := auditByAction(t, st, "sso_sign_in_failed")
	if len(entries) != 1 || entries[0].Details["reason"] != "user_locked" || entries[0].ActorUsername != "sso:alex"+fakeIdPUserEmailDomain {
		t.Fatalf("audit = %+v", entries)
	}
	if n := countRows(t, st, `select count(*) from login_attempts where username like 'sso:%' and ok = false`); n != 1 {
		t.Fatalf("login_attempts = %d", n)
	}
}

func TestACS_LoginFlow_PendingLinkSignsInExistingUser(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	seedMockupTags(t, st)
	dana, err := st.GetUserByUsername(t.Context(), "dana")
	if err != nil {
		t.Fatal(err)
	}
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	if err := st.SetPendingLink(t.Context(), dana.ID, "dana"+fakeIdPUserEmailDomain, pavel.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	usersBefore := countRows(t, st, `select count(*) from users`)

	b := newBrowser(t, srv)
	resp := b.signIn("/api/auth/saml/login", "dana")
	resp.Body.Close()
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("ACS = %q", resp.Header.Get("Location"))
	}
	var me struct {
		Username string         `json:"username"`
		Perms    map[string]int `json:"perms"`
	}
	decode(t, b.get("/api/me"), &me)
	if me.Username != "dana" || len(me.Perms) == 0 {
		t.Fatalf("me = %+v", me)
	}
	if n := countRows(t, st, `select count(*) from users`); n != usersBefore {
		t.Fatal("a new user was created instead of binding the pending link")
	}
	linked, _ := st.GetUser(t.Context(), dana.ID)
	if linked.ExternalID != "dana"+fakeIdPUserEmailDomain || linked.IdPEntityID != idp.EntityID() {
		t.Fatalf("dana = %+v", linked)
	}
}

func TestSAMLMetadata_PublicAndShape(t *testing.T) {
	srv, _, _ := newServerWithFakeIdP(t)
	resp := newAPIClient(t, srv).get("/api/auth/saml/metadata")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/samlmetadata+xml" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "harbormaster-sp-metadata.xml") {
		t.Fatalf("metadata = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
	body, _ := io.ReadAll(resp.Body)
	var ed crewjam.EntityDescriptor
	if err := xml.Unmarshal(body, &ed); err != nil {
		t.Fatal(err)
	}
	if len(ed.SPSSODescriptors) != 1 || len(ed.SPSSODescriptors[0].AssertionConsumerServices) == 0 ||
		ed.SPSSODescriptors[0].AssertionConsumerServices[0].Location != srv.URL+"/api/auth/saml/acs" {
		t.Fatalf("metadata ACS = %+v", ed.SPSSODescriptors)
	}

	plain, _, _ := newTestServer(t)
	missing := newAPIClient(t, plain).get("/api/auth/saml/metadata")
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured metadata = %d", missing.StatusCode)
	}
}

func TestSAMLLink_StartRequiresCSRFAndPassword(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)

	if resp := c.get("/api/auth/saml/link"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET link = %d", resp.StatusCode)
	}
	noCSRF := c.do(http.MethodPost, "/api/auth/saml/link", map[string]string{"password": testPassword}, map[string]string{"X-CSRF-Token": ""})
	if code, _ := errorCode(t, noCSRF); noCSRF.StatusCode != http.StatusForbidden || code != "csrf" {
		t.Fatalf("no CSRF = %d %q", noCSRF.StatusCode, code)
	}
	wrong := c.post("/api/auth/saml/link", map[string]string{"password": "not-the-password-1"})
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", wrong.StatusCode)
	}
	if n := countRows(t, st, `select count(*) from saml_requests`); n != 0 {
		t.Fatalf("rows after refused starts = %d", n)
	}

	resp := c.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link start = %d", resp.StatusCode)
	}
	cookie := cookieNamed(resp, "hm_saml_req")
	var out struct {
		RedirectURL string `json:"redirect_url"`
	}
	decode(t, resp, &out)
	loc := mustURL(t, out.RedirectURL)
	if loc.Host != strings.TrimPrefix(srv.URL, "http://") || loc.Path != "/fake-idp/sso" || cookie == nil || cookie.Path != "/api/auth/saml/acs" {
		t.Fatalf("redirect_url %s cookie %+v", out.RedirectURL, cookie)
	}
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	var purpose string
	var userID int64
	var hash string
	if err := st.Pool.QueryRow(t.Context(), `select purpose, user_id, binding_hash from saml_requests where id = $1`, loc.Query().Get("RelayState")).
		Scan(&purpose, &userID, &hash); err != nil {
		t.Fatal(err)
	}
	if purpose != "link" || userID != pavel.ID || hash != sha256Hex(cookie.Value) {
		t.Fatalf("row = %q %d %q", purpose, userID, hash)
	}
}

func TestSAMLLink_WrongPasswordsCountTowardUserLockout(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	for i := 0; i < testConfig().LoginMaxPerUser; i++ {
		resp := c.post("/api/auth/saml/link", map[string]string{"password": "not-the-password-1"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, resp.StatusCode)
		}
	}
	if n := len(auditByAction(t, st, "sign_in_failed")); n != testConfig().LoginMaxPerUser {
		t.Fatalf("sign_in_failed audits = %d", n)
	}
	resp := c.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("locked user with the right password = %d", resp.StatusCode)
	}
	other := newAPIClient(t, srv)
	signIn := other.post("/api/auth/login", map[string]string{"username": "pavel", "password": testPassword})
	signIn.Body.Close()
	if signIn.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password sign-in after link-start lockout = %d", signIn.StatusCode)
	}
}

func TestSAMLLink_StartWorksWhileSSODisabled(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	c := login(t, srv, "pavel", testPassword)
	resp := c.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link start while disabled = %d", resp.StatusCode)
	}
}

func TestSAMLLink_StartRefusals(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	if _, err := st.BindSSOIdentity(t.Context(), store.BindSSO{
		UserID: pavel.ID, IdPEntityID: idp.EntityID(), ExternalID: "x@example.test", ExternalIDFormat: "f",
	}); err != nil {
		t.Fatal(err)
	}
	resp := c.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	if code, _ := errorCode(t, resp); resp.StatusCode != http.StatusConflict || code != "already_linked" {
		t.Fatalf("already linked = %d %q", resp.StatusCode, code)
	}

	plain, _, _ := newTestServer(t)
	pc := login(t, plain, "pavel", testPassword)
	resp = pc.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured = %d", resp.StatusCode)
	}
}

func startLink(t *testing.T, c *apiClient) (redirectURL string, cookie *http.Cookie) {
	t.Helper()
	resp := c.post("/api/auth/saml/link", map[string]string{"password": testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("link start = %d", resp.StatusCode)
	}
	cookie = cookieNamed(resp, "hm_saml_req")
	var out struct {
		RedirectURL string `json:"redirect_url"`
	}
	decode(t, resp, &out)
	return out.RedirectURL, cookie
}

func TestACS_LinkFlow_BindsAndKeepsSession(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	if _, err := st.SetSAMLEnabled(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	c := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, c.http.Jar) // the SPA's browser: carries hm_session and the binding cookie
	redirectURL, _ := startLink(t, c)
	acs, form := b.idpResponse(redirectURL, "pavel")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/?sso_link=ok" {
		t.Fatalf("ACS = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if cookieNamed(resp, "hm_session") != nil {
		t.Fatal("link outcome must not set a session cookie")
	}
	if n := countRows(t, st, `select count(*) from sessions`); n != 1 {
		t.Fatalf("sessions = %d", n)
	}
	me := c.get("/api/me")
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("existing session stopped working: %d", me.StatusCode)
	}
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	if !pavel.SSOLinked() || pavel.ExternalID != "pavel"+fakeIdPUserEmailDomain || pavel.IdPEntityID != idp.EntityID() || !pavel.HasPassword() {
		t.Fatalf("pavel = %+v", pavel)
	}
}

func TestACS_LinkFlow_NoSessionCookieNeeded(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	redirectURL, cookie := startLink(t, c)
	jar, _ := cookiejar.New(nil) // a browser with no hm_session at all
	jar.SetCookies(mustURL(t, srv.URL), []*http.Cookie{{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path}})
	b := browserWithJar(t, srv, jar)
	acs, form := b.idpResponse(redirectURL, "pavel")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.Header.Get("Location") != "/?sso_link=ok" {
		t.Fatalf("ACS = %q", resp.Header.Get("Location"))
	}
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	if !pavel.SSOLinked() {
		t.Fatal("the row's user was not linked")
	}
}

func TestACS_LinkFlow_IdentityTakenRedirect(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	if _, err := st.CreateSAMLUser(t.Context(), store.NewSAMLUser{
		Username: "alex", IdPEntityID: idp.EntityID(), ExternalID: "pavel" + fakeIdPUserEmailDomain, ExternalIDFormat: "f",
	}); err != nil {
		t.Fatal(err)
	}
	c := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, c.http.Jar)
	redirectURL, _ := startLink(t, c)
	acs, form := b.idpResponse(redirectURL, "pavel")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.Header.Get("Location") != "/?sso_link=sso_identity_taken" {
		t.Fatalf("ACS = %q", resp.Header.Get("Location"))
	}
	if n := len(auditByAction(t, st, "sso_link_failed")); n != 1 {
		t.Fatalf("sso_link_failed audits = %d", n)
	}
	pavel, _ := st.GetUserByUsername(t.Context(), "pavel")
	if pavel.SSOLinked() {
		t.Fatal("pavel was linked to a taken identity")
	}
}

func TestACS_LinkFlow_FailureBeforeBindingIsAuditedAndRedirectsToLinkError(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	idp.SetTamper(fakeIdPUnsigned())
	c := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, c.http.Jar)
	redirectURL, _ := startLink(t, c)
	acs, form := b.idpResponse(redirectURL, "pavel")
	resp := b.postForm(acs, form)
	resp.Body.Close()
	if resp.Header.Get("Location") != "/?sso_link="+saml.ReasonUnsigned {
		t.Fatalf("ACS = %q", resp.Header.Get("Location"))
	}
	entries := auditByAction(t, st, "sso_link_failed")
	if len(entries) != 1 || entries[0].Details["reason"] != saml.ReasonUnsigned {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestACS_GetIsMethodNotAllowed(t *testing.T) {
	srv, _, _ := newServerWithFakeIdP(t)
	resp := newBrowser(t, srv).get("/api/auth/saml/acs")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET ACS = %d", resp.StatusCode)
	}
}
