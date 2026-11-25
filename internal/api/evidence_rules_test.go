package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// evidenceRulesEnv is a server with two readers of the Prod tag: one holding View only and one holding View and Logs.
type evidenceRulesEnv struct {
	*logSearchEnv
	viewOnly, viewAndLogs *apiClient
	admin, noGrant        *apiClient
	// args holds the arguments each service-scoped tool needs to answer 200 for the payments service.
	args map[string]any
}

func newEvidenceRulesEnv(t *testing.T) *evidenceRulesEnv {
	t.Helper()
	env := &evidenceRulesEnv{logSearchEnv: newLogSearchEnv(t, nil)}
	env.addReaders(t)
	return env
}

// newNestedEvidenceEnv is the same estate with every array an analytics tool's rule names filled: ARM64 verdicts
// for the Spot and ARM64 advisors, services below desired, 60 days of availability, utilisation history and services
// at their scaling maximum. Without that the arrays are empty and the id members are not proven.
func newNestedEvidenceEnv(t *testing.T) *evidenceRulesEnv {
	t.Helper()
	env := &evidenceRulesEnv{logSearchEnv: &logSearchEnv{}}
	env.srv, env.api, env.fk, env.st, _ = newServerCustomAPI(t, true, func(s *Server) {
		withARM64(s)
		withDailyRetention(s)
		s.LogSearch = &logs.Searcher{Clients: s.Clients, Now: s.Now, PollEvery: 5 * time.Millisecond, Timeout: 3 * time.Second}
	})
	seedMockupTags(t, env.st)
	forceProviders(env.fk, "FARGATE")
	pollAll(t, env.st, env.fk)
	pollCountsAll(t, env.st, env.fk)
	if _, err := env.st.TrackX86FargateImages(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	arm := armEnv{srv: env.srv, st: env.st}
	arm.mark(t, "/payments-api:", "ok", "", true, true, "linux/amd64", "linux/arm64")
	arm.mark(t, "/catalog-api:", "ok", "", true, false)

	below := map[model.ServiceKey]bool{}
	for i, svc := range visibleServices(t, env.st) {
		below[svc.Key] = i%2 == 0
	}
	seedBelowDesired(t, env.st, below)
	seedAvailabilityDays(t, env.st, 1, 60)
	runner := &rollup.Runner{Store: env.st, Clients: env.fk, Now: time.Now, UtilEnabled: true, CountsRetention: 7 * 24 * time.Hour}
	if _, err := runner.RunDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.Pool.Exec(t.Context(), `insert into scaling (account_id, region, cluster_name, service_name, has_target, min_capacity,
		  max_capacity, policies, scheduled_actions, activities, polled_at)
		 select account_id, region, cluster_name, service_name, true, 1, desired, '[]', '[]', '[]', now() from services where coalesce(desired, 0) > 0
		 on conflict (account_id, region, cluster_name, service_name) do update set has_target = true, max_capacity = excluded.max_capacity`); err != nil {
		t.Fatal(err)
	}
	env.addReaders(t)
	return env
}

// addReaders creates the Prod View-only, Prod View and Logs, and Dev-only readers and finds a task and a revision.
func (env *evidenceRulesEnv) addReaders(t *testing.T) {
	t.Helper()
	hash := mustHash(t)
	tags, err := env.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, actions := range map[string]authz.ActionSet{
		"ev-view-only": authz.ActionSet(authz.View),
		"ev-view-logs": authz.ActionSet(authz.View | authz.Logs),
		"ev-no-grant":  authz.ActionSet(authz.View),
	} {
		user, err := env.st.CreateUser(t.Context(), name, hash, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range tags {
			// ev-no-grant reads Dev only, so it holds nothing on the payments service (a Prod service).
			if (tag.Name == "Prod") == (name != "ev-no-grant") {
				if err := env.st.SetPermission(t.Context(), user.ID, tag.ID, actions); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	env.viewOnly = login(t, env.srv, "ev-view-only", testPassword)
	env.viewAndLogs = login(t, env.srv, "ev-view-logs", testPassword)
	env.noGrant = login(t, env.srv, "ev-no-grant", testPassword)
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
	if checked != 23 {
		t.Errorf("checked %d tools with an evidence rule, want 23", checked)
	}
}

// Each per-item rule must name an array and an id member that the handler really produces, and the id must be a
// service id Harbormaster can parse.
func TestEvidenceRules_PerItemShapesMatchTheHandlers(t *testing.T) {
	env := newNestedEvidenceEnv(t)
	perItem, nested, arrays := 0, 0, 0
	for _, tool := range hmtools.Tools() {
		rule, _ := hmtools.EvidenceRuleFor(tool.Name)
		if rule.Kind != hmtools.EvidencePerItem && rule.Kind != hmtools.EvidencePerItemNested {
			continue
		}
		status, body := callTool(t, env.admin, tool, env.args)
		if status != http.StatusOK {
			t.Errorf("%s: status %d: %.200s", tool.Name, status, body)
			continue
		}
		wanted := []hmtools.ItemArray{{Path: []string{rule.Items}, IDField: rule.IDField}}
		if rule.Kind == hmtools.EvidencePerItemNested {
			nested++
			wanted = rule.Arrays
		} else {
			perItem++
		}
		withService := 0
		for _, array := range wanted {
			arrays++
			elements, err := elementsAt(body, array.Path)
			if err != nil {
				t.Errorf("%s: %v", tool.Name, err)
				continue
			}
			if rule.Kind == hmtools.EvidencePerItemNested && len(elements) == 0 {
				t.Errorf("%s: %v is empty in the seeded estate, so its id member is not proven", tool.Name, array.Path)
			}
			for i, element := range elements {
				raw, present := element[array.IDField]
				if !present {
					t.Errorf("%s: %v element %d has no %q member", tool.Name, array.Path, i, array.IDField)
					continue
				}
				var id string
				if err := json.Unmarshal(raw, &id); err != nil {
					t.Errorf("%s: %v element %d %q is not a string: %s", tool.Name, array.Path, i, array.IDField, raw)
					continue
				}
				if id == "" {
					continue // an account-level alert has no service; the rule drops it
				}
				if _, err := model.ParseServiceID(id); err != nil {
					t.Errorf("%s: %v element %d id %q: %v", tool.Name, array.Path, i, id, err)
					continue
				}
				withService++
			}
		}
		if withService == 0 {
			t.Errorf("%s: no element carried a service id, so the shape is not proven", tool.Name)
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
	if perItem != 4 || nested != 3 || arrays != 11 {
		t.Errorf("checked %d per-item tools, %d nested tools and %d arrays, want 4, 3 and 11", perItem, nested, arrays)
	}
}

// elementsAt follows path through the members of an answer and returns the objects of the array it ends at. A step
// that is missing, null or not an object, or an end that is not an array of objects, is an error: the rule names a
// path the handler does not produce.
func elementsAt(body []byte, path []string) ([]map[string]json.RawMessage, error) {
	current := json.RawMessage(body)
	for _, step := range path {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(current, &object); err != nil || object == nil {
			return nil, fmt.Errorf("path %v: the value before %q is not an object", path, step)
		}
		next, present := object[step]
		if !present {
			return nil, fmt.Errorf("path %v: no member %q", path, step)
		}
		current = next
	}
	var elements []map[string]json.RawMessage
	if err := json.Unmarshal(current, &elements); err != nil || elements == nil {
		return nil, fmt.Errorf("path %v does not end at an array of objects: %.80s", path, current)
	}
	return elements, nil
}

func unitIDs(units []hmtools.Unit) []string {
	var ids []string
	for _, unit := range units {
		ids = append(ids, unit.ServiceID)
	}
	return ids
}

// A reader with no grant on a service must get nothing citable about it: every service-scoped tool answers 404 and
// no per-item answer holds it. A reader of Prod only must get units for Prod services only.
func TestEvidenceRules_AReaderOnlyGetsEvidenceForTheirOwnServices(t *testing.T) {
	env := newEvidenceRulesEnv(t)
	adminIDs := map[string]bool{}
	for _, tool := range hmtools.Tools() {
		rule, _ := hmtools.EvidenceRuleFor(tool.Name)
		switch rule.Kind {
		case hmtools.EvidenceServiceArg:
			if status, body := callTool(t, env.noGrant, tool, env.args); status != http.StatusNotFound {
				t.Errorf("%s: a reader with no grant got %d: %.200s", tool.Name, status, body)
			}
		case hmtools.EvidencePerItem:
			status, body := callTool(t, env.admin, tool, env.args)
			if status != http.StatusOK {
				t.Fatalf("%s: admin got %d", tool.Name, status)
			}
			for _, id := range unitIDs(hmtools.Units(tool.Name, env.args, string(body))) {
				adminIDs[tool.Name+" "+id] = true
			}
			// The no-grant reader holds View on Dev only: nothing about the Prod payments service may reach it.
			status, body = callTool(t, env.noGrant, tool, env.args)
			switch {
			case tool.Name == "search_logs" && status == http.StatusNotFound:
			case status != http.StatusOK:
				t.Errorf("%s: a reader with no grant on the service got %d: %.200s", tool.Name, status, body)
			default:
				for _, id := range unitIDs(hmtools.Units(tool.Name, env.args, string(body))) {
					if id == paymentsID() || !adminIDs[tool.Name+" "+id] {
						t.Errorf("%s: a reader with no grant on the service got a unit for %s", tool.Name, id)
					}
				}
			}
			reader := env.viewOnly
			if rule.Action == hmtools.EvidenceLogs {
				reader = env.viewAndLogs
			}
			status, body = callTool(t, reader, tool, env.args)
			if status != http.StatusOK {
				t.Errorf("%s: the Prod reader got %d: %.200s", tool.Name, status, body)
				continue
			}
			readerIDs := unitIDs(hmtools.Units(tool.Name, env.args, string(body)))
			if len(readerIDs) == 0 {
				t.Errorf("%s: the Prod reader got no units", tool.Name)
			}
			for _, id := range readerIDs {
				if id == stagingWebID() || (!adminIDs[tool.Name+" "+id] && tool.Name != "search_logs") {
					t.Errorf("%s: the Prod reader got a unit for %s, which they may not read", tool.Name, id)
				}
				if tool.Name == "search_logs" && id != paymentsID() {
					t.Errorf("search_logs: unit for %s, want only the requested service", id)
				}
			}
			if tool.Name == "list_services" {
				adminCount := 0
				for key := range adminIDs {
					if strings.HasPrefix(key, "list_services ") {
						adminCount++
					}
				}
				if len(readerIDs) >= adminCount {
					t.Errorf("list_services: the Prod reader got %d units, admin %d: no narrowing", len(readerIDs), adminCount)
				}
			}
		}
	}
}

// Units reads the text the model is shown, which has been through the secret scrubber. The ids in it must survive
// that, or no evidence could ever be cited. This stays red until the scrubber keeps Harbormaster's own service ids
// (branch agent/ids); it is its own test so the failure is read for what it is.
func TestEvidenceRules_UnitsKeepTheirIDsThroughTheScrubber(t *testing.T) {
	env := newNestedEvidenceEnv(t)
	checked := 0
	for _, tool := range hmtools.Tools() {
		rule, _ := hmtools.EvidenceRuleFor(tool.Name)
		if rule.Kind != hmtools.EvidencePerItem && rule.Kind != hmtools.EvidencePerItemNested {
			continue
		}
		checked++
		status, body := callTool(t, env.admin, tool, env.args)
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", tool.Name, status)
		}
		want := unitIDs(hmtools.Units(tool.Name, env.args, string(body)))
		if len(want) == 0 {
			t.Fatalf("%s: the handler's own answer gave no units", tool.Name)
		}
		for _, id := range want {
			if _, err := model.ParseServiceID(id); err != nil {
				t.Fatalf("%s: handler id %q does not parse: %v", tool.Name, id, err)
			}
		}
		got := unitIDs(hmtools.Units(tool.Name, env.args, secretshape.ScrubText(string(body))))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: after scrubbing %d units with ids %.300q, want the handler's %d ids %.300q", tool.Name, len(got), got, len(want), want)
		}
	}
	if checked != 7 {
		t.Errorf("checked %d per-item and nested tools, want 7", checked)
	}
}

// Analytics rows are filtered by the handler to the caller's services; the units made from them must name those
// services and no others, and a reader with fewer tags must get fewer of them.
func TestEvidenceRules_NestedRowsOnlyNameServicesTheCallerCanView(t *testing.T) {
	env := newNestedEvidenceEnv(t)
	idsOf := func(client *apiClient, tool hmtools.Tool) []string {
		status, body := callTool(t, client, tool, env.args)
		if status != http.StatusOK {
			t.Fatalf("%s: status %d: %.200s", tool.Name, status, body)
		}
		return unitIDs(hmtools.Units(tool.Name, env.args, string(body)))
	}
	listServices := func(client *apiClient) map[string]bool {
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		decode(t, client.get("/api/services"), &list)
		visible := map[string]bool{}
		for _, item := range list.Items {
			visible[item.ID] = true
		}
		return visible
	}
	adminVisible, prodVisible, devVisible := listServices(env.admin), listServices(env.viewOnly), listServices(env.noGrant)
	if len(prodVisible) == 0 || len(devVisible) == 0 || len(prodVisible) >= len(adminVisible) || len(devVisible) >= len(adminVisible) {
		t.Fatalf("visible services: admin %d, Prod reader %d, Dev reader %d; want two strict, non-empty subsets", len(adminVisible), len(prodVisible), len(devVisible))
	}
	checked := 0
	for _, tool := range hmtools.Tools() {
		if rule, _ := hmtools.EvidenceRuleFor(tool.Name); rule.Kind != hmtools.EvidencePerItemNested {
			continue
		}
		checked++
		adminIDs := idsOf(env.admin, tool)
		for _, id := range adminIDs {
			if !adminVisible[id] {
				t.Errorf("%s: admin unit names %s, which the admin's service list lacks", tool.Name, id)
			}
		}
		for name, reader := range map[string]struct {
			client  *apiClient
			visible map[string]bool
		}{"Prod reader": {env.viewOnly, prodVisible}, "Dev reader": {env.noGrant, devVisible}} {
			got := idsOf(reader.client, tool)
			for _, id := range got {
				if !reader.visible[id] {
					t.Errorf("%s: the %s got a unit for %s, which they cannot view", tool.Name, name, id)
				}
			}
			if len(got) >= len(adminIDs) {
				t.Errorf("%s: the %s got %d units, admin %d: no narrowing", tool.Name, name, len(got), len(adminIDs))
			}
		}
		if len(idsOf(env.viewOnly, tool)) == 0 {
			t.Errorf("%s: the Prod reader got no units, so the filter is not proven", tool.Name)
		}
	}
	if checked != 3 {
		t.Errorf("checked %d nested tools, want 3", checked)
	}
}
