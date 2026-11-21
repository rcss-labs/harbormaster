package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// The Send test bucket: five at once, then one every ten seconds, per user.
	notifyTestBucketCapacity        = 5
	notifyTestBucketRefillPerSecond = 0.1

	maxNotificationScope    = 100
	maxPreviewMatches       = 20
	notificationTestTimeout = 10 * time.Second
	notificationWriteTime   = 5 * time.Second

	needSessionKeyMessage  = "Set HM_SESSION_KEY before adding notification routes."
	enterDestinationAgain  = "Enter the destination again."
	staleRouteMessage      = "This route was changed by someone else. Reload and try again."
	duplicateRouteMessage  = "A route with this name already exists."
	routeLimitMessage      = "You can have up to 50 routes. Delete one first."
	invalidScopeMessage    = "One of the scope entries is not valid."
	thresholdMessage       = "A threshold must be between 1 and 500."
	onceMessage            = "A cluster or account can appear once per route."
	maxThreshold           = 500
	notificationTestAlert  = "Test message"
	notificationTestDetail = "This is a test from the Notifications page."
)

var (
	accountIDPattern = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern    = regexp.MustCompile(`^[a-z0-9-]{1,30}$`)
)

func notifyConflict(message string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "conflict", Message: message}
}

func notificationIDFrom(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errNotFound
	}
	return id, nil
}

// openRouteSecret decodes a route's sealed secret. ok is false when there is one and it does not open (the session
// key changed, or there is no key).
func (s *Server) openRouteSecret(route store.NotificationRoute) (secret notify.Secret, ok bool) {
	if len(route.SecretEnc) == 0 {
		return notify.Secret{}, true
	}
	if s.NotifyBox == nil {
		return notify.Secret{}, false
	}
	plain, err := s.NotifyBox.Open(route.SecretEnc)
	if err != nil || json.Unmarshal(plain, &secret) != nil {
		return notify.Secret{}, false
	}
	return secret, true
}

func needsSecret(destination string) bool { return destination != notify.DestEmail }

// scopeFromInput validates the scope entries. A target that does not exist right now is accepted (it is reported
// as missing); only a malformed entry is refused. Repeats are dropped.
func (s *Server) scopeFromInput(ctx context.Context, in []NotificationSelectorDTO) ([]store.NotificationSelector, error) {
	if len(in) > maxNotificationScope {
		return nil, invalid(fmt.Sprintf("A route can have at most %d scope entries.", maxNotificationScope))
	}
	var tags map[authz.TagID]TagDTO
	seen := map[store.NotificationSelector]bool{}
	out := make([]store.NotificationSelector, 0, len(in))
	for _, entry := range in {
		var sel store.NotificationSelector
		threshold := 0
		switch entry.Kind {
		case store.NotificationSelectorAccount, store.NotificationSelectorCluster:
			// A cluster or account entry is a threshold entry: N failing services send one aggregated message.
			threshold = 1
			if entry.Threshold != nil {
				threshold = *entry.Threshold
			}
			if threshold < 1 || threshold > maxThreshold {
				return nil, invalid(thresholdMessage)
			}
		default:
			if entry.Threshold != nil {
				return nil, invalid(invalidScopeMessage)
			}
		}
		switch entry.Kind {
		case store.NotificationSelectorAccount:
			if !accountIDPattern.MatchString(entry.AccountID) || (entry.Region != "" && !regionPattern.MatchString(entry.Region)) {
				return nil, invalid(invalidScopeMessage)
			}
			sel = store.NotificationSelector{Kind: entry.Kind, AccountID: entry.AccountID, Region: entry.Region, Threshold: threshold}
		case store.NotificationSelectorCluster:
			key, err := model.ParseClusterID(entry.ClusterID)
			if err != nil {
				return nil, invalid(invalidScopeMessage)
			}
			sel = store.NotificationSelector{Kind: entry.Kind, AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Threshold: threshold}
		case store.NotificationSelectorService:
			key, err := model.ParseServiceID(entry.ServiceID)
			if err != nil {
				return nil, invalid(invalidScopeMessage)
			}
			sel = store.NotificationSelector{Kind: entry.Kind, AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: key.Service}
		case store.NotificationSelectorAWS:
			if err := offhours.ValidateAWSTag(entry.Key, entry.Value); err != nil {
				return nil, invalid(invalidScopeMessage)
			}
			sel = store.NotificationSelector{Kind: entry.Kind, Key: entry.Key, Value: entry.Value}
		case store.NotificationSelectorHarbormaster:
			if tags == nil {
				var err error
				if tags, err = s.tagDTOs(ctx); err != nil {
					return nil, err
				}
			}
			if _, ok := tags[authz.TagID(entry.TagID)]; !ok {
				return nil, invalid(invalidScopeMessage)
			}
			sel = store.NotificationSelector{Kind: entry.Kind, TagID: authz.TagID(entry.TagID)}
		default:
			return nil, invalid(invalidScopeMessage)
		}
		identity := sel
		identity.Threshold = 0
		if seen[identity] {
			if threshold > 0 {
				return nil, invalid(onceMessage) // the same cluster or account twice is a mistake, not a repeat
			}
			continue
		}
		seen[identity] = true
		out = append(out, sel)
	}
	return out, nil
}

