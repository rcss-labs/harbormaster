package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const ssoTestBaseURL = "https://hm.example.test"

// ssoCfgView is the part of SSOConfigDTO the tests read.
type ssoCfgView struct {
	Configured  bool   `json:"configured"`
	Enabled     bool   `json:"enabled"`
	IdPEntityID string `json:"idp_entity_id"`
	IdPCerts    []struct {
		Fingerprint string `json:"fingerprint"`
		Subject     string `json:"subject"`
	} `json:"idp_certs"`
	MetadataURL  *string `json:"metadata_url"`
	SPEntityID   string  `json:"sp_entity_id"`
	ACSURL       string  `json:"acs_url"`
	MetadataPath string  `json:"metadata_path"`
	SPCert       *struct {
		Fingerprint string `json:"fingerprint"`
	} `json:"sp_cert"`
	SPKeyState                   string   `json:"sp_key_state"`
	NameIDFormat                 string   `json:"name_id_format"`
	ButtonLabel                  string   `json:"button_label"`
	BreakGlassAdmins             int      `json:"break_glass_admins"`
	PasswordUsersBlockedOnEnable int      `json:"password_users_blocked_on_enable"`
	Warnings                     []string `json:"warnings"`
}

type ssoUserView struct {
	ID                   int64   `json:"id"`
	Username             string  `json:"username"`
	HasPassword          bool    `json:"has_password"`
	BreakGlass           bool    `json:"break_glass"`
	SSO                  string  `json:"sso"`
	SSONameID            *string `json:"sso_name_id"`
	PendingLinkExpiresAt *string `json:"pending_link_expires_at"`
	Email                *string `json:"email"`
	DisplayName          *string `json:"display_name"`
}

func ssoGetConfig(t *testing.T, c *apiClient) ssoCfgView {
	t.Helper()
	resp := c.get("/api/sso")
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/sso = %d %s", resp.StatusCode, b)
	}
	var out ssoCfgView
	decode(t, resp, &out)
	return out
}

func ssoUsers(t *testing.T, c *apiClient) map[string]ssoUserView {
	t.Helper()
	var list struct {
		Items []ssoUserView `json:"items"`
	}
	decode(t, c.get("/api/users"), &list)
	out := map[string]ssoUserView{}
	for _, u := range list.Items {
		out[u.Username] = u
	}
	return out
}

func ssoUserID(t *testing.T, st *store.Store, username string) int64 {
	t.Helper()
	u, err := st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func ssoUserPath(t *testing.T, st *store.Store, username, suffix string) string {
	t.Helper()
	return "/api/users/" + strconv.FormatInt(ssoUserID(t, st, username), 10) + suffix
}

func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, b)
	}
}

func expectInvalid(t *testing.T, resp *http.Response, message string) {
	t.Helper()
	code, msg := errorCode(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid" || msg != message {
		t.Fatalf("got %d %q %q, want 422 invalid %q", resp.StatusCode, code, msg, message)
	}
}

func withBaseURL(s *Server) { s.Cfg.BaseURL = ssoTestBaseURL }

// importFakeIdPMetadata configures SSO on an unconfigured server from a stand-alone fake IdP's metadata.
func importFakeIdPMetadata(t *testing.T, c *apiClient) *fakeidp.Server {
	t.Helper()
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	resp := c.post("/api/sso/metadata", map[string]any{"xml": string(idp.Metadata())})
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	return idp
}

// ssoSignInAs signs user in through the fake IdP and returns the browser (and so the SSO session).
func ssoSignInAs(t *testing.T, srv *httptest.Server, user string) *browser {
	t.Helper()
	b := newBrowser(t, srv)
	resp := b.signIn("/api/auth/saml/login", user)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("SSO sign-in as %s = %d %q", user, resp.StatusCode, resp.Header.Get("Location"))
	}
	return b
}

