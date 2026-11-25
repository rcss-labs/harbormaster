package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func (e *notifyEnv) devTag() store.Tag {
	e.t.Helper()
	tags, err := e.st.ListTags(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Builtin {
			return tag
		}
	}
	e.t.Fatal("no built-in tag")
	return store.Tag{}
}

// seedTaggedServices makes the services ACTIVE, each with the given value of the AWS tag "env" (none when empty).
func (e *notifyEnv) seedTaggedServices(envs map[model.ServiceKey]string) {
	e.t.Helper()
	var rows []model.Service
	for k, env := range envs {
		raw := `{"Tags":[]}`
		if env != "" {
			raw = `{"Tags":[{"Key":"env","Value":"` + env + `"}]}`
		}
		rows = append(rows, model.Service{Key: k, Status: "ACTIVE", Raw: []byte(raw), PolledAt: time.Now()})
	}
	if _, err := storetest.Seed(e.st).UpsertServices(e.t.Context(), rows); err != nil {
		e.t.Fatal(err)
	}
}

func investigateInput(e *notifyEnv, name string, on any) map[string]any {
	return e.webhookInput(name, func(in map[string]any) {
		if on != nil {
			in["investigate"] = on
		}
	})
}

func routeAuditInvestigate(t *testing.T, e *notifyEnv, action string) (before, after any) {
	t.Helper()
	rows := settingsAuditRows(t, e.st, action)
	if len(rows) == 0 {
		t.Fatalf("no %s audit row", action)
	}
	d := rows[0].Details
	b, _ := d["before"].(map[string]any)
	a, _ := d["after"].(map[string]any)
	return b["investigate"], a["investigate"]
}

func TestRoute_InvestigateRoundTripsAndIsAudited(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{agent: true})
	created := e.create(investigateInput(e, "diagnose", true))
	if !created.Investigate {
		t.Fatalf("created = %+v, want investigate true", created)
	}
	if routes := e.list(); len(routes.Items) != 1 || !routes.Items[0].Investigate {
		t.Fatalf("list = %+v", routes.Items)
	}
	if stored, err := e.st.GetNotificationRoute(t.Context(), created.ID); err != nil || !stored.Investigate {
		t.Fatalf("stored = %+v, %v", stored.Investigate, err)
	}
	if before, after := routeAuditInvestigate(t, e, "notification_route_create"); before != nil || after != true {
		t.Errorf("create audit investigate = %v -> %v, want absent -> true", before, after)
	}

	kicks := e.kicks.Load()
	update := investigateInput(e, "diagnose", false)
	update["version"] = created.Version
	resp := e.put(created.ID, update)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var updated NotificationRouteDTO
	decode(t, resp, &updated)
	if updated.Investigate {
		t.Errorf("updated = %+v, want investigate false", updated)
	}
	if before, after := routeAuditInvestigate(t, e, "notification_route_update"); before != true || after != false {
		t.Errorf("update audit investigate = %v -> %v, want true -> false", before, after)
	}
	if e.kicks.Load() == kicks {
		t.Error("a route change did not kick notify")
	}
}

