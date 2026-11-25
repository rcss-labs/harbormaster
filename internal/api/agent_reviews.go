package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/reviewsched"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	maxReviewNameRunes    = 80
	maxReviewRoutes       = 5
	maxReviewScopeEntries = 10
	maxReviewZoneLen      = 64
	maxReviewClusterIDLen = 400
)

// clusterNamePattern is what an ECS cluster name may hold, the rule the review job reads scope entries by.
var clusterNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// reviewTypeOptions are the review types the form offers, in its order, with their fixed labels. The ids are the
// store's; the agent's catalogue says which of them it supports.
var reviewTypeOptions = []AgentReviewTypeDTO{
	{ID: "cost", Label: "Cost"}, {ID: "security", Label: "Security findings"},
	{ID: "reliability", Label: "Reliability"}, {ID: "capacity", Label: "Capacity"},
}

var (
	errReviewStale = &APIError{Status: http.StatusConflict, Code: "review_changed",
		Message: "This review was changed by someone else. Reload and try again."}
	errReviewBusy = &APIError{Status: http.StatusConflict, Code: "review_busy",
		Message: "This review is already waiting or running."}
	errReviewNoModel = &APIError{Status: http.StatusUnprocessableEntity, Code: "no_model",
		Message: "Choose a reviewer model in the agent settings first."}
	errReviewsOff = &APIError{Status: http.StatusUnprocessableEntity, Code: "reviews_off",
		Message: "Reviews are turned off on this server."}
	errReviewLimit = &APIError{Status: http.StatusUnprocessableEntity, Code: "review_limit",
		Message: "You can have up to 20 reviews. Delete one first."}
	errReviewNotSupported = &APIError{Status: http.StatusUnprocessableEntity, Code: "route_not_supported",
		Message: "PagerDuty routes cannot receive reviews. Choose another route."}
)

// reviewFieldError is a 422 that names the member of the request it refuses, in "error.field".
type reviewFieldError struct{ field, code, message string }

func (e *reviewFieldError) Error() string { return e.message }

func reviewInvalid(field, message string) *reviewFieldError {
	return &reviewFieldError{field: field, code: "invalid", message: message}
}

// failReview answers like fail, and adds the field of a refusal that has one.
func (s *Server) failReview(w http.ResponseWriter, err error) {
	var fieldErr *reviewFieldError
	if errors.As(err, &fieldErr) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{
			"code": fieldErr.code, "message": fieldErr.message, "field": fieldErr.field}})
		return
	}
	s.fail(w, err)
}

// agentReviewRoutes registers the admin routes of scheduled reviews; serve registers them only while HM_AGENT_URL is
// set, next to agentIdentityRoutes. Like those, they are session-only (no route sets Token) and admin-only.
func (s *Server) agentReviewRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/agent/reviews", s.listAgentReviews, routeOptions{Admin: true})
	s.handle(mux, "POST /api/agent/reviews", s.createAgentReview, routeOptions{Admin: true})
	s.handle(mux, "POST /api/agent/reviews/preview", s.previewAgentReview, routeOptions{Admin: true})
	s.handle(mux, "GET /api/agent/reviews/runs", s.listAgentReviewRuns, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/agent/reviews/{id}", s.updateAgentReview, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/agent/reviews/{id}", s.deleteAgentReview, routeOptions{Admin: true})
	s.handle(mux, "POST /api/agent/reviews/{id}/run", s.runAgentReviewNow, routeOptions{Admin: true})
}

func (s *Server) kickReview() {
	if s.KickReview != nil {
		s.KickReview()
	}
}

// reviewRunCost is what one review run reserves at the reviewer model: the model's price at the agent's run token
// cap. ok is false without a reviewer model, while the agent cannot be asked and when it no longer offers the model.
func (s *Server) reviewRunCost(r *http.Request) (micro int64, ok bool, err error) {
	settings, err := s.Store.GetAgentSettings(r.Context())
	if err != nil {
		return 0, false, err
	}
	ref, chosen := settings.RoleModels[reviewerRoleName]
	if !chosen {
		return 0, false, nil
	}
	cat, catErr := s.catalogue(r)
	if catErr != nil {
		return 0, false, nil
	}
	known, offered := findModel(cat, ref)
	if !offered {
		return 0, false, nil
	}
	return known.MaxRunCostMicroUSD(cat.MaxRunTokens), true, nil
}

