package api

import (
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// timelineRoutes registers the Service Timeline reads. The timeline and task reads need View only: they hold
// recorded history, never log text. The log error counts read CloudWatch and need Logs.
func (s *Server) timelineRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/services/{serviceId}/timeline", s.getTimeline, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/timeline/tasks/{taskId}", s.getTimelineTask, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/timeline/log-errors", s.getTimelineLogErrors, routeOptions{})
}