// clientFromBrowser lets the JSON helpers reuse a browser's session.
func clientFromBrowser(t *testing.T, srv *httptest.Server, b *browser) *apiClient {
	t.Helper()
	c := newAPIClient(t, srv)
	c.http.Jar = b.http.Jar
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	decode(t, c.get("/api/me"), &me)
	c.csrf = me.CSRFToken
	return c
}

func TestSSO_GetUnconfigured_ShowsSPValues(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, withBaseURL)
	c := login(t, srv, "pavel", testPassword)
	got := ssoGetConfig(t, c)
	if got.Configured || got.Enabled {
		t.Fatalf("configured/enabled = %v/%v", got.Configured, got.Enabled)
	}
	if got.SPEntityID != ssoTestBaseURL+"/api/auth/saml/metadata" || got.ACSURL != ssoTestBaseURL+"/api/auth/saml/acs" ||
		got.MetadataPath != "/api/auth/saml/metadata" {
		t.Fatalf("sp values = %+v", got)
	}
	if got.IdPCerts == nil || got.Warnings == nil || got.SPCert != nil || got.SPKeyState != "missing" {
		t.Fatalf("zero DTO shape = %+v", got)
	}
}

func TestSSO_ImportMetadataFromXML_ThenGet(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withBaseURL)
	c := login(t, srv, "pavel", testPassword)
	idp := importFakeIdPMetadata(t, c)

	got := ssoGetConfig(t, c)
	if !got.Configured || got.Enabled || got.IdPEntityID != idp.EntityID() || len(got.IdPCerts) != 1 ||
		got.IdPCerts[0].Fingerprint == "" || got.SPCert == nil || got.SPKeyState != "ok" {
		t.Fatalf("config = %+v", got)
	}
	entries := auditByAction(t, st, "sso_metadata_import")
	if len(entries) != 1 {
		t.Fatalf("sso_metadata_import audit rows = %d", len(entries))
	}
	raw, _ := json.Marshal(entries[0])
	if strings.Contains(string(raw), "BEGIN") || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("audit leaks key material: %s", raw)
	}
	if keys, _ := st.ListSPKeys(t.Context()); len(keys) != 1 {
		t.Fatalf("sp keys = %d, want one generated on first save", len(keys))
	}
}

func TestSSO_ImportMetadataFromURL_FakeIdP(t *testing.T) {
	srv, idp, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	resp := c.post("/api/sso/metadata", map[string]any{"url": srv.URL + "/fake-idp/metadata"})
	expectStatus(t, resp, http.StatusOK)
	var got ssoCfgView
	decode(t, resp, &got)
	if !got.Configured || got.MetadataURL == nil || *got.MetadataURL != srv.URL+"/fake-idp/metadata" || got.IdPEntityID != idp.EntityID() {
		t.Fatalf("config = %+v", got)
	}
	entries := auditByAction(t, st, "sso_metadata_import")
	if len(entries) != 1 {
		t.Fatalf("audit rows = %d", len(entries))
	}
	if n := countRows(t, st, `select count(*) from sessions where auth_source = 'saml'`); n != 0 {
		t.Fatal("same entity ID and certificates must not end sessions")
	}
}

func TestSSO_ImportRejectsBadMetadata_422Messages(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.Cfg.CookieSecure = true })
	c := login(t, srv, "pavel", testPassword)
	const ns = `xmlns="urn:oasis:names:tc:SAML:2.0:metadata"`
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"garbage", map[string]any{"xml": "not xml"}, "Metadata is not valid SAML metadata."},
		{"no idp descriptor", map[string]any{"xml": `<EntityDescriptor ` + ns + ` entityID="x"></EntityDescriptor>`}, "Metadata has no IDPSSODescriptor."},
		{"http url in production", map[string]any{"url": "http://idp.example.test/metadata"}, "Fetching metadata needs an https URL."},
		{"neither", map[string]any{}, "Send either a metadata URL or metadata XML."},
		{"both", map[string]any{"url": "https://a.example.test/m", "xml": "<x/>"}, "Send either a metadata URL or metadata XML."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectInvalid(t, c.post("/api/sso/metadata", tc.body), tc.want)
		})
	}
}

