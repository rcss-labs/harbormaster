package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/oauthsrv"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	oauthTestIssuer   = "https://mcp.example.com"
	oauthTestRedirect = "https://claude.ai/api/mcp/auth_callback"
	oauthTestVerifier = "abcdefghijklmnopqrstuvwxyz0123456789-._~ABCDE"
)

func oauthChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// oauthDocs serves a client metadata document over https and returns the resolver that trusts it. The document's host
// (127.0.0.1) is the allowlisted one.
type oauthDocs struct {
	server   *httptest.Server
	clientID string
	resolver *oauthsrv.ClientResolver
}

func newOAuthDocs(t *testing.T) *oauthDocs {
	t.Helper()
	d := &oauthDocs{}
	d.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": "https://" + r.Host + r.URL.Path, "client_name": "Claude",
			"redirect_uris": []string{oauthTestRedirect},
		})
	}))
	t.Cleanup(d.server.Close)
	d.clientID = d.server.URL + "/client.json"
	hc := d.server.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.resolver = oauthsrv.NewClientResolver([]string{"127.0.0.1"}, []string{oauthTestRedirect, "http://localhost/callback"})
	d.resolver.HTTP = hc
	d.resolver.AllowExplicitPort = true // the test server listens on a random port
	return d
}

// oauthConfig turns tokens and OAuth on with the settings the tests assume.
func oauthConfig(s *Server, oauth bool) {
	s.Cfg.APITokens = true
	s.Cfg.OAuth = oauth
	s.Cfg.MCPPublicURL = oauthTestIssuer
	s.Cfg.OAuthAccessTTL = time.Hour
	s.Cfg.TokenMaxTTL = 90 * 24 * time.Hour
	s.Cfg.TokenMaxTTLSSO = 7 * 24 * time.Hour
	s.Cfg.TokenMaxPerUser = 10
	s.Cfg.TokenLogSearchMaxGB = 2
	s.Cfg.TokenLogSearchDailyGB = 20
	s.Cfg.OAuthClientHosts = []string{"127.0.0.1"}
	s.Cfg.OAuthRedirectURIs = []string{oauthTestRedirect, "http://localhost/callback"}
	// auth.NewService copied the config before this hook ran, so rebuild it with tokens on.
	s.Auth = auth.NewService(s.Store, s.Cfg)
}

// oauthAPIEnv is the API with OAuth on, a client document server and two signed-in users (admin sam, omer).
type oauthAPIEnv struct {
	srv  *httptest.Server
	api  *Server
	st   *store.Store
	fk   *fake.Fake
	logs *syncBuffer
	docs *oauthDocs
	sam  *apiClient
	omer *apiClient
}

func newOAuthAPIEnv(t *testing.T) *oauthAPIEnv {
	t.Helper()
	return newOAuthAPIEnvWith(t, true, nil)
}

func newOAuthAPIEnvWith(t *testing.T, oauth bool, extra func(*Server)) *oauthAPIEnv {
	t.Helper()
	docs := newOAuthDocs(t)
	srv, api, fk, st, logs := newServerCustomAPI(t, true, func(s *Server) {
		oauthConfig(s, oauth)
		if oauth {
			s.OAuth = oauthsrv.NewService(s.Store, s.Cfg)
			s.OAuth.Clients = docs.resolver
		}
		if extra != nil {
			extra(s)
		}
	})
	return &oauthAPIEnv{srv: srv, api: api, st: st, fk: fk, logs: logs, docs: docs,
		sam: login(t, srv, "sam", testPassword), omer: login(t, srv, "omer", testPassword)}
}

// request is a valid authorization request for the env's client.
func (e *oauthAPIEnv) request() oauthsrv.AuthorizeRequest {
	return oauthsrv.AuthorizeRequest{
		ResponseType: "code", ClientID: e.docs.clientID, RedirectURI: oauthTestRedirect,
		CodeChallenge: oauthChallenge(oauthTestVerifier), CodeChallengeMethod: "S256", Scope: oauthsrv.Scope,
		State: "st4te", Resource: oauthTestIssuer + "/mcp",
	}
}

func authorizePath(req oauthsrv.AuthorizeRequest) string {
	q := url.Values{}
	for k, v := range map[string]string{
		"response_type": req.ResponseType, "client_id": req.ClientID, "redirect_uri": req.RedirectURI,
		"code_challenge": req.CodeChallenge, "code_challenge_method": req.CodeChallengeMethod, "scope": req.Scope,
		"state": req.State, "resource": req.Resource,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return "/api/oauth/authorize?" + q.Encode()
}

// decide posts the consent decision as the signed-in client.
func (e *oauthAPIEnv) decide(c *apiClient, req oauthsrv.AuthorizeRequest, decision string) *http.Response {
	return c.post("/api/oauth/authorize", map[string]any{"request": req, "decision": decision})
}

// approve approves the request and returns the authorization code from the redirect.
func (e *oauthAPIEnv) approve(t *testing.T, c *apiClient, req oauthsrv.AuthorizeRequest) string {
	t.Helper()
	resp := e.decide(c, req, "approve")
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("approve: %d %s", resp.StatusCode, b)
	}
	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	decode(t, resp, &out)
	parsed, err := url.Parse(out.RedirectTo)
	if err != nil || parsed.Query().Get("code") == "" {
		t.Fatalf("approve redirect %q has no code", out.RedirectTo)
	}
	return parsed.Query().Get("code")
}

// tokenResponse is the token endpoint's success body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// postToken sends a form to the token endpoint, as the gateway's relay does.
func (e *oauthAPIEnv) postToken(t *testing.T, form url.Values) *http.Response {
	t.Helper()
	return e.postRaw(t, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

func (e *oauthAPIEnv) postRaw(t *testing.T, contentType string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/oauth/token", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *oauthAPIEnv) codeForm(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect},
		"code_verifier": {oauthTestVerifier}, "client_id": {e.docs.clientID},
	}
}

func (e *oauthAPIEnv) refreshForm(token string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {e.docs.clientID}}
}

// connect runs the whole consent and exchange for the client and returns the issued tokens.
func (e *oauthAPIEnv) connect(t *testing.T, c *apiClient) tokenResponse {
	t.Helper()
	resp := e.postToken(t, e.codeForm(e.approve(t, c, e.request())))
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("token: %d %s", resp.StatusCode, b)
	}
	var out tokenResponse
	decode(t, resp, &out)
	return out
}

// bearer calls the API with an access token.
func (e *oauthAPIEnv) bearer(t *testing.T, method, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// oauthError decodes an RFC 6749 error body.
func oauthError(t *testing.T, resp *http.Response) (code, redirectTo string) {
	t.Helper()
	var out struct {
		Error      string `json:"error"`
		RedirectTo string `json:"redirect_to"`
	}
	decode(t, resp, &out)
	return out.Error, out.RedirectTo
}

// seedEstate loads the mockup estate so service routes have something to find.
func (e *oauthAPIEnv) seedEstate(t *testing.T) {
	t.Helper()
	seedMockupTags(t, e.st)
	pollAll(t, e.st, e.fk)
}
