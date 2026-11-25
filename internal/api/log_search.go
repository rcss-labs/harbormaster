package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

const (
	defaultLogSearchMaxServices = 20
	defaultLogSearchMaxRange    = 168 * time.Hour
	maxLogSearchQueryChars      = 4000
	// logSearchFutureSlack is how far past now a custom range may end (clock skew between browser and server).
	logSearchFutureSlack = time.Minute
	// logInsightsUSDPerGiB is the Logs Insights price per GiB scanned.
	logInsightsUSDPerGiB = 0.005

	searchBucketCapacity        = 6
	searchBucketRefillPerSecond = 0.1
	auditWriteTimeout           = 5 * time.Second
)

// logSearchPresets are the named ranges ending now; "custom" takes from and to.
var logSearchPresets = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour}

// LogSearchServiceDTO is one service the caller can search the logs of.
type LogSearchServiceDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Cluster     string `json:"cluster"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Region      string `json:"region"`
}

// LogSearchRequest is the body of POST /api/log-search.
type LogSearchRequest struct {
	ServiceIDs []string   `json:"service_ids"`
	Range      string     `json:"range"`
	From       *time.Time `json:"from"`
	To         *time.Time `json:"to"`
	Query      string     `json:"query"`
}

type LogSearchRowDTO struct {
	T                time.Time `json:"t"`
	ServiceID        string    `json:"service_id"`
	Container        string    `json:"container"`
	TaskID           string    `json:"task_id"`
	Message          string    `json:"message"`
	MessageTruncated bool      `json:"message_truncated"`
}

// LogSearchServiceStateDTO is how one requested service fared: ok, no_awslogs (not queried) or target_failed.
type LogSearchServiceStateDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// LogSearchTargetDTO is the outcome for one account and log region.
type LogSearchTargetDTO struct {
	AccountID      string `json:"account_id"`
	AccountName    string `json:"account_name"`
	Region         string `json:"region"`
	State          string `json:"state"`
	Message        string `json:"message"`
	BytesScanned   int64  `json:"bytes_scanned"`
	RecordsMatched int64  `json:"records_matched"`
}

type LogSearchResultDTO struct {
	Rows           []LogSearchRowDTO `json:"rows"`
	RecordsMatched int64             `json:"records_matched"`
	BytesScanned   int64             `json:"bytes_scanned"`
	Capped         bool              `json:"capped"`
	// ScanLimitReached is true when a query was stopped at the scan cap (ScanLimitBytes); the rows are partial.
	ScanLimitReached bool                       `json:"scan_limit_reached"`
	ScanLimitBytes   int64                      `json:"scan_limit_bytes"`
	CostUSD          float64                    `json:"cost_usd"`
	DurationMS       int64                      `json:"duration_ms"`
	From             time.Time                  `json:"from"`
	To               time.Time                  `json:"to"`
	Services         []LogSearchServiceStateDTO `json:"services"`
	Targets          []LogSearchTargetDTO       `json:"targets"`
}

var (
	errSearchRateLimited = &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "You are searching too fast. Try again in a few seconds."}
	errSearchRunning     = &APIError{Status: http.StatusConflict, Code: "conflict", Message: "You already have a log search running."}
	errTokenSearchOff    = &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "Log search is turned off for access tokens."}
)

const budgetExhaustedMessage = "Today's log search budget for access tokens is used up. It resets at 00:00 UTC."

// validLogSearchRange reports whether name is a range a log search or a saved search may use.
// The saved-searches API calls it so both accept exactly the same values.
func validLogSearchRange(name string) bool {
	_, preset := logSearchPresets[name]
	return preset || name == "custom"
}

// logSearchWindow validates a range selection and returns the [from, to] window it covers. now is the
// server's clock; maxRange is the longest window allowed. A preset ends now; custom needs both times,
// from before to, to at most a minute in the future, and a window no longer than maxRange.
func logSearchWindow(name string, from, to *time.Time, now time.Time, maxRange time.Duration) (time.Time, time.Time, error) {
	var start, end time.Time
	switch {
	case !validLogSearchRange(name):
		return start, end, invalid("Choose a time range: 15 minutes, 1 hour, 24 hours or custom.")
	case name == "custom":
		if from == nil || to == nil {
			return start, end, invalid("Custom ranges need a start and an end.")
		}
		start, end = from.UTC(), to.UTC()
		if !start.Before(end) {
			return start, end, invalid("The start must be before the end.")
		}
		if end.After(now.Add(logSearchFutureSlack)) {
			return start, end, invalid("The end of the range cannot be in the future.")
		}
	default:
		end = now.UTC()
		start = end.Add(-logSearchPresets[name])
	}
	if end.Sub(start) > maxRange {
		return start, end, invalid("Choose a range of at most " + describeDuration(maxRange) + ".")
	}
	return start, end, nil
}

func describeDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%d %s", days, plural(days, "day", "days"))
	}
	hours := int(d.Hours())
	if hours >= 1 && d%time.Hour == 0 {
		return fmt.Sprintf("%d %s", hours, plural(hours, "hour", "hours"))
	}
	return d.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// listLogSearchServices answers GET /api/log-search/services: the services the caller can see and has Logs on.
// It reads only the stored snapshot; no AWS call and no task definition is read.
func (s *Server) listLogSearchServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subject := auth.SubjectFrom(ctx)
	rows, err := s.Store.ListServices(ctx, store.TargetFilter{}, "", visibilityOf(subject))
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, store.TargetFilter{})
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]LogSearchServiceDTO, 0, len(rows))
	for _, row := range rows {
		if !authz.Can(subject, authz.Logs, row.EffectiveTagID) {
			continue
		}
		key := row.Key
		items = append(items, LogSearchServiceDTO{
			ID: key.ID(), Name: key.Service, Cluster: key.Cluster,
			AccountID: key.AccountID, AccountName: names[key.AccountID], Region: key.Region,
		})
	}
	slices.SortFunc(items, func(a, b LogSearchServiceDTO) int {
		return cmp.Or(strings.Compare(a.AccountName, b.AccountName), strings.Compare(a.Cluster, b.Cluster),
			strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	writeJSON(w, http.StatusOK, ListResponse[LogSearchServiceDTO]{Items: items, PolledAt: polledAt})
}

// searchedService is a requested service after authorization.
type searchedService struct {
	key model.ServiceKey
	sel logsSelection
}

// postLogSearch answers POST /api/log-search. The search runs inside the request (a few seconds to the
// configured timeout): the client's connection is the cancel button, and the user's one slot is held
// exactly as long as the work runs, with nothing to recover after a restart.
func (s *Server) postLogSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req LogSearchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}

	plan, ok := s.planLogSearch(w, r, req)
	if !ok {
		return
	}
	from, to, queried, searchServices := plan.from, plan.to, plan.queried, plan.searchServices
	user, _ := auth.UserFrom(ctx)
	result := LogSearchResultDTO{Rows: []LogSearchRowDTO{}, From: from, To: to, Services: plan.states, Targets: []LogSearchTargetDTO{}}
	stateByService := map[string]*LogSearchServiceStateDTO{}
	for i := range result.Services {
		stateByService[result.Services[i].ID] = &result.Services[i]
	}
	if len(queried) == 0 {
		writeJSON(w, http.StatusOK, result)
		return
	}

	// 4. Reject bad query text before any AWS call.
	if err := logs.ValidateQuery(req.Query); err != nil {
		s.fail(w, searchQueryError(err))
		return
	}

	// 5. Guardrails: one running search per user, then a token bucket. The slot comes first so a user
	// pressing Run during a running search gets 409 without burning tokens into a 429.
	release, acquired, err := s.Limits.Acquire(ctx, limitSearch, user.ID, 1)
	if err != nil || !acquired {
		s.fail(w, errSearchRunning)
		return
	}
	defer release()
	if !s.allowRate(ctx, rateLogSearch, strconv.FormatInt(user.ID, 10), searchBucketCapacity, searchBucketRefillPerSecond) {
		s.fail(w, errSearchRateLimited)
		return
	}
	// 5b. Access tokens search against a daily scan budget. It is read after the slot is taken, so two searches of
	// one user cannot both pass the check; the cap of this search is whatever the budget has left.
	viaToken := auth.IsToken(ctx)
	searchMaxBytes := int64(0)
	var reservation tokenScanReservation
	if viaToken {
		var allowed bool
		if reservation, allowed = s.reserveTokenLogScan(w, r, user); !allowed {
			return
		}
		searchMaxBytes = reservation.bytes
	}

	// 6. Search. r.Context() ends when the client goes away, which stops the queries; so does BeginDrain
	// (Shutdown), so a running search sends StopQuery and audits inside the shutdown drain. Only the search
	// watches the drain: other handlers keep draining normally.
	searchCtx, cancelSearch := context.WithCancel(ctx)
	defer cancelSearch()
	go func() {
		select {
		case <-s.draining:
			cancelSearch()
		case <-searchCtx.Done():
		}
	}()
	found, searchErr := s.LogSearch.Search(searchCtx, logs.SearchRequest{Services: searchServices, Query: req.Query, From: from, To: to, MaxBytes: searchMaxBytes})

	// 7. Map the outcome.
	names := s.accountNamesFor(ctx, found.Targets)
	allowed := make(map[string]bool, len(queried))
	for _, svc := range queried {
		allowed[svc.key.ID()] = true
	}
	// Rows are filtered a second time here: the searcher already drops rows outside the selected services,
	// but this is the layer that knows what the caller may read, so it does not trust the layer below.
	for _, row := range found.Rows {
		if !allowed[row.ServiceID] {
			continue
		}
		result.Rows = append(result.Rows, LogSearchRowDTO{
			T: row.T, ServiceID: row.ServiceID, Container: row.Container, TaskID: row.TaskID,
			Message: row.Message, MessageTruncated: row.MessageTruncated,
		})
	}
	result.RecordsMatched, result.BytesScanned, result.Capped = found.RecordsMatched, found.BytesScanned, found.Capped
	result.ScanLimitReached, result.ScanLimitBytes = found.ScanLimitReached, s.LogSearch.MaxBytes
	if viaToken {
		result.ScanLimitBytes = searchMaxBytes
	}
	result.CostUSD = float64(found.BytesScanned) / (1 << 30) * logInsightsUSDPerGiB
	result.DurationMS = found.Duration.Milliseconds()
	targetStates := make([]map[string]any, 0, len(found.Targets))
	for _, target := range found.Targets {
		name := names[target.Target.AccountID]
		message := strings.ReplaceAll(target.Message, target.Target.AccountID, name)
		result.Targets = append(result.Targets, LogSearchTargetDTO{
			AccountID: target.Target.AccountID, AccountName: name, Region: target.Target.Region,
			State: string(target.State), Message: message,
			BytesScanned: target.BytesScanned, RecordsMatched: target.RecordsMatched,
		})
		targetStates = append(targetStates, map[string]any{"account_id": target.Target.AccountID, "region": target.Target.Region, "state": string(target.State)})
		if target.State == logs.StateOK || target.State == logs.StateScanLimit {
			continue // a query stopped at the scan cap still returned its partial rows
		}
		for _, id := range target.ServiceIDs {
			if state := stateByService[id]; state != nil {
				state.State, state.Message = "target_failed", message
			}
		}
	}

	// 8. Audit every run that reached AWS, including a stopped or failed one. The request context may
	// already be cancelled (that is why we are here), so the write detaches from it.
	s.auditLogSearch(r, user, queried, req.Range, req.Query, from, to, found, targetStates)
	if viaToken {
		s.settleTokenLogScan(r, user.ID, reservation, found)
	}

	var queryErr *logs.QueryError
	switch {
	case errors.As(searchErr, &queryErr):
		s.fail(w, searchQueryError(queryErr))
	case searchErr != nil:
		s.log().Warn("log search failed", "error", searchErr)
		writeError(w, http.StatusBadGateway, "aws_error", "Logs Insights could not run this search. Try again shortly.")
	default:
		writeJSON(w, http.StatusOK, result)
	}
}

// logSearchPlan is a validated, authorized search request: the window, the services the caller may search and
// how each fared before any query (no_awslogs ones are reported, not queried).
type logSearchPlan struct {
	from, to       time.Time
	queried        []searchedService
	searchServices []logs.SearchService
	states         []LogSearchServiceStateDTO
}

// planLogSearch validates the request and authorizes its services, for a search and for its estimate alike, so
// the two can never resolve log groups differently. On failure it has already answered and returns false.
func (s *Server) planLogSearch(w http.ResponseWriter, r *http.Request, req LogSearchRequest) (logSearchPlan, bool) {
	ctx := r.Context()
	var plan logSearchPlan
	ids := dedupe(req.ServiceIDs)
	var err error
	plan.from, plan.to, err = s.checkLogSearchRequest(ids, req.Range, req.From, req.To, req.Query)
	if err != nil {
		s.fail(w, err)
		return plan, false
	}

	// Each service the caller may search.
	services, err := s.resolveSearchServices(ctx, auth.SubjectFrom(ctx), ids)
	var forbidden errLogsForbidden
	if errors.As(err, &forbidden) {
		forbiddenAction(w, authz.Logs, forbidden.tag)
		return plan, false
	}
	if err != nil {
		s.fail(w, err)
		return plan, false
	}

	// Services with nothing in CloudWatch are reported, not queried.
	plan.states = []LogSearchServiceStateDTO{}
	for _, svc := range services {
		state := LogSearchServiceStateDTO{ID: svc.key.ID(), Name: svc.key.Service, State: "ok"}
		if svc.sel.group == "" {
			state.State, state.Message = "no_awslogs", "This service has no container that logs to CloudWatch (awslogs)."
		} else {
			plan.queried = append(plan.queried, svc)
			plan.searchServices = append(plan.searchServices, logs.SearchService{
				ID: svc.key.ID(), AccountID: svc.key.AccountID, LogRegion: svc.sel.region, Group: svc.sel.group, Prefixes: svc.sel.prefixes,
			})
		}
		plan.states = append(plan.states, state)
	}
	return plan, true
}

// LogSearchEstimateDTO is the answer of POST /api/log-search/estimate: an upper bound, not a prediction.
// StoredBytes is the stored size of every log group the search would query. Logs Insights scans only the
// time range, so the real scan is usually much smaller; the bound ignores the range on purpose because
// CloudWatch does not report how the stored bytes spread over time.
type LogSearchEstimateDTO struct {
	StoredBytes      int64   `json:"stored_bytes"`
	CostUSD          float64 `json:"cost_usd"`
	Groups           int     `json:"groups"`
	UnknownGroups    int     `json:"unknown_groups"`
	ScanLimitBytes   int64   `json:"scan_limit_bytes"`
	ExceedsScanLimit bool    `json:"exceeds_scan_limit"`
	// BudgetBytes and BudgetRemainingBytes are set for access token callers only: the day's scan budget and what is
	// left of it.
	BudgetBytes          *int64 `json:"budget_bytes,omitempty"`
	BudgetRemainingBytes *int64 `json:"budget_remaining_bytes,omitempty"`
}

// postLogSearchEstimate answers POST /api/log-search/estimate with the same body as a search. It runs the same
// validation and authorization as the search and reads only the stored size of the log groups the search would
// query; it starts no query, takes no search slot and costs nothing.
func (s *Server) postLogSearchEstimate(w http.ResponseWriter, r *http.Request) {
	var req LogSearchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	plan, ok := s.planLogSearch(w, r, req)
	if !ok {
		return
	}
	var est logs.Estimate
	if len(plan.searchServices) > 0 {
		var err error
		est, err = s.LogSearch.Estimate(r.Context(), logs.SearchRequest{Services: plan.searchServices, From: plan.from, To: plan.to})
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	limit := s.LogSearch.MaxBytes
	var budget, remaining *int64
	if auth.IsToken(r.Context()) {
		user, _ := auth.UserFrom(r.Context())
		state, err := s.tokenLogSearchState(r.Context(), user)
		if err != nil {
			s.fail(w, err)
			return
		}
		limit = s.tokenCapBytes(user)
		left := max(state.DailyBytes-state.UsedBytes, 0)
		budget, remaining = &state.DailyBytes, &left
	}
	writeJSON(w, http.StatusOK, LogSearchEstimateDTO{
		StoredBytes: est.StoredBytes, CostUSD: float64(est.StoredBytes) / logs.BytesPerGiB * logInsightsUSDPerGiB,
		Groups: est.Groups, UnknownGroups: est.UnknownGroups,
		ScanLimitBytes: limit, ExceedsScanLimit: limit > 0 && est.StoredBytes > limit,
		BudgetBytes: budget, BudgetRemainingBytes: remaining,
	})
}

// tokenScanReservation is the part of the day's budget a token search holds while it runs.
type tokenScanReservation struct {
	bytes int64     // charged to the ledger up front; also the scan cap of this search
	day   time.Time // the ledger day it was charged to, so a search spanning midnight settles the same row
}

// reserveTokenLogScan decides whether a token-initiated search may run and charges its budget up front: the smaller
// of the token cap, the global cap and what the day's budget has left. The reservation is atomic in the store, so
// searches cannot both take the same remaining budget, and a search dropped before any poll is still charged. When
// the search may not run, it has already answered (403 with the budget at 0, 429 with Retry-After when the day's
// budget is used up) and returns false.
func (s *Server) reserveTokenLogScan(w http.ResponseWriter, r *http.Request, user store.User) (tokenScanReservation, bool) {
	state, err := s.tokenLogSearchState(r.Context(), user)
	if err != nil {
		s.fail(w, err)
		return tokenScanReservation{}, false
	}
	if state.DailyBytes == 0 {
		s.fail(w, errTokenSearchOff)
		return tokenScanReservation{}, false
	}
	day := s.now()
	reserved, err := s.Store.ReserveTokenLogScan(r.Context(), user.ID, day, state.DailyBytes, state.MaxBytesPerSearch)
	if err != nil {
		s.fail(w, err)
		return tokenScanReservation{}, false
	}
	if reserved <= 0 {
		retry := max(int(state.ResetsAt.Sub(s.now()).Seconds()), 1)
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeError(w, http.StatusTooManyRequests, "log_search_budget_exhausted", budgetExhaustedMessage)
		return tokenScanReservation{}, false
	}
	return tokenScanReservation{bytes: reserved, day: day}, true
}

// settleTokenLogScan replaces the reservation with what the search scanned. A search that finished refunds the
// difference; one that was cut off (cancelled or timed out) keeps the whole reservation, because the scan it
// started may bill beyond what the final statistics show. The charge is never below the bytes actually scanned.
// It runs on a context detached from the request, like the audit write, so an abandoned search is settled all the same.
func (s *Server) settleTokenLogScan(r *http.Request, userID int64, reservation tokenScanReservation, found logs.SearchResult) {
	actual := found.BytesScanned
	for _, target := range found.Targets {
		if target.State == logs.StateCancelled || target.State == logs.StateTimeout {
			actual = max(actual, reservation.bytes)
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if err := s.Store.SettleTokenLogScan(ctx, userID, reservation.day, reservation.bytes, actual); err != nil {
		s.log().Error("token log scan usage write failed", "error", err)
	}
}

// checkLogSearchRequest is the validation both log search endpoints share, all of it before any service is
// read: the service count, the time window and the query length. ids must already be deduplicated.
func (s *Server) checkLogSearchRequest(ids []string, rangeName string, from, to *time.Time, query string) (time.Time, time.Time, error) {
	maxServices := cmp.Or(s.Cfg.LogSearchMaxServices, defaultLogSearchMaxServices)
	var start, end time.Time
	switch {
	case len(ids) == 0:
		return start, end, invalid("Choose at least one service.")
	case len(ids) > maxServices:
		return start, end, invalid(fmt.Sprintf("Choose at most %d services.", maxServices))
	}
	start, end, err := logSearchWindow(rangeName, from, to, s.now(), cmp.Or(s.Cfg.LogSearchMaxRange, defaultLogSearchMaxRange))
	if err != nil {
		return start, end, err
	}
	if utf8.RuneCountInString(query) > maxLogSearchQueryChars {
		return start, end, invalid("Queries can be up to 4,000 characters.")
	}
	return start, end, nil
}

// resolveSearchServices authorizes the services a log search names, for a search and for a saved search alike.
// Every id is parsed and looked up before any answer: an id the caller cannot see makes the whole request a
// not-found (D3), and only when none is missing is the first id without Logs returned as errLogsForbidden.
func (s *Server) resolveSearchServices(ctx context.Context, subject authz.Subject, ids []string) ([]searchedService, error) {
	keys := make([]model.ServiceKey, 0, len(ids))
	for _, id := range ids {
		key, err := model.ParseServiceID(id)
		if err != nil {
			return nil, invalid("That is not a valid service.")
		}
		keys = append(keys, key)
	}
	var services []searchedService
	var firstForbidden error
	notFound := false
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key.ID()] {
			continue
		}
		seen[key.ID()] = true
		sel, err := s.serviceLogScope(ctx, subject, key)
		var forbidden errLogsForbidden
		switch {
		case errors.Is(err, store.ErrNotFound):
			notFound = true
		case errors.As(err, &forbidden):
			if firstForbidden == nil {
				firstForbidden = forbidden
			}
		case err != nil:
			return nil, err
		default:
			services = append(services, searchedService{key: key, sel: sel})
		}
	}
	if notFound {
		return nil, store.ErrNotFound
	}
	if firstForbidden != nil {
		return nil, firstForbidden
	}
	return services, nil
}

func searchQueryError(err error) error {
	var queryErr *logs.QueryError
	if errors.As(err, &queryErr) {
		return invalid(queryErr.Message)
	}
	return err
}

// auditLogSearch records who searched which services' logs with which query. Log text can hold anything,
// so reading it is an audit event; it is not an operation on an estate resource, so it stays out of the
// operations log that feeds the service timeline and analytics.
func (s *Server) auditLogSearch(r *http.Request, user store.User, queried []searchedService, rangeName, query string, from, to time.Time, found logs.SearchResult, targetStates []map[string]any) {
	ids := make([]string, 0, len(queried))
	names := make([]string, 0, len(queried))
	for _, svc := range queried {
		ids = append(ids, svc.key.ID())
		names = append(names, svc.key.Service)
	}
	target := names[0]
	if len(names) > 1 {
		target += fmt.Sprintf(" +%d", len(names)-1)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	details := map[string]any{
		"service_ids": ids, "service_names": names, "range": rangeName,
		"from": from, "to": to, "query": query,
		"bytes_scanned": found.BytesScanned, "records_matched": found.RecordsMatched, "target_states": targetStates,
	}
	if cred, ok := auth.CredentialFrom(r.Context()); ok && cred.Kind == auth.CredentialToken {
		details["via"] = "token"
		details["token_id"] = cred.TokenPublicID
	}
	err := s.Store.Audit(ctx, store.AuditEntry{
		ActorUserID: &user.ID, ActorUsername: user.Username,
		Action: "log_search", Target: target,
		Summary: fmt.Sprintf("Searched logs of %d %s", len(names), plural(len(names), "service", "services")),
		Details: details,
		IP:      s.clientIP(r),
	})
	if err != nil {
		s.log().Error("audit write failed", "action", "log_search", "error", err)
	}
}

// accountNamesFor maps each target's account id to its name, falling back to the id itself.
func (s *Server) accountNamesFor(ctx context.Context, targets []logs.TargetResult) map[string]string {
	names := map[string]string{}
	if len(targets) == 0 {
		return names
	}
	byID := map[string]string{}
	if accounts, err := s.Store.ListAccounts(ctx); err == nil {
		for _, account := range accounts {
			byID[account.AccountID] = account.Name
		}
	}
	for _, target := range targets {
		id := target.Target.AccountID
		names[id] = id
		if name := byID[id]; name != "" {
			names[id] = name
		}
	}
	return names
}

func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
