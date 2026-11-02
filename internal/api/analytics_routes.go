package api

import "net/http"

// analyticsDeliveryOperationsRoutes registers the Delivery and Operations analytics reads. Task T7 folds this
// into the single analytics route registration once every tab handler exists.
func (s *Server) analyticsDeliveryOperationsRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/analytics/delivery", s.analyticsDelivery, routeOptions{})
	s.handle(mux, "GET /api/analytics/operations", s.analyticsOperations, routeOptions{})
}
