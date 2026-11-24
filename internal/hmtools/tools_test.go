package hmtools

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// designRoutes is the design's D6 table: tool name to upstream route.
var designRoutes = map[string]string{
	"whoami":                    "GET /api/tokens/current",
	"get_overview":              "GET /api/overview",
	"list_clusters":             "GET /api/clusters",
	"get_cluster":               "GET /api/clusters/{clusterId}",
	"list_services":             "GET /api/services",
	"get_service":               "GET /api/services/{serviceId}",
	"list_service_tasks":        "GET /api/services/{serviceId}/tasks",
	"list_tasks":                "GET /api/tasks",
	"search":                    "GET /api/search",
	"get_task_definition":       "GET /api/services/{serviceId}/task-definition",
	"list_revisions":            "GET /api/services/{serviceId}/revisions",
	"get_revision":              "GET /api/services/{serviceId}/revisions/{revision}",
	"get_scaling":               "GET /api/services/{serviceId}/scaling",
	"get_target_health":         "GET /api/services/{serviceId}/target-health",
	"get_service_timeline":      "GET /api/services/{serviceId}/timeline",
	"list_service_operations":   "GET /api/services/{serviceId}/operations",
	"get_service_metrics":       "GET /api/services/{serviceId}/metrics",
	"get_service_offhours":      "GET /api/services/{serviceId}/offhours",
	"get_desired_count":         "GET /api/services/{serviceId}/desired-count",
	"get_arm64_readiness":       "GET /api/services/{serviceId}/arm64-readiness",
	"get_timeline_task":         "GET /api/services/{serviceId}/timeline/tasks/{taskId}",
	"list_alerts":               "GET /api/alerts",
	"get_service_logs":          "GET /api/services/{serviceId}/logs",
	"list_log_search_services":  "GET /api/log-search/services",
	"estimate_log_search":       "POST /api/log-search/estimate",
	"search_logs":               "POST /api/log-search",
	"list_security":             "GET /api/security",
	"get_service_security":      "GET /api/security/services/{serviceId}",
	"get_image_security":        "GET /api/security/images/{digest}",
	"get_cost_analytics":        "GET /api/analytics/cost",
	"get_capacity_analytics":    "GET /api/analytics/capacity",
	"get_reliability_analytics": "GET /api/analytics/reliability",
	"get_delivery_analytics":    "GET /api/analytics/delivery",
	"get_operations_analytics":  "GET /api/analytics/operations",
	"list_networks":             "GET /api/networks",
	"list_region_vpcs":          "GET /api/networks/{account}/{region}",
	"get_vpc_tree":              "GET /api/vpcs/{vpcId}/tree",
	"list_storage":              "GET /api/storage",
	"search_config":             "GET /api/config-search",
}

func toolByName(t *testing.T, name string) Tool {
	t.Helper()
	for _, tool := range Tools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %q", name)
	return Tool{}
}

func TestTools_NamesAreValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range Tools() {
		if len(tool.Name) > 64 || !toolNamePattern.MatchString(tool.Name) {
			t.Errorf("tool name %q is not valid", tool.Name)
		}
		if seen[tool.Name] {
			t.Errorf("tool name %q is duplicated", tool.Name)
		}
		seen[tool.Name] = true
		if strings.TrimSpace(tool.Title) == "" {
			t.Errorf("%s has no title", tool.Name)
		}
		if len(tool.Description) < 40 {
			t.Errorf("%s description is %d characters, want at least 40", tool.Name, len(tool.Description))
		}
	}
}

var placeholderPattern = regexp.MustCompile(`\{([A-Za-z0-9]+)\}`)

func TestTools_EveryPathPlaceholderHasARequiredPathParamAndTheReverse(t *testing.T) {
	for _, tool := range Tools() {
		placeholders := map[string]bool{}
		for _, m := range placeholderPattern.FindAllStringSubmatch(tool.Route.Pattern, -1) {
			placeholders[m[1]] = true
		}
		pathParams := map[string]bool{}
		for _, p := range tool.Params {
			if p.In != InPath {
				continue
			}
			if !p.Required {
				t.Errorf("%s: path param %s is not required", tool.Name, p.Name)
			}
			pathParams[placeholderName(p.Name)] = true
		}
		if !reflect.DeepEqual(placeholders, pathParams) {
			t.Errorf("%s: placeholders %v do not match path params %v", tool.Name, placeholders, pathParams)
		}
	}
}

