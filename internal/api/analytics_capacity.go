package api

import "net/http"

func (s *Server) analyticsCapacity(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "todo", "todo")
}
