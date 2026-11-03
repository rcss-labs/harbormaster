package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// operationEvent maps a history row of one service to the shared activity-feed shape.
func operationEvent(service ServiceRef, op store.OperationRow) EventDTO {
	text := op.Text
	// A change set's reason lives in the details; the feed shows only text, so it would otherwise never be seen.
	if reason, _ := op.Details["reason"].(string); reason != "" {
		text += fmt.Sprintf(" Reason: %q.", reason)
	}
	return EventDTO{
		TS: op.TS, Source: op.Source, Actor: op.Actor, Text: text, Service: service,
		Cluster: ClusterRef{ID: op.Key.ClusterKey.ID(), Name: op.Key.Cluster},
	}
}

func encodeTimeCursor(ts time.Time) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts.UTC().Format(time.RFC3339Nano)))
}

// timeCursorFrom reads ?cursor= as base64url(RFC3339Nano); nil when absent.
func timeCursorFrom(r *http.Request) (*time.Time, error) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("cursor is not valid.")
	}
	ts, err := time.Parse(time.RFC3339Nano, string(decoded))
	if err != nil {
		return nil, invalid("cursor is not valid.")
	}
	return &ts, nil
}

// serviceOperations answers GET /api/services/{serviceId}/operations?include_ecs=&cursor=&limit=.
func (s *Server) serviceOperations(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	includeECS := true
	if raw := r.URL.Query().Get("include_ecs"); raw != "" {
		if includeECS, err = strconv.ParseBool(raw); err != nil {
			s.fail(w, invalid("include_ecs must be true or false."))
			return
		}
	}
	limit, err := limitFrom(r, s.Cfg)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, err := timeCursorFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	row, err := s.Store.GetServiceView(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	tasks, err := s.serviceTasks(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	unhealthy, err := s.Store.UnhealthyTargets(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region})
	if err != nil {
		s.fail(w, err)
		return
	}
	ref := ServiceRef{ID: key.ID(), Name: key.Service,
		Status: status.Service(status.Inputs{Service: row.Service, RunningTasks: modelTasks(tasks), TargetUnhealthy: unhealthy[key]})}
	operations, err := s.Store.ListOperations(ctx, key, includeECS, before, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region})
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]EventDTO, 0, len(operations))
	for _, op := range operations {
		items = append(items, operationEvent(ref, op))
	}
	var next *string
	if len(operations) == limit {
		c := encodeTimeCursor(operations[len(operations)-1].TS)
		next = &c
	}
	writeJSON(w, http.StatusOK, ListResponse[EventDTO]{Items: items, Next: next, PolledAt: polledAt})
}
