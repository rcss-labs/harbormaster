package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// evidenceRulesEnv is a server with two readers of the Prod tag: one holding View only and one holding View and Logs.
type evidenceRulesEnv struct {
	*logSearchEnv
	viewOnly, viewAndLogs *apiClient
	admin                 *apiClient
	// args holds the arguments each service-scoped tool needs to answer 200 for the payments service.
	args map[string]any
}

func newEvidenceRulesEnv(t *testing.T) *evidenceRulesEnv {
	t.Helper()
	env := &evidenceRulesEnv{logSearchEnv: newLogSearchEnv(t, nil)}
	hash := mustHash(t)
	tags, err := env.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, actions := range map[string]authz.ActionSet{
		"ev-view-only": authz.ActionSet(authz.View),
		"ev-view-logs": authz.ActionSet(authz.View | authz.Logs),
	} {
		user, err := env.st.CreateUser(t.Context(), name, hash, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range tags {
			if tag.Name == "Prod" {
				if err := env.st.SetPermission(t.Context(), user.ID, tag.ID, actions); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	env.viewOnly = login(t, env.srv, "ev-view-only", testPassword)
	env.viewAndLogs = login(t, env.srv, "ev-view-logs", testPassword)
	env.admin = login(t, env.srv, "sam", testPassword)

	// A task and a revision that really exist, for the two tools that take one.
	var tasks struct {
		Items []struct {
			ShortID  string `json:"short_id"`
			Revision int    `json:"revision"`
		} `json:"items"`
	}
	decode(t, env.admin.get("/api/services/"+paymentsID()+"/tasks"), &tasks)
	if len(tasks.Items) == 0 {
		t.Fatal("the payments service has no tasks in the fake estate")
	}
	env.args = map[string]any{
		"service_id": paymentsID(), "task_id": tasks.Items[0].ShortID, "revision": tasks.Items[0].Revision,
		"q": "payments", "query": "",
	}
	return env
}

// callTool sends the tool's own route, built by hmtools from args, as client, and returns the status and body.
func callTool(t *testing.T, client *apiClient, tool hmtools.Tool, args map[string]any) (int, []byte) {
	t.Helper()
	given := map[string]any{}
	for _, p := range tool.Params {
		if value, ok := args[p.Name]; ok {
			given[p.Name] = value
		}
	}
	if tool.Name == "search_logs" {
		given = map[string]any{"service_ids": []string{paymentsID()}, "range": "1h", "query": logSearchQuery}
	}
	call, err := tool.BuildCall(given)
	if err != nil {
		t.Fatalf("%s: %v", tool.Name, err)
	}
	path := tool.Route.Pattern
	for placeholder, value := range call.Path {
		path = strings.ReplaceAll(path, "{"+placeholder+"}", url.PathEscape(value))
	}
	if len(call.Query) > 0 {
		path += "?" + call.Query.Encode()
	}
	var resp *http.Response
	switch tool.Route.Method {
	case http.MethodGet:
		resp = client.get(path)
	case http.MethodPost:
		resp = client.post(path, call.Body)
	default:
		t.Fatalf("%s: method %s", tool.Name, tool.Route.Method)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// A label is derived from the tool name alone, so it must not be lower than the route's own gate: a view tool must
// answer a reader who holds View only, and a logs tool must refuse that reader and answer one who holds Logs too.
func TestEvidenceRules_ActionMatchesTheRouteGate(t *testing.T) {
	env := newEvidenceRulesEnv(t)
	checked := 0
	for _, tool := range hmtools.Tools() {
		rule, ok := hmtools.EvidenceRuleFor(tool.Name)
		if !ok {
			t.Errorf("%s has no evidence rule", tool.Name)
			continue
		}
		if rule.Kind == hmtools.EvidenceNone {
			continue
		}
		checked++
		switch rule.Action {
		case hmtools.EvidenceView:
			if status, body := callTool(t, env.viewOnly, tool, env.args); status != http.StatusOK {
				t.Errorf("%s is labelled view but a View-only reader got %d: %.200s", tool.Name, status, body)
			}
		case hmtools.EvidenceLogs:
			if status, body := callTool(t, env.viewOnly, tool, env.args); status != http.StatusForbidden {
				t.Errorf("%s is labelled logs but a View-only reader got %d: %.200s", tool.Name, status, body)
			}
		default:
			t.Errorf("%s: unknown action %q", tool.Name, rule.Action)
			continue
		}
		if status, body := callTool(t, env.viewAndLogs, tool, env.args); status != http.StatusOK {
			t.Errorf("%s: a reader with View and Logs got %d: %.200s", tool.Name, status, body)
		}
	}
	if checked != 20 {
		t.Errorf("checked %d tools with an evidence rule, want 20", checked)
	}
}

// Each per-item rule must name an array and an id member that the handler really produces, and the id must be a
// service id Harbormaster can parse.
func TestEvidenceRules_PerItemShapesMatchTheHandlers(t *testing.T) {
	env := newEvidenceRulesEnv(t)
	checked := 0
	for _, tool := range hmtools.Tools() {
		rule, _ := hmtools.EvidenceRuleFor(tool.Name)
		if rule.Kind != hmtools.EvidencePerItem {
			continue
		}
		checked++
		status, body := callTool(t, env.admin, tool, env.args)
		if status != http.StatusOK {
			t.Errorf("%s: status %d: %.200s", tool.Name, status, body)
			continue
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(body, &top); err != nil {
			t.Errorf("%s: answer is not an object: %v", tool.Name, err)
			continue
		}
		var elements []map[string]json.RawMessage
		if err := json.Unmarshal(top[rule.Items], &elements); err != nil {
			t.Errorf("%s: member %q is not an array of objects: %v", tool.Name, rule.Items, err)
			continue
		}
		withService := 0
		for i, element := range elements {
			raw, present := element[rule.IDField]
			if !present {
				t.Errorf("%s: element %d has no %q member", tool.Name, i, rule.IDField)
				continue
			}
			var id string
			if err := json.Unmarshal(raw, &id); err != nil {
				t.Errorf("%s: element %d %q is not a string: %s", tool.Name, i, rule.IDField, raw)
				continue
			}
			if id == "" {
				continue // an account-level alert has no service; the rule drops it
			}
			if _, err := model.ParseServiceID(id); err != nil {
				t.Errorf("%s: element %d id %q: %v", tool.Name, i, id, err)
				continue
			}
			withService++
		}
		if withService == 0 {
			t.Errorf("%s: no element carried a service id (%d elements), so the shape is not proven", tool.Name, len(elements))
		}
		units := hmtools.Units(tool.Name, env.args, string(body))
		if len(units) != withService {
			t.Errorf("%s: %d units, want %d (one per element with a service id)", tool.Name, len(units), withService)
		}
		for _, unit := range units {
			if unit.Action != rule.Action {
				t.Errorf("%s: unit action %q, want %q", tool.Name, unit.Action, rule.Action)
			}
		}
	}
	if checked != 4 {
		t.Errorf("checked %d per-item tools, want 4", checked)
	}
}