func TestSSO_Put_RequiresSessionKey(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.Cfg.SessionKey = "" })
	c := login(t, srv, "pavel", testPassword)
	body := map[string]any{"idp_entity_id": "https://idp.example.test", "idp_sso_redirect_url": "http://idp.example.test/sso"}
	expectInvalid(t, c.do(http.MethodPut, "/api/sso", body, nil), "Set HM_SESSION_KEY before configuring single sign-on.")
}

func putBody(entityID string) map[string]any {
	return map[string]any{
		"idp_entity_id": entityID, "idp_sso_redirect_url": "http://idp.example.test/sso",
		"name_id_format": "unspecified", "clock_skew_seconds": 120, "button_label": "Sign in with Acme",
	}
}

func TestSSO_Put_ChangingEntityIDEndsSAMLSessions_AuditsCount(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	alex := ssoSignInAs(t, srv, "alex")
	c := login(t, srv, "pavel", testPassword)

	// Label-only change keeps SSO sessions and the stored certificates.
	same := ssoGetConfig(t, c).IdPEntityID
	body := putBody(same)
	body["idp_sso_redirect_url"] = ssoGetConfig(t, c).IdPEntityID + "/sso"
	expectStatus(t, c.do(http.MethodPut, "/api/sso", body, nil), http.StatusOK)
	if meUsername(t, alex) != "alex" {
		t.Fatal("a non-identity change ended an SSO session")
	}
	if got := ssoGetConfig(t, c); got.ButtonLabel != "Sign in with Acme" || len(got.IdPCerts) != 1 {
		t.Fatalf("after label change = %+v", got)
	}

	expectStatus(t, c.do(http.MethodPut, "/api/sso", putBody("https://other.example.test/idp"), nil), http.StatusOK)
	if meUsername(t, alex) != "" {
		t.Fatal("changing the entity ID must end SAML sessions")
	}
	entries := auditByAction(t, st, "sso_config_update")
	if len(entries) != 2 {
		t.Fatalf("sso_config_update rows = %d", len(entries))
	}
	latest := entries[0].Details
	if latest["idp_entity_id_changed"] != true || latest["sessions_ended"] != float64(1) {
		t.Fatalf("details = %v", latest)
	}
	raw, _ := json.Marshal(latest)
	if strings.Contains(string(raw), "BEGIN") {
		t.Fatalf("audit leaks certificate: %s", raw)
	}
	if n := countRows(t, st, `select count(*) from sessions where auth_source = 'password'`); n != 1 {
		t.Fatalf("password sessions = %d, want pavel's", n)
	}
}

func TestSSO_Enable_RequiresBreakGlassAdmin_Message(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, withBaseURL)
	c := login(t, srv, "pavel", testPassword)
	importFakeIdPMetadata(t, c)
	resp := c.do(http.MethodPut, "/api/sso/enabled", map[string]bool{"enabled": true}, nil)
	expectInvalid(t, resp, "Flag at least one global admin as break-glass before turning on single sign-on.")
}

func TestSSO_Disable_ConfirmsSessionsEnded(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	alex := ssoSignInAs(t, srv, "alex")
	c := login(t, srv, "pavel", testPassword)
	resp := c.do(http.MethodPut, "/api/sso/enabled", map[string]bool{"enabled": false}, nil)
	expectStatus(t, resp, http.StatusOK)
	var got ssoCfgView
	decode(t, resp, &got)
	if got.Enabled || !got.Configured {
		t.Fatalf("config = %+v", got)
	}
	if meUsername(t, alex) != "" {
		t.Fatal("SAML session survived disabling")
	}
	if meUsername(t, newBrowserWithClient(t, srv, c)) != "pavel" {
		t.Fatal("the admin who disabled SSO was signed out")
	}
	entries := auditByAction(t, st, "sso_disabled")
	if len(entries) != 1 || entries[0].Details["sessions_ended"] != float64(1) {
		t.Fatalf("sso_disabled audit = %+v", entries)
	}
}

