package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// reviewCatalogue offers fake-1 (a run reserves 0.60 USD) and the reviewer role for three of the four built-in types and custom, listed
// out of order on purpose.
const reviewCatalogue = `{"providers":[{"provider":"fake","models":[{"id":"fake-1","input_per_mtok_micro_usd":1000000,"output_per_mtok_micro_usd":3000000,"context_tokens":8000},{"id":"fake-2","input_per_mtok_micro_usd":2000000,"output_per_mtok_micro_usd":6000000,"context_tokens":8000}]}],` +
	`"roles":["chat","investigator","reviewer","test"],"review_types":["custom","reliability","cost","security"],"limits":{"max_run_tokens":200000,"run_timeout_seconds":300}}`

// reviewsEnv is the notification test server with the agent on, reviews on (24 a day) and a fake agent that serves the
// catalogue and counts every other request, so a test can prove an endpoint made no model call.
type reviewsEnv struct {
	*notifyEnv
	omer       *apiClient
	agentCalls atomic.Int32
	route      store.NotificationRoute
	now        time.Time
}

func newReviewsEnv(t *testing.T, agentDown bool) *reviewsEnv {
	t.Helper()
	e := &reviewsEnv{notifyEnv: newNotifyEnv(t, notifyEnvOptions{agent: true}), now: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalogue" {
			e.agentCalls.Add(1)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(reviewCatalogue))
	}))
	t.Cleanup(agent.Close)
	if agentDown {
		agent.Close()
	}
	client, err := agentclient.New(agent.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Agent, e.api.AgentHeartbeat = client, time.Hour
	e.api.Cfg.AgentReviewsPerDay, e.api.Cfg.AgentReviewMaxLate = 24, 6*time.Hour
	e.api.Now = func() time.Time { return e.now }
	e.api.KickReview = func() { e.kicks.Add(1) }
	e.omer = login(t, e.srv, "omer", testPassword)
	e.route = e.emailRoute("Ops mail", "email")
	return e
}

func (e *reviewsEnv) emailRoute(name, destination string) store.NotificationRoute {
	e.t.Helper()
	route, err := e.st.CreateNotificationRoute(e.t.Context(), store.NotificationRoute{Name: name, Destination: destination, MinSeverity: "warning",
		Kinds: []string{"service_failing"}, QuietTimezone: "UTC", Enabled: true}, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return route
}

// setReviewerModel stores fake/fake-1 as the reviewer model (and a 10 USD allowance).
func (e *reviewsEnv) setReviewerModel() {
	e.t.Helper()
	ref := store.AgentModelRef{Provider: "fake", Model: "fake-1"}
	if err := storetest.PutAgentSettings(e.t.Context(), e.st, store.AgentSettings{MonthlyBudgetMicroUSD: 10_000_000,
		RoleModels: map[string]store.AgentModelRef{"chat": ref, "reviewer": ref}}); err != nil {
		e.t.Fatal(err)
	}
}

// setModels stores the chat and, when it is not empty, the reviewer model; both are of provider fake.
func (e *reviewsEnv) setModels(chat, reviewer string) {
	e.t.Helper()
	roles := map[string]store.AgentModelRef{"chat": {Provider: "fake", Model: chat}}
	if reviewer != "" {
		roles["reviewer"] = store.AgentModelRef{Provider: "fake", Model: reviewer}
	}
	if err := storetest.PutAgentSettings(e.t.Context(), e.st, store.AgentSettings{MonthlyBudgetMicroUSD: 10_000_000, RoleModels: roles}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reviewsEnv) input(mutate ...func(map[string]any)) map[string]any {
	in := map[string]any{
		"name": "Weekly cost", "type": "cost", "enabled": true,
		"schedule": map[string]any{"cadence": "weekly", "at_minute": 480, "weekday": 1, "month_day": nil, "timezone": "Europe/Berlin"},
		"scope":    []any{}, "route_ids": []int64{e.route.ID},
	}
	for _, m := range mutate {
		m(in)
	}
	return in
}

func withSchedule(fields map[string]any) func(map[string]any) {
	return func(in map[string]any) {
		schedule := map[string]any{}
		for k, v := range in["schedule"].(map[string]any) {
			schedule[k] = v
		}
		for k, v := range fields {
			schedule[k] = v
		}
		in["schedule"] = schedule
	}
}

func with(key string, value any) func(map[string]any) {
	return func(in map[string]any) { in[key] = value }
}

func (e *reviewsEnv) create(in map[string]any) ReviewDTO {
	e.t.Helper()
	resp := e.admin.post("/api/agent/reviews", in)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(e.t, resp)
		e.t.Fatalf("create = %d %s %s", resp.StatusCode, code, msg)
	}
	var out ReviewDTO
	decode(e.t, resp, &out)
	return out
}

func (e *reviewsEnv) update(id int64, in map[string]any) *http.Response {
	return e.admin.do(http.MethodPut, fmt.Sprintf("/api/agent/reviews/%d", id), in, nil)
}

func (e *reviewsEnv) list() []ReviewDTO {
	e.t.Helper()
	var out ItemsResponse[ReviewDTO]
	decode(e.t, e.admin.get("/api/agent/reviews"), &out)
	return out.Items
}

// firstReview is the first listed review; the test stops when there is none.
func (e *reviewsEnv) firstReview() ReviewDTO {
	e.t.Helper()
	listed := e.list()
	if len(listed) == 0 {
		e.t.Fatal("no reviews listed")
	}
	return listed[0]
}

func (e *reviewsEnv) stored() []store.Review {
	e.t.Helper()
	rows, err := e.st.ListReviews(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// refused reads the {"error":{"code","message","field"}} body of a refusal.
func refused(t *testing.T, resp *http.Response) (code, field string) {
	t.Helper()
	var out struct {
		Error struct{ Code, Message, Field string } `json:"error"`
	}
	decode(t, resp, &out)
	return out.Error.Code, out.Error.Field
}

func timePtr(t time.Time) *time.Time { return &t }

type reviewRoute struct{ method, path string }

// reviewRouteTable is every route of the reviews API; {id} paths use a number that need not exist (an admin boundary
// answers before the handler looks).
var reviewRouteTable = []reviewRoute{
	{"GET", "/api/agent/reviews"}, {"POST", "/api/agent/reviews"}, {"PUT", "/api/agent/reviews/1"}, {"DELETE", "/api/agent/reviews/1"},
	{"POST", "/api/agent/reviews/preview"}, {"POST", "/api/agent/reviews/1/run"}, {"GET", "/api/agent/reviews/runs"},
}

func TestAgentReviews_AdminOnly(t *testing.T) {
	e := newReviewsEnv(t, false)
	for _, r := range reviewRouteTable {
		resp := e.omer.do(r.method, r.path, map[string]any{}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if len(e.stored()) != 0 || e.kicks.Load() != 0 || e.agentCalls.Load() != 0 {
		t.Error("a non-admin changed something or reached the agent")
	}

	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
	}, nil))
	for name, c := range map[string]*apiClient{
		"personal token": tokenClient(t, srv, st, "sam"),
		"run token":      runTokenClient(t, srv, api, st, "sam"),
	} {
		for _, r := range reviewRouteTable {
			resp := c.do(r.method, r.path, map[string]any{}, nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: %s %s: status %d, want 403", name, r.method, r.path, resp.StatusCode)
				resp.Body.Close()
				continue
			}
			if code, _ := errorCode(t, resp); code != "token_not_allowed" {
				t.Errorf("%s: %s %s: code %q, want token_not_allowed", name, r.method, r.path, code)
			}
		}
	}
}

func TestAgentReviews_AbsentWhenDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	c := login(t, srv, "sam", testPassword)
	for _, r := range reviewRouteTable {
		resp := c.do(r.method, r.path, map[string]any{}, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s with the agent off: status %d, want 404", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestAgentReviews_CreateRoundTrip(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.seedTaggedServices(map[model.ServiceKey]string{nxKey: "prod", nzKey: ""})
	tag, err := e.st.CreateTag(t.Context(), "Prod", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	second := e.emailRoute("Second mail", "email")
	got := e.create(e.input(
		with("name", "  Nightly security  "), with("type", "security"), with("enabled", false),
		withSchedule(map[string]any{"cadence": "monthly", "weekday": nil, "month_day": 3, "at_minute": 90, "timezone": "Asia/Kolkata"}),
		with("scope", []any{
			clusterScope(nxKey.ClusterKey), accountScope(sandboxAccountID), map[string]any{"kind": "account", "account_id": stagingAccountID, "region": "us-east-1"},
			map[string]any{"kind": "harbormaster", "tag_id": int64(tag.ID)},
		}),
		with("route_ids", []int64{second.ID, e.route.ID}),
	))
	if got.ID == 0 || got.Name != "Nightly security" || got.Type != "security" || got.Enabled || got.Version != 1 || got.LastRun != nil {
		t.Errorf("created = %+v", got)
	}
	if s := got.Schedule; s.Cadence != "monthly" || s.AtMinute != 90 || s.Weekday != nil || s.MonthDay == nil || *s.MonthDay != 3 || s.Timezone != "Asia/Kolkata" {
		t.Errorf("schedule = %+v", got.Schedule)
	}
	if len(got.Routes) != 2 || got.Routes[0].ID != e.route.ID || got.Routes[1].ID != second.ID || got.Routes[1].Name != "Second mail" ||
		got.Routes[1].Destination != "email" || !got.Routes[1].Enabled {
		t.Errorf("routes = %+v, want both with names", got.Routes)
	}
	byKind := map[string][]NotificationSelectorDTO{}
	for _, entry := range got.Scope {
		byKind[entry.Kind] = append(byKind[entry.Kind], entry)
		// The test estate has no cluster row, so the cluster entry is reported missing; the others are not.
		if entry.Threshold != nil || entry.Missing != (entry.Kind == "cluster") {
			t.Errorf("scope entry %+v: want no threshold and missing only for the cluster", entry)
		}
	}
	if len(got.Scope) != 4 || len(byKind["cluster"]) != 1 || byKind["cluster"][0].Cluster != nxKey.Cluster || byKind["cluster"][0].AccountName != "sandbox-admin" ||
		len(byKind["account"]) != 2 || len(byKind["harbormaster"]) != 1 || byKind["harbormaster"][0].TagName != "Prod" {
		t.Errorf("scope = %+v", got.Scope)
	}
	if regions := []string{byKind["account"][0].Region, byKind["account"][1].Region}; !slices.Contains(regions, "us-east-1") || !slices.Contains(regions, "") {
		t.Errorf("account regions = %v, want one entry per account and region", regions)
	}
	if got.NextRunAt != nil {
		t.Errorf("a disabled review has next run %v", got.NextRunAt)
	}
	if list := e.list(); len(list) != 1 || list[0].ID != got.ID || len(list[0].Scope) != 4 {
		t.Errorf("list = %+v", list)
	}
	rows := e.stored()
	if len(rows) != 1 || rows[0].Name != "Nightly security" || rows[0].MonthDay != 3 || rows[0].Weekday != 0 || len(rows[0].Scope) != 4 ||
		rows[0].Scope[0].Threshold != 0 || len(rows[0].RouteIDs) != 2 {
		t.Errorf("stored = %+v", rows)
	}

	// An update replaces everything and bumps the version; a delete removes it.
	resp := e.update(got.ID, e.input(with("version", got.Version), with("name", "Renamed"), with("enabled", true), with("scope", []any{})))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}
	var updated ReviewDTO
	decode(t, resp, &updated)
	if updated.Version != 2 || updated.Name != "Renamed" || !updated.Enabled || len(updated.Scope) != 0 || updated.Schedule.Cadence != "weekly" || len(updated.Routes) != 1 {
		t.Errorf("updated = %+v", updated)
	}
	if resp := e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", got.ID), nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: status %d", resp.StatusCode)
	}
	if list := e.list(); len(list) != 0 {
		t.Errorf("after delete: %+v", list)
	}
	if resp := e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", got.ID), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: status %d, want 404", resp.StatusCode)
	}
}