// buildRoute validates in and builds the route to store. With existing set it is an update: secrets the request
// does not carry are kept, and a change of destination starts from no secret. With preview set it validates
// everything except the destination and its secret. secretChanged reports whether the sealed secret must be
// rewritten.
func (s *Server) buildRoute(ctx context.Context, in NotificationRouteInput, existing *store.NotificationRoute, preview bool) (route store.NotificationRoute, secretChanged bool, err error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return route, false, invalid("Enter a route name.")
	}
	switch in.Destination {
	case notify.DestSlack, notify.DestTelegram, notify.DestPagerDuty, notify.DestWebhook, notify.DestEmail:
	default:
		return route, false, invalid("Choose a destination.")
	}
	if in.MinSeverity != "warning" && in.MinSeverity != "critical" {
		return route, false, invalid("Choose the lowest severity.")
	}
	kinds := make([]string, 0, len(in.Kinds))
	for _, kind := range in.Kinds {
		if !notify.KnownKind(kind) {
			return route, false, invalid("Choose at least one alert type.")
		}
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return route, false, invalid("Choose at least one alert type.")
	}
	if !slices.Contains([]int{0, 1800, 7200}, in.RepeatSeconds) {
		return route, false, invalid("Choose how often to repeat.")
	}
	if _, zoneErr := time.LoadLocation(in.QuietTimezone); in.QuietTimezone == "" || zoneErr != nil {
		return route, false, invalid("Choose a valid time zone.")
	}
	label := strings.TrimSpace(in.Label)
	if utf8.RuneCountInString(label) > 80 {
		return route, false, invalid("The label can be at most 80 characters.")
	}
	scope, err := s.scopeFromInput(ctx, in.Scope)
	if err != nil {
		return route, false, err
	}
	route = store.NotificationRoute{
		Name: name, Enabled: in.Enabled, Destination: in.Destination, MinSeverity: in.MinSeverity, Kinds: kinds,
		RepeatSeconds: in.RepeatSeconds, QuietEnabled: in.QuietEnabled, QuietTimezone: in.QuietTimezone,
		NotifyResolved: in.NotifyResolved, Scope: scope, Fallback: in.Fallback,
	}
	if existing != nil {
		route.ID, route.Version = existing.ID, existing.Version
	}
	if preview {
		return route, false, nil
	}

	if needsSecret(in.Destination) && s.NotifyBox == nil {
		return route, false, invalid(needSessionKeyMessage)
	}
	var secret notify.Secret
	unreadable := false
	sameDestination := existing != nil && existing.Destination == in.Destination
	if sameDestination && len(existing.SecretEnc) > 0 {
		if stored, ok := s.openRouteSecret(*existing); ok {
			secret = stored
		} else {
			unreadable = true
		}
	}
	secretChanged = !sameDestination
	apply := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
			secretChanged = true
		}
	}
	apply(&secret.URL, in.URL)
	apply(&secret.RoutingKey, in.RoutingKey)
	apply(&secret.SigningSecret, in.SigningSecret)
	apply(&secret.BotToken, in.BotToken)
	// Keep only what the destination uses, so switching destination cannot carry an old credential along.
	switch in.Destination {
	case notify.DestSlack:
		secret = notify.Secret{URL: secret.URL}
	case notify.DestWebhook:
		secret = notify.Secret{URL: secret.URL, SigningSecret: secret.SigningSecret}
	case notify.DestPagerDuty:
		secret = notify.Secret{RoutingKey: secret.RoutingKey}
	case notify.DestTelegram:
		secret = notify.Secret{BotToken: secret.BotToken}
	case notify.DestEmail:
		secret = notify.Secret{}
	}

	config := store.NotificationConfig{}
	switch in.Destination {
	case notify.DestSlack:
		config.Label, config.URLHost = label, urlHost(secret.URL)
	case notify.DestWebhook:
		config.URLHost, config.Signed = urlHost(secret.URL), secret.SigningSecret != ""
	case notify.DestPagerDuty:
		config.Label, config.PagerDutyRegion = label, in.PagerDutyRegion
		if config.PagerDutyRegion == "" {
			config.PagerDutyRegion = "us"
		}
	case notify.DestTelegram:
		config.Label, config.ChatID = label, strings.TrimSpace(in.ChatID)
		if in.ThreadID != nil {
			config.ThreadID = *in.ThreadID
		}
	case notify.DestEmail:
		seen := map[string]bool{}
		for _, recipient := range in.Recipients {
			if recipient = strings.TrimSpace(recipient); recipient != "" && !seen[recipient] {
				seen[recipient] = true
				config.Recipients = append(config.Recipients, recipient)
			}
		}
	}
	if err := s.Notify.ValidateDestination(in.Destination, config, secret); err != nil {
		if unreadable {
			return route, false, invalid(enterDestinationAgain)
		}
		return route, false, invalid(err.Error())
	}
	route.Config = config

	switch {
	case !needsSecret(in.Destination):
		route.SecretEnc = nil
	case !secretChanged && existing != nil:
		route.SecretEnc = existing.SecretEnc
	default:
		raw, err := json.Marshal(secret)
		if err != nil {
			return route, false, err
		}
		if route.SecretEnc, err = s.NotifyBox.Seal(raw); err != nil {
			return route, false, err
		}
	}
	return route, secretChanged, nil
}