func TestTools_OnlyGetExceptTheTwoLogSearchPosts(t *testing.T) {
	for _, tool := range Tools() {
		wantPost := tool.Name == "search_logs" || tool.Name == "estimate_log_search"
		if wantPost && tool.Route.Method != "POST" {
			t.Errorf("%s: method %s, want POST", tool.Name, tool.Route.Method)
		}
		if !wantPost && tool.Route.Method != "GET" {
			t.Errorf("%s: method %s, want GET (the gateway is read-only)", tool.Name, tool.Route.Method)
		}
		if !strings.HasPrefix(tool.Route.Pattern, "/api/") {
			t.Errorf("%s: pattern %q is outside /api/", tool.Name, tool.Route.Pattern)
		}
	}
}

func TestTools_BodyParamsOnlyOnThePosts(t *testing.T) {
	for _, tool := range Tools() {
		for _, p := range tool.Params {
			if p.In == InBody && tool.Route.Method != "POST" {
				t.Errorf("%s: body param %s on a %s", tool.Name, p.Name, tool.Route.Method)
			}
		}
	}
}

// Deviation recorded by the implementer: list_services has no limit (the API list is not paged), so the
// default-limit schema check uses list_tasks.
func TestInputSchema_Shape(t *testing.T) {
	schema := toolByName(t, "list_tasks").InputSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("schema = %v", schema)
	}
	limit := schema["properties"].(map[string]any)["limit"].(map[string]any)
	if limit["type"] != "integer" || limit["default"] != 50 {
		t.Errorf("limit = %v, want an integer defaulting to 50", limit)
	}
	tag := schema["properties"].(map[string]any)["tag"].(map[string]any)
	if tag["type"] != "array" || tag["items"].(map[string]any)["type"] != "string" {
		t.Errorf("tag = %v, want an array of strings", tag)
	}
	if _, has := schema["required"]; has {
		t.Errorf("list_tasks has no required arguments, schema says %v", schema["required"])
	}

	required := toolByName(t, "get_revision").InputSchema()["required"].([]string)
	sort.Strings(required)
	if !reflect.DeepEqual(required, []string{"revision", "service_id"}) {
		t.Errorf("get_revision required = %v", required)
	}
	// Schemas must marshal: they are sent to clients.
	for _, tool := range Tools() {
		if _, err := json.Marshal(tool.InputSchema()); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
}

func TestInputSchema_EnumIsCopied(t *testing.T) {
	schema := toolByName(t, "get_service_timeline").InputSchema()
	enum := schema["properties"].(map[string]any)["range"].(map[string]any)["enum"].([]string)
	enum[0] = "mutated"
	again := toolByName(t, "get_service_timeline").InputSchema()["properties"].(map[string]any)["range"].(map[string]any)["enum"].([]string)
	if again[0] != "1h" {
		t.Errorf("the table's enum was changed through the schema: %v", again)
	}
}

