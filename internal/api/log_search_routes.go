package api

import "net/http"

// logSearchRoutes registers the cross-service log search routes.
func (s *Server) logSearchRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/log-search/services", s.listLogSearchServices, routeOptions{})
	// A search answers with a fresh result each time; a conditional GET makes no sense for it.
	s.handle(mux, "POST /api/log-search", s.postLogSearch, routeOptions{NoETag: true})
}
