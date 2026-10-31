package api

import "net/http"

// observeRoutes registers the plan 03 (observe) read routes.
func (s *Server) observeRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/services/{serviceId}/scaling", s.getScaling, routeOptions{})
}