func newBrowserWithClient(t *testing.T, srv *httptest.Server, c *apiClient) *browser {
	return browserWithJar(t, srv, c.http.Jar)
}

func TestSSO_RotateKey_MetadataListsBothCerts(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	before := ssoGetConfig(t, c)
	resp := c.post("/api/sso/key", nil)
	expectStatus(t, resp, http.StatusOK)
	var after ssoCfgView
	decode(t, resp, &after)
	if after.SPCert == nil || before.SPCert == nil || after.SPCert.Fingerprint == before.SPCert.Fingerprint {
		t.Fatalf("sp cert did not change: %+v -> %+v", before.SPCert, after.SPCert)
	}
	meta, err := http.Get(srv.URL + "/api/auth/saml/metadata")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(meta.Body)
	meta.Body.Close()
	if n := len(regexp.MustCompile(`<[A-Za-z0-9]*:?X509Certificate[ >]`).FindAll(body, -1)); n != 2 {
		t.Fatalf("metadata lists %d certificates, want 2", n)
	}
	entries := auditByAction(t, st, "sso_key_rotated")
	if len(entries) != 1 || entries[0].Details["fingerprint"] != after.SPCert.Fingerprint {
		t.Fatalf("sso_key_rotated audit = %+v", entries)
	}
}

func TestSSO_TakeTest_OnceThen404(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	b := browserWithJar(t, srv, c.http.Jar)
	acsResp := b.signIn("/api/auth/saml/login?test=1", "alex")
	acsResp.Body.Close()
	loc := acsResp.Header.Get("Location")
	id := strings.TrimPrefix(loc, "/settings/sso?test=")
	if id == loc || id == "" {
		t.Fatalf("ACS location = %q", loc)
	}

	resp := c.get("/api/sso/tests/" + id)
	expectStatus(t, resp, http.StatusOK)
	var got struct {
		OK         bool                `json:"ok"`
		NameID     string              `json:"name_id"`
		Attributes map[string][]string `json:"attributes"`
		Username   string              `json:"username"`
		Outcome    string              `json:"outcome"`
	}
	decode(t, resp, &got)
	if !got.OK || got.NameID != "alex"+fakeIdPUserEmailDomain || got.Outcome != "would_create" || got.Username != "alex" || len(got.Attributes) == 0 {
		t.Fatalf("result = %+v", got)
	}
	second := c.get("/api/sso/tests/" + id)
	if code, _ := errorCode(t, second); second.StatusCode != http.StatusNotFound || code != "not_found" {
		t.Fatalf("second read = %d %q", second.StatusCode, code)
	}
	entries := auditByAction(t, st, "sso_test")
	if len(entries) != 1 || entries[0].ActorUsername != "pavel" || entries[0].ActorUserID == nil {
		t.Fatalf("sso_test audit = %+v: want exactly one, attributed to the admin who read the result", entries)
	}
}

func TestSSO_TestResult_WouldLink(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	resp := c.do(http.MethodPut, ssoUserPath(t, st, "omer", "/sso-link"), map[string]string{"name_id": "alex" + fakeIdPUserEmailDomain}, nil)
	expectStatus(t, resp, http.StatusNoContent)
	b := browserWithJar(t, srv, c.http.Jar)
	acsResp := b.signIn("/api/auth/saml/login?test=1", "alex")
	acsResp.Body.Close()
	id := strings.TrimPrefix(acsResp.Header.Get("Location"), "/settings/sso?test=")
	var got struct {
		Outcome      string  `json:"outcome"`
		ExistingUser *string `json:"existing_user"`
	}
	decode(t, c.get("/api/sso/tests/"+id), &got)
	if got.Outcome != "would_link" || got.ExistingUser == nil || *got.ExistingUser != "omer" {
		t.Fatalf("result = %+v", got)
	}
}

