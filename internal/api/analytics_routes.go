package api

import "net/http"

// analyticsRoutes registers the read-only Analytics tabs. Each reads with the caller's View visibility and the
// account/region/tag scope, so no admin requirement applies.
func (s *Server) analyticsRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/analytics/delivery", s.analyticsDelivery, routeOptions{})
	s.handle(mux, "GET /api/analytics/reliability", s.analyticsReliability, routeOptions{})
	s.handle(mux, "GET /api/analytics/capacity", s.analyticsCapacity, routeOptions{})
	s.handle(mux, "GET /api/analytics/operations", s.analyticsOperations, routeOptions{})
}
