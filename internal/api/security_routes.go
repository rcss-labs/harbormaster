package api

import "net/http"

// securityRoutes registers image security. Every handler checks the caller's visibility and permissions itself.
func (s *Server) securityRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/security", s.listSecurity, routeOptions{})
	s.handle(mux, "GET /api/security/images/{digest}", s.getSecurityImage, routeOptions{})
	s.handle(mux, "GET /api/security/services/{serviceId}", s.getSecurityService, routeOptions{})
	s.handle(mux, "PUT /api/security/services/{serviceId}/setting", s.putScanSetting, routeOptions{})
	s.handle(mux, "POST /api/security/services/{serviceId}/scans", s.postScanRequest, routeOptions{})
	s.handle(mux, "GET /api/security/services/{serviceId}/scans", s.getScanStatus, routeOptions{})
}