// worstCaseMonthUSD is the most a cadence can reserve in a month at one run's reservation.
func worstCaseMonthUSD(cadence string, runCost int64, known bool) *string {
	if !known {
		return nil
	}
	usd := formatUSD(runCost*int64(reviewsched.RunsPerMonth(cadence)), true)
	return &usd
}

func scheduleOf(r store.Review) reviewsched.Schedule {
	return reviewsched.Schedule{Cadence: r.Cadence, AtMinute: r.AtMinute, Weekday: r.Weekday, MonthDay: r.MonthDay, Timezone: r.Timezone}
}

// nextOccurrence is the first occurrence after now, nil when the schedule does not validate.
func nextOccurrence(s reviewsched.Schedule, now time.Time) *time.Time {
	next, err := reviewsched.Next(s, now)
	if err != nil {
		return nil
	}
	return &next
}

func optionalInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// reviewDTOs renders reviews with their scope names, routes, last run and money. Reads of the agent happen once.
func (s *Server) reviewDTOs(r *http.Request, reviews []store.Review) ([]ReviewDTO, error) {
	ctx := r.Context()
	targets, err := s.Store.NotificationTargets(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.Store.ListNotificationRoutes(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.Store.LatestReviewRuns(ctx)
	if err != nil {
		return nil, err
	}
	var runCost int64
	var costKnown bool
	if len(reviews) > 0 {
		if runCost, costKnown, err = s.reviewRunCost(r); err != nil {
			return nil, err
		}
	}
	routeByID := make(map[int64]store.NotificationRoute, len(routes))
	for _, route := range routes {
		routeByID[route.ID] = route
	}
	now := s.now()
	out := make([]ReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		dto := ReviewDTO{
			ID: review.ID, Name: review.Name, Type: review.Type, Enabled: review.Enabled, Version: review.Version,
			Schedule: ReviewScheduleDTO{Cadence: review.Cadence, AtMinute: review.AtMinute, Weekday: optionalInt(review.Weekday),
				MonthDay: optionalInt(review.MonthDay), Timezone: review.Timezone},
			Scope:             []NotificationSelectorDTO{},
			Routes:            []ReviewRouteDTO{},
			WorstCaseMonthUSD: worstCaseMonthUSD(review.Cadence, runCost, costKnown),
		}
		for _, info := range notify.DescribeScope(review.Scope, targets, tags) {
			dto.Scope = append(dto.Scope, selectorDTO(info))
		}
		for _, id := range review.RouteIDs {
			if route, ok := routeByID[id]; ok {
				dto.Routes = append(dto.Routes, ReviewRouteDTO{ID: route.ID, Name: route.Name, Destination: route.Destination, Enabled: route.Enabled})
			}
		}
		if review.Enabled && s.Cfg.AgentReviewsPerDay > 0 {
			dto.NextRunAt = nextOccurrence(scheduleOf(review), now)
		}
		if run, ok := latest[review.ID]; ok {
			dto.LastRun = &ReviewLastRunDTO{ID: run.ID, State: run.State, Reason: run.Reason, FinishedAt: run.FinishedAt}
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *Server) reviewDTOFor(r *http.Request, review store.Review) (ReviewDTO, error) {
	dtos, err := s.reviewDTOs(r, []store.Review{review})
	if err != nil {
		return ReviewDTO{}, err
	}
	return dtos[0], nil
}

// reviewAuditView is the audit snapshot of a review: its definition, with the scope as short entries.
func reviewAuditView(review store.Review) map[string]any {
	scope := make([]string, len(review.Scope))
	for i, sel := range review.Scope {
		switch sel.Kind {
		case store.NotificationSelectorAccount:
			scope[i] = "account:" + sel.AccountID
			if sel.Region != "" {
				scope[i] += "/" + sel.Region
			}
		case store.NotificationSelectorCluster:
			scope[i] = "cluster:" + sel.AccountID + "/" + sel.Region + "/" + sel.Cluster
		default:
			scope[i] = "tag:" + strconv.FormatInt(int64(sel.TagID), 10)
		}
	}
	return map[string]any{
		"name": review.Name, "type": review.Type, "enabled": review.Enabled, "scope": scope, "route_ids": nonNilIDs(review.RouteIDs), "version": review.Version,
		"schedule": map[string]any{"cadence": review.Cadence, "at_minute": review.AtMinute, "weekday": optionalInt(review.Weekday),
			"month_day": optionalInt(review.MonthDay), "timezone": review.Timezone},
	}
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func (s *Server) listAgentReviews(w http.ResponseWriter, r *http.Request) {
	reviews, err := s.Store.ListReviews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.reviewDTOs(r, reviews)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ItemsResponse[ReviewDTO]{Items: items})
}

// reviewName trims the name and refuses what would break a message headline: no text, too long, a break or another
// control character, or nothing visible once the senders clean it.
func reviewName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxReviewNameRunes || !utf8.ValidString(name) {
		return "", reviewInvalid("name", "Enter a name of 1 to 80 characters.")
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) }) {
		return "", reviewInvalid("name", "The name must be one line, without line breaks or control characters.")
	}
	if notify.CleanReviewName(name) == "" {
		return "", reviewInvalid("name", "Enter a name with visible characters.")
	}
	return name, nil
}

func reviewSchedule(in ReviewScheduleDTO) (reviewsched.Schedule, error) {
	schedule := reviewsched.Schedule{Cadence: in.Cadence, AtMinute: in.AtMinute, Timezone: in.Timezone}
	if in.Weekday != nil {
		schedule.Weekday = *in.Weekday
	}
	if in.MonthDay != nil {
		schedule.MonthDay = *in.MonthDay
	}
	// The zone is checked on its own, so a bad zone is told apart from a bad cadence or time.
	if len(in.Timezone) > maxReviewZoneLen || reviewsched.Validate(reviewsched.Schedule{Cadence: reviewsched.Daily, Timezone: in.Timezone}) != nil {
		return schedule, reviewInvalid("timezone", "Choose a time zone from the list.")
	}
	if err := reviewsched.Validate(schedule); err != nil {
		return schedule, reviewInvalid("schedule", "The schedule is not valid: "+err.Error()+".")
	}
	return schedule, nil
}

// reviewScope validates the scope entries: accounts, clusters and Harbormaster tags, at most ten, without a
// threshold. Repeats are dropped. A tag that is gone is kept when the stored review has it, as in a route's scope.
func (s *Server) reviewScope(ctx context.Context, in []ReviewScopeInput, stored []store.NotificationSelector) ([]store.NotificationSelector, error) {
	if len(in) > maxReviewScopeEntries {
		return nil, reviewInvalid("scope", "A review can have at most 10 scope entries.")
	}
	entries := make([]NotificationSelectorDTO, 0, len(in))
	seen := map[ReviewScopeInput]bool{}
	for _, entry := range in {
		okFields := false
		switch entry.Kind {
		case store.NotificationSelectorAccount:
			okFields = entry.ClusterID == "" && entry.TagID == 0
		case store.NotificationSelectorCluster:
			okFields = entry.AccountID == "" && entry.Region == "" && entry.TagID == 0 && len(entry.ClusterID) <= maxReviewClusterIDLen
		case store.NotificationSelectorHarbormaster:
			okFields = entry.AccountID == "" && entry.Region == "" && entry.ClusterID == ""
		}
		if !okFields {
			return nil, reviewInvalid("scope", invalidScopeMessage)
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		entries = append(entries, NotificationSelectorDTO{Kind: entry.Kind, AccountID: entry.AccountID, Region: entry.Region, ClusterID: entry.ClusterID, TagID: entry.TagID})
	}
	scope, err := s.scopeFromInput(ctx, entries, stored)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
			return nil, reviewInvalid("scope", apiErr.Message)
		}
		return nil, err
	}
	for i := range scope {
		scope[i].Threshold = 0 // a review's scope is a focus, not a count
		sel := scope[i]
		switch sel.Kind {
		case store.NotificationSelectorAccount, store.NotificationSelectorCluster:
			if !accountIDPattern.MatchString(sel.AccountID) || (sel.Region != "" && !regionPattern.MatchString(sel.Region)) ||
				(sel.Kind == store.NotificationSelectorCluster && (sel.Region == "" || !clusterNamePattern.MatchString(sel.Cluster))) {
				return nil, reviewInvalid("scope", invalidScopeMessage)
			}
		}
	}
	return scope, nil
}