func TestRoute_InvestigateRefusedWhenAgentOff(t *testing.T) {
	e := newNotifyEnv(t)
	resp := e.admin.post("/api/notifications/routes", investigateInput(e, "diagnose", true))
	if code, _ := errorCode(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "agent_disabled" {
		t.Fatalf("create with the switch on and the agent off: status %d code %q, want 422 agent_disabled", resp.StatusCode, code)
	}
	if routes := e.list(); len(routes.Items) != 0 {
		t.Fatalf("a refused create stored %d routes", len(routes.Items))
	}

	// Off is fine; turning it on afterwards is refused and changes nothing.
	created := e.create(investigateInput(e, "plain", false))
	update := investigateInput(e, "plain", true)
	update["version"] = created.Version
	resp = e.put(created.ID, update)
	if code, _ := errorCode(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || code != "agent_disabled" {
		t.Fatalf("update to on with the agent off: status %d code %q, want 422 agent_disabled", resp.StatusCode, code)
	}
	if stored, _ := e.st.GetNotificationRoute(t.Context(), created.ID); stored.Investigate || stored.Version != created.Version {
		t.Errorf("a refused update changed the route: %+v", stored)
	}

	// A route that already has it on (the agent was switched off later) can still be saved with it on.
	stored, _ := e.st.GetNotificationRoute(t.Context(), created.ID)
	stored.Investigate = true
	if _, err := e.st.UpdateNotificationRoute(t.Context(), stored, 0); err != nil {
		t.Fatal(err)
	}
	stored, _ = e.st.GetNotificationRoute(t.Context(), created.ID)
	update = investigateInput(e, "renamed", true)
	update["version"] = stored.Version
	if resp = e.put(created.ID, update); resp.StatusCode != http.StatusOK {
		t.Errorf("saving a route that already investigates: status %d, want 200", resp.StatusCode)
	}
}

func TestRoute_OlderClientOmittingTheMemberKeepsTheStoredValue(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{agent: true})
	created := e.create(investigateInput(e, "diagnose", true))
	update := investigateInput(e, "diagnose", nil) // no investigate member at all
	update["name"] = "renamed"
	update["version"] = created.Version
	resp := e.put(created.ID, update)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var updated NotificationRouteDTO
	decode(t, resp, &updated)
	if !updated.Investigate || updated.Name != "renamed" {
		t.Errorf("updated = %+v, want the name changed and investigate still true", updated)
	}
	if before, after := routeAuditInvestigate(t, e, "notification_route_update"); before != true || after != true {
		t.Errorf("audit investigate = %v -> %v, want true -> true", before, after)
	}
	// A create that omits it is off.
	if plain := e.create(investigateInput(e, "second", nil)); plain.Investigate {
		t.Errorf("a create without the member = %+v, want off", plain)
	}
}

func previewAgent(t *testing.T, e *notifyEnv, in map[string]any) *NotificationPreviewAgentDTO {
	t.Helper()
	return e.preview(in).Agent
}

func TestPreview_CountsServicesTheIdentityCannotSee(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{agent: true})
	e.seedTaggedServices(map[model.ServiceKey]string{nxKey: "prod", nyKey: "", nzKey: "prod"})
	prod, err := e.st.CreateTag(t.Context(), "Prod", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetClusterTag(t.Context(), nzKey.ClusterKey, prod.ID); err != nil {
		t.Fatal(err)
	}
	// The identity may View what carries the built-in tag, not Prod.
	if resp := putIdentity(e.admin, []map[string]any{{"tag_id": int64(e.devTag().ID), "logs": false}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT identity: status %d", resp.StatusCode)
	}

	everything := previewAgent(t, e, investigateInput(e, "draft", true))
	if everything == nil || everything.ServicesTotal != 3 || everything.ServicesUnseen != 1 {
		t.Errorf("empty scope = %+v, want 3 services and 1 unseen", everything)
	}
	byService := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{serviceScope(nxKey), serviceScope(nzKey)}
	}))
	if byService == nil || byService.ServicesTotal != 2 || byService.ServicesUnseen != 1 {
		t.Errorf("service entries = %+v, want 2 and 1", byService)
	}
	byCluster := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{clusterScope(nxKey.ClusterKey), clusterScope(nzKey.ClusterKey)}
	}))
	if byCluster == nil || byCluster.ServicesTotal != 3 || byCluster.ServicesUnseen != 1 {
		t.Errorf("cluster entries = %+v, want 3 and 1", byCluster)
	}
	byAccount := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{accountScope(sandboxAccountID)}
	}))
	if byAccount == nil || byAccount.ServicesTotal != 3 || byAccount.ServicesUnseen != 1 {
		t.Errorf("account entry = %+v, want 3 and 1", byAccount)
	}

	// Tag selectors resolve through the existing resolver, then the same count applies.
	byAWS := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{map[string]any{"kind": "aws", "key": "env", "value": "prod"}}
	}))
	if byAWS == nil || byAWS.ServicesTotal != 2 || byAWS.ServicesUnseen != 1 {
		t.Errorf("aws tag entry = %+v, want 2 and 1", byAWS)
	}
	byHarbormaster := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{map[string]any{"kind": "harbormaster", "tag_id": int64(prod.ID)}}
	}))
	if byHarbormaster == nil || byHarbormaster.ServicesTotal != 1 || byHarbormaster.ServicesUnseen != 1 {
		t.Errorf("harbormaster tag entry = %+v, want 1 and 1", byHarbormaster)
	}

	// Giving the identity Prod as well leaves nothing unseen.
	if resp := putIdentity(e.admin, []map[string]any{{"tag_id": int64(e.devTag().ID), "logs": false}, {"tag_id": int64(prod.ID), "logs": true}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT identity: status %d", resp.StatusCode)
	}
	if got := previewAgent(t, e, investigateInput(e, "draft", true)); got == nil || got.ServicesUnseen != 0 || got.ServicesTotal != 3 {
		t.Errorf("after granting Prod = %+v, want 3 and 0 unseen", got)
	}
	if got := previewAgent(t, e, e.webhookInput("draft", func(in map[string]any) {
		in["investigate"] = true
		in["scope"] = []any{map[string]any{"kind": "harbormaster", "tag_id": int64(prod.ID)}}
	})); got == nil || got.ServicesUnseen != 0 {
		t.Errorf("harbormaster tag entry after granting Prod = %+v, want 0 unseen", got)
	}
}

func TestPreview_AgentIsNullWithoutTheSwitch(t *testing.T) {
	e := newNotifyEnv(t, notifyEnvOptions{agent: true})
	e.seedActiveServices(nxKey)
	for name, on := range map[string]any{"false": false, "omitted": nil} {
		if got := previewAgent(t, e, investigateInput(e, "draft", on)); got != nil {
			t.Errorf("switch %s: agent = %+v, want null", name, got)
		}
	}
	// A draft of a route that has the switch on, from a client that omits the member, follows the stored value.
	created := e.create(investigateInput(e, "stored", true))
	in := investigateInput(e, "stored", nil)
	in["route_id"] = created.ID
	if got := previewAgent(t, e, in); got == nil || got.ServicesTotal != 1 {
		t.Errorf("omitted member on a stored route that investigates: agent = %+v, want counted", got)
	}

	// With the agent off the switch is ignored: the preview still answers 200 and says nothing about the agent.
	off := newNotifyEnv(t)
	off.seedActiveServices(nxKey)
	if got := previewAgent(t, off, investigateInput(off, "draft", true)); got != nil {
		t.Errorf("agent off: agent = %+v, want null", got)
	}
}
