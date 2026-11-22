package api

import "net/http"

// oauthRoutes registers the OAuth authorization server for MCP connectors. They exist only while HM_OAUTH is on (which
// needs HM_API_TOKENS). None is token-readable: consent needs a browser session, and the token endpoint is public.
func (s *Server) oauthRoutes(mux *http.ServeMux) {
	// The consent page's two calls: session only, CSRF on the POST (the default chain).
	s.handle(mux, "GET /api/oauth/authorize", s.validateOAuthAuthorize, routeOptions{NoETag: true})
	s.handle(mux, "POST /api/oauth/authorize", s.decideOAuthAuthorize, routeOptions{NoETag: true})
}