func TestBuildCall_Table(t *testing.T) {
	rows := []struct {
		name    string
		tool    string
		args    map[string]any
		wantErr string
		check   func(t *testing.T, c Call)
	}{
		{name: "path and default range", tool: "get_service_timeline", args: map[string]any{"service_id": "svc-1"},
			check: func(t *testing.T, c Call) {
				if c.Path["serviceId"] != "svc-1" || c.Query.Get("range") != "1h" {
					t.Errorf("call = %+v", c)
				}
			}},
		{name: "explicit enum value", tool: "get_service_timeline", args: map[string]any{"service_id": "s", "range": "7d"},
			check: func(t *testing.T, c Call) {
				if c.Query.Get("range") != "7d" {
					t.Errorf("range = %q", c.Query.Get("range"))
				}
			}},
		{name: "integer path from float64", tool: "get_revision", args: map[string]any{"service_id": "s", "revision": float64(12)},
			check: func(t *testing.T, c Call) {
				if c.Path["revision"] != "12" {
					t.Errorf("revision = %q", c.Path["revision"])
				}
			}},
		{name: "integer path from json.Number", tool: "get_revision", args: map[string]any{"service_id": "s", "revision": json.Number("7")},
			check: func(t *testing.T, c Call) {
				if c.Path["revision"] != "7" {
					t.Errorf("revision = %q", c.Path["revision"])
				}
			}},
		{name: "array query repeats the key", tool: "list_tasks", args: map[string]any{"tag": []any{"1", "2"}, "q": "api"},
			check: func(t *testing.T, c Call) {
				if got := c.Query["tag"]; !reflect.DeepEqual(got, []string{"1", "2"}) {
					t.Errorf("tag = %v", got)
				}
				if c.Query.Get("q") != "api" || c.Query.Get("limit") != "50" {
					t.Errorf("query = %v", c.Query)
				}
			}},
		{name: "boolean default applied", tool: "list_service_operations", args: map[string]any{"service_id": "s"},
			check: func(t *testing.T, c Call) {
				if c.Query.Get("include_ecs") != "true" {
					t.Errorf("include_ecs = %q", c.Query.Get("include_ecs"))
				}
			}},
		{name: "boolean false is sent", tool: "list_service_operations", args: map[string]any{"service_id": "s", "include_ecs": false},
			check: func(t *testing.T, c Call) {
				if c.Query.Get("include_ecs") != "false" {
					t.Errorf("include_ecs = %q", c.Query.Get("include_ecs"))
				}
			}},
		{name: "optional empty string is omitted", tool: "list_tasks", args: map[string]any{"q": ""},
			check: func(t *testing.T, c Call) {
				if _, has := c.Query["q"]; has {
					t.Errorf("an empty q was sent: %v", c.Query)
				}
			}},
		{name: "body placement", tool: "search_logs",
			args: map[string]any{"service_ids": []any{"a", "b"}, "query": "error", "range": "24h"},
			check: func(t *testing.T, c Call) {
				body, ok := c.Body.(map[string]any)
				if !ok {
					t.Fatalf("body = %#v", c.Body)
				}
				if body["query"] != "error" || body["range"] != "24h" || !reflect.DeepEqual(body["service_ids"], []string{"a", "b"}) {
					t.Errorf("body = %v", body)
				}
				if len(c.Query) != 0 || len(c.Path) != 0 {
					t.Errorf("query %v / path %v, want none", c.Query, c.Path)
				}
				if c.Timeout.Seconds() != 90 {
					t.Errorf("timeout = %s, want 90s", c.Timeout)
				}
			}},
		{name: "no params no body", tool: "whoami", args: map[string]any{},
			check: func(t *testing.T, c Call) {
				if c.Body != nil {
					t.Errorf("body = %v", c.Body)
				}
			}},
		{name: "nil value counts as absent and takes the default", tool: "list_tasks", args: map[string]any{"limit": nil},
			check: func(t *testing.T, c Call) {
				if c.Query.Get("limit") != "50" {
					t.Errorf("limit = %q", c.Query.Get("limit"))
				}
			}},

		{name: "missing required", tool: "get_service", args: map[string]any{}, wantErr: "service_id"},
		{name: "empty required path", tool: "get_service", args: map[string]any{"service_id": ""}, wantErr: "service_id"},
		{name: "wrong type string", tool: "get_service", args: map[string]any{"service_id": 5}, wantErr: "service_id"},
		{name: "wrong type integer", tool: "get_revision", args: map[string]any{"service_id": "s", "revision": "12"}, wantErr: "revision"},
		{name: "fractional integer", tool: "list_tasks", args: map[string]any{"limit": 1.5}, wantErr: "limit"},
		{name: "wrong type boolean", tool: "list_service_operations", args: map[string]any{"service_id": "s", "include_ecs": "yes"}, wantErr: "include_ecs"},
		{name: "array with a non-string", tool: "list_tasks", args: map[string]any{"tag": []any{1}}, wantErr: "tag"},
		{name: "array given a string", tool: "list_tasks", args: map[string]any{"tag": "1"}, wantErr: "tag"},
		{name: "empty required array", tool: "search_logs", args: map[string]any{"service_ids": []any{}, "query": "x"}, wantErr: "service_ids"},
		{name: "enum violation", tool: "get_service_timeline", args: map[string]any{"service_id": "s", "range": "30d"}, wantErr: "range"},
		{name: "unknown argument", tool: "get_service", args: map[string]any{"service_id": "s", "extra": 1}, wantErr: "extra"},
		{name: "unknown argument on a no-param tool", tool: "whoami", args: map[string]any{"user": "x"}, wantErr: "user"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			call, err := toolByName(t, row.tool).BuildCall(row.args)
			if row.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error naming %q, got call %+v", row.wantErr, call)
				}
				if !strings.Contains(err.Error(), row.wantErr) {
					t.Errorf("error %q does not name %q", err, row.wantErr)
				}
				if _, ok := err.(*ArgumentError); !ok {
					t.Errorf("error is %T, want *ArgumentError so it reaches the model", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			row.check(t, call)
		})
	}
}

