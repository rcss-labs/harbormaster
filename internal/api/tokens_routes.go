package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// tokenRoutes registers access token management. They exist only while HM_API_TOKENS is on. Management is
// session-only: a token credential is refused on everything here except whoami, the one route marked Token.
func (s *Server) tokenRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/tokens", s.listTokens, routeOptions{})
	// The answer carries the secret once, so it is never cached.
	s.handle(mux, "POST /api/tokens", s.createToken, routeOptions{NoETag: true})
	s.handle(mux, "DELETE /api/tokens/{id}", s.deleteToken, routeOptions{})
	s.handle(mux, "GET /api/tokens/current", s.whoAmI, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/users/{id}/tokens", s.listUserTokens, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/users/{id}/tokens/{tokenId}", s.deleteUserToken, routeOptions{Admin: true})
}
