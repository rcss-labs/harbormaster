package api

import "net/http"

// agentRoutes registers the chat API and its admin settings. Every route is session-only: none sets Token, so access tokens and run tokens
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
	s.handle(mux, "POST /api/users/{id}/ai-chat", s.setUserAIChat, routeOptions{Admin: true})
}