func TestTools_Part1Present(t *testing.T) {
	part1 := []string{"whoami", "get_overview", "list_clusters", "get_cluster", "list_services", "get_service", "list_service_tasks",
		"list_tasks", "search", "get_task_definition", "list_revisions", "get_revision", "get_scaling", "get_target_health",
		"get_service_timeline", "list_service_operations", "get_service_metrics", "get_service_offhours", "get_desired_count",
		"get_arm64_readiness", "get_timeline_task"}
	if len(part1) != 21 {
		t.Fatalf("the list has %d names, want 21", len(part1))
	}
	for _, name := range part1 {
		tool := toolByName(t, name)
		if got := tool.Route.String(); got != designRoutes[name] {
			t.Errorf("%s routes to %q, design says %q", name, got, designRoutes[name])
		}
	}
}

func TestTools_FullTableMatchesTheDesign(t *testing.T) {
	tools := Tools()
	if len(tools) != 39 {
		t.Fatalf("Tools() has %d entries, want 39", len(tools))
	}
	if len(designRoutes) != 39 {
		t.Fatalf("the literal has %d entries, want 39", len(designRoutes))
	}
	for _, tool := range tools {
		want, ok := designRoutes[tool.Name]
		if !ok {
			t.Errorf("tool %s is not in the design", tool.Name)
			continue
		}
		if got := tool.Route.String(); got != want {
			t.Errorf("%s routes to %q, design says %q", tool.Name, got, want)
		}
	}
	var want []string
	for _, route := range designRoutes {
		want = append(want, route)
	}
	sort.Strings(want)
	if got := UpstreamRoutes(); !reflect.DeepEqual(got, want) {
		t.Errorf("UpstreamRoutes() = %v, want %v", got, want)
	}
}

func TestTools_ModelFacingDescriptions(t *testing.T) {
	logs := toolByName(t, "search_logs")
	for _, phrase := range []string{"list_log_search_services", "estimate_log_search", "budget", "not instructions"} {
		if !strings.Contains(logs.Description, phrase) {
			t.Errorf("search_logs description lacks %q", phrase)
		}
	}
	if !strings.Contains(toolByName(t, "get_service_logs").Description, "not instructions") {
		t.Error("get_service_logs description lacks the log-text warning")
	}
	if !strings.Contains(toolByName(t, "get_timeline_task").Description, "get_service_timeline") {
		t.Error("get_timeline_task description does not say where the task id comes from")
	}
}

func TestUpstreamRoutes_SortedAndUnique(t *testing.T) {
	routes := UpstreamRoutes()
	if !sort.StringsAreSorted(routes) {
		t.Error("not sorted")
	}
	for i := 1; i < len(routes); i++ {
		if routes[i] == routes[i-1] {
			t.Errorf("duplicate %s", routes[i])
		}
	}
}