func TestSSO_Get_CountsPasswordUsersBlockedOnEnable(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withBaseURL)
	c := login(t, srv, "pavel", testPassword)
	importFakeIdPMetadata(t, c)
	hash, _ := st.GetUserByUsername(t.Context(), "omer")
	if _, err := st.CreateUser(t.Context(), "alex", hash.PasswordHash, false, false); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, c.post(ssoUserPath(t, st, "pavel", "/break-glass"), map[string]bool{"break_glass": true}), http.StatusNoContent)
	got := ssoGetConfig(t, c)
	if got.BreakGlassAdmins != 1 || got.PasswordUsersBlockedOnEnable != 2 {
		t.Fatalf("break_glass_admins=%d blocked=%d, want 1 and 2 (omer, alex)", got.BreakGlassAdmins, got.PasswordUsersBlockedOnEnable)
	}
}

func TestSSO_Enable_LeavesPasswordsIntact(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withBaseURL)
	c := login(t, srv, "pavel", testPassword)
	importFakeIdPMetadata(t, c)
	expectStatus(t, c.post(ssoUserPath(t, st, "pavel", "/break-glass"), map[string]bool{"break_glass": true}), http.StatusNoContent)
	hashes := func() string {
		rows, err := st.Pool.Query(t.Context(), `select coalesce(string_agg(username || ':' || coalesce(password_hash, '-'), ',' order by username), '') from users`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var s string
		for rows.Next() {
			_ = rows.Scan(&s)
		}
		return s
	}
	before := hashes()
	resp := c.do(http.MethodPut, "/api/sso/enabled", map[string]bool{"enabled": true}, nil)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	if after := hashes(); after != before {
		t.Fatal("enabling SSO changed password hashes")
	}
	for _, e := range auditByAction(t, st, "password_reset") {
		t.Fatalf("unexpected password audit row %+v", e)
	}
	if len(auditByAction(t, st, "sso_enabled")) != 1 {
		t.Fatal("sso_enabled not audited")
	}
}

func TestSSO_NonAdmin403(t *testing.T) {
	srv, _, st := newTestServer(t)
	c := login(t, srv, "omer", testPassword)
	pavelPath := ssoUserPath(t, st, "pavel", "")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/sso"}, {http.MethodPut, "/api/sso"}, {http.MethodPost, "/api/sso/metadata"},
		{http.MethodPut, "/api/sso/enabled"}, {http.MethodPost, "/api/sso/key"}, {http.MethodGet, "/api/sso/tests/_x"},
		{http.MethodPost, pavelPath + "/break-glass"}, {http.MethodPut, pavelPath + "/sso-link"},
		{http.MethodDelete, pavelPath + "/sso-link"}, {http.MethodPost, pavelPath + "/sso-unlink"},
	} {
		resp := c.do(tc.method, tc.path, map[string]any{}, nil)
		if code, _ := errorCode(t, resp); resp.StatusCode != http.StatusForbidden || code != "forbidden" {
			t.Errorf("%s %s = %d %q", tc.method, tc.path, resp.StatusCode, code)
		}
	}
}

func TestBreakGlass_GrantRevoke_Audited(t *testing.T) {
	srv, _, st := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	path := ssoUserPath(t, st, "omer", "/break-glass")
	expectStatus(t, c.post(path, map[string]bool{"break_glass": true}), http.StatusNoContent)
	if !ssoUsers(t, c)["omer"].BreakGlass {
		t.Fatal("omer is not break-glass")
	}
	expectStatus(t, c.post(path, map[string]bool{"break_glass": true}), http.StatusNoContent) // no change, no audit
	expectStatus(t, c.post(path, map[string]bool{"break_glass": false}), http.StatusNoContent)
	if ssoUsers(t, c)["omer"].BreakGlass {
		t.Fatal("omer is still break-glass")
	}
	if g, r := auditByAction(t, st, "break_glass_grant"), auditByAction(t, st, "break_glass_revoke"); len(g) != 1 || len(r) != 1 ||
		g[0].Summary != "Made omer a break-glass account." || r[0].Summary != "Removed break-glass from omer." {
		t.Fatalf("audit grant=%+v revoke=%+v", g, r)
	}
}

