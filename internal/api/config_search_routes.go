package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// configSearchRoutes registers estate-wide config search.
func (s *Server) configSearchRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/config-search", s.getConfigSearch, routeOptions{Token: auth.ScopeRead})
}
