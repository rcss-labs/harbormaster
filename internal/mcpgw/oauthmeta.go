package mcpgw

import (
	"encoding/json"
	"net/http"
)

// OAuthScope is the only scope Harbormaster issues.
const OAuthScope = "harbormaster:read"

const (
	protectedResourcePath    = "/.well-known/oauth-protected-resource"
	protectedResourceMCPPath = "/.well-known/oauth-protected-resource/mcp"
	authorizationServerPath  = "/.well-known/oauth-authorization-server"
)

// oauthChallenge is the WWW-Authenticate value of every 401 while OAuth is on.
func oauthChallenge(cfg Config) string {
	return `Bearer resource_metadata="` + cfg.PublicURL + protectedResourceMCPPath + `", scope="` + OAuthScope + `"`
}

// protectedResourceMetadata is RFC 9728 metadata for the MCP endpoint.
func protectedResourceMetadata(cfg Config) any {
	return struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		ScopesSupported        []string `json:"scopes_supported"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}{
		Resource:               cfg.MCPURL(),
		AuthorizationServers:   []string{cfg.PublicURL},
		ScopesSupported:        []string{OAuthScope},
		BearerMethodsSupported: []string{"header"},
	}
}

// authorizationServerMetadata is RFC 8414 metadata. The authorization endpoint
// is Harbormaster's private URL; the token endpoint is the gateway's relay.
// There is deliberately no registration_endpoint: clients are identified by
// Client ID Metadata Documents.
func authorizationServerMetadata(cfg Config) any {
	return struct {
		Issuer                                     string   `json:"issuer"`
		AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
		TokenEndpoint                              string   `json:"token_endpoint"`
		ResponseTypesSupported                     []string `json:"response_types_supported"`
		GrantTypesSupported                        []string `json:"grant_types_supported"`
		CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
		TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
		ScopesSupported                            []string `json:"scopes_supported"`
		ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
		AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	}{
		Issuer:                                     cfg.PublicURL,
		AuthorizationEndpoint:                      cfg.authorizationEndpoint(),
		TokenEndpoint:                              cfg.PublicURL + "/oauth/token",
		ResponseTypesSupported:                     []string{"code"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		ScopesSupported:                            []string{OAuthScope},
		ClientIDMetadataDocumentSupported:          true,
		AuthorizationResponseIssParameterSupported: true,
	}
}

// authorizationEndpoint is where a browser is sent to approve a connector.
func (c Config) authorizationEndpoint() string { return c.AuthorizeURL + "/oauth/authorize" }

// serveOAuthMetadata answers the three well-known documents; with OAuth off
// they do not exist.
func (g *gateway) serveOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	if !g.cfg.OAuthEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var document any
	if r.URL.Path == authorizationServerPath {
		document = authorizationServerMetadata(g.cfg)
	} else {
		document = protectedResourceMetadata(g.cfg)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=300")
	_, _ = w.Write(encoded)
}