// urlHost is the host of a URL, without port, path or query: the only part of a destination the UI may show.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// selectorDTO renders a described selector with its names.
func selectorDTO(info notify.SelectorInfo) NotificationSelectorDTO {
	sel := info.Selector
	dto := NotificationSelectorDTO{Kind: sel.Kind, AccountID: sel.AccountID, Region: sel.Region, Missing: info.Missing}
	if sel.Threshold > 0 {
		threshold := sel.Threshold
		dto.Threshold = &threshold
	}
	if sel.AccountID != "" {
		dto.AccountName = info.AccountName
		if dto.AccountName == "" {
			dto.AccountName = sel.AccountID
		}
	}
	switch sel.Kind {
	case store.NotificationSelectorCluster:
		dto.Cluster = sel.Cluster
		dto.ClusterID = model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}.ID()
	case store.NotificationSelectorService:
		cluster := model.ClusterKey{AccountID: sel.AccountID, Region: sel.Region, Cluster: sel.Cluster}
		dto.Cluster, dto.ClusterID, dto.Service = sel.Cluster, cluster.ID(), sel.Service
		dto.ServiceID = model.ServiceKey{ClusterKey: cluster, Service: sel.Service}.ID()
	case store.NotificationSelectorAWS:
		dto.Key, dto.Value = sel.Key, sel.Value
	case store.NotificationSelectorHarbormaster:
		dto.TagID, dto.TagName = int64(sel.TagID), info.TagName
	}
	return dto
}

