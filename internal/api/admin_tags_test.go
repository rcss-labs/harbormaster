package api

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func authzTag(id int64) authz.TagID { return authz.TagID(id) }

type testTagItem struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Color             string `json:"color"`
	Builtin           bool   `json:"builtin"`
	Clusters          int    `json:"clusters"`
	ServicesDirect    int    `json:"services_direct"`
	ServicesEffective int    `json:"services_effective"`
	Users             []struct {
		Username string `json:"username"`
		Preset   string `json:"preset"`
	} `json:"users"`
}

type testAssignments struct {
	Clusters []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		AccountName string `json:"account_name"`
		Region      string `json:"region"`
		TagID       *int64 `json:"tag_id"`
		Effective   TagDTO `json:"effective"`
		Services    []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			TagID     *int64 `json:"tag_id"`
			Effective TagDTO `json:"effective"`
		} `json:"services"`
	} `json:"clusters"`
}

func tagByName(t *testing.T, c *apiClient, name string) testTagItem {
	t.Helper()
	var out struct {
		Items []testTagItem `json:"items"`
	}
	decode(t, c.get("/api/tags"), &out)
	for _, it := range out.Items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("tag %q not in %+v", name, out.Items)
	return testTagItem{}
}

func TestTags_AdminOnlyAndList(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	omer := login(t, srv, "omer", testPassword)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/tags"}, {"POST", "/api/tags"}, {"PATCH", "/api/tags/1"}, {"DELETE", "/api/tags/1"},
		{"GET", "/api/tags/1/impact"}, {"GET", "/api/tag-assignments"},
		{"PUT", "/api/tag-assignments/clusters/x"}, {"PUT", "/api/tag-assignments/services/x"},
	} {
		resp := omer.do(tc.method, tc.path, map[string]any{}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	pavel := login(t, srv, "pavel", testPassword)
	var out struct {
		Items []testTagItem `json:"items"`
	}
	decode(t, pavel.get("/api/tags"), &out)
	if len(out.Items) != 3 || out.Items[0].Name != "Dev" || out.Items[1].Name != "Prod" || out.Items[2].Name != "Staging" {
		t.Fatalf("items = %+v, want Dev, Prod, Staging", out.Items)
	}
	prod := out.Items[1]
	if prod.Clusters != 1 || prod.ServicesDirect != 0 || prod.ServicesEffective == 0 || prod.Color != "red" || prod.Builtin {
		t.Fatalf("prod = %+v", prod)
	}
	if len(prod.Users) != 2 || prod.Users[0].Username != "dana" || prod.Users[0].Preset != "Custom" || prod.Users[1].Username != "oncall" {
		t.Fatalf("prod users = %+v", prod.Users)
	}
	if !out.Items[0].Builtin {
		t.Fatalf("Dev not builtin")
	}
}

func TestTags_CreateValidateConflict(t *testing.T) {
	srv, _, st := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	for _, name := range []string{"", "1abc", "has!bang", "abcdefghijklmnopqrstuvwxy"} {
		resp := pavel.post("/api/tags", map[string]any{"name": name, "color": "red"})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%q: status %d", name, resp.StatusCode)
		}
		if _, msg := errorCode(t, resp); msg != "Use 1–24 letters, digits, spaces, hyphens or underscores, starting with a letter." {
			t.Fatalf("%q: message %q", name, msg)
		}
	}
	resp := pavel.post("/api/tags", map[string]any{"name": "QA", "color": "pink"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad colour status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Pick one of the tag colours." {
		t.Fatalf("colour message %q", msg)
	}
	resp = pavel.post("/api/tags", map[string]any{"name": "  QA  ", "color": "violet"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var created testTagItem
	decode(t, resp, &created)
	if created.Name != "QA" || created.Color != "violet" || created.Builtin || created.ID == 0 || created.Users == nil {
		t.Fatalf("created = %+v", created)
	}
	if a := lastAudit(t, st); a.Action != "tag_create" || a.Summary != "Created tag QA." {
		t.Fatalf("audit = %+v", a)
	}
	resp = pavel.post("/api/tags", map[string]any{"name": "qa", "color": "red"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "A tag called qa already exists." {
		t.Fatalf("conflict message %q", msg)
	}
}

func TestTags_UpdateRenameAndRecolour(t *testing.T) {
	srv, _, st := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	var qa testTagItem
	decode(t, pavel.post("/api/tags", map[string]any{"name": "QA", "color": "violet"}), &qa)
	tagPath := "/api/tags/" + itoa(qa.ID)

	resp := pavel.do("PATCH", tagPath, map[string]any{"name": "QA2", "color": "violet"}, nil)
	var renamed testTagItem
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status %d", resp.StatusCode)
	}
	decode(t, resp, &renamed)
	if renamed.Name != "QA2" || renamed.ID != qa.ID {
		t.Fatalf("renamed = %+v", renamed)
	}
	if a := lastAudit(t, st); a.Action != "tag_update" || a.Summary != "Renamed tag QA to QA2." {
		t.Fatalf("audit = %+v", a)
	}
	pavel.do("PATCH", tagPath, map[string]any{"name": "QA2", "color": "green"}, nil).Body.Close()
	if a := lastAudit(t, st); a.Summary != "Changed the colour of tag QA2." {
		t.Fatalf("audit = %+v", a)
	}
	resp = pavel.do("PATCH", tagPath, map[string]any{"name": "dev", "color": "green"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("rename onto Dev status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "A tag called dev already exists." {
		t.Fatalf("message %q", msg)
	}
	resp = pavel.do("PATCH", "/api/tags/9999", map[string]any{"name": "Zed", "color": "green"}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown tag status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTags_DevCannotBeRenamedOrDeleted(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pavel := login(t, srv, "pavel", testPassword)
	dev := tagByName(t, pavel, "Dev")
	path := "/api/tags/" + itoa(dev.ID)

	resp := pavel.do("PATCH", path, map[string]any{"name": "Development", "color": "neutral"}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rename status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Dev is the default tag." {
		t.Fatalf("rename message %q", msg)
	}
	resp = pavel.do("PATCH", path, map[string]any{"name": "Dev", "color": "blue"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recolour status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := tagByName(t, pavel, "Dev").Color; got != "blue" {
		t.Fatalf("Dev colour = %q", got)
	}
	resp = pavel.do("DELETE", path, nil, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Dev is the default tag." {
		t.Fatalf("delete message %q", msg)
	}
	resp = pavel.do("DELETE", "/api/tags/9999", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTags_ImpactLists(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	pavel := login(t, srv, "pavel", testPassword)
	prod := tagByName(t, pavel, "Prod")
	web := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "web-frontend"}
	tagID := authzTag(prod.ID)
	if err := st.SetServiceTag(t.Context(), web, &tagID); err != nil {
		t.Fatal(err)
	}
	var impact struct {
		Clusters []struct{ ID, Name string } `json:"clusters"`
		Services []struct{ ID, Name string } `json:"services"`
		Users    []string                    `json:"users"`
	}
	decode(t, pavel.get("/api/tags/"+itoa(prod.ID)+"/impact"), &impact)
	if len(impact.Clusters) != 1 || impact.Clusters[0].Name != "core-services" || impact.Clusters[0].ID != web.ClusterKey.ID() {
		t.Fatalf("clusters = %+v", impact.Clusters)
	}
	if len(impact.Services) != 1 || impact.Services[0].Name != "web-frontend" || impact.Services[0].ID != web.ID() {
		t.Fatalf("services = %+v", impact.Services)
	}
	if !reflect.DeepEqual(impact.Users, []string{"dana", "oncall"}) {
		t.Fatalf("users = %v", impact.Users)
	}
	resp := pavel.get("/api/tags/9999/impact")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown impact status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTags_DeleteCascadesVisibility(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	pavel := login(t, srv, "pavel", testPassword)
	omer := login(t, srv, "omer", testPassword)
	staging := tagByName(t, pavel, "Staging")

	resp := pavel.do("DELETE", "/api/tags/"+itoa(staging.ID), nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if a := lastAudit(t, st); a.Action != "tag_delete" || a.Summary != "Deleted tag Staging." {
		t.Fatalf("audit = %+v", a)
	}
	var clusters struct {
		Items []struct {
			Name string `json:"name"`
			Tag  TagDTO `json:"tag"`
		} `json:"items"`
	}
	decode(t, omer.get("/api/clusters"), &clusters)
	found := false
	for _, c := range clusters.Items {
		if c.Name == "staging-web" {
			found = true
			if c.Tag.Name != "Dev" {
				t.Fatalf("staging-web tag = %+v, want Dev", c.Tag)
			}
		}
	}
	if !found {
		t.Fatalf("omer lost staging-web: %+v", clusters.Items)
	}
}

func TestAssignments_ClusterChangeWritesOplogForEachService(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	pavel := login(t, srv, "pavel", testPassword)
	staging := tagByName(t, pavel, "Staging")

	var as testAssignments
	decode(t, pavel.get("/api/tag-assignments?account="+homelabAccountID+"&region=us-east-1"), &as)
	var core int = -1
	for i, c := range as.Clusters {
		if c.Name == "core-services" {
			core = i
		}
		if c.AccountName != "homelab-admin" || c.Region != "us-east-1" {
			t.Fatalf("filter leaked cluster %+v", c)
		}
	}
	if core < 0 {
		t.Fatalf("core-services missing: %+v", as.Clusters)
	}
	cs := as.Clusters[core]
	if cs.TagID == nil || cs.Effective.Name != "Prod" || cs.Effective.Inherited || len(cs.Services) == 0 {
		t.Fatalf("core-services = %+v", cs)
	}
	for _, s := range cs.Services {
		if s.TagID != nil || s.Effective.Name != "Prod" || !s.Effective.Inherited {
			t.Fatalf("service %+v should inherit Prod", s)
		}
	}

	resp := pavel.do("PUT", "/api/tag-assignments/clusters/"+cs.ID, map[string]any{"tag_id": staging.ID}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("assign status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if a := lastAudit(t, st); a.Action != "tag_assign" || a.Summary != "Tagged cluster core-services as Staging (was Prod)." {
		t.Fatalf("audit = %+v", a)
	}
	for _, s := range cs.Services {
		key, err := model.ParseServiceID(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		ops, err := st.ListOperations(t.Context(), key, false, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) == 0 || ops[0].Text != "Changed tag of cluster core-services from Prod to Staging." || ops[0].Actor != "pavel" || ops[0].Source != "harbormaster" {
			t.Fatalf("ops for %s = %+v", s.Name, ops)
		}
	}

	// Same tag again: no-op, no new audit row.
	before := lastAudit(t, st).ID
	resp = pavel.do("PUT", "/api/tag-assignments/clusters/"+cs.ID, map[string]any{"tag_id": staging.ID}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("repeat status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if lastAudit(t, st).ID != before {
		t.Fatal("no-change assignment wrote an audit row")
	}

	// null = untag, falls back to Dev.
	resp = pavel.do("PUT", "/api/tag-assignments/clusters/"+cs.ID, map[string]any{"tag_id": nil}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("untag status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if a := lastAudit(t, st); a.Summary != "Tagged cluster core-services as Dev (was Staging)." {
		t.Fatalf("audit = %+v", a)
	}
	if _, ok, _ := st.ClusterTag(t.Context(), mustClusterKey(t, cs.ID)); ok {
		t.Fatal("cluster still tagged after null")
	}

	// Validation.
	resp = pavel.do("PUT", "/api/tag-assignments/clusters/"+cs.ID, map[string]any{}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing tag_id status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = pavel.do("PUT", "/api/tag-assignments/clusters/"+cs.ID, map[string]any{"tag_id": 9999}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown tag status %d", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "Tag not found." {
		t.Fatalf("unknown tag message %q", msg)
	}
	resp = pavel.do("PUT", "/api/tag-assignments/clusters/"+model.ClusterKey{AccountID: "1", Region: "r", Cluster: "nope"}.ID(), map[string]any{"tag_id": staging.ID}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown cluster status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAssignments_ServiceInheritIsNull(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	pavel := login(t, srv, "pavel", testPassword)
	staging := tagByName(t, pavel, "Staging")
	web := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "web-frontend"}
	path := "/api/tag-assignments/services/" + web.ID()

	resp := pavel.do("PUT", path, map[string]any{"tag_id": staging.ID}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("assign status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if a := lastAudit(t, st); a.Action != "tag_assign" || a.Summary != "Tagged service web-frontend as Staging (was inherit (Prod))." {
		t.Fatalf("audit = %+v", a)
	}
	ops, err := st.ListOperations(t.Context(), web, false, nil, 10)
	if err != nil || len(ops) == 0 || ops[0].Text != "Tagged service web-frontend as Staging (was inherit (Prod))." || ops[0].Actor != "pavel" {
		t.Fatalf("ops = %+v err %v", ops, err)
	}
	var as testAssignments
	decode(t, pavel.get("/api/tag-assignments"), &as)
	for _, c := range as.Clusters {
		for _, s := range c.Services {
			if s.ID == web.ID() && (s.TagID == nil || *s.TagID != staging.ID || s.Effective.Name != "Staging" || s.Effective.Inherited) {
				t.Fatalf("web-frontend = %+v", s)
			}
		}
	}

	resp = pavel.do("PUT", path, map[string]any{"tag_id": nil}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("inherit status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if a := lastAudit(t, st); a.Summary != "Tagged service web-frontend as inherit (Prod) (was Staging)." {
		t.Fatalf("audit = %+v", a)
	}
	if _, ok, _ := st.ServiceTag(t.Context(), web); ok {
		t.Fatal("service still has a direct tag")
	}
	before := lastAudit(t, st).ID
	pavel.do("PUT", path, map[string]any{"tag_id": nil}, nil).Body.Close()
	if lastAudit(t, st).ID != before {
		t.Fatal("no-change inherit wrote an audit row")
	}
	resp = pavel.do("PUT", path, map[string]any{}, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing tag_id status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = pavel.do("PUT", "/api/tag-assignments/services/"+model.ServiceKey{ClusterKey: web.ClusterKey, Service: "nope"}.ID(), map[string]any{"tag_id": nil}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown service status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func mustClusterKey(t *testing.T, id string) model.ClusterKey {
	t.Helper()
	k, err := model.ParseClusterID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