// reviewRouteIDs validates the routes: one to five, each existing and not PagerDuty. Repeats are dropped.
func (s *Server) reviewRouteIDs(ctx context.Context, in []int64) ([]int64, error) {
	ids := make([]int64, 0, len(in))
	for _, id := range in {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) < 1 || len(ids) > maxReviewRoutes {
		return nil, reviewInvalid("route_ids", "Choose 1 to 5 routes.")
	}
	for _, id := range ids {
		route, err := s.Store.GetNotificationRoute(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, reviewInvalid("route_ids", "One of the routes no longer exists. Reload and try again.")
		}
		if err != nil {
			return nil, err
		}
		if route.Destination == notify.DestPagerDuty {
			return nil, &reviewFieldError{field: "route_ids", code: errReviewNotSupported.Code, message: errReviewNotSupported.Message}
		}
	}
	return ids, nil
}

// buildReview validates in in the order the form shows it (name, type, schedule, scope, routes) and builds the
// definition to store. Nothing is written. stored is the review being edited, when there is one.
func (s *Server) buildReview(ctx context.Context, in ReviewInput, stored *store.Review) (store.Review, error) {
	name, err := reviewName(in.Name)
	if err != nil {
		return store.Review{}, err
	}
	if !slices.ContainsFunc(reviewTypeOptions, func(t AgentReviewTypeDTO) bool { return t.ID == in.Type }) {
		return store.Review{}, reviewInvalid("type", "Choose a review type.")
	}
	schedule, err := reviewSchedule(in.Schedule)
	if err != nil {
		return store.Review{}, err
	}
	var storedScope []store.NotificationSelector
	if stored != nil {
		storedScope = stored.Scope
	}
	scope, err := s.reviewScope(ctx, in.Scope, storedScope)
	if err != nil {
		return store.Review{}, err
	}
	routeIDs, err := s.reviewRouteIDs(ctx, in.RouteIDs)
	if err != nil {
		return store.Review{}, err
	}
	review := store.Review{Name: name, Type: in.Type, Enabled: in.Enabled, Cadence: schedule.Cadence, AtMinute: schedule.AtMinute,
		Weekday: schedule.Weekday, MonthDay: schedule.MonthDay, Timezone: schedule.Timezone, Scope: scope, RouteIDs: routeIDs}
	if stored != nil {
		review.ID = stored.ID
	}
	return review, nil
}