func TestBreakGlass_PasswordlessUser422Message(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	ssoSignInAs(t, srv, "alex")
	c := login(t, srv, "pavel", testPassword)
	resp := c.post(ssoUserPath(t, st, "alex", "/break-glass"), map[string]bool{"break_glass": true})
	expectInvalid(t, resp, "Set a password for alex first.")
}

func TestBreakGlass_LastWhileEnabled422(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	c := login(t, srv, "pavel", testPassword)
	resp := c.post(ssoUserPath(t, st, "pavel", "/break-glass"), map[string]bool{"break_glass": false})
	expectInvalid(t, resp, "Keep at least one break-glass global admin while single sign-on is on.")
	if !ssoUsers(t, c)["pavel"].BreakGlass {
		t.Fatal("the last break-glass admin lost the flag")
	}
}

func TestPendingLink_SetClear_Audited(t *testing.T) {
	srv, _, st := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	path := ssoUserPath(t, st, "omer", "/sso-link")
	expectStatus(t, c.do(http.MethodPut, path, map[string]string{"name_id": "  omer@idp.example  "}, nil), http.StatusNoContent)
	omer := ssoUsers(t, c)["omer"]
	if omer.SSO != "pending" || omer.SSONameID == nil || *omer.SSONameID != "omer@idp.example" || omer.PendingLinkExpiresAt == nil {
		t.Fatalf("omer = %+v", omer)
	}
	expires, err := time.Parse(time.RFC3339Nano, *omer.PendingLinkExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires) - 14*24*time.Hour; d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry = %v", expires)
	}
	expectStatus(t, c.do(http.MethodDelete, path, nil, nil), http.StatusNoContent)
	omer = ssoUsers(t, c)["omer"]
	if omer.SSO != "none" || omer.SSONameID != nil || omer.PendingLinkExpiresAt != nil {
		t.Fatalf("omer after clear = %+v", omer)
	}
	if set, cleared := auditByAction(t, st, "sso_link_pending_set"), auditByAction(t, st, "sso_link_pending_cleared"); len(set) != 1 || len(cleared) != 1 ||
		set[0].Details["name_id"] != "omer@idp.example" {
		t.Fatalf("audit set=%+v cleared=%+v", set, cleared)
	}
	// Nothing left to clear.
	if resp := c.do(http.MethodDelete, path, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second clear = %d", resp.StatusCode)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 257)} {
		expectInvalid(t, c.do(http.MethodPut, path, map[string]string{"name_id": bad}, nil), "Enter a NameID of 1 to 256 characters.")
	}
}

func TestPendingLink_Conflicts409(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	ssoSignInAs(t, srv, "alex") // alex@example.test is now linked
	c := login(t, srv, "pavel", testPassword)

	linkedUser := c.do(http.MethodPut, ssoUserPath(t, st, "alex", "/sso-link"), map[string]string{"name_id": "x@idp.example"}, nil)
	if code, _ := errorCode(t, linkedUser); linkedUser.StatusCode != http.StatusConflict || code != "already_linked" {
		t.Fatalf("linked user = %d %q", linkedUser.StatusCode, code)
	}
	taken := c.do(http.MethodPut, ssoUserPath(t, st, "omer", "/sso-link"), map[string]string{"name_id": "alex" + fakeIdPUserEmailDomain}, nil)
	if code, _ := errorCode(t, taken); taken.StatusCode != http.StatusConflict || code != "sso_identity_taken" {
		t.Fatalf("linked elsewhere = %d %q", taken.StatusCode, code)
	}
	expectStatus(t, c.do(http.MethodPut, ssoUserPath(t, st, "omer", "/sso-link"), map[string]string{"name_id": "shared@idp.example"}, nil), http.StatusNoContent)
	pending := c.do(http.MethodPut, ssoUserPath(t, st, "pavel", "/sso-link"), map[string]string{"name_id": "shared@idp.example"}, nil)
	if code, _ := errorCode(t, pending); pending.StatusCode != http.StatusConflict || code != "sso_identity_taken" {
		t.Fatalf("pending elsewhere = %d %q", pending.StatusCode, code)
	}
}