func TestAgentReviews_ValidationTable(t *testing.T) {
	e := newReviewsEnv(t, false)
	pagerduty := e.emailRoute("Pager", "pagerduty")
	tag := int64(999999)
	tooMany := make([]int64, 6)
	for i := range tooMany {
		tooMany[i] = e.emailRoute(fmt.Sprintf("Extra %d", i), "email").ID
	}
	eleven := make([]any, 11)
	for i := range eleven {
		eleven[i] = map[string]any{"kind": "account", "account_id": fmt.Sprintf("%012d", i+1)}
	}
	cases := []struct {
		name         string
		in           map[string]any
		code, field  string
		wantStatuses int
	}{
		{"empty name", e.input(with("name", "")), "invalid", "name", 422},
		{"blank name", e.input(with("name", "   ")), "invalid", "name", 422},
		{"long name", e.input(with("name", strings.Repeat("n", 81))), "invalid", "name", 422},
		{"name with a line break", e.input(with("name", "two\nlines")), "invalid", "name", 422},
		{"name with a separator", e.input(with("name", "two lines")), "invalid", "name", 422},
		{"name that cleans to nothing", e.input(with("name", "​‍⁠")), "invalid", "name", 422},
		{"unknown type", e.input(with("type", "nonsense")), "invalid", "type", 422},
		{"unknown cadence", e.input(withSchedule(map[string]any{"cadence": "hourly"})), "invalid", "schedule", 422},
		{"minute out of range", e.input(withSchedule(map[string]any{"at_minute": 1440})), "invalid", "schedule", 422},
		{"weekly without a weekday", e.input(withSchedule(map[string]any{"weekday": nil})), "invalid", "schedule", 422},
		{"weekday on a daily review", e.input(withSchedule(map[string]any{"cadence": "daily"})), "invalid", "schedule", 422},
		{"day 29", e.input(withSchedule(map[string]any{"cadence": "monthly", "weekday": nil, "month_day": 29})), "invalid", "schedule", 422},
		{"unknown time zone", e.input(withSchedule(map[string]any{"timezone": "Mars/Olympus"})), "invalid", "timezone", 422},
		{"the host zone", e.input(withSchedule(map[string]any{"timezone": "Local"})), "invalid", "timezone", 422},
		{"empty time zone", e.input(withSchedule(map[string]any{"timezone": ""})), "invalid", "timezone", 422},
		{"long time zone", e.input(withSchedule(map[string]any{"timezone": strings.Repeat("Z", 65)})), "invalid", "timezone", 422},
		{"service scope", e.input(with("scope", []any{map[string]any{"kind": "service"}})), "invalid", "scope", 422},
		{"aws tag scope", e.input(with("scope", []any{map[string]any{"kind": "aws"}})), "invalid", "scope", 422},
		{"a member of another kind", e.input(with("scope", []any{map[string]any{"kind": "account", "account_id": sandboxAccountID, "tag_id": 3}})), "invalid", "scope", 422},
		{"a service id", e.input(with("scope", []any{serviceScope(nxKey)})), "invalid", "", 422},
		{"a threshold", e.input(with("scope", []any{accountScope(sandboxAccountID, 3)})), "invalid", "", 422},
		{"eleven scope entries", e.input(with("scope", eleven)), "invalid", "scope", 422},
		{"bad account id", e.input(with("scope", []any{map[string]any{"kind": "account", "account_id": "12"}})), "invalid", "scope", 422},
		{"long region", e.input(with("scope", []any{map[string]any{"kind": "account", "account_id": sandboxAccountID, "region": strings.Repeat("r", 31)}})), "invalid", "scope", 422},
		{"bad cluster id", e.input(with("scope", []any{map[string]any{"kind": "cluster", "cluster_id": strings.Repeat("c", 5000)}})), "invalid", "scope", 422},
		{"unknown tag", e.input(with("scope", []any{map[string]any{"kind": "harbormaster", "tag_id": tag}})), "invalid", "scope", 422},
		{"no routes", e.input(with("route_ids", []int64{})), "invalid", "route_ids", 422},
		{"six routes", e.input(with("route_ids", tooMany)), "invalid", "route_ids", 422},
		{"a route that does not exist", e.input(with("route_ids", []int64{e.route.ID, 987654})), "invalid", "route_ids", 422},
		{"a pagerduty route", e.input(with("route_ids", []int64{e.route.ID, pagerduty.ID})), "route_not_supported", "route_ids", 422},
	}
	for _, tc := range cases {
		resp := e.admin.post("/api/agent/reviews", tc.in)
		if resp.StatusCode != tc.wantStatuses {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.wantStatuses)
			resp.Body.Close()
			continue
		}
		if code, field := refused(t, resp); code != tc.code || field != tc.field {
			t.Errorf("%s: code %q field %q, want %q and %q", tc.name, code, field, tc.code, tc.field)
		}
	}
	if rows := e.stored(); len(rows) != 0 {
		t.Errorf("a refused review was written: %+v", rows)
	}
	if rows := settingsAuditRows(t, e.st, "agent_review_create"); len(rows) != 0 || e.kicks.Load() != 0 {
		t.Errorf("a refusal was audited (%d) or kicked (%d)", len(rows), e.kicks.Load())
	}

	// The same refusals apply to an update, and leave the stored review as it was.
	review := e.create(e.input())
	for _, tc := range cases {
		resp := e.update(review.ID, e.input(with("version", review.Version), func(in map[string]any) {
			for k, v := range tc.in {
				if k != "route_ids" || tc.field == "route_ids" {
					in[k] = v
				}
			}
		}))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("update, %s: status %d, want 422", tc.name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if rows := e.stored(); len(rows) != 1 || rows[0].Version != 1 || rows[0].Name != "Weekly cost" {
		t.Errorf("a refused update changed the review: %+v", rows)
	}
	// A duplicate name, without regard to case, is refused as a validation error, not as a stale version.
	other := e.input(with("name", "WEEKLY COST"))
	resp := e.admin.post("/api/agent/reviews", other)
	if code, field := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "name_taken" || field != "name" {
		t.Errorf("duplicate name: status %d code %q field %q", resp.StatusCode, code, field)
	}
}

func TestAgentReviews_RefusesPagerDutyRoutes(t *testing.T) {
	e := newReviewsEnv(t, false)
	pager := e.emailRoute("Pager", "pagerduty")
	resp := e.admin.post("/api/agent/reviews", e.input(with("route_ids", []int64{pager.ID})))
	if code, _ := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "route_not_supported" {
		t.Errorf("create with a pagerduty route: status %d code %q", resp.StatusCode, code)
	}
	review := e.create(e.input())
	resp = e.update(review.ID, e.input(with("version", review.Version), with("route_ids", []int64{e.route.ID, pager.ID})))
	if code, _ := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "route_not_supported" {
		t.Errorf("update with a pagerduty route: status %d code %q", resp.StatusCode, code)
	}
	if rows := e.stored(); len(rows) != 1 || len(rows[0].RouteIDs) != 1 || rows[0].Version != 1 {
		t.Errorf("stored = %+v", rows)
	}
}

func TestAgentReviews_LimitOfTwenty(t *testing.T) {
	e := newReviewsEnv(t, false)
	for i := 1; i <= store.MaxAgentReviews; i++ {
		e.create(e.input(with("name", fmt.Sprintf("Review %d", i))))
	}
	resp := e.admin.post("/api/agent/reviews", e.input(with("name", "One too many")))
	if code, _ := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "review_limit" {
		t.Errorf("21st review: status %d code %q, want 422 review_limit", resp.StatusCode, code)
	}
	if n := len(e.stored()); n != store.MaxAgentReviews {
		t.Errorf("%d reviews stored, want %d", n, store.MaxAgentReviews)
	}
	// An existing review can still be changed at the limit.
	listed := e.list()
	if len(listed) == 0 {
		t.Fatal("no reviews listed")
	}
	first := listed[0]
	if resp := e.update(first.ID, e.input(with("version", first.Version), with("name", first.Name+" b"))); resp.StatusCode != http.StatusOK {
		t.Errorf("update at the limit: status %d", resp.StatusCode)
	}
}

func TestAgentReviews_VersionConflict(t *testing.T) {
	e := newReviewsEnv(t, false)
	review := e.create(e.input())
	if resp := e.update(review.ID, e.input(with("version", review.Version), with("name", "First edit"))); resp.StatusCode != http.StatusOK {
		t.Fatalf("first update: status %d", resp.StatusCode)
	}
	kicksBefore := e.kicks.Load()
	// A stale version answers 409 before anything else is looked at, an invalid body included.
	for name, in := range map[string]map[string]any{
		"valid":   e.input(with("version", review.Version), with("name", "Second edit")),
		"invalid": e.input(with("version", review.Version), with("name", ""), with("type", "nonsense")),
	} {
		resp := e.update(review.ID, in)
		if code, _ := refused(t, resp); resp.StatusCode != http.StatusConflict || code != "review_changed" {
			t.Errorf("%s stale update: status %d code %q, want 409 review_changed", name, resp.StatusCode, code)
		}
	}
	if rows := e.stored(); len(rows) != 1 || rows[0].Name != "First edit" || rows[0].Version != 2 || e.kicks.Load() != kicksBefore {
		t.Errorf("a stale update changed or kicked: %+v", rows)
	}
	if resp := e.update(review.ID, e.input()); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("update without a version: status %d, want 422", resp.StatusCode)
	}
	if resp := e.update(987654, e.input(with("version", 1))); resp.StatusCode != http.StatusNotFound {
		t.Errorf("update of an unknown review: status %d, want 404", resp.StatusCode)
	}
}

