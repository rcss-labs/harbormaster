package api

import (
	"net/http"
	"time"
)

// AuditEntryDTO is one row of the audit log. Details is null when the entry has none.
type AuditEntryDTO struct {
	ID      int64          `json:"id"`
	TS      time.Time      `json:"ts"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details"`
	IP      string         `json:"ip"`
}

// listAudit answers GET /api/audit?cursor=&limit=, newest entries first.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := pageFrom(r, s.Cfg)
	if err != nil {
		s.fail(w, err)
		return
	}
	entries, err := s.Store.ListAudit(r.Context(), limit, offset)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]AuditEntryDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, AuditEntryDTO{ID: e.ID, TS: e.TS, Actor: e.ActorUsername, Action: e.Action,
			Target: e.Target, Summary: e.Summary, Details: e.Details, IP: e.IP})
	}
	writeJSON(w, http.StatusOK, ListResponse[AuditEntryDTO]{Items: items, Next: nextCursor(limit, offset, len(items)), PolledAt: s.now()})
}
