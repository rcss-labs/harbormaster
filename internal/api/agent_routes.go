package api

import "net/http"

// agentRoutes registers the chat API, its admin settings, the investigation reads and the review reads. Every route is session-only: none sets Token, so access tokens and run tokens
// are refused with 403 token_not_allowed. serve registers them only while HM_AGENT_URL is set.
func (s *Server) agentRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/agent/status", s.agentStatus, routeOptions{})
	s.handle(mux, "GET /api/agent/conversations", s.listAgentConversations, routeOptions{})
	s.handle(mux, "POST /api/agent/conversations", s.createAgentConversation, routeOptions{})
	s.handle(mux, "GET /api/agent/conversations/{id}", s.getAgentConversation, routeOptions{})
	s.handle(mux, "DELETE /api/agent/conversations/{id}", s.deleteAgentConversation, routeOptions{})
	s.handle(mux, "POST /api/agent/conversations/{id}/messages", s.postAgentMessage, routeOptions{NoETag: true})
	s.handle(mux, "GET /api/agent/settings", s.getAgentSettings, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/agent/settings", s.putAgentSettings, routeOptions{Admin: true})
	s.handle(mux, "POST /api/agent/settings/test", s.testAgentModel, routeOptions{Admin: true})
	s.handle(mux, "GET /api/agent/usage", s.getAgentUsage, routeOptions{Admin: true})
	s.handle(mux, "GET /api/services/{serviceId}/investigations", s.listServiceInvestigations, routeOptions{})
	s.handle(mux, "GET /api/investigations/{id}", s.getInvestigation, routeOptions{})
	s.handle(mux, "GET /api/investigations/by-incident/{incidentId}", s.getIncidentInvestigation, routeOptions{})
	s.handle(mux, "GET /api/reviews/runs", s.listReviewRuns, routeOptions{})
	s.handle(mux, "GET /api/reviews/runs/{id}", s.getReviewRun, routeOptions{})
	s.handle(mux, "POST /api/users/{id}/ai-chat", s.setUserAIChat, routeOptions{Admin: true})
}