func TestPendingLink_ExpiredShowsInDTO(t *testing.T) {
	var offset atomic.Int64
	srv, _, st, _ := newServerCustom(t, true, func(s *Server) {
		s.Now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	})
	c := login(t, srv, "pavel", testPassword)
	expectStatus(t, c.do(http.MethodPut, ssoUserPath(t, st, "omer", "/sso-link"), map[string]string{"name_id": "omer@idp.example"}, nil), http.StatusNoContent)
	offset.Store(int64(15 * 24 * time.Hour))
	omer := ssoUsers(t, c)["omer"]
	if omer.SSO != "pending_expired" || omer.SSONameID == nil {
		t.Fatalf("omer = %+v", omer)
	}
}

func TestUnlink_SetsTemporaryPasswordWhenNone(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	alex := ssoSignInAs(t, srv, "alex")
	c := login(t, srv, "pavel", testPassword)

	resp := c.post(ssoUserPath(t, st, "alex", "/sso-unlink"), nil)
	expectStatus(t, resp, http.StatusOK)
	var out map[string]string
	decode(t, resp, &out)
	if out["temporary_password"] == "" {
		t.Fatalf("body = %v", out)
	}
	if meUsername(t, alex) != "" {
		t.Fatal("the unlinked user's SSO session survived")
	}
	u := ssoUsers(t, c)["alex"]
	if u.SSO != "none" || !u.HasPassword {
		t.Fatalf("alex = %+v", u)
	}
	entries := auditByAction(t, st, "sso_unlinked")
	if len(entries) != 1 || entries[0].Details["password_set"] != true || entries[0].Details["sessions_ended"] != float64(1) {
		t.Fatalf("audit = %+v", entries)
	}

	// A linked user who has a password keeps it and gets no temporary one.
	omer, _ := st.GetUserByUsername(t.Context(), "omer")
	if _, err := st.BindSSOIdentity(t.Context(), store.BindSSO{UserID: omer.ID, IdPEntityID: "e", ExternalID: "omer@idp.example", ExternalIDFormat: "x"}); err != nil {
		t.Fatal(err)
	}
	resp = c.post(ssoUserPath(t, st, "omer", "/sso-unlink"), nil)
	expectStatus(t, resp, http.StatusOK)
	var empty map[string]any
	decode(t, resp, &empty)
	if len(empty) != 0 {
		t.Fatalf("body = %v, want {}", empty)
	}
	entries = auditByAction(t, st, "sso_unlinked")
	if len(entries) != 2 || entries[0].Details["password_set"] != false {
		t.Fatalf("audit = %+v", entries)
	}
	after, _ := st.GetUser(t.Context(), omer.ID)
	if after.PasswordHash != omer.PasswordHash {
		t.Fatal("unlink changed an existing password")
	}
}

func TestUnlink_404WhenNotLinked(t *testing.T) {
	srv, _, st := newTestServer(t)
	c := login(t, srv, "pavel", testPassword)
	if resp := c.post(ssoUserPath(t, st, "omer", "/sso-unlink"), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unlink unlinked = %d", resp.StatusCode)
	}
}

func TestResetPassword_PasswordlessUserWorks(t *testing.T) {
	srv, _, st := newServerWithFakeIdP(t)
	ssoSignInAs(t, srv, "alex")
	c := login(t, srv, "pavel", testPassword)
	resp := c.post(ssoUserPath(t, st, "alex", "/reset-password"), nil)
	expectStatus(t, resp, http.StatusOK)
	var out map[string]string
	decode(t, resp, &out)
	if out["temporary_password"] == "" || !ssoUsers(t, c)["alex"].HasPassword {
		t.Fatalf("body = %v", out)
	}
}