func TestAgentReviews_UnknownTimeZoneIs422(t *testing.T) {
	e := newReviewsEnv(t, false)
	for _, zone := range []string{"Mars/Olympus", "Local", "localtime", "../etc/passwd", "posix/UTC", "Europe/Berlin\x00"} {
		resp := e.admin.post("/api/agent/reviews", e.input(withSchedule(map[string]any{"timezone": zone})))
		if code, field := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid" || field != "timezone" {
			t.Errorf("create with zone %q: status %d code %q field %q", zone, resp.StatusCode, code, field)
		}
	}
	review := e.create(e.input())
	resp := e.update(review.ID, e.input(with("version", review.Version), withSchedule(map[string]any{"timezone": "Mars/Olympus"})))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("update with an unknown zone: status %d, want 422", resp.StatusCode)
	}
	if rows := e.stored(); len(rows) != 1 || rows[0].Timezone != "Europe/Berlin" || rows[0].Version != 1 {
		t.Errorf("stored = %+v, want one review in Europe/Berlin at version 1", rows)
	}
}

func TestAgentReviews_AuditedWithOldAndNew(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.seedTaggedServices(map[model.ServiceKey]string{nxKey: ""})
	review := e.create(e.input(with("scope", []any{clusterScope(nxKey.ClusterKey)})))
	rows := settingsAuditRows(t, e.st, "agent_review_create")
	if len(rows) != 1 || rows[0].Target != "Weekly cost" || rows[0].Details["before"] != nil {
		t.Fatalf("create audit = %+v", rows)
	}
	after, _ := rows[0].Details["after"].(map[string]any)
	schedule, _ := after["schedule"].(map[string]any)
	if after["name"] != "Weekly cost" || after["type"] != "cost" || after["enabled"] != true || schedule["timezone"] != "Europe/Berlin" ||
		schedule["cadence"] != "weekly" || fmt.Sprint(after["route_ids"]) != fmt.Sprint([]any{float64(e.route.ID)}) || fmt.Sprint(after["scope"]) != "["+"cluster:"+nxKey.AccountID+"/"+nxKey.Region+"/"+nxKey.Cluster+"]" {
		t.Errorf("create audit after = %v", after)
	}

	second := e.emailRoute("Second", "email")
	e.update(review.ID, e.input(with("version", review.Version), with("name", "Renamed"), with("type", "capacity"), with("enabled", false),
		with("route_ids", []int64{second.ID}), with("scope", []any{}), withSchedule(map[string]any{"cadence": "daily", "weekday": nil, "timezone": "UTC"}))).Body.Close()
	rows = settingsAuditRows(t, e.st, "agent_review_update")
	if len(rows) != 1 || rows[0].Target != "Renamed" {
		t.Fatalf("update audit = %+v", rows)
	}
	before, _ := rows[0].Details["before"].(map[string]any)
	after, _ = rows[0].Details["after"].(map[string]any)
	if before["name"] != "Weekly cost" || before["type"] != "cost" || before["enabled"] != true || len(before["scope"].([]any)) != 1 ||
		after["name"] != "Renamed" || after["type"] != "capacity" || after["enabled"] != false || len(after["scope"].([]any)) != 0 ||
		fmt.Sprint(after["route_ids"]) != fmt.Sprint([]any{float64(second.ID)}) || after["schedule"].(map[string]any)["cadence"] != "daily" ||
		before["schedule"].(map[string]any)["cadence"] != "weekly" {
		t.Errorf("update audit before = %v, after = %v", before, after)
	}

	e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", review.ID), nil, nil).Body.Close()
	rows = settingsAuditRows(t, e.st, "agent_review_delete")
	if len(rows) != 1 || rows[0].Target != "Renamed" || rows[0].Details["after"] != nil {
		t.Fatalf("delete audit = %+v", rows)
	}
	if before, _ := rows[0].Details["before"].(map[string]any); before["name"] != "Renamed" || before["type"] != "capacity" {
		t.Errorf("delete audit before = %v", rows[0].Details["before"])
	}
}

