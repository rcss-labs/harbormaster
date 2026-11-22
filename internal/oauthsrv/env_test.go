package oauthsrv

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	testIssuer   = "https://mcp.example.com"
	testRedirect = "https://claude.ai/api/mcp/auth_callback"
	// verifier is a valid PKCE verifier (43 unreserved characters).
	testVerifier = "abcdefghijklmnopqrstuvwxyz0123456789-._~ABCDE"
)

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// testConfig is the OAuth configuration the service tests share.
func testConfig() config.Config {
	return config.Config{
		APITokens: true, OAuth: true, MCPPublicURL: testIssuer, OAuthAccessTTL: time.Hour,
		TokenMaxTTL: 90 * 24 * time.Hour, TokenMaxTTLSSO: 7 * 24 * time.Hour,
		OAuthClientHosts:  []string{"127.0.0.1"},
		OAuthRedirectURIs: []string{testRedirect, "http://localhost/callback"},
	}
}

// oauthEnv is a service over a real store and a client document server.
type oauthEnv struct {
	t       *testing.T
	st      *store.Store
	svc     *Service
	docs    *docServer
	user    store.User
	subject authz.Subject
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	st := storetest.Open(t)
	doc := goodDoc()
	doc["redirect_uris"] = []string{testRedirect, "http://localhost/callback"}
	d := newDocServer(t, serveDocument(doc))
	svc := NewService(st, testConfig())
	svc.Clients = d.resolver()
	svc.Clients.RedirectPatterns = testConfig().OAuthRedirectURIs
	user, err := st.CreateUser(t.Context(), "alice", "h", false, false)
	if err != nil {
		t.Fatal(err)
	}
	return &oauthEnv{t: t, st: st, svc: svc, docs: d, user: user, subject: authz.Subject{UserID: user.ID, Username: "alice"}}
}

// newUser creates a user; sso links an identity, admin sets the flag.
func (e *oauthEnv) newUser(name string, admin, sso bool) store.User {
	e.t.Helper()
	u, err := e.st.CreateUser(e.t.Context(), name, "h", admin, false)
	if err != nil {
		e.t.Fatal(err)
	}
	if sso {
		if _, err := e.st.Pool.Exec(e.t.Context(),
			`update users set idp_entity_id = 'idp', external_id = $2, external_id_format = 'persistent' where id = $1`,
			u.ID, name); err != nil {
			e.t.Fatal(err)
		}
		u, err = e.st.GetUser(e.t.Context(), u.ID)
		if err != nil {
			e.t.Fatal(err)
		}
	}
	return u
}

// request is a valid authorization request for the env's client.
func (e *oauthEnv) request() AuthorizeRequest {
	return AuthorizeRequest{
		ResponseType: "code", ClientID: e.docs.clientID(), RedirectURI: testRedirect,
		CodeChallenge: challengeOf(testVerifier), CodeChallengeMethod: "S256", Scope: Scope,
		State: "st4te", Resource: testIssuer + "/mcp",
	}
}

// approve runs an approval for user and returns the issued code.
func (e *oauthEnv) approve(user store.User, req AuthorizeRequest) string {
	e.t.Helper()
	to, aerr := e.svc.Approve(e.t.Context(), req, user, "password")
	if aerr != nil {
		e.t.Fatalf("approve: %+v", aerr)
	}
	parsed, err := url.Parse(to)
	if err != nil || parsed.Query().Get("code") == "" {
		e.t.Fatalf("approve redirect %q has no code", to)
	}
	return parsed.Query().Get("code")
}

func (e *oauthEnv) exchange(code string, now time.Time) (TokenResponse, *TokenError) {
	e.t.Helper()
	return e.svc.Exchange(e.t.Context(), TokenRequest{
		GrantType: "authorization_code", Code: code, RedirectURI: testRedirect, CodeVerifier: testVerifier,
		ClientID: e.docs.clientID(),
	}, now)
}

func (e *oauthEnv) refresh(token string, now time.Time) (TokenResponse, *TokenError) {
	e.t.Helper()
	return e.svc.Exchange(e.t.Context(), TokenRequest{GrantType: "refresh_token", RefreshToken: token, ClientID: e.docs.clientID()}, now)
}

func (e *oauthEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(e.t.Context(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}
