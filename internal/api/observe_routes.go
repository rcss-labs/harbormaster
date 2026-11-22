package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// observeRoutes registers the plan 03 (observe) read routes.
func (s *Server) observeRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/services/metrics", s.getServicesMetrics, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/scaling", s.getScaling, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/metrics", s.getServiceMetrics, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/target-health", s.getTargetHealth, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/clusters/metrics", s.getClustersMetrics, routeOptions{})
	s.handle(mux, "GET /api/clusters/{clusterId}/metrics", s.getClusterMetrics, routeOptions{})
	s.handle(mux, "GET /api/overview/metrics", s.getOverviewMetrics, routeOptions{})
	s.handle(mux, "GET /api/alerts", s.getAlerts, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/logs", s.getLogs, routeOptions{Token: auth.ScopeRead})
	// The tail route must not be wrapped by the ETag middleware: the response is a WebSocket upgrade.
	s.handle(mux, "GET /api/services/{serviceId}/logs/tail", s.tailLogs, routeOptions{NoETag: true})
}