// routeDTOs renders routes with their scope names and the failing flag.
func (s *Server) routeDTOs(ctx context.Context, routes []store.NotificationRoute) ([]NotificationRouteDTO, error) {
	targets, err := s.Store.NotificationTargets(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := s.Store.NotificationSummary(ctx, s.now())
	if err != nil {
		return nil, err
	}
	failing := map[int64]bool{}
	for _, id := range summary.FailingRouteIDs {
		failing[id] = true
	}
	out := make([]NotificationRouteDTO, 0, len(routes))
	for _, route := range routes {
		out = append(out, s.routeDTO(route, notify.DescribeScope(route.Scope, targets, tags), failing[route.ID]))
	}
	return out, nil
}

func (s *Server) routeDTO(route store.NotificationRoute, scope []notify.SelectorInfo, failing bool) NotificationRouteDTO {
	c := route.Config
	dto := NotificationRouteDTO{
		ID: route.ID, Name: route.Name, Enabled: route.Enabled, Destination: route.Destination, Target: notify.TargetLabel(route),
		Label: c.Label, Recipients: append([]string{}, c.Recipients...), PagerDutyRegion: c.PagerDutyRegion, ChatID: c.ChatID,
		SecretSet: len(route.SecretEnc) > 0, Signed: c.Signed, MinSeverity: route.MinSeverity, Kinds: append([]string{}, route.Kinds...),
		RepeatSeconds: route.RepeatSeconds, QuietEnabled: route.QuietEnabled, QuietTimezone: route.QuietTimezone,
		NotifyResolved: route.NotifyResolved, Scope: make([]NotificationSelectorDTO, 0, len(scope)), Fallback: route.Fallback,
		Failing: failing, Version: route.Version, UpdatedAt: route.UpdatedAt,
	}
	if c.ThreadID > 0 {
		thread := c.ThreadID
		dto.ThreadID = &thread
	}
	if dto.SecretSet {
		_, readable := s.openRouteSecret(route)
		dto.SecretUnreadable = !readable
	}
	allMissing := len(scope) > 0
	for _, info := range scope {
		dto.Scope = append(dto.Scope, selectorDTO(info))
		allMissing = allMissing && info.Missing
	}
	dto.MatchesNothing = allMissing
	return dto
}

func (s *Server) routeDTOFor(ctx context.Context, route store.NotificationRoute) (NotificationRouteDTO, error) {
	dtos, err := s.routeDTOs(ctx, []store.NotificationRoute{route})
	if err != nil {
		return NotificationRouteDTO{}, err
	}
	return dtos[0], nil
}

func (s *Server) listNotificationRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.Store.ListNotificationRoutes(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.routeDTOs(r.Context(), routes)
	if err != nil {
		s.fail(w, err)
		return
	}
	kinds := make([]NotificationKindDTO, len(notify.Kinds))
	for i, k := range notify.Kinds {
		kinds[i] = NotificationKindDTO{Kind: k.Kind, Label: k.Label}
	}
	writeJSON(w, http.StatusOK, NotificationRoutesDTO{Items: items, EmailAvailable: s.Notify.EmailAvailable(), SecretsAvailable: s.NotifyBox != nil, Kinds: kinds,
		SettleSeconds: int(s.Cfg.Notify.Settle.Seconds())})
}

// routeAuditView is the audit snapshot of a route: its non-secret fields, never a URL, key or token.
func routeAuditView(route store.NotificationRoute) map[string]any {
	scope := make([]string, len(route.Scope))
	for i, sel := range route.Scope {
		switch sel.Kind {
		case store.NotificationSelectorAccount:
			scope[i] = "account:" + sel.AccountID + "/" + sel.Region + ">=" + strconv.Itoa(sel.Threshold)
		case store.NotificationSelectorCluster:
			scope[i] = "cluster:" + sel.Cluster + ">=" + strconv.Itoa(sel.Threshold)
		case store.NotificationSelectorService:
			scope[i] = "service:" + sel.Cluster + "/" + sel.Service
		case store.NotificationSelectorAWS:
			scope[i] = "aws:" + sel.Key + "=" + sel.Value
		default:
			scope[i] = "tag:" + strconv.FormatInt(int64(sel.TagID), 10)
		}
	}
	return map[string]any{
		"name": route.Name, "enabled": route.Enabled, "destination": route.Destination, "min_severity": route.MinSeverity,
		"kinds": route.Kinds, "repeat_seconds": route.RepeatSeconds, "quiet_enabled": route.QuietEnabled,
		"quiet_timezone": route.QuietTimezone, "notify_resolved": route.NotifyResolved, "fallback": route.Fallback,
		"scope": scope, "version": route.Version,
	}
}

func (s *Server) kickNotify() {
	if s.KickNotify != nil {
		s.KickNotify()
	}
}

func (s *Server) createNotificationRoute(w http.ResponseWriter, r *http.Request) {
	var in NotificationRouteInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	route, secretChanged, err := s.buildRoute(ctx, in, nil, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	created, err := s.Store.CreateNotificationRoute(ctx, route, actor.ID)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.fail(w, notifyConflict(duplicateRouteMessage))
		return
	case errors.Is(err, store.ErrRouteLimit):
		s.fail(w, notifyConflict(routeLimitMessage))
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "notification_route_create", created.Name, "Created notification route "+strconv.Quote(created.Name)+".",
		map[string]any{"before": nil, "after": routeAuditView(created), "secret_changed": secretChanged})
	s.kickNotify()
	dto, err := s.routeDTOFor(ctx, created)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) updateNotificationRoute(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var in NotificationRouteInput
	if err := decodeStrictJSON(w, r, &in); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	before, err := s.Store.GetNotificationRoute(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if in.Version == nil {
		s.fail(w, invalid("version is required."))
		return
	}
	if *in.Version != before.Version {
		s.fail(w, notifyConflict(staleRouteMessage))
		return
	}
	route, secretChanged, err := s.buildRoute(ctx, in, &before, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	updated, err := s.Store.UpdateNotificationRoute(ctx, route, actor.ID)
	if errors.Is(err, store.ErrConflict) {
		if current, getErr := s.Store.GetNotificationRoute(ctx, id); getErr == nil && current.Version != before.Version {
			s.fail(w, notifyConflict(staleRouteMessage))
		} else {
			s.fail(w, notifyConflict(duplicateRouteMessage))
		}
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "notification_route_update", updated.Name, "Updated notification route "+strconv.Quote(updated.Name)+".",
		map[string]any{"before": routeAuditView(before), "after": routeAuditView(updated), "secret_changed": secretChanged})
	s.kickNotify()
	dto, err := s.routeDTOFor(ctx, updated)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) deleteNotificationRoute(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetNotificationRoute(r.Context(), id)
	if err == nil {
		err = s.Store.DeleteNotificationRoute(r.Context(), id)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "notification_route_delete", before.Name, "Deleted notification route "+strconv.Quote(before.Name)+".",
		map[string]any{"before": routeAuditView(before), "after": nil})
	s.kickNotify()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notificationTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.Store.NotificationTargets(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := NotificationTargetsDTO{
		Accounts: make([]NotificationTargetAccountDTO, 0, len(targets.Accounts)),
		Clusters: make([]NotificationTargetClusterDTO, 0, len(targets.Clusters)),
		Services: make([]NotificationTargetServiceDTO, 0, len(targets.Services)),
	}
	accountNames := map[string]string{}
	for _, a := range targets.Accounts {
		accountNames[a.AccountID] = a.Name
		out.Accounts = append(out.Accounts, NotificationTargetAccountDTO{AccountID: a.AccountID, Name: a.Name, Regions: append([]string{}, a.Regions...)})
	}
	for _, c := range targets.Clusters {
		out.Clusters = append(out.Clusters, NotificationTargetClusterDTO{ID: c.Key.ID(), Name: c.Key.Cluster, AccountID: c.Key.AccountID, AccountName: c.AccountName, Region: c.Key.Region})
	}
	for _, sv := range targets.Services {
		out.Services = append(out.Services, NotificationTargetServiceDTO{ID: sv.Key.ID(), Name: sv.Key.Service, Cluster: sv.Key.Cluster,
			ClusterID: sv.Key.ClusterKey.ID(), AccountID: sv.Key.AccountID, AccountName: sv.AccountName, Region: sv.Key.Region})
	}
	writeJSON(w, http.StatusOK, out)
}

// previewNotification renders the message a draft route would send and counts what it would send right now, with
// the code the dispatcher uses: the same planner (BuildPlan) over the same open incidents, with the draft standing
// in for the stored route it edits. It validates everything except the destination and its secret, and never
// receives or returns one.
func (s *Server) previewNotification(w http.ResponseWriter, r *http.Request) {
	var req NotificationPreviewRequest
	if err := decodeStrictJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	draft, _, err := s.buildRoute(ctx, req.NotificationRouteInput, nil, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	draft.Enabled = true
	if req.RouteID != nil {
		draft.ID = *req.RouteID
	}
	stored, err := s.Store.ListNotificationRoutes(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes := []store.NotificationRoute{draft}
	for _, other := range stored {
		if req.RouteID == nil || other.ID != *req.RouteID {
			routes = append(routes, other)
		}
	}
	scopes := map[int64]notify.Scope{}
	needTotals := false
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		scope, err := notify.ResolveScope(ctx, s.Store, route.Scope)
		if err != nil {
			s.fail(w, err)
			return
		}
		scopes[route.ID] = scope
		needTotals = needTotals || len(scope.Groups) > 0
	}
	targets, err := s.Store.NotificationTargets(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	totals := notify.Totals{}
	if needTotals {
		totals = notify.TotalsFrom(targets)
	}
	incidents, err := s.Store.ListOpenNotificationIncidents(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	names := map[string]string{}
	for _, a := range targets.Accounts {
		names[a.AccountID] = a.Name
	}

	now := s.now()
	plan := notify.BuildPlan(routes, scopes, incidents, totals, names, now, s.Cfg.Notify.Settle)
	immediate := notify.BuildPlan(routes, scopes, incidents, totals, names, now, 0)
	share := func(p notify.Plan) (matches []store.NotificationIncident, open []store.GroupState, below []notify.GroupPreview) {
		for _, incident := range incidents {
			if slices.Contains(p.PerAlert[incident.ID], draft.ID) {
				matches = append(matches, incident)
			}
		}
		for _, g := range p.Groups {
			if g.RouteID == draft.ID {
				open = append(open, g)
			}
		}
		for _, g := range p.Below {
			if g.State.RouteID == draft.ID {
				below = append(below, g)
			}
		}
		return
	}
	matches, openGroups, below := share(plan)
	allMatches, allOpen, allBelow := share(immediate)
	counted := func(open []store.GroupState, below []notify.GroupPreview) int {
		n := 0
		for _, g := range open {
			n += g.Failing
		}
		for _, g := range below {
			n += g.State.Failing
		}
		return n
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if (matches[i].Severity == model.AlertDanger) != (matches[j].Severity == model.AlertDanger) {
			return matches[i].Severity == model.AlertDanger
		}
		return matches[i].OpenedAt.After(matches[j].OpenedAt)
	})
	out := NotificationPreviewDTO{
		Matches: []NotificationMatchDTO{}, MatchCount: len(matches), Groups: []NotificationGroupPreviewDTO{},
		NotificationCount: len(matches) + len(openGroups),
		Pending:           (len(allMatches) - len(matches)) + (counted(allOpen, allBelow) - counted(openGroups, below)),
	}
	for i, m := range matches {
		if i == maxPreviewMatches {
			break
		}
		out.Matches = append(out.Matches, NotificationMatchDTO{Service: m.Key.Service, Cluster: m.Key.Cluster, Kind: m.Kind, Severity: severityName(m.Severity), Title: m.Title})
	}
	for _, g := range openGroups {
		out.Groups = append(out.Groups, groupPreviewDTO(g, true, names))
	}
	for _, g := range below {
		out.Groups = append(out.Groups, groupPreviewDTO(g.State, false, names))
	}

	subject, message := s.previewSubject(draft, matches, openGroups, names, now)
	out.Sample = message == previewSample
	rendered, err := notify.RenderPreview(draft.Destination, notify.Message{Event: notify.EventOpened, Subject: subject, BaseURL: s.Cfg.BaseURL, RouteName: draft.Name})
	if err != nil {
		s.fail(w, err)
		return
	}
	out.Destination, out.Title, out.Body, out.JSON = rendered.Destination, rendered.Title, rendered.Body, rendered.JSON
	writeJSON(w, http.StatusOK, out)
}

const (
	previewSample = "sample"
	previewReal   = "real"
)

func severityName(s model.AlertSeverity) string {
	name, _ := notify.SeverityName(s)
	return name
}

func groupPreviewDTO(g store.GroupState, open bool, names map[string]string) NotificationGroupPreviewDTO {
	dto := NotificationGroupPreviewDTO{
		Kind: g.ScopeKind, AccountID: g.Key.AccountID, Region: g.Key.Region, Threshold: g.Threshold, Failing: g.Failing, Total: g.Total,
		Open: open, Unreachable: strings.HasSuffix(g.Title, ": unreachable"), Services: []string{},
	}
	account := names[g.Key.AccountID]
	if account == "" {
		account = g.Key.AccountID
	}
	if g.ScopeKind == store.NotificationSelectorCluster {
		dto.Label, dto.ClusterID = g.Key.Cluster, g.Key.ClusterKey.ID()
	} else if g.Key.Region != "" {
		dto.Label = account + " / " + g.Key.Region
	} else {
		dto.Label = account
	}
	for i, member := range g.Members {
		if i == maxPreviewMatches/2 {
			break
		}
		dto.Services = append(dto.Services, member.Service)
	}
	return dto
}

// previewSubject picks what the editor renders: the first open group, else the first per-alert match, else a
// built-in sample (an aggregated one when the draft has only cluster or account entries).
func (s *Server) previewSubject(draft store.NotificationRoute, matches []store.NotificationIncident, groups []store.GroupState, names map[string]string, now time.Time) (notify.Subject, string) {
	accountName := func(id string) string {
		if names[id] != "" {
			return names[id]
		}
		return id
	}
	if len(groups) > 0 {
		g := groups[0]
		severity := severityName(g.Severity)
		return notify.Subject{
			Key: g.Key, Kind: g.Kind, Severity: severity, Title: g.Title, Text: g.Text, At: now, AccountName: accountName(g.Key.AccountID),
			Scope: g.ScopeKind, RouteID: draft.ID, Threshold: g.Threshold, Failing: g.Failing, Total: g.Total, Members: g.Members,
		}, previewReal
	}
	if len(matches) > 0 {
		m := matches[0]
		return notify.Subject{Key: m.Key, Kind: m.Kind, Severity: severityName(m.Severity), Title: m.Title, Text: m.Text, At: m.OpenedAt, AccountName: accountName(m.Key.AccountID)}, previewReal
	}
	cluster := model.ClusterKey{AccountID: "123456789012", Region: "us-east-1", Cluster: "background-jobs"}
	onlyThresholds := len(draft.Scope) > 0
	for _, sel := range draft.Scope {
		if sel.Kind != store.NotificationSelectorCluster && sel.Kind != store.NotificationSelectorAccount {
			onlyThresholds = false
		}
	}
	if onlyThresholds {
		cluster.Cluster = "core-services"
		members := []store.IncidentMember{
			{Cluster: "core-services", Service: "payments-api", Kind: "service_failing", Title: "payments-api: 0 of 3 tasks running", Critical: true},
			{Cluster: "core-services", Service: "auth", Kind: "restart_loop", Title: "auth: restart loop"},
		}
		return notify.Subject{
			Key: model.ServiceKey{ClusterKey: cluster}, Kind: notify.KindClusterFailing, Severity: "critical", At: now, AccountName: "example-account",
			Title: "core-services: 2 of 5 services failing", Text: members[0].Title + "\n" + members[1].Title + "\nNotifies when 2 or more services are failing.",
			Scope: "cluster", RouteID: draft.ID, Threshold: 2, Failing: 2, Total: 5, Members: members,
		}, previewSample
	}
	return notify.Subject{
		Key:  model.ServiceKey{ClusterKey: cluster, Service: "image-resizer"},
		Kind: "restart_loop", Severity: "warning", Title: "image-resizer: restart loop", Text: "3 tasks stopped with an error in the last hour.",
		At: now, AccountName: "example-account",
	}, previewSample
}

// deliveryDTO renders a log row.
func deliveryDTO(d store.NotificationDelivery) NotificationDeliveryDTO {
	dto := NotificationDeliveryDTO{
		ID: d.ID, At: d.CreatedAt, FinishedAt: d.FinishedAt, Event: d.Event, Alert: d.AlertTitle, Service: d.Key.Service, Cluster: d.Key.Cluster,
		RouteID: d.RouteID, Route: d.RouteName, Destination: d.Destination, Target: d.Target, Status: d.Status, Note: d.Note,
		HeldUntil: d.HeldUntil, Attempts: d.Attempts, CanRetry: d.Status == "failed" && d.RouteID != nil,
	}
	if d.Key.Service != "" {
		dto.ServiceID = d.Key.ID()
	}
	dto.AccountID, dto.Region = d.Key.AccountID, d.Key.Region
	if d.Key.Cluster != "" {
		dto.ClusterID = d.Key.ClusterKey.ID()
	}
	switch {
	case d.Details.Scope != "":
		dto.Scope = d.Details.Scope
	case d.Event == "test":
	case d.Key.Service != "":
		dto.Scope = "service"
	case d.Key.AccountID != "":
		dto.Scope = "account" // an account-level alert (the account is unreachable)
	}
	return dto
}

// testNotificationRoute sends one test message to the saved route, from the task that serves the request, with a
// single attempt, and records it in the delivery log. A failed send is still a 200: the row says what happened.
func (s *Server) testNotificationRoute(w http.ResponseWriter, r *http.Request) {
	id, err := notificationIDFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	route, err := s.Store.GetNotificationRoute(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	if !s.notifyTestBucket.Allow(strconv.FormatInt(actor.ID, 10)) {
		s.fail(w, errRateLimited)
		return
	}
	secret, ok := s.openRouteSecret(route)
	if !ok {
		s.fail(w, invalid(enterDestinationAgain))
		return
	}

	// The row exists before the send so its id can key a PagerDuty test event; it settles right after. It carries a
	// lease that outlasts the send, so if this task dies in between, the dispatcher fails the row instead of
	// leaving it pending for good.
	routeID := route.ID
	lease := s.now().Add(notificationTestTimeout + notificationWriteTime + 10*time.Second)
	pending, err := s.Store.InsertNotificationTest(ctx, store.NotificationDelivery{
		RouteID: &routeID, RouteName: route.Name, Destination: route.Destination, Target: notify.TargetLabel(route),
		Severity: "warning", AlertTitle: notificationTestAlert, AlertText: notificationTestDetail, Status: "pending",
		ClaimedUntil: &lease,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, notificationTestTimeout)
	defer cancel()
	result := s.Notify.Send(sendCtx, route, secret, notify.Message{
		Event: notify.EventTest, DeliveryID: pending.ID, BaseURL: s.Cfg.BaseURL, RouteName: route.Name,
		Subject: notify.Subject{Kind: "test", Severity: "warning", Title: notificationTestAlert, Text: notificationTestDetail, At: s.now()},
	})
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), notificationWriteTime)
	defer cancelWrite()
	outcome := store.DeliveryOutcome{Delivered: result.Delivered, StatusCode: result.StatusCode, DurationMS: int(result.Duration.Milliseconds()), Note: result.Note}
	if err := s.Store.FinishNotificationDelivery(writeCtx, pending.ID, pending.ClaimedUntil, 1, outcome, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	row, err := s.Store.GetNotificationDelivery(writeCtx, pending.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "notification_route_test", route.Name, "Sent a test to notification route "+strconv.Quote(route.Name)+".",
		map[string]any{"status": row.Status})
	writeJSON(w, http.StatusOK, deliveryDTO(row))
}
