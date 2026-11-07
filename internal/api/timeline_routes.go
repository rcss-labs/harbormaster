package api

import "net/http"

// timelineRoutes registers the Service Timeline reads. They need View only: the timeline holds recorded history,
// never log text.
func (s *Server) timelineRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/services/{serviceId}/timeline", s.getTimeline, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/timeline/tasks/{taskId}", s.getTimelineTask, routeOptions{})
}