// scopeInputOf is a stored scope entry as the request states it.
func scopeInputOf(sel store.NotificationSelector) ReviewScopeInput {
	switch sel.Kind {
	case store.NotificationSelectorCluster:
		return ReviewScopeInput{Kind: sel.Kind, ClusterID: model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}.ID()}
	case store.NotificationSelectorHarbormaster:
		return ReviewScopeInput{Kind: sel.Kind, TagID: int64(sel.TagID)}
	}
	return ReviewScopeInput{Kind: sel.Kind, AccountID: sel.AccountID, Region: sel.Region}
}

// onlyTurnsOff reports whether the request is the stored, enabled review with nothing changed but the switch turned
// off, the routes aside: the form does not offer a route that became PagerDuty, so it may leave out such a route (and
// only such a route) and add none. It checks nothing else; the answer is only that the stored review may be kept.
func (s *Server) onlyTurnsOff(ctx context.Context, in ReviewInput, stored store.Review) (bool, error) {
	if !stored.Enabled || in.Enabled {
		return false, nil
	}
	same := strings.TrimSpace(in.Name) == stored.Name && in.Type == stored.Type && in.Schedule.Cadence == stored.Cadence &&
		in.Schedule.AtMinute == stored.AtMinute && in.Schedule.Timezone == stored.Timezone &&
		derefInt(in.Schedule.Weekday) == stored.Weekday && derefInt(in.Schedule.MonthDay) == stored.MonthDay
	if !same {
		return false, nil
	}
	want := map[ReviewScopeInput]bool{}
	for _, sel := range stored.Scope {
		want[scopeInputOf(sel)] = true
	}
	got := map[ReviewScopeInput]bool{}
	for _, entry := range in.Scope {
		got[entry] = true
	}
	if !maps.Equal(want, got) {
		return false, nil
	}
	for _, id := range in.RouteIDs {
		if !slices.Contains(stored.RouteIDs, id) {
			return false, nil
		}
	}
	for _, id := range stored.RouteIDs {
		if slices.Contains(in.RouteIDs, id) {
			continue
		}
		route, err := s.Store.GetNotificationRoute(ctx, id)
		if err != nil {
			return false, err
		}
		if route.Destination != notify.DestPagerDuty {
			return false, nil // an allowed route was dropped: that is an edit, checked in full
		}
	}
	return true, nil
}

