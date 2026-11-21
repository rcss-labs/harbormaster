package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultDeliveryPage       = 50
	onlyFailedRetryMessage    = "Only failed deliveries can be retried."
	routeDeletedRetryMessage  = "The route was deleted."
	invalidDeliveryCursorText = "Invalid cursor."
)

// NotificationSummaryDTO is GET /api/notifications/summary: what the failed-deliveries banner and the Settings
// badge show.
type NotificationSummaryDTO struct {
	Failed24h     int `json:"failed_24h"`
	FailingRoutes int `json:"failing_routes"`
}

// encodeDeliveryCursor is opaque to clients: the keyset position of the last row of a page.
func encodeDeliveryCursor(c *store.DeliveryCursor) *string {
	if c == nil {
		return nil
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.CreatedAt.UnixNano(), 10) + ":" + strconv.FormatInt(c.ID, 10)))
	return &token
}

func decodeDeliveryCursor(token string) (*store.DeliveryCursor, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, invalid(invalidDeliveryCursorText)
	}
	nanos, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, invalid(invalidDeliveryCursorText)
	}
	n, errN := strconv.ParseInt(nanos, 10, 64)
	i, errI := strconv.ParseInt(id, 10, 64)
	if errN != nil || errI != nil {
		return nil, invalid(invalidDeliveryCursorText)
	}
	return &store.DeliveryCursor{CreatedAt: time.Unix(0, n).UTC(), ID: i}, nil
}

func (s *Server) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := query.Get("status")
	if status != "" && status != "failed" {
		s.fail(w, invalid("The status filter must be empty or failed."))
		return
	}
	cursor, err := decodeDeliveryCursor(query.Get("cursor"))
	if err != nil {
		s.fail(w, err)
		return
	}
	limit := defaultDeliveryPage
	if raw := query.Get("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 1 {
			s.fail(w, invalid("The limit must be a positive number."))
			return
		}
		limit = n
	}
	if max := s.Cfg.PageSizeMax; max > 0 && limit > max {
		limit = max
	}
	rows, next, err := s.Store.ListNotificationDeliveries(r.Context(), store.DeliveryFilter{Status: status, Before: cursor, Limit: limit})
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]NotificationDeliveryDTO, len(rows))
	for i, row := range rows {
		items[i] = deliveryDTO(row)
	}
	writeJSON(w, http.StatusOK, ListResponse[NotificationDeliveryDTO]{Items: items, Next: encodeDeliveryCursor(next), PolledAt: s.now()})
}

func (s *Server) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	row, err := s.Store.GetNotificationDelivery(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if row.Status != "failed" {
		s.fail(w, notifyConflict(onlyFailedRetryMessage))
		return
	}
	if row.RouteID == nil {
		s.fail(w, notifyConflict(routeDeletedRetryMessage))
		return
	}
	retried, err := s.Store.RetryNotificationDelivery(ctx, id, s.now())
	if errors.Is(err, store.ErrConflict) {
		// Another request retried it first, or the route was deleted in between.
		s.fail(w, notifyConflict(onlyFailedRetryMessage))
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "notification_delivery_retry", retried.RouteName, "Retried a failed notification to "+strconv.Quote(retried.RouteName)+".",
		map[string]any{"delivery_id": retried.ID, "event": retried.Event})
	s.kickNotify()
	writeJSON(w, http.StatusOK, deliveryDTO(retried))
}

func (s *Server) notificationSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.Store.NotificationSummary(r.Context(), s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, NotificationSummaryDTO{Failed24h: summary.Failed24h, FailingRoutes: len(summary.FailingRouteIDs)})
}
