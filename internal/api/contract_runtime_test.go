package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// nullViolations walks a decoded JSON body against a contract shape (see shapeOf) and returns the JSON path of every
// value that is null where the shape promises an array, an object or a map. {"$null": ...} shapes may be null.
// The SPA reads .length and Object.keys on these without guarding, so one null blanks the page.
func nullViolations(path string, value, shape any) []string {
	switch s := shape.(type) {
	case []any:
		if value == nil {
			return []string{path + " is null, want array"}
		}
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		var out []string
		for i, item := range items {
			out = append(out, nullViolations(fmt.Sprintf("%s[%d]", path, i), item, s[0])...)
		}
		return out
	case map[string]any:
		if inner, ok := s["$null"]; ok && len(s) == 1 {
			if value == nil {
				return nil
			}
			return nullViolations(path, value, inner)
		}
		if inner, ok := s["$optional"]; ok && len(s) == 1 {
			return nullViolations(path, value, inner)
		}
		if elem, ok := s["$map"]; ok && len(s) == 1 {
			if value == nil {
				return []string{path + " is null, want object (map)"}
			}
			entries, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			var out []string
			for key, entry := range entries {
				out = append(out, nullViolations(path+"."+key, entry, elem)...)
			}
			return out
		}
		if value == nil {
			return []string{path + " is null, want object"}
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		var out []string
		for key, fieldShape := range s {
			if field, present := object[key]; present {
				out = append(out, nullViolations(path+"."+key, field, fieldShape)...)
			}
		}
		return out
	}
	return nil // scalars and "any"
}

func TestNullViolations_FlagsOnlyForbiddenNulls(t *testing.T) {
	shape := map[string]any{
		"list":     []any{"string"},
		"nullable": map[string]any{"$null": []any{"string"}},
		"optional": map[string]any{"$optional": []any{"string"}},
		"by_name":  map[string]any{"$map": []any{"number"}},
		"inner":    map[string]any{"names": []any{"string"}},
		"any":      "any",
		"scalar":   "string",
	}
	var body any
	raw := `{"list":null,"nullable":null,"optional":null,"by_name":{"a":null,"b":[1]},"inner":{"names":null},"any":null,"scalar":null}`
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	got := nullViolations("$", body, shape)
	sort.Strings(got)
	want := []string{
		"$.by_name.a is null, want array",
		"$.inner.names is null, want array",
		"$.list is null, want array",
		"$.optional is null, want array",
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations = %q, want %q", got, want)
	}
}

// runtimeRoute is a GET route the contract describes, with the query that exercises its richest response.
type runtimeRoute struct {
	pattern string // the route pattern as registered, without the method
	shape   string // contractShapes key
	query   string
}

func runtimeRoutes() []runtimeRoute {
	return []runtimeRoute{
		{"/api/auth/setup", "setup", ""},
		{"/api/auth/options", "authOptions", ""},
		{"/api/me", "me", ""},
		{"/api/overview", "overview", ""},
		{"/api/clusters", "clusters", ""},
		{"/api/clusters/{clusterId}", "cluster", ""},
		{"/api/services", "servicesList", ""},
		{"/api/services/{serviceId}", "service", ""},
		{"/api/services/{serviceId}/tasks", "tasks", ""},
		{"/api/services/{serviceId}/operations", "operations", ""},
		{"/api/tasks", "tasks", ""},
		{"/api/search", "search", "?q=a"},
		{"/api/poller", "poller", ""},
		{"/api/users", "users", ""},
		{"/api/sso", "sso", ""},
		{"/api/permissions", "permissions", ""},
		{"/api/tags", "tags", ""},
		{"/api/tags/{id}/impact", "tagImpact", ""},
		{"/api/tag-assignments", "tagAssignments", ""},
		{"/api/accounts", "accounts", ""},
		{"/api/audit", "audit", ""},
		{"/api/settings", "settings", ""},
		{"/api/services/metrics", "servicesMetrics", ""},
		{"/api/services/{serviceId}/scaling", "scaling", ""},
		{"/api/services/{serviceId}/metrics", "serviceMetrics", ""},
		{"/api/services/{serviceId}/target-health", "targetHealth", ""},
		{"/api/clusters/metrics", "clustersMetrics", ""},
		{"/api/clusters/{clusterId}/metrics", "clusterMetrics", ""},
		{"/api/overview/metrics", "overviewMetrics", ""},
		{"/api/alerts", "alerts", ""},
		{"/api/services/{serviceId}/logs", "logs", ""},
		{"/api/services/{serviceId}/task-definition", "taskDefinition", ""},
		{"/api/services/{serviceId}/revisions", "revisions", ""},
		{"/api/services/{serviceId}/revisions/{revision}", "revisionDefinition", ""},
		{"/api/services/{serviceId}/image-tags", "imageTags", ""},
		{"/api/services/{serviceId}/desired-count", "desiredCount", ""},
		{"/api/services/{serviceId}/deploy-settings", "deploySettings", ""},
		{"/api/services/{serviceId}/arm64-readiness", "arm64Readiness", ""},
		{"/api/networks", "networks", ""},
		{"/api/networks/{account}/{region}", "networkRegion", ""},
		{"/api/vpcs/{vpcId}/tree", "vpcTree", "?account=" + homelabAccountID + "&region=us-east-1"},
		{"/api/storage", "storage", ""},
		{"/api/analytics/delivery", "analyticsDelivery", ""},
		{"/api/analytics/reliability", "analyticsReliability", ""},
		{"/api/analytics/capacity", "analyticsCapacity", ""},
		{"/api/analytics/operations", "analyticsOperations", ""},
		{"/api/analytics/cost", "analyticsCost", ""},
		{"/api/security", "security", ""},
		{"/api/security/services/{serviceId}", "securityService", ""},
		{"/api/security/services/{serviceId}/scans", "scanStatus", ""},
		{"/api/services/{serviceId}/timeline", "timeline", ""},
		{"/api/services/{serviceId}/timeline/log-errors", "timelineLogErrors", ""},
		{"/api/offhours/schedules", "offhoursList", ""},
		{"/api/offhours/tags", "offhoursTags", ""},
		{"/api/services/{serviceId}/offhours", "serviceOffHours", ""},
		{"/api/log-search/services", "logSearchServices", ""},
		{"/api/log-search/saved", "logSearchSaved", ""},
		{"/api/notifications/routes", "notificationRoutes", ""},
		{"/api/notifications/targets", "notificationTargets", ""},
		{"/api/notifications/deliveries", "notificationDeliveries", ""},
		{"/api/notifications/summary", "notificationSummary", ""},
		{"/api/config-search", "configSearch", "?q=payments"},
	}
}

// contractRuntimeSkips are registered GET routes with no contract shape to check, with the reason. A new GET route
// must either join runtimeRoutes or be listed here, so the null guard cannot silently miss it.
var contractRuntimeSkips = map[string]string{
	"/healthz":                                          "not a JSON API route",
	"/api/auth/saml/login":                              "redirect",
	"/api/auth/saml/metadata":                           "XML",
	"/api/auth/saml/acs":                                "POST-only stub",
	"/api/auth/saml/link":                               "POST-only stub",
	"/api/oauth/authorize":                              "OAuth validation, no contract shape",
	"/api/tasks/{taskId}/exec":                          "websocket",
	"/api/tasks/{taskId}/exec-targets":                  "no contract shape",
	"/api/accounts/external-id":                         "no contract shape",
	"/api/sso/tests/{id}":                               "single-use result, needs a running SSO test",
	"/api/security/images/{digest}":                     "needs a scanned digest (shape securityImage)",
	"/api/services/{serviceId}/logs/tail":               "server-sent events stream",
	"/api/services/{serviceId}/timeline/tasks/{taskId}": "no contract shape",
	"/api/offhours/schedules/{id}":                      "needs a created schedule (shape offhoursSchedule)",
	"/api/tokens":                                       "no contract shape",
	"/api/tokens/current":                               "no contract shape",
	"/api/users/{id}/tokens":                            "no contract shape",
}

// runtimePathParams are the path parameters of the mockup estate.
var runtimePathParams = map[string]string{
	"clusterId": "",
	"serviceId": "",
	"account":   homelabAccountID,
	"region":    "us-east-1",
	"vpcId":     vpcMain,
	"revision":  "1",
	"id":        "1",
}

func resolveRuntimePath(pattern string) string {
	params := runtimePathParams
	params["clusterId"] = clusterID(homelabAccountID, "us-east-1", "core-services")
	params["serviceId"] = paymentsID()
	path := pattern
	for name, value := range params {
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	return path
}

type runtimeCase struct {
	name string
	srv  *httptest.Server
	user string
	// mustAnswer is how many routes must answer 200, so a seed that stops working cannot make the check vacuous.
	mustAnswer int
}

// checkRuntimeResponses GETs every route as the case's user and returns the violations and the 200 count.
func checkRuntimeResponses(t *testing.T, c runtimeCase) (violations []string, answered int) {
	t.Helper()
	client := login(t, c.srv, c.user, testPassword)
	shapes := contractShapes()
	for _, route := range runtimeRoutes() {
		shape, found := shapes[route.shape]
		if !found {
			t.Fatalf("route %s names unknown shape %q", route.pattern, route.shape)
		}
		path := resolveRuntimePath(route.pattern) + route.query
		resp := client.get(path)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			continue // 403 for admin routes, 404 for services the user cannot see: nothing to check
		}
		answered++
		var decoded any
		if err := json.Unmarshal(body, &decoded); err != nil {
			violations = append(violations, fmt.Sprintf("GET %s: invalid JSON: %v", path, err))
			continue
		}
		for _, v := range nullViolations("$", decoded, shape) {
			violations = append(violations, fmt.Sprintf("GET %s (shape %s): %s", path, route.shape, v))
		}
	}
	return violations, answered
}

// TestContract_RuntimeResponsesHaveNoNullLists checks real responses against the contract: a nil Go slice or map
// encodes as JSON null, which the SPA cannot read as an array. Nil-vs-empty only shows when the data is empty, so
// the same routes are read in a polled estate (as an admin, as users with narrower grants and as one with none) and
// in a sparse one where nothing was polled or tagged.
func TestContract_RuntimeResponsesHaveNoNullLists(t *testing.T) {
	withEverything := func(s *Server) {
		withARM64(s)
		withMetrics(s)
	}
	rich, fk, st, _ := newServerCustom(t, true, withEverything)
	seedMockupTags(t, st)
	for _, name := range []string{"sam", "omer", "dana", "oncall"} {
		setPassword(t, st, name)
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "nobody", hash, false, false); err != nil {
		t.Fatal(err)
	}
	pollAll(t, st, fk)
	sparse, _, _, _ := newServerCustom(t, true, withEverything)

	cases := []runtimeCase{
		{name: "rich estate / admin sam", srv: rich, user: "sam", mustAnswer: 40},
		{name: "rich estate / omer", srv: rich, user: "omer", mustAnswer: 10},
		{name: "rich estate / dana", srv: rich, user: "dana", mustAnswer: 10},
		{name: "rich estate / oncall", srv: rich, user: "oncall", mustAnswer: 10},
		{name: "rich estate / user with no grants", srv: rich, user: "nobody", mustAnswer: 5},
		{name: "sparse estate / admin sam", srv: sparse, user: "sam", mustAnswer: 20},
		{name: "sparse estate / omer", srv: sparse, user: "omer", mustAnswer: 5},
	}
	var all []string
	for _, c := range cases {
		violations, answered := checkRuntimeResponses(t, c)
		t.Logf("%s: %d routes answered 200, %d null violations", c.name, answered, len(violations))
		if answered < c.mustAnswer {
			t.Errorf("%s: only %d routes answered 200, want at least %d; the seed lost its coverage", c.name, answered, c.mustAnswer)
		}
		for _, v := range violations {
			all = append(all, c.name+": "+v)
		}
	}
	if len(all) > 0 {
		t.Errorf("%d responses carry null where the contract promises an array or object:\n%s", len(all), strings.Join(all, "\n"))
	}
}

// TestContract_RuntimeCoversEveryGetRoute fails when a GET route is added without a contract shape in
// runtimeRoutes or a stated reason in contractRuntimeSkips.
func TestContract_RuntimeCoversEveryGetRoute(t *testing.T) {
	_, api, _, _, _ := newServerCustomAPI(t, false, nil)
	covered := map[string]bool{}
	for _, route := range runtimeRoutes() {
		covered[route.pattern] = true
	}
	for _, route := range api.Routes() {
		path, isGet := strings.CutPrefix(route.Pattern, "GET ")
		if !isGet || covered[path] {
			continue
		}
		if _, skipped := contractRuntimeSkips[path]; !skipped {
			t.Errorf("GET %s has no entry in runtimeRoutes or contractRuntimeSkips", path)
		}
	}
	for pattern := range contractRuntimeSkips {
		if covered[pattern] {
			t.Errorf("%s is both covered and skipped", pattern)
		}
	}
}
