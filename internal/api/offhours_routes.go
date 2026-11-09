package api

import "net/http"

// offHoursRoutes registers the off-hours schedule administration (Admin) and the per-service state, wake and sleep
// (View to read, Operate to change).
func (s *Server) offHoursRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/offhours/schedules", s.listOffHoursSchedules, routeOptions{Admin: true})
	s.handle(mux, "GET /api/offhours/schedules/{id}", s.getOffHoursSchedule, routeOptions{Admin: true})
	s.handle(mux, "POST /api/offhours/schedules", s.createOffHoursSchedule, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/offhours/schedules/{id}", s.updateOffHoursSchedule, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/offhours/schedules/{id}", s.deleteOffHoursSchedule, routeOptions{Admin: true})
	s.handle(mux, "POST /api/offhours/preview", s.previewOffHours, routeOptions{Admin: true})
	s.handle(mux, "GET /api/offhours/tags", s.listOffHoursTags, routeOptions{Admin: true})
	s.handle(mux, "GET /api/services/{serviceId}/offhours", s.getServiceOffHours, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/offhours/wake", s.wakeServiceOffHours, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/offhours/sleep", s.sleepServiceOffHours, routeOptions{})
}
