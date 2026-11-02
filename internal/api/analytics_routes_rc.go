package api

import "net/http"

// analyticsReliabilityCapacityRoutes registers the Reliability and Capacity analytics tabs. Task T7 replaces it
// with the full analytics route table (all four tabs), at which point this file goes away.
func (s *Server) analyticsReliabilityCapacityRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/analytics/reliability", s.analyticsReliability, routeOptions{})
	s.handle(mux, "GET /api/analytics/capacity", s.analyticsCapacity, routeOptions{})
}
