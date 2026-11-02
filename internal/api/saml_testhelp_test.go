package api

import (
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// newServerWithFakeIdP returns a running API with the fake IdP mounted on the same origin and SSO
// configured and enabled against it. pavel (admin, password, break-glass) and omer exist.
func newServerWithFakeIdP(t *testing.T) (*httptest.Server, *fakeidp.Server, *store.Store) {
	srv, idp, st, _ := newServerWithFakeIdPCustom(t, nil)
	return srv, idp, st
}

// newServerWithFakeIdPCustom is newServerWithFakeIdP with a hook that adjusts the Server before New.
func newServerWithFakeIdPCustom(t *testing.T, mutate func(*Server)) (*httptest.Server, *fakeidp.Server, *store.Store, *syncBuffer) {
	t.Helper()
	st := storetest.Open(t)
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	idp, err := fakeidp.New(fakeidp.Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.BaseURL = base

	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	pavel, err := st.CreateUser(t.Context(), "pavel", hash, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "omer", hash, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetBreakGlassChecked(t.Context(), pavel.ID, true); err != nil {
		t.Fatal(err)
	}
	configureFakeIdP(t, st, idp, cfg)

	logs := &syncBuffer{}
	startSAMLAPI(t, srv, st, cfg, idp, slog.New(slog.NewTextHandler(logs, nil)), mutate)
	if _, err := st.SetSAMLEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	return srv, idp, st, logs
}

// newSecondServer starts another API over the same store, standing in for a second ECS task. It keeps
// the first server's BaseURL because the ACS URL comes from configuration, not from the Host header.
func newSecondServer(t *testing.T, first *httptest.Server, st *store.Store, idp *fakeidp.Server) *httptest.Server {
	t.Helper()
	cfg := testConfig()
	cfg.BaseURL = first.URL
	second := httptest.NewUnstartedServer(nil)
	startSAMLAPI(t, second, st, cfg, idp, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return second
}

func startSAMLAPI(t *testing.T, srv *httptest.Server, st *store.Store, cfg config.Config, idp *fakeidp.Server, log *slog.Logger, mutate func(*Server)) {
	t.Helper()
	setup, err := auth.NewSetup(t.Context(), st, log)
	if err != nil {
		t.Fatal(err)
	}
	fk := fake.New(fake.MockupEstate(time.Now))
	cfg.FakeAWS = true // demo mode: http loopback IdP URLs are allowed
	server := Server{
		Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
		Clients: fk, Prober: fk, Cfg: cfg, Log: log, Now: time.Now,
		Deploy:  &deploy.Service{Store: st, Clients: fk, Scaling: st, MaxDesiredCount: 50, Now: time.Now},
		FakeIdP: idp.Handler(),
		SAML:    &samlService{fetcher: &saml.MetadataFetcher{AllowInsecure: true}}, // the in-process IdP is on http loopback
	}
	if mutate != nil {
		mutate(&server)
	}
	srv.Config.Handler = New(server).Handler()
	srv.Start()
	t.Cleanup(srv.Close)
}

// configureFakeIdP saves the SSO configuration the fake IdP's metadata describes and creates the SP key.
func configureFakeIdP(t *testing.T, st *store.Store, idp *fakeidp.Server, cfg config.Config) {
	t.Helper()
	md, err := saml.ParseIdPMetadata(idp.Metadata(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var certs []string
	for _, c := range md.SigningCerts {
		certs = append(certs, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})))
	}
	if _, err := st.UpsertSAMLConfig(t.Context(), store.SAMLConfigRow{
		IdPEntityID: md.EntityID, IdPSSORedirectURL: md.SSORedirectURL, IdPCertsPEM: certs,
		SPEntityID: saml.SPEntityID(cfg.BaseURL), NameIDFormat: "unspecified",
		ClockSkewSeconds: 120, ButtonLabel: "Sign in with SSO",
	}); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := saml.GenerateSPKey("127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := saml.NewKeyCipher(cfg.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertSPKey(t.Context(), certPEM, sealed); err != nil {
		t.Fatal(err)
	}
}

// browser is an HTTP client with a cookie jar that never follows redirects, so tests see every 302/303.
type browser struct {
	t    *testing.T
	http *http.Client
	base string
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser { return newBrowserAt(t, srv.URL) }

func newBrowserAt(t *testing.T, base string) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return browserWithJarAt(t, base, jar)
}

func browserWithJar(t *testing.T, srv *httptest.Server, jar http.CookieJar) *browser {
	return browserWithJarAt(t, srv.URL, jar)
}

func browserWithJarAt(t *testing.T, base string, jar http.CookieJar) *browser {
	t.Helper()
	return &browser{t: t, base: base, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *browser) get(target string) *http.Response {
	b.t.Helper()
	resp, err := b.http.Get(absolute(b.base, target))
	if err != nil {
		b.t.Fatal(err)
	}
	return resp
}

func (b *browser) postForm(target string, form url.Values) *http.Response {
	b.t.Helper()
	resp, err := b.http.Post(absolute(b.base, target), "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	return resp
}

func absolute(base, target string) string {
	if strings.HasPrefix(target, "http") {
		return target
	}
	return base + target
}

// idpResponse follows the redirect to the fake IdP as user (auto-submit) and returns the form it
// would POST to the ACS: its action URL and the SAMLResponse/RelayState values.
func (b *browser) idpResponse(location, user string) (acs string, form url.Values) {
	b.t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		b.t.Fatal(err)
	}
	q := u.Query()
	q.Set("auto", "1")
	q.Set("user", user)
	u.RawQuery = q.Encode()
	resp := b.get(u.String())
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("fake IdP: %d %s", resp.StatusCode, body)
	}
	field := func(re string) string {
		m := regexp.MustCompile(re).FindStringSubmatch(string(body))
		if m == nil {
			b.t.Fatalf("fake IdP page lacks %s: %s", re, body)
		}
		return html.UnescapeString(m[1])
	}
	return field(`<form method="post" action="([^"]*)"`), url.Values{
		"SAMLResponse": {field(`name="SAMLResponse" value="([^"]*)"`)},
		"RelayState":   {field(`name="RelayState" value="([^"]*)"`)},
	}
}

// startLogin calls GET path (a login or test start) and returns the IdP location.
func (b *browser) startLogin(path string) string {
	b.t.Helper()
	resp := b.get(path)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		b.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	return resp.Header.Get("Location")
}

// signIn runs the whole browser loop for user and returns the ACS response.
func (b *browser) signIn(path, user string) *http.Response {
	b.t.Helper()
	acs, form := b.idpResponse(b.startLogin(path), user)
	return b.postForm(acs, form)
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func countRows(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func auditByAction(t *testing.T, st *store.Store, action string) []store.AuditEntry {
	t.Helper()
	all, err := st.ListAudit(t.Context(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// lastFailureReason returns the reason of the newest sso_sign_in_failed audit entry.
func lastFailureReason(t *testing.T, st *store.Store) string {
	t.Helper()
	entries := auditByAction(t, st, "sso_sign_in_failed")
	if len(entries) == 0 {
		t.Fatal("no sso_sign_in_failed audit entry")
	}
	reason, _ := entries[0].Details["reason"].(string)
	return reason
}

func meUsername(t *testing.T, b *browser) string {
	t.Helper()
	resp := b.get("/api/me")
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return ""
	}
	var me struct {
		Username string `json:"username"`
	}
	decode(t, resp, &me)
	return me.Username
}

func mustJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func fakeIdPUnsigned() fakeidp.Tamper { return fakeidp.Tamper{OmitSignature: true} }
