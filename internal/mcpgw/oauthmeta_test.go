package mcpgw

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const testAuthorizeURL = "https://harbormaster.internal.example"

func withOAuth(c *Config) { c.AuthorizeURL = testAuthorizeURL }

func withOAuthViaGateway(c *Config) {
	c.AuthorizeURL = testAuthorizeURL
	c.AuthorizeViaGateway = true
}

func getPath(t *testing.T, h *gatewayHarness, method, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, h.gateway.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp, readBody(t, resp)
}

func TestOAuthMeta_ProtectedResource(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: withOAuth})
	const want = `{"resource":"https://mcp.example.com/mcp","authorization_servers":["https://mcp.example.com"],` +
		`"scopes_supported":["harbormaster:read"],"bearer_methods_supported":["header"]}`
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		resp, body := getPath(t, h, http.MethodGet, path)
		if resp.StatusCode != http.StatusOK || body != want {
			t.Errorf("%s: status %d body %s", path, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type %q", path, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "max-age=300" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
		resp, _ = getPath(t, h, http.MethodPost, path)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET" {
			t.Errorf("%s: POST status %d allow %q, want 405 GET", path, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls for static metadata", n)
	}
}

func TestOAuthMeta_AuthorizationServer(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: withOAuth})
	resp, body := getPath(t, h, http.MethodGet, "/.well-known/oauth-authorization-server")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "max-age=300" {
		t.Errorf("headers: %v", resp.Header)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"issuer":                                         "https://mcp.example.com",
		"authorization_endpoint":                         testAuthorizeURL + "/oauth/authorize",
		"token_endpoint":                                 "https://mcp.example.com/oauth/token",
		"response_types_supported":                       []any{"code"},
		"grant_types_supported":                          []any{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []any{"S256"},
		"token_endpoint_auth_methods_supported":          []any{"none"},
		"scopes_supported":                               []any{"harbormaster:read"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("metadata mismatch\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if _, present := got["registration_endpoint"]; present {
		t.Error("registration_endpoint must not be advertised")
	}
	resp, _ = getPath(t, h, http.MethodDelete, "/.well-known/oauth-authorization-server")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE status %d, want 405", resp.StatusCode)
	}
}

func TestOAuthMeta_AdvertisesTheGatewayAuthorizeURLOnlyWhenSwitchedOn(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"off", withOAuth, testAuthorizeURL + "/oauth/authorize"},
		{"on", withOAuthViaGateway, "https://mcp.example.com/oauth/authorize"},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{mutateConfig: row.mutate})
			_, body := getPath(t, h, http.MethodGet, "/.well-known/oauth-authorization-server")
			var got struct {
				AuthorizationEndpoint string `json:"authorization_endpoint"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			if got.AuthorizationEndpoint != row.want {
				t.Errorf("authorization_endpoint = %q, want %q", got.AuthorizationEndpoint, row.want)
			}
		})
	}
}

func TestOAuthMeta_OffWithoutAuthorizeURL(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, path := range []string{
		"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
	} {
		if resp, _ := getPath(t, h, http.MethodGet, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestHandler_401PointsAtTheResourceMetadata(t *testing.T) {
	const want = `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="harbormaster:read"`

	// Malformed token (never reaches upstream) and an upstream 401 (tools/call) both carry the pointer.
	h := newHarness(t, harnessOptions{mutateConfig: withOAuth, upstream: respondJSON(http.StatusUnauthorized, `{}`)})
	for name, headers := range map[string]map[string]string{
		"no token":       {},
		"garbage":        {"Authorization": "Bearer nope"},
		"revoked token":  h.authed(),
		"initialize too": {},
	} {
		method := "tools/call"
		if name == "initialize too" {
			method = "initialize"
		}
		resp := h.rpc(t, headers, method, map[string]any{"name": "whoami"})
		readBody(t, resp)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got != want {
			t.Errorf("%s: WWW-Authenticate = %q", name, got)
		}
	}

	// With OAuth off the phase 2 challenge is unchanged.
	off := newHarness(t, harnessOptions{})
	resp := off.rpc(t, map[string]string{}, "tools/list", nil)
	readBody(t, resp)
	if got := resp.Header.Get("WWW-Authenticate"); got != `Bearer realm="harbormaster-mcp"` || strings.Contains(got, "resource_metadata") {
		t.Errorf("OAuth off: WWW-Authenticate = %q", got)
	}
}

func TestLoadConfig_Authorize(t *testing.T) {
	load := func(set map[string]string) (Config, error) {
		env := validEnv()
		for k, v := range set {
			env[k] = v
		}
		return LoadConfig(envFrom(env))
	}
	cfg, err := load(nil)
	if err != nil || cfg.AuthorizeURL != "" || cfg.OAuthEnabled() || cfg.AuthorizeViaGateway {
		t.Errorf("default: %+v %v", cfg, err)
	}
	cfg, err = load(map[string]string{"HM_MCP_AUTHORIZE_URL": "https://hm.example.com/"})
	if err != nil || cfg.AuthorizeURL != "https://hm.example.com" || !cfg.OAuthEnabled() {
		t.Errorf("authorize url: %+v %v", cfg, err)
	}
	// The gateway redirect needs a target: the switch alone changes nothing.
	cfg, err = load(map[string]string{"HM_MCP_AUTHORIZE_VIA_GATEWAY": "true"})
	if err != nil || cfg.AuthorizeViaGateway {
		t.Errorf("via gateway without a url: %+v %v", cfg, err)
	}
	cfg, err = load(map[string]string{"HM_MCP_AUTHORIZE_URL": "https://hm.example.com", "HM_MCP_AUTHORIZE_VIA_GATEWAY": "true"})
	if err != nil || !cfg.AuthorizeViaGateway {
		t.Errorf("via gateway with a url: %+v %v", cfg, err)
	}
	for name, set := range map[string]map[string]string{
		"http public host": {"HM_MCP_AUTHORIZE_URL": "http://hm.example.com"},
		"with a path":      {"HM_MCP_AUTHORIZE_URL": "https://hm.example.com/oauth"},
		"user info":        {"HM_MCP_AUTHORIZE_URL": "https://u:p@hm.example.com"},
		"bad switch":       {"HM_MCP_AUTHORIZE_URL": "https://hm.example.com", "HM_MCP_AUTHORIZE_VIA_GATEWAY": "perhaps"},
	} {
		if _, err := load(set); err == nil {
			t.Errorf("%s: want an error", name)
		} else if !strings.Contains(err.Error(), "HM_MCP_AUTHORIZE") {
			t.Errorf("%s: error %q does not name the variable", name, err)
		}
	}
}
