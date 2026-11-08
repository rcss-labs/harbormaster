package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxSavedLogSearchNameChars is the longest name a saved log search may have, in characters.
const maxSavedLogSearchNameChars = 80

var errSavedLimit = &APIError{Status: http.StatusConflict, Code: "conflict",
	Message: fmt.Sprintf("You can save up to %d queries. Delete one first.", store.MaxSavedLogSearches)}

// SavedLogSearchDTO is one of the caller's saved log searches. ServiceIDs keeps only the services the caller
// can still read logs of; HiddenServices counts the rest, whose identity is not sent.
type SavedLogSearchDTO struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Query          string     `json:"query"`
	ServiceIDs     []string   `json:"service_ids"`
	HiddenServices int        `json:"hidden_services"`
	Range          string     `json:"range"`
	From           *time.Time `json:"from"`
	To             *time.Time `json:"to"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SaveLogSearchRequest is the body of POST /api/log-search/saved.
type SaveLogSearchRequest struct {
	Name       string     `json:"name"`
	Query      string     `json:"query"`
	ServiceIDs []string   `json:"service_ids"`
	Range      string     `json:"range"`
	From       *time.Time `json:"from"`
	To         *time.Time `json:"to"`
}

// listSavedLogSearches answers GET /api/log-search/saved.
func (s *Server) listSavedLogSearches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, _ := auth.UserFrom(ctx)
	subject := auth.SubjectFrom(ctx)
	searches, err := s.Store.ListSavedLogSearches(ctx, user.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]SavedLogSearchDTO, 0, len(searches))
	// Saved searches often share services, so each id is looked up once per request.
	readableByID := map[string]bool{}
	for _, search := range searches {
		readable := []string{}
		hidden := 0
		for _, id := range search.ServiceIDs {
			ok, seen := readableByID[id]
			if !seen {
				var err error
				if ok, err = s.canReadServiceLogs(ctx, subject, id); err != nil {
					s.fail(w, err)
					return
				}
				readableByID[id] = ok
			}
			if ok {
				readable = append(readable, id)
			} else {
				hidden++
			}
		}
		items = append(items, savedLogSearchDTO(search, readable, hidden))
	}
	writeJSON(w, http.StatusOK, ListResponse[SavedLogSearchDTO]{Items: items, PolledAt: s.now()})
}

// canReadServiceLogs reports whether the subject can see the service and has Logs on it. It is the gate
// logScope applies, without reading the task definition, so a listing stays cheap. An id that no longer
// parses, or a service that is gone or not visible, is not readable; any other error is returned.
func (s *Server) canReadServiceLogs(ctx context.Context, subject authz.Subject, id string) (bool, error) {
	key, err := model.ParseServiceID(id)
	if err != nil {
		return false, nil
	}
	row, err := s.Store.GetServiceView(ctx, key, visibilityOf(subject))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return authz.Can(subject, authz.Logs, row.EffectiveTagID), nil
}

// saveLogSearch answers POST /api/log-search/saved. It checks the same things a search checks before it reads
// anything, so a saved search can always be run; the query text must also pass logs.ValidateQuery.
func (s *Server) saveLogSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req SaveLogSearchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxSavedLogSearchNameChars {
		s.fail(w, invalid("Name the search with 1 to 80 characters."))
		return
	}
	ids := dedupe(req.ServiceIDs)
	from, to, err := s.checkLogSearchRequest(ids, req.Range, req.From, req.To, req.Query)
	if err != nil {
		s.fail(w, err)
		return
	}
	subject := auth.SubjectFrom(ctx)
	user, _ := auth.UserFrom(ctx)
	if _, err := s.resolveSearchServices(ctx, subject, ids); err != nil {
		var forbidden errLogsForbidden
		if errors.As(err, &forbidden) {
			forbiddenAction(w, authz.Logs, forbidden.tag)
			return
		}
		s.fail(w, err)
		return
	}
	if err := logs.ValidateQuery(req.Query); err != nil {
		s.fail(w, searchQueryError(err))
		return
	}

	// Only a custom range keeps its times; a preset ends at the moment it is run.
	search := store.SavedLogSearch{UserID: user.ID, Name: name, Query: req.Query, ServiceIDs: ids, Range: req.Range}
	if req.Range == "custom" {
		search.From, search.To = &from, &to
	}
	saved, err := s.Store.SaveLogSearch(ctx, search)
	if errors.Is(err, store.ErrSavedLimit) {
		s.fail(w, errSavedLimit)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, savedLogSearchDTO(saved, saved.ServiceIDs, 0))
}

// deleteSavedLogSearch answers DELETE /api/log-search/saved/{id}. Another user's id is not found.
func (s *Server) deleteSavedLogSearch(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, store.ErrNotFound)
		return
	}
	if err := s.Store.DeleteSavedLogSearch(r.Context(), user.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func savedLogSearchDTO(search store.SavedLogSearch, readable []string, hidden int) SavedLogSearchDTO {
	return SavedLogSearchDTO{
		ID: strconv.FormatInt(search.ID, 10), Name: search.Name, Query: search.Query,
		ServiceIDs: readable, HiddenServices: hidden, Range: search.Range,
		From: search.From, To: search.To, UpdatedAt: search.UpdatedAt,
	}
}
