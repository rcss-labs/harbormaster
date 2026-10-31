package api

import "net/http"

// observeRoutes registers the plan 03 (observe) read routes.
func (s *Server) observeRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/services/{serviceId}/scaling", s.getScaling, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/metrics", s.getServiceMetrics, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/target-health", s.getTargetHealth, routeOptions{})
	s.handle(mux, "GET /api/clusters/metrics", s.getClustersMetrics, routeOptions{})
	s.handle(mux, "GET /api/clusters/{clusterId}/metrics", s.getClusterMetrics, routeOptions{})
	s.handle(mux, "GET /api/overview/metrics", s.getOverviewMetrics, routeOptions{})
	s.handle(mux, "GET /api/alerts", s.getAlerts, routeOptions{})
}
