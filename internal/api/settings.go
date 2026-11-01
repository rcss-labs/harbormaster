package api

import (
	"fmt"
	"net/http"
)

const (
	minStoppedTasksWindowMinutes = 1
	maxStoppedTasksWindowMinutes = 1440
)

// getSettings answers GET /api/settings for any signed-in user.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	minutes, err := s.Store.StoppedTasksWindowMinutes(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SettingsDTO{StoppedTasksWindowMinutes: minutes})
}

// putSettings answers PUT /api/settings (admins). It audits only real changes.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StoppedTasksWindowMinutes *int `json:"stopped_tasks_window_minutes"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.StoppedTasksWindowMinutes == nil ||
		*body.StoppedTasksWindowMinutes < minStoppedTasksWindowMinutes || *body.StoppedTasksWindowMinutes > maxStoppedTasksWindowMinutes {
		s.fail(w, invalid(fmt.Sprintf("stopped_tasks_window_minutes must be between %d and %d.",
			minStoppedTasksWindowMinutes, maxStoppedTasksWindowMinutes)))
		return
	}
	next := *body.StoppedTasksWindowMinutes
	previous, err := s.Store.StoppedTasksWindowMinutes(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if next != previous {
		if err := s.Store.SetStoppedTasksWindowMinutes(r.Context(), next); err != nil {
			s.fail(w, err)
			return
		}
		s.auditAdmin(r, "settings_update", "setting:stopped_tasks_window_minutes",
			fmt.Sprintf("Changed the stopped tasks window from %d to %d minutes.", previous, next),
			map[string]any{"from": previous, "to": next})
	}
	writeJSON(w, http.StatusOK, SettingsDTO{StoppedTasksWindowMinutes: next})
}