func TestAgentReviews_ChangeKicksTheJob(t *testing.T) {
	e := newReviewsEnv(t, false)
	review := e.create(e.input())
	if got := e.kicks.Load(); got != 1 {
		t.Fatalf("kicks after create = %d, want 1", got)
	}
	e.update(review.ID, e.input(with("version", review.Version), with("name", "Edited"))).Body.Close()
	if got := e.kicks.Load(); got != 2 {
		t.Fatalf("kicks after update = %d, want 2", got)
	}
	// Reads, a preview and refusals do not kick.
	e.list()
	e.admin.post("/api/agent/reviews/preview", e.input()).Body.Close()
	e.admin.post("/api/agent/reviews", e.input(with("name", ""))).Body.Close()
	e.update(review.ID, e.input(with("version", 1))).Body.Close()
	if got := e.kicks.Load(); got != 2 {
		t.Fatalf("kicks after reads and refusals = %d, want 2", got)
	}
	e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", review.ID), nil, nil).Body.Close()
	if got := e.kicks.Load(); got != 3 {
		t.Errorf("kicks after delete = %d, want 3", got)
	}
}

func TestAgentReviews_NextRunFollowsTheSchedule(t *testing.T) {
	e := newReviewsEnv(t, false) // now is Wednesday 2026-11-25 10:00 UTC
	cases := []struct {
		name     string
		schedule map[string]any
		want     time.Time
	}{
		{"weekly Monday in Berlin", map[string]any{"cadence": "weekly", "weekday": 1, "at_minute": 480, "timezone": "Europe/Berlin"}, time.Date(2026, 11, 30, 7, 0, 0, 0, time.UTC)},
		{"daily in Kolkata", map[string]any{"cadence": "daily", "weekday": nil, "at_minute": 600, "timezone": "Asia/Kolkata"}, time.Date(2026, 11, 26, 4, 30, 0, 0, time.UTC)},
		{"monthly in Auckland", map[string]any{"cadence": "monthly", "weekday": nil, "month_day": 1, "at_minute": 0, "timezone": "Pacific/Auckland"}, time.Date(2026, 11, 30, 11, 0, 0, 0, time.UTC)},
		{"daily later today in Los Angeles", map[string]any{"cadence": "daily", "weekday": nil, "at_minute": 1380, "timezone": "America/Los_Angeles"}, time.Date(2026, 11, 26, 7, 0, 0, 0, time.UTC)},
	}
	for i, tc := range cases {
		created := e.create(e.input(with("name", fmt.Sprintf("Review %d", i)), withSchedule(tc.schedule)))
		if created.NextRunAt == nil || !created.NextRunAt.Equal(tc.want) {
			t.Errorf("%s: next run on create = %v, want %v", tc.name, created.NextRunAt, tc.want)
		}
	}
	for i, review := range e.list() {
		if review.NextRunAt == nil || !review.NextRunAt.Equal(cases[i].want) {
			t.Errorf("%s: next run in the list = %v, want %v", cases[i].name, review.NextRunAt, cases[i].want)
		}
	}
	// The clock moves: the next run moves with it.
	e.now = time.Date(2026, 11, 30, 7, 0, 0, 0, time.UTC)
	if got := e.firstReview().NextRunAt; got == nil || !got.Equal(time.Date(2026, 12, 7, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("next run after the occurrence = %v, want a week later", got)
	}
}

func TestAgentReviews_NoNextRunWhenDisabledOrOff(t *testing.T) {
	e := newReviewsEnv(t, false)
	on := e.create(e.input(with("name", "On")))
	off := e.create(e.input(with("name", "Off"), with("enabled", false)))
	// A schedule that no longer loads (stored before the zone data changed) shows no next run, and the list still answers.
	broken, err := e.st.CreateReview(t.Context(), store.Review{Name: "Broken", Type: "cost", Enabled: true, Cadence: "daily", AtMinute: 5,
		Timezone: "Mars/Olympus", RouteIDs: []int64{e.route.ID}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	byName := func() map[string]ReviewDTO {
		out := map[string]ReviewDTO{}
		for _, r := range e.list() {
			out[r.Name] = r
		}
		return out
	}
	got := byName()
	if got["On"].NextRunAt == nil || got["Off"].NextRunAt != nil || got["Broken"].NextRunAt != nil || got["Broken"].ID != broken.ID {
		t.Errorf("next runs on = %v, off = %v, broken = %v", got["On"].NextRunAt, got["Off"].NextRunAt, got["Broken"].NextRunAt)
	}
	if on.NextRunAt == nil || off.NextRunAt != nil {
		t.Errorf("create answered on = %v, off = %v", on.NextRunAt, off.NextRunAt)
	}

	// With reviews off an enabled review can still be saved, and shows no next run.
	e.api.Cfg.AgentReviewsPerDay = 0
	if got := byName()["On"]; got.NextRunAt != nil || !got.Enabled {
		t.Errorf("with reviews off: %+v, want enabled and no next run", got)
	}
	created := e.create(e.input(with("name", "Saved while off")))
	if !created.Enabled || created.NextRunAt != nil {
		t.Errorf("created while reviews are off = %+v", created)
	}
}

// ---- preview ----

func (e *reviewsEnv) preview(in map[string]any) ReviewPreviewDTO {
	e.t.Helper()
	resp := e.admin.post("/api/agent/reviews/preview", in)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(e.t, resp)
		e.t.Fatalf("preview = %d %s %s", resp.StatusCode, code, msg)
	}
	var out ReviewPreviewDTO
	decode(e.t, resp, &out)
	return out
}

func TestReviewPreview_CountsServicesTheIdentityCannotSee(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.seedTaggedServices(map[model.ServiceKey]string{nxKey: "prod", nyKey: "", nzKey: "prod"})
	prod, err := e.st.CreateTag(t.Context(), "Prod", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetClusterTag(t.Context(), nzKey.ClusterKey, prod.ID); err != nil {
		t.Fatal(err)
	}
	if resp := putIdentity(e.admin, []map[string]any{{"tag_id": int64(e.devTag().ID), "logs": false}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT identity: status %d", resp.StatusCode)
	}
	scoped := func(entries ...any) map[string]any {
		return e.input(with("scope", entries), with("enabled", false), with("route_ids", []int64{}))
	}
	for name, tc := range map[string]struct {
		in            map[string]any
		total, unseen int
	}{
		"everything":   {e.input(), 3, 1},
		"one cluster":  {scoped(clusterScope(nzKey.ClusterKey)), 1, 1},
		"two clusters": {scoped(clusterScope(nxKey.ClusterKey), clusterScope(nzKey.ClusterKey)), 3, 1},
		"an account":   {scoped(accountScope(sandboxAccountID)), 3, 1},
		"a tag":        {scoped(map[string]any{"kind": "harbormaster", "tag_id": int64(prod.ID)}), 1, 1},
	} {
		if got := e.preview(tc.in); got.ServicesTotal != tc.total || got.ServicesUnseen != tc.unseen {
			t.Errorf("%s = %+v, want %d services and %d unseen", name, got, tc.total, tc.unseen)
		}
	}
	if resp := putIdentity(e.admin, []map[string]any{{"tag_id": int64(e.devTag().ID), "logs": false}, {"tag_id": int64(prod.ID), "logs": false}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT identity: status %d", resp.StatusCode)
	}
	if got := e.preview(e.input()); got.ServicesTotal != 3 || got.ServicesUnseen != 0 {
		t.Errorf("after granting Prod = %+v, want 3 and 0 unseen", got)
	}
	// A preview writes nothing and ignores what is off: a disabled draft with reviews off still counts and has a next run.
	e.api.Cfg.AgentReviewsPerDay = 0
	got := e.preview(e.input(with("enabled", false)))
	if got.ServicesTotal != 3 || got.NextRunAt == nil {
		t.Errorf("preview with reviews off = %+v, want counts and a next run", got)
	}
	if len(e.stored()) != 0 {
		t.Error("a preview stored a review")
	}
	// It still refuses a draft whose schedule or scope is malformed.
	for _, in := range []map[string]any{e.input(withSchedule(map[string]any{"timezone": "Mars/Olympus"})), e.input(with("scope", []any{serviceScope(nxKey)}))} {
		if resp := e.admin.post("/api/agent/reviews/preview", in); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("malformed preview: status %d, want 422", resp.StatusCode)
		}
	}
}

func TestReviewPreview_WorstCaseMonthFromTheCatalogue(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.setReviewerModel() // one run reserves 200000 tokens at 3 USD per million: 0.60
	for name, tc := range map[string]struct {
		schedule map[string]any
		want     string
	}{
		"daily":   {map[string]any{"cadence": "daily", "weekday": nil}, "18.60"},
		"weekly":  {map[string]any{"cadence": "weekly"}, "3.00"},
		"monthly": {map[string]any{"cadence": "monthly", "weekday": nil, "month_day": 2}, "0.60"},
	} {
		got := e.preview(e.input(withSchedule(tc.schedule)))
		if got.WorstCaseMonthUSD == nil || *got.WorstCaseMonthUSD != tc.want {
			t.Errorf("%s: worst case = %v, want %s", name, got.WorstCaseMonthUSD, tc.want)
		}
	}
	// The same figure sits on a saved review.
	if saved := e.create(e.input()); saved.WorstCaseMonthUSD == nil || *saved.WorstCaseMonthUSD != "3.00" {
		t.Errorf("saved review worst case = %v, want 3.00", saved.WorstCaseMonthUSD)
	}
	if e.agentCalls.Load() != 0 {
		t.Error("a preview called the agent beyond the catalogue")
	}
}

func TestReviewPreview_MoneyIsNullWithoutAModel(t *testing.T) {
	e := newReviewsEnv(t, false)
	check := func(label string) {
		t.Helper()
		resp := e.admin.post("/api/agent/reviews/preview", e.input())
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", label, resp.StatusCode)
		}
		var got ReviewPreviewDTO
		decode(t, resp, &got)
		if got.WorstCaseMonthUSD != nil || got.NextRunAt == nil {
			t.Errorf("%s: %+v, want a null cost and a next run", label, got)
		}
		if saved := e.list(); len(saved) > 0 && saved[0].WorstCaseMonthUSD != nil {
			t.Errorf("%s: the saved review shows %v", label, *saved[0].WorstCaseMonthUSD)
		}
	}
	check("no model chosen")
	e.setReviewerModel()
	retired := store.AgentModelRef{Provider: "fake", Model: "retired-1"}
	if err := storetest.PutAgentSettings(t.Context(), e.st, store.AgentSettings{MonthlyBudgetMicroUSD: 1, RoleModels: map[string]store.AgentModelRef{"reviewer": retired}}); err != nil {
		t.Fatal(err)
	}
	check("a model the agent no longer offers")

	down := newReviewsEnv(t, true)
	down.setReviewerModel()
	resp := down.admin.post("/api/agent/reviews/preview", down.input())
	var got ReviewPreviewDTO
	decode(t, resp, &got)
	if resp.StatusCode != http.StatusOK || got.WorstCaseMonthUSD != nil {
		t.Errorf("agent unreachable: status %d, %+v, want 200 and a null cost", resp.StatusCode, got)
	}
}

// ---- run now ----

func (e *reviewsEnv) runNow(id int64) *http.Response {
	return e.admin.post(fmt.Sprintf("/api/agent/reviews/%d/run", id), nil)
}

func TestReviewRunNow_RecordsAuditsAndKicks(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.setReviewerModel()
	review := e.create(e.input(with("enabled", false))) // a disabled review can be run by hand
	kicks := e.kicks.Load()
	resp := e.runNow(review.ID)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run now: status %d, want 202", resp.StatusCode)
	}
	var started ReviewRunStartedDTO
	decode(t, resp, &started)
	run, err := e.st.GetReviewRun(t.Context(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Manual || run.State != "requested" || run.ReviewID == nil || *run.ReviewID != review.ID || run.ReviewName != "Weekly cost" ||
		!strings.HasPrefix(run.PeriodKey, "x:") || !run.RequestedAt.Equal(e.now) {
		t.Errorf("run = %+v", run)
	}
	if got := e.kicks.Load(); got != kicks+1 {
		t.Errorf("kicks = %d, want %d", got, kicks+1)
	}
	rows := settingsAuditRows(t, e.st, "agent_review_run")
	if len(rows) != 1 || rows[0].Target != "Weekly cost" || rows[0].Details["review_id"] != float64(review.ID) || rows[0].Details["run_id"] != float64(started.RunID) {
		t.Errorf("audit = %+v", rows)
	}
	// The run shows as the review's last run.
	if last := e.firstReview().LastRun; last == nil || last.ID != started.RunID || last.State != "requested" || last.FinishedAt != nil {
		t.Errorf("last run = %+v", last)
	}
}

func TestReviewRunNow_RefusedWhileBusyOffOrWithoutAModel(t *testing.T) {
	e := newReviewsEnv(t, false)
	review := e.create(e.input())
	runs := func() int {
		got, err := e.st.ListReviewRuns(t.Context(), store.ReviewRunFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	kicks := e.kicks.Load()
	expect := func(label string, resp *http.Response, status int, code string) {
		t.Helper()
		if resp.StatusCode != status {
			t.Errorf("%s: status %d, want %d", label, resp.StatusCode, status)
			resp.Body.Close()
			return
		}
		if got, _ := refused(t, resp); got != code {
			t.Errorf("%s: code %q, want %q", label, got, code)
		}
	}
	expect("no model", e.runNow(review.ID), http.StatusUnprocessableEntity, "no_model")
	e.setReviewerModel()
	e.api.Cfg.AgentReviewsPerDay = 0
	expect("reviews off", e.runNow(review.ID), http.StatusUnprocessableEntity, "reviews_off")
	e.api.Cfg.AgentReviewsPerDay = 24
	expect("unknown review", e.runNow(987654), http.StatusNotFound, "not_found")
	if runs() != 0 || e.kicks.Load() != kicks || len(settingsAuditRows(t, e.st, "agent_review_run")) != 0 {
		t.Fatalf("a refused run was recorded (%d), kicked or audited", runs())
	}

	if resp := e.runNow(review.ID); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first run: status %d", resp.StatusCode)
	}
	expect("waiting", e.runNow(review.ID), http.StatusConflict, "review_busy")
	// Running is busy too.
	if _, _, err := e.st.ClaimReviewRun(t.Context(), e.now, time.Minute, time.Time{}); err != nil {
		t.Fatal(err)
	}
	expect("running", e.runNow(review.ID), http.StatusConflict, "review_busy")
	if runs() != 1 || len(settingsAuditRows(t, e.st, "agent_review_run")) != 1 || e.kicks.Load() != kicks+1 {
		t.Errorf("runs = %d, audits = %d, kicks = %d; want 1, 1, %d", runs(), len(settingsAuditRows(t, e.st, "agent_review_run")), e.kicks.Load(), kicks+1)
	}
}

func TestReviewRunNow_MakesNoModelCall(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.setReviewerModel()
	review := e.create(e.input())
	if resp := e.runNow(review.ID); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run now: status %d", resp.StatusCode)
	}
	if n := e.agentCalls.Load(); n != 0 {
		t.Errorf("the agent received %d requests besides the catalogue; run now must only record the run", n)
	}
	if runs, err := e.st.ListReviewRuns(t.Context(), store.ReviewRunFilter{}); err != nil || len(runs) != 1 {
		t.Errorf("recorded runs = %d, %v; want the one requested run", len(runs), err)
	}
	var reserved int
	if err := e.st.Pool.QueryRow(t.Context(), `select count(*) from agent_runs`).Scan(&reserved); err != nil || reserved != 0 {
		t.Errorf("agent runs after run now = %d, %v; the job reserves, not the request", reserved, err)
	}
}

// ---- runs list ----

// finishReviewRun marks a run done with a summary and two settled agent runs of the given costs.
func (e *reviewsEnv) finishReviewRun(id int64, at time.Time, summary string, costs ...int64) {
	e.t.Helper()
	ids := []uuid.UUID{}
	for _, cost := range costs {
		runID := uuid.New()
		ids = append(ids, runID)
		if _, err := e.st.Pool.Exec(e.t.Context(), `insert into agent_runs (id, role, provider, model, state, cost_micro_usd, started_at, finished_at)
			values ($1, 'reviewer', 'fake', 'fake-1', 'done', $2, $3, $3)`, runID, cost, at); err != nil {
			e.t.Fatal(err)
		}
	}
	var prev, latest any
	switch len(ids) {
	case 1:
		latest = ids[0]
	case 2:
		prev, latest = ids[0], ids[1]
	}
	if _, err := e.st.Pool.Exec(e.t.Context(), `update agent_review_runs set state = 'done', summary = $2, attempts = $3, provider = 'fake', model = 'fake-1',
		scope_label = 'Everything', services_total = 7, started_at = $4, finished_at = $4, prev_run_id = $5, run_id = $6 where id = $1`,
		id, summary, max(len(costs), 1), at, prev, latest); err != nil {
		e.t.Fatal(err)
	}
}

func TestAgentReviewRuns_AdminListWithCostOfBothAttempts(t *testing.T) {
	e := newReviewsEnv(t, false)
	a := e.create(e.input(with("name", "A")))
	b := e.create(e.input(with("name", "B")))
	var ids []int64
	for i, review := range []ReviewDTO{a, a, b} {
		run, err := e.st.RequestManualReviewRun(t.Context(), review.ID, fmt.Sprintf("Manual run %d", i), false, e.now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
		switch i {
		case 0:
			e.finishReviewRun(run.ID, e.now.Add(time.Hour), "All quiet.", 700_000, 250_000) // two paid attempts
		case 1:
			e.finishReviewRun(run.ID, e.now.Add(time.Hour), "Costs rose.", 40_000)
		}
	}
	var page AgentReviewRunsDTO
	decode(t, e.admin.get("/api/agent/reviews/runs"), &page)
	if len(page.Items) != 3 || page.NextCursor != "" || page.Items[0].ID != ids[2] || page.Items[2].ID != ids[0] {
		t.Fatalf("list = %+v, want three newest first", page)
	}
	oldest, middle, newest := page.Items[2], page.Items[1], page.Items[0]
	if oldest.CostUSD != "0.95" || oldest.Attempts != 2 || oldest.Summary != "All quiet." || oldest.State != "done" || oldest.Provider != "fake" || oldest.Model != "fake-1" ||
		oldest.Name != "A" || oldest.Type != "cost" || !oldest.Manual || oldest.ScopeLabel != "Everything" || oldest.ServicesTotal != 7 || oldest.PeriodLabel != "Manual run 0" ||
		oldest.ReviewID == nil || *oldest.ReviewID != a.ID || oldest.FinishedAt == nil {
		t.Errorf("both attempts = %+v, want 0.95 (700000 + 250000, rounded up)", oldest)
	}
	if middle.CostUSD != "0.04" || middle.Attempts != 1 {
		t.Errorf("one attempt = %+v", middle)
	}
	if newest.CostUSD != "" || newest.State != "requested" || newest.Summary != "" || newest.StartedAt != nil || newest.FinishedAt != nil {
		t.Errorf("a run that has not started = %+v, want no cost, no summary, no times", newest)
	}
	// The summary is only ever shown for a done run.
	if _, err := e.st.Pool.Exec(t.Context(), `update agent_review_runs set state = 'failed', reason = 'error' where id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	if got := fetchRuns(t, e, "").Items; len(got) != 3 || got[1].State != "failed" || got[1].Summary != "" {
		t.Errorf("failed run = %+v, want no summary", got)
	}

	var filtered AgentReviewRunsDTO
	decode(t, e.admin.get(fmt.Sprintf("/api/agent/reviews/runs?review_id=%d", b.ID)), &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].ID != ids[2] {
		t.Errorf("filtered by review = %+v", filtered)
	}
	first := fetchRuns(t, e, "?limit=2")
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := fetchRuns(t, e, "?limit=2&cursor="+first.NextCursor)
	if len(second.Items) != 1 || second.Items[0].ID != ids[0] || second.NextCursor != "" {
		t.Errorf("second page = %+v", second)
	}
	for _, bad := range []string{"?limit=0", "?limit=x", "?cursor=!!", "?review_id=x"} {
		if resp := e.admin.get("/api/agent/reviews/runs" + bad); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", bad, resp.StatusCode)
		}
	}
}

func fetchRuns(t *testing.T, e *reviewsEnv, query string) AgentReviewRunsDTO {
	t.Helper()
	var out AgentReviewRunsDTO
	decode(t, e.admin.get("/api/agent/reviews/runs"+query), &out)
	return out
}

// ---- settings ----

func putReviewerRoles(c *apiClient, roles map[string]any) *http.Response {
	return putSettingsRoles(c, "30", roles)
}

func TestAgentSettings_ReviewerModelRoundTrip(t *testing.T) {
	e := newReviewsEnv(t, false)
	if _, ok := getSettings(t, e.admin).RoleModels["reviewer"]; ok {
		t.Error("a reviewer model before one was chosen")
	}
	resp := putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var saved settingsBody
	decode(t, resp, &saved)
	for _, s := range []settingsBody{saved, getSettings(t, e.admin)} {
		got := s.RoleModels["reviewer"]
		if got["provider"] != "fake" || got["model"] != "fake-1" || got["available"] != true || got["max_question_usd"] != "0.60" {
			t.Errorf("reviewer = %v", got)
		}
	}
	if stored, err := e.st.GetAgentSettings(t.Context()); err != nil || stored.RoleModels["reviewer"].Model != "fake-1" {
		t.Errorf("stored roles = %v, %v", stored.RoleModels, err)
	}
	rows := settingsAuditRows(t, e.st, "agent_settings_update")
	if len(rows) != 1 || rows[0].Details["old_reviewer_model"] != "" || rows[0].Details["new_reviewer_model"] != "fake/fake-1" {
		t.Errorf("audit = %+v", rows)
	}
	// A model that is not offered is refused for this role as for the others; a role nobody knows still is.
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": map[string]string{"provider": "fake", "model": "nope"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("reviewer not in the catalogue: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "planner": fakeChat()})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	// A body without the key keeps it (an older bundle); an explicit null clears it and is audited.
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT without the reviewer: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := getSettings(t, e.admin).RoleModels["reviewer"]; got["model"] != "fake-1" {
		t.Errorf("after a save without the key: %v, want the reviewer kept", got)
	}
	if rows := settingsAuditRows(t, e.st, "agent_settings_update"); len(rows) == 0 || rows[0].Details["new_reviewer_model"] != "fake/fake-1" {
		t.Errorf("audit of a kept model = %v", rows[0].Details)
	}
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT with a null reviewer: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, ok := getSettings(t, e.admin).RoleModels["reviewer"]; ok {
		t.Error("reviewer model still set after null")
	}
	if rows := settingsAuditRows(t, e.st, "agent_settings_update"); len(rows) == 0 || rows[0].Details["old_reviewer_model"] != "fake/fake-1" || rows[0].Details["new_reviewer_model"] != "" {
		t.Errorf("audit of a cleared model = %v", rows[0].Details)
	}
}

func TestAgentSettings_ReviewsBlock(t *testing.T) {
	e := newReviewsEnv(t, false)
	e.api.Cfg.AgentReviewsPerDay, e.api.Cfg.AgentReviewMaxLate = 12, 90*time.Minute
	type block struct {
		Enabled         bool                         `json:"enabled"`
		PerDay          int                          `json:"per_day"`
		MaxLateMinutes  int                          `json:"max_late_minutes"`
		MaxReviews      int                          `json:"max_reviews"`
		Types           []struct{ ID, Label string } `json:"types"`
		SupportedTypes  []string                     `json:"supported_types"`
		WorstCaseRunUSD *string                      `json:"worst_case_run_usd"`
	}
	read := func(c *apiClient) block {
		t.Helper()
		var out struct {
			Reviews block `json:"reviews"`
		}
		decode(t, c.get("/api/agent/settings"), &out)
		return out.Reviews
	}
	got := read(e.admin)
	if !got.Enabled || got.PerDay != 12 || got.MaxLateMinutes != 90 || got.MaxReviews != 20 || got.WorstCaseRunUSD != nil {
		t.Errorf("block = %+v", got)
	}
	if fmt.Sprint(got.Types) != "[{cost Cost} {security Security findings} {reliability Reliability} {capacity Capacity}]" {
		t.Errorf("types = %v", got.Types)
	}
	if fmt.Sprint(got.SupportedTypes) != "[cost security reliability]" {
		t.Errorf("supported types = %v, want the catalogue's, in the form's order", got.SupportedTypes)
	}
	e.setReviewerModel()
	if got := read(e.admin); got.WorstCaseRunUSD == nil || *got.WorstCaseRunUSD != "0.60" {
		t.Errorf("worst case run = %v, want 0.60", got.WorstCaseRunUSD)
	}
	e.api.Cfg.AgentReviewsPerDay = 0
	if got := read(e.admin); got.Enabled || got.PerDay != 0 {
		t.Errorf("with HM_AGENT_REVIEWS_PER_DAY=0: %+v, want reviews off", got)
	}

	down := newReviewsEnv(t, true)
	down.setReviewerModel()
	if got := read(down.admin); got.SupportedTypes == nil || len(got.SupportedTypes) != 0 || got.WorstCaseRunUSD != nil || len(got.Types) != 4 {
		t.Errorf("agent unreachable: %+v, want empty (not null) supported types and no cost", got)
	}
}

func TestAgentSettings_StaleReviewerModelIsFlaggedAndSavable(t *testing.T) {
	e := newReviewsEnv(t, false)
	stored, _ := e.st.GetAgentSettings(t.Context())
	stored.RoleModels["reviewer"] = store.AgentModelRef{Provider: "fake", Model: "retired-1"}
	if err := storetest.PutAgentSettings(t.Context(), e.st, stored); err != nil {
		t.Fatal(err)
	}
	got := getSettings(t, e.admin).RoleModels["reviewer"]
	if got["model"] != "retired-1" || got["available"] != false || got["max_question_usd"] != "" {
		t.Errorf("reviewer = %v, want retired-1 flagged unavailable with nothing reserved", got)
	}
	resp := putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": map[string]string{"provider": "fake", "model": "retired-1"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unchanged stale reviewer model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": map[string]string{"provider": "fake", "model": "other-gone"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("changed to a model not offered: status %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
	resp = putReviewerRoles(e.admin, map[string]any{"chat": fakeChat(), "reviewer": fakeChat()})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("changed to an offered model: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---- delivery log ----

func TestNotificationDeliveries_CarryTheReviewRunOfAReviewRow(t *testing.T) {
	e := newReviewsEnv(t, false)
	review := e.create(e.input())
	run, err := e.st.RequestManualReviewRun(t.Context(), review.ID, "Manual run", false, e.now)
	if err != nil {
		t.Fatal(err)
	}
	routeID := e.route.ID
	if ok, err := e.st.InsertReviewDelivery(t.Context(), store.NotificationDelivery{RouteID: &routeID, RouteName: e.route.Name, Destination: "email", Target: "ops@example.com",
		AlertTitle: "Cost review: Weekly cost"}, run.ID, e.now, nil); err != nil || !ok {
		t.Fatalf("insert review delivery = %v, %v", ok, err)
	}
	e.seedDelivery(&e.route, "delivered", time.Minute, "")
	var reviewRows, other int
	for _, d := range e.deliveries("").Items {
		switch {
		case d.ReviewRunID != nil && *d.ReviewRunID == run.ID:
			reviewRows++
		case d.ReviewRunID == nil:
			other++
		}
	}
	if reviewRows != 1 || other != 1 {
		t.Errorf("delivery log: %d rows with the review run, %d without; want 1 and 1", reviewRows, other)
	}
}

// ---- fix round 1 ----

func TestAgentReviews_AGoneTagStaysOnPreviewAndUpdate(t *testing.T) {
	e := newReviewsEnv(t, false)
	tag, err := e.st.CreateTag(t.Context(), "Temp", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	tagScope := []any{map[string]any{"kind": "harbormaster", "tag_id": int64(tag.ID)}}
	review := e.create(e.input(with("scope", tagScope)))
	if err := e.st.DeleteTag(t.Context(), tag.ID); err != nil {
		t.Fatal(err)
	}
	// The draft's own tag entry is kept as stored: it matches no service and shows as missing.
	preview := e.admin.post("/api/agent/reviews/preview", e.input(with("scope", tagScope)))
	if preview.StatusCode != http.StatusOK {
		code, msg := errorCode(t, preview)
		t.Fatalf("preview with a deleted tag: status %d %s %s, want 200", preview.StatusCode, code, msg)
	}
	var counts ReviewPreviewDTO
	decode(t, preview, &counts)
	if counts.ServicesTotal != 0 {
		t.Errorf("a gone tag matched %d services", counts.ServicesTotal)
	}
	resp := e.update(review.ID, e.input(with("version", review.Version), with("scope", tagScope), with("name", "Kept")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update keeping a deleted tag: status %d, want 200", resp.StatusCode)
	}
	var updated ReviewDTO
	decode(t, resp, &updated)
	if len(updated.Scope) != 1 || !updated.Scope[0].Missing || updated.Scope[0].TagID != int64(tag.ID) {
		t.Errorf("scope = %+v, want the deleted tag shown as missing", updated.Scope)
	}
	// A save refuses a tag the review never held, and so does a create. (A preview cannot tell a gone tag from an unknown one.)
	other := []any{map[string]any{"kind": "harbormaster", "tag_id": int64(tag.ID) + 1000}}
	if resp := e.update(review.ID, e.input(with("version", updated.Version), with("scope", other))); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("update with an unknown tag: status %d, want 422", resp.StatusCode)
	}
	if resp := e.admin.post("/api/agent/reviews", e.input(with("name", "Fresh"), with("scope", tagScope))); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("create with a deleted tag: status %d, want 422", resp.StatusCode)
	}
}

func TestAgentReviews_AnAdminCanAlwaysTurnAReviewOff(t *testing.T) {
	e := newReviewsEnv(t, false) // no reviewer model is set: turning off needs none
	tag, err := e.st.CreateTag(t.Context(), "Temp", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	tagScope := []any{map[string]any{"kind": "harbormaster", "tag_id": int64(tag.ID)}}
	good := e.emailRoute("Good", "email")
	pager := e.emailRoute("Soon pager", "email")
	setup := func(name string) ReviewDTO {
		t.Helper()
		return e.create(e.input(with("name", name), with("scope", tagScope), with("route_ids", []int64{good.ID, pager.ID})))
	}
	storedRoutes := func(id int64) []int64 {
		r, err := e.st.GetReview(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return r.RouteIDs
	}
	off := func(review ReviewDTO, routes []int64, change ...func(map[string]any)) *http.Response {
		in := e.input(append([]func(map[string]any){with("version", review.Version), with("name", review.Name), with("enabled", false),
			with("scope", tagScope), with("route_ids", routes)}, change...)...)
		return e.update(review.ID, in)
	}

	// A route became PagerDuty and the tag is gone; the web sends the routes that are still allowed.
	review := setup("Pager later")
	if _, err := e.st.Pool.Exec(t.Context(), `update notification_routes set destination = 'pagerduty' where id = $1`, pager.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteTag(t.Context(), tag.ID); err != nil {
		t.Fatal(err)
	}
	// Enabled and unchanged it is still refused, and so is a change of anything beside the switch.
	if resp := e.update(review.ID, e.input(with("version", review.Version), with("name", review.Name), with("scope", tagScope), with("route_ids", []int64{good.ID, pager.ID}))); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("saving it still on with a PagerDuty route: status %d, want 422", resp.StatusCode)
	}
	if resp := off(review, []int64{good.ID, pager.ID}, with("name", "Renamed")); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("turning off and renaming with a PagerDuty route stored: status %d, want 422 (not the exception)", resp.StatusCode)
	}
	if resp := off(review, []int64{}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("turning off and dropping an allowed route: status %d, want 422 (only a not-allowed route may be dropped)", resp.StatusCode)
	}
	resp := off(review, []int64{good.ID})
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("turn off: status %d %s %s, want 200", resp.StatusCode, code, msg)
	}
	var offDTO ReviewDTO
	decode(t, resp, &offDTO)
	if offDTO.Enabled || offDTO.Version != review.Version+1 || len(offDTO.Routes) != 2 || len(offDTO.Scope) != 1 {
		t.Errorf("after turning off = %+v, want off with the stored routes and scope untouched", offDTO)
	}
	if got := storedRoutes(review.ID); len(got) != 2 {
		t.Errorf("stored routes = %v, want both kept", got)
	}
	// Turning it back on is a full save and is refused while a route is PagerDuty.
	if resp := e.update(review.ID, e.input(with("version", offDTO.Version), with("name", review.Name), with("scope", tagScope), with("route_ids", []int64{good.ID, pager.ID}))); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("turning on with a PagerDuty route: status %d, want 422", resp.StatusCode)
	}

	// Every route deleted: the stored review has none, and can still be turned off.
	bare := e.create(e.input(with("name", "Bare"), with("route_ids", []int64{good.ID})))
	if err := e.st.DeleteNotificationRoute(t.Context(), good.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteNotificationRoute(t.Context(), pager.ID); err != nil {
		t.Fatal(err)
	}
	if resp := e.update(bare.ID, e.input(with("version", bare.Version), with("name", "Bare"), with("enabled", false), with("route_ids", []int64{}))); resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Errorf("turn off with no routes left: status %d %s %s, want 200", resp.StatusCode, code, msg)
	}
	if got := storedRoutes(bare.ID); len(got) != 0 {
		t.Errorf("stored routes = %v, want none", got)
	}
	// An off review that is saved again off with no routes is a full save: refused.
	again, _ := e.st.GetReview(t.Context(), bare.ID)
	if resp := e.update(bare.ID, e.input(with("version", again.Version), with("name", "Bare"), with("enabled", false), with("route_ids", []int64{}))); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("saving an off review with no routes: status %d, want 422", resp.StatusCode)
	}
}

func TestAgentReviews_ReviewerModelIsNotTheChatModel(t *testing.T) {
	e := newReviewsEnv(t, false)
	review := e.create(e.input())
	cost := func() (preview, saved, block, run string) {
		t.Helper()
		str := func(p *string) string {
			if p == nil {
				return "null"
			}
			return *p
		}
		preview = str(e.preview(e.input()).WorstCaseMonthUSD)
		saved = str(e.firstReview().WorstCaseMonthUSD)
		var settings struct {
			Reviews AgentReviewsDTO `json:"reviews"`
		}
		decode(t, e.admin.get("/api/agent/settings"), &settings)
		block = str(settings.Reviews.WorstCaseRunUSD)
		resp := e.runNow(review.ID)
		code := ""
		if resp.StatusCode != http.StatusAccepted {
			code, _ = refused(t, resp)
		} else {
			resp.Body.Close()
			code = "accepted"
		}
		return preview, saved, block, code
	}
	// A chat model alone gives a review no model: no money, and run now says so.
	e.setModels("fake-1", "")
	if preview, saved, block, run := cost(); preview != "null" || saved != "null" || block != "null" || run != "no_model" {
		t.Errorf("chat only: preview %s, saved %s, block %s, run now %s; want null, null, null and no_model", preview, saved, block, run)
	}
	// Two differently priced models: the reviewer's price counts (fake-2 reserves 1.20, fake-1 0.60), weekly is five runs.
	e.setModels("fake-1", "fake-2")
	if preview, saved, block, run := cost(); preview != "6.00" || saved != "6.00" || block != "1.20" || run != "accepted" {
		t.Errorf("reviewer fake-2: preview %s, saved %s, block %s, run now %s; want 6.00, 6.00, 1.20 and accepted", preview, saved, block, run)
	}
	e.setModels("fake-2", "fake-1")
	if preview, saved, block, _ := cost(); preview != "3.00" || saved != "3.00" || block != "0.60" {
		t.Errorf("reviewer fake-1 with chat fake-2: preview %s, saved %s, block %s; want 3.00, 3.00, 0.60", preview, saved, block)
	}
}

func TestAgentReviews_UpdateKeepsOnlyTheStoredTags(t *testing.T) {
	// The stored scope is what lets an update keep a tag that is gone; with another tag in the request it is refused.
	e := newReviewsEnv(t, false)
	keep, _ := e.st.CreateTag(t.Context(), "Keep", "red", false)
	drop, _ := e.st.CreateTag(t.Context(), "Drop", "red", false)
	review := e.create(e.input(with("scope", []any{map[string]any{"kind": "harbormaster", "tag_id": int64(keep.ID)}, map[string]any{"kind": "harbormaster", "tag_id": int64(drop.ID)}})))
	if err := e.st.DeleteTag(t.Context(), drop.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteTag(t.Context(), keep.ID); err != nil {
		t.Fatal(err)
	}
	// Both stay, but a tag id the review never held does not.
	both := []any{map[string]any{"kind": "harbormaster", "tag_id": int64(keep.ID)}, map[string]any{"kind": "harbormaster", "tag_id": int64(drop.ID)}}
	if resp := e.update(review.ID, e.input(with("version", review.Version), with("scope", both))); resp.StatusCode != http.StatusOK {
		t.Errorf("keeping both deleted tags: status %d, want 200", resp.StatusCode)
	}
}

func TestAgentReviews_ScopeTextMatchesTheAppPatterns(t *testing.T) {
	e := newReviewsEnv(t, false)
	cluster := func(account, region, name string) map[string]any {
		return map[string]any{"kind": "cluster", "cluster_id": model.ClusterKey{AccountID: account, Region: region, Cluster: name}.ID()}
	}
	account := func(id, region string) map[string]any {
		return map[string]any{"kind": "account", "account_id": id, "region": region}
	}
	bad := map[string]map[string]any{
		"cluster: account of 11 digits":    cluster("12345678901", "us-east-1", "core"),
		"cluster: account with a letter":   cluster("12345678901a", "us-east-1", "core"),
		"cluster: region in capitals":      cluster(sandboxAccountID, "US-EAST-1", "core"),
		"cluster: region of 31 characters": cluster(sandboxAccountID, strings.Repeat("a", 31), "core"),
		"cluster: no region":               cluster(sandboxAccountID, "", "core"),
		"cluster: name with a space":       cluster(sandboxAccountID, "us-east-1", "core services"),
		"cluster: name with a dot":         cluster(sandboxAccountID, "us-east-1", "core.services"),
		"cluster: name with a slash":       cluster(sandboxAccountID, "us-east-1", "core/services"),
		"cluster: name of 256 characters":  cluster(sandboxAccountID, "us-east-1", strings.Repeat("c", 256)),
		"cluster: empty name":              cluster(sandboxAccountID, "us-east-1", ""),
		"cluster: name with a newline":     cluster(sandboxAccountID, "us-east-1", "core\nservices"),
		"account: 13 digits":               account("1234567890123", ""),
		"account: region in capitals":      account(sandboxAccountID, "EU-WEST-1"),
		"account: region with a space":     account(sandboxAccountID, "eu west"),
	}
	for name, entry := range bad {
		for _, route := range []string{"create", "preview"} {
			var resp *http.Response
			if route == "create" {
				resp = e.admin.post("/api/agent/reviews", e.input(with("scope", []any{entry})))
			} else {
				resp = e.admin.post("/api/agent/reviews/preview", e.input(with("scope", []any{entry})))
			}
			if code, field := refused(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "invalid" || field != "scope" {
				t.Errorf("%s (%s): status %d code %q field %q, want 422 invalid scope", name, route, resp.StatusCode, code, field)
			}
		}
	}
	if n := len(e.stored()); n != 0 {
		t.Errorf("%d reviews were written", n)
	}
	for name, entry := range map[string]map[string]any{
		"cluster name of 255 characters": cluster(sandboxAccountID, "us-east-1", strings.Repeat("c", 255)),
		"cluster name with - and _":      cluster(sandboxAccountID, "eu-central-1", "Core_services-2"),
		"account with a region":          account(sandboxAccountID, "eu-west-1"),
	} {
		if resp := e.admin.post("/api/agent/reviews/preview", e.input(with("scope", []any{entry}))); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", name, resp.StatusCode)
		}
	}
}