func TestChangePassword_Passwordless422(t *testing.T) {
	srv, _, _ := newServerWithFakeIdP(t)
	alex := clientFromBrowser(t, srv, ssoSignInAs(t, srv, "alex"))
	resp := alex.post("/api/auth/password", map[string]string{"current": "whatever-1", "new": "another-good-password-1"})
	expectInvalid(t, resp, "This account has no password.")
}

func TestMe_AndUsers_CarryCredentialFields(t *testing.T) {
	srv, _, _ := newServerWithFakeIdP(t)
	alex := clientFromBrowser(t, srv, ssoSignInAs(t, srv, "alex"))
	var me struct {
		HasPassword bool    `json:"has_password"`
		SSOLinked   bool    `json:"sso_linked"`
		BreakGlass  bool    `json:"break_glass"`
		Email       *string `json:"email"`
		DisplayName *string `json:"display_name"`
	}
	decode(t, alex.get("/api/me"), &me)
	if me.HasPassword || !me.SSOLinked || me.BreakGlass || me.Email == nil || *me.Email != "alex"+fakeIdPUserEmailDomain ||
		me.DisplayName == nil || *me.DisplayName != "Alex" {
		t.Fatalf("alex me = %+v (email=%v)", me, me.Email)
	}
	pavel := login(t, srv, "pavel", testPassword)
	decode(t, pavel.get("/api/me"), &me)
	if !me.HasPassword || me.SSOLinked || !me.BreakGlass {
		t.Fatalf("pavel me = %+v", me)
	}
	users := ssoUsers(t, pavel)
	a, p := users["alex"], users["pavel"]
	if a.HasPassword || a.BreakGlass || a.SSO != "linked" || a.SSONameID == nil || *a.SSONameID != "alex"+fakeIdPUserEmailDomain ||
		a.Email == nil || a.DisplayName == nil || a.PendingLinkExpiresAt != nil {
		t.Fatalf("alex user = %+v", a)
	}
	if !p.HasPassword || !p.BreakGlass || p.SSO != "none" || p.SSONameID != nil {
		t.Fatalf("pavel user = %+v", p)
	}
}

func TestLogin_SSORequiredIs403(t *testing.T) {
	srv, _, _ := newServerWithFakeIdP(t)
	c := newAPIClient(t, srv)
	resp := c.do(http.MethodPost, "/api/auth/login", map[string]string{"username": "omer", "password": testPassword}, nil)
	code, msg := errorCode(t, resp)
	if resp.StatusCode != http.StatusForbidden || code != "sso_required" || msg != "Use single sign-on to sign in to this account." {
		t.Fatalf("got %d %q %q", resp.StatusCode, code, msg)
	}
	if len(resp.Cookies()) != 0 {
		t.Fatal("a refused sign-in set a cookie")
	}
}

func TestSSO_ImportMetadataFromURL_BlocksLinkLocalWhenCookieInsecure(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("secret-credentials"))
	}))
	t.Cleanup(target.Close)
	// CookieSecure=false and FakeAWS=false: a plain-http install must still get the filtered, https-only fetcher.
	srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.Cfg.CookieSecure, s.Cfg.FakeAWS = false, false })
	c := login(t, srv, "pavel", testPassword)
	for _, target := range []string{"http://169.254.169.254/latest/meta-data/", "https://169.254.170.2/v2/credentials", target.URL} {
		resp := c.post("/api/sso/metadata", map[string]any{"url": target})
		code, msg := errorCode(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid" || strings.Contains(msg, "secret-credentials") {
			t.Fatalf("%s: got %d %q %q", target, resp.StatusCode, code, msg)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("the server made an outbound request to a local address")
	}
}

func TestSSO_Put_HTTPSignInURLOnlyInFakeAWSMode(t *testing.T) {
	srv, _, _, _ := newServerCustom(t, true, func(s *Server) { s.Cfg.CookieSecure, s.Cfg.FakeAWS = false, false })
	c := login(t, srv, "pavel", testPassword)
	body := putBody("https://idp.example.test")
	expectInvalid(t, c.do(http.MethodPut, "/api/sso", body, nil), "The sign-in URL must be an https URL.")
}