func derefInt(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// reviewStoreError maps what the store refuses to the answer the form expects.
func reviewStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrReviewLimit):
		return &reviewFieldError{field: "name", code: errReviewLimit.Code, message: errReviewLimit.Message}
	case errors.Is(err, store.ErrReviewInvalid):
		// Everything was checked before the write; this is a change that raced it (a route deleted, say).
		return reviewInvalid("", "The review is not valid any more. Reload and try again.")
	}
	return err
}

var errReviewNameTaken = &reviewFieldError{field: "name", code: "name_taken", message: "A review with this name already exists."}

func (s *Server) createAgentReview(w http.ResponseWriter, r *http.Request) {
	var in ReviewInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	review, err := s.buildReview(ctx, in, nil)
	if err != nil {
		s.failReview(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	created, err := s.Store.CreateReview(ctx, review, actor.ID)
	if errors.Is(err, store.ErrConflict) {
		s.failReview(w, errReviewNameTaken)
		return
	}
	if err != nil {
		s.failReview(w, reviewStoreError(err))
		return
	}
	s.auditAdmin(r, "agent_review_create", created.Name, "Created review "+strconv.Quote(created.Name)+".",
		map[string]any{"before": nil, "after": reviewAuditView(created)})
	s.kickReview()
	dto, err := s.reviewDTOFor(r, created)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) updateAgentReview(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var in ReviewInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	before, err := s.Store.GetReview(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if in.Version == nil {
		s.failReview(w, reviewInvalid("version", "Send the version of the review you are changing."))
		return
	}
	if *in.Version != before.Version {
		s.fail(w, errReviewStale) // before anything else is looked at
		return
	}
	var review store.Review
	if only, err := s.onlyTurnsOff(ctx, in, before); err != nil {
		s.fail(w, err)
		return
	} else if only {
		// An admin can always turn a review off: it keeps its stored routes and scope, whatever they became.
		review = before
		review.Enabled = false
	} else if review, err = s.buildReview(ctx, in, &before); err != nil {
		s.failReview(w, err)
		return
	}
	review.Version = before.Version
	actor, _ := auth.UserFrom(ctx)
	updated, err := s.Store.UpdateReview(ctx, review, actor.ID)
	if errors.Is(err, store.ErrConflict) {
		// Someone else saved first, or another review has the name.
		if current, getErr := s.Store.GetReview(ctx, id); getErr != nil {
			s.fail(w, getErr)
		} else if current.Version != before.Version {
			s.fail(w, errReviewStale)
		} else {
			s.failReview(w, errReviewNameTaken)
		}
		return
	}
	if err != nil {
		s.failReview(w, reviewStoreError(err))
		return
	}
	s.auditAdmin(r, "agent_review_update", updated.Name, "Updated review "+strconv.Quote(updated.Name)+".",
		map[string]any{"before": reviewAuditView(before), "after": reviewAuditView(updated)})
	s.kickReview()
	dto, err := s.reviewDTOFor(r, updated)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) deleteAgentReview(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetReview(r.Context(), id)
	if err == nil {
		err = s.Store.DeleteReview(r.Context(), id)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "agent_review_delete", before.Name, "Deleted review "+strconv.Quote(before.Name)+".",
		map[string]any{"before": reviewAuditView(before), "after": nil})
	s.kickReview()
	w.WriteHeader(http.StatusNoContent)
}

// previewAgentReview answers what a draft would cover: its services and how many the agent identity cannot View now
// (the counting of a route's preview), its next occurrence and its month's worst case. It validates the name, type,
// schedule and scope, and ignores what is off (the switch, reviews being off, the routes) and a missing model: the
// money members are then null. Nothing is written.
func (s *Server) previewAgentReview(w http.ResponseWriter, r *http.Request) {
	var in ReviewInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if _, err := reviewName(in.Name); err != nil {
		s.failReview(w, err)
		return
	}
	if !slices.ContainsFunc(reviewTypeOptions, func(t AgentReviewTypeDTO) bool { return t.ID == in.Type }) {
		s.failReview(w, reviewInvalid("type", "Choose a review type."))
		return
	}
	schedule, err := reviewSchedule(in.Schedule)
	if err != nil {
		s.failReview(w, err)
		return
	}
	// A tag the draft names that is gone is kept, as an update of the review keeps it: it matches no service.
	var ownTags []store.NotificationSelector
	for _, entry := range in.Scope {
		if entry.Kind == store.NotificationSelectorHarbormaster {
			ownTags = append(ownTags, store.NotificationSelector{Kind: entry.Kind, TagID: authz.TagID(entry.TagID)})
		}
	}
	scopeEntries, err := s.reviewScope(ctx, in.Scope, ownTags)
	if err != nil {
		s.failReview(w, err)
		return
	}
	scope, err := notify.ResolveScope(ctx, s.Store, scopeEntries)
	if err != nil {
		s.fail(w, err)
		return
	}
	targets, err := s.Store.NotificationTargets(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.previewAgent(ctx, scope, targets)
	if err != nil {
		s.fail(w, err)
		return
	}
	runCost, known, err := s.reviewRunCost(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ReviewPreviewDTO{ServicesTotal: counts.ServicesTotal, ServicesUnseen: counts.ServicesUnseen,
		NextRunAt: nextOccurrence(schedule, s.now()), WorstCaseMonthUSD: worstCaseMonthUSD(schedule.Cadence, runCost, known)})
}

// runAgentReviewNow records a run of the review for the job to take up and kicks it. It reserves nothing and calls no
// model: the job does both, under the monthly allowance, like any scheduled run. A disabled review can be run.
func (s *Server) runAgentReviewNow(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	review, err := s.Store.GetReview(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.Cfg.AgentReviewsPerDay <= 0 {
		s.fail(w, errReviewsOff)
		return
	}
	settings, err := s.Store.GetAgentSettings(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, ok := settings.RoleModels[reviewerRoleName]; !ok {
		s.fail(w, errReviewNoModel)
		return
	}
	now := s.now()
	run, err := s.Store.RequestManualReviewRun(ctx, id, "Run on "+now.UTC().Format("2 Jan 2006, 15:04")+" UTC", now)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.fail(w, errReviewBusy)
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "agent_review_run", review.Name, "Ran review "+strconv.Quote(review.Name)+" now.",
		map[string]any{"review_id": review.ID, "run_id": run.ID, "type": review.Type})
	s.kickReview()
	writeJSON(w, http.StatusAccepted, ReviewRunStartedDTO{RunID: run.ID})
}

func reviewRunRowDTO(run store.ReviewRun, cost string) AgentReviewRunDTO {
	dto := AgentReviewRunDTO{
		ID: run.ID, ReviewID: run.ReviewID, Name: run.ReviewName, Type: run.ReviewType, Manual: run.Manual, State: run.State, Reason: run.Reason,
		PeriodLabel: run.PeriodLabel, ScopeLabel: run.ScopeLabel, ServicesTotal: run.ServicesTotal, ScheduledFor: run.ScheduledFor,
		RequestedAt: run.RequestedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Provider: run.Provider, Model: run.Model,
		Attempts: run.Attempts, CostUSD: cost,
	}
	if run.State == "done" { // a summary is shown for a finished run only
		dto.Summary, dto.SummaryReplaced = run.Summary, run.SummaryReplaced
	}
	return dto
}

// listAgentReviewRuns answers GET /api/agent/reviews/runs?review_id=&cursor=&limit=, newest first.
func (s *Server) listAgentReviewRuns(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.ReviewRunFilter{}
	if raw := query.Get("review_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			s.fail(w, invalid("The review id must be a positive number."))
			return
		}
		filter.ReviewID = &id
	}
	if token := query.Get("cursor"); token != "" {
		at, id, err := decodeInvestigationCursor(token)
		if err != nil {
			s.fail(w, err)
			return
		}
		filter.BeforeAt, filter.BeforeID = at, id
	}
	limit := defaultInvestigationPage
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.fail(w, invalid("The limit must be a positive number."))
			return
		}
		limit = n
	}
	limit = min(limit, maxInvestigationPage(s.Cfg.PageSizeMax))
	filter.Limit = limit + 1 // one more than the page says whether there is a next one
	rows, err := s.Store.ListReviewRuns(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeInvestigationCursor(rows[limit-1].RequestedAt, rows[limit-1].ID)
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	costs, err := s.Store.ReviewRunCosts(r.Context(), ids)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := AgentReviewRunsDTO{Items: make([]AgentReviewRunDTO, 0, len(rows)), NextCursor: next}
	for _, row := range rows {
		cost := ""
		if micro, ok := costs[row.ID]; ok {
			cost = formatUSD(micro, true)
		}
		out.Items = append(out.Items, reviewRunRowDTO(row, cost))
	}
	writeJSON(w, http.StatusOK, out)
}
