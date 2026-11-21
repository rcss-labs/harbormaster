package api

import "net/http"

// notificationRoutes registers the alert notification administration. Every route is global-admin only: the
// delivery log names services of every tag, and a route's webhook is a credential.
func (s *Server) notificationRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/notifications/routes", s.listNotificationRoutes, routeOptions{Admin: true})
	s.handle(mux, "POST /api/notifications/routes", s.createNotificationRoute, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/notifications/routes/{id}", s.updateNotificationRoute, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/notifications/routes/{id}", s.deleteNotificationRoute, routeOptions{Admin: true})
	s.handle(mux, "POST /api/notifications/preview", s.previewNotification, routeOptions{Admin: true})
	s.handle(mux, "POST /api/notifications/routes/{id}/test", s.testNotificationRoute, routeOptions{Admin: true, NoETag: true})
	s.handle(mux, "GET /api/notifications/targets", s.notificationTargets, routeOptions{Admin: true})
	s.handle(mux, "GET /api/notifications/deliveries", s.listNotificationDeliveries, routeOptions{Admin: true})
	s.handle(mux, "POST /api/notifications/deliveries/{id}/retry", s.retryNotificationDelivery, routeOptions{Admin: true, NoETag: true})
	s.handle(mux, "GET /api/notifications/summary", s.notificationSummary, routeOptions{Admin: true})
}
