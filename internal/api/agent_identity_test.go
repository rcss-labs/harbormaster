package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

type identityBody struct {
	Username    string `json:"username"`
	Permissions []struct {
		TagID   int64  `json:"tag_id"`
		TagName string `json:"tag_name"`
		Logs    bool   `json:"logs"`
	} `json:"permissions"`
}

func putIdentity(c *apiClient, permissions any) *http.Response {
	return c.do(http.MethodPut, "/api/agent/identity", map[string]any{"permissions": permissions}, nil)
}

func identityAccess(t *testing.T, env *settingsEnv) map[authz.TagID]authz.ActionSet {
	t.Helper()
	identity, err := env.st.GetAgentIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	subject, err := env.st.LoadSubject(t.Context(), identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	return subject.Perms
}

func TestAgentIdentity_AdminOnly(t *testing.T) {
	env := newSettingsEnv(t, false)
	tag, err := env.st.CreateTag(t.Context(), "Prod", "red", false)
	if err != nil {
		t.Fatal(err)
	}
	getSettings(t, env.sam) // the identity exists from the first read
	for _, r := range []struct{ method, path string }{
		{"PUT", "/api/agent/identity"}, {"GET", "/api/agent/investigations"},
	} {
		resp := env.omer.do(r.method, r.path, map[string]any{"permissions": []map[string]any{{"tag_id": int64(tag.ID), "logs": false}}}, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin: status %d, want 403", r.method, r.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if got := identityAccess(t, env); len(got) != 0 {
		t.Errorf("a non-admin gave the identity access: %v", got)
	}
}

func TestAgentIdentity_SetsViewOrViewAndLogsOnly(t *testing.T) {
	env := newSettingsEnv(t, false)
	prod, _ := env.st.CreateTag(t.Context(), "Prod", "red", false)
	stage, _ := env.st.CreateTag(t.Context(), "Staging", "blue", false)

	resp := putIdentity(env.sam, []map[string]any{{"tag_id": int64(prod.ID), "logs": true}, {"tag_id": int64(stage.ID), "logs": false}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	var saved identityBody
	decode(t, resp, &saved)
	if saved.Username != "harbormaster-agent" || len(saved.Permissions) != 2 {
		t.Fatalf("identity = %+v", saved)
	}
	byTag := map[int64]bool{}
	for _, p := range saved.Permissions {
		byTag[p.TagID] = p.Logs
		if p.TagName == "" {
			t.Errorf("permission %+v has no tag name", p)
		}
	}
	if !byTag[int64(prod.ID)] || byTag[int64(stage.ID)] {
		t.Errorf("logs by tag = %v, want Prod true and Staging false", byTag)
	}
	want := map[authz.TagID]authz.ActionSet{prod.ID: authz.ActionSet(authz.View | authz.Logs), stage.ID: authz.ActionSet(authz.View)}
	if got := identityAccess(t, env); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("stored access = %v, want %v", got, want)
	}
	var read struct {
		Identity identityBody `json:"identity"`
	}
	decode(t, env.sam.get("/api/agent/settings"), &read)
	if len(read.Identity.Permissions) != 2 {
		t.Errorf("settings identity = %+v", read.Identity)
	}

	// Anything more than View and Logs, a tag that does not exist, a repeated tag and a malformed entry are 422 and write nothing.
	for name, body := range map[string]any{
		"an extra action":   []map[string]any{{"tag_id": int64(stage.ID), "logs": true, "deploy": true}},
		"an actions list":   []map[string]any{{"tag_id": int64(stage.ID), "actions": []string{"start", "stop"}}},
		"an unknown tag":    []map[string]any{{"tag_id": int64(stage.ID), "logs": false}, {"tag_id": 987654, "logs": false}},
		"a repeated tag":    []map[string]any{{"tag_id": int64(stage.ID), "logs": false}, {"tag_id": int64(stage.ID), "logs": true}},
		"a zero tag":        []map[string]any{{"tag_id": 0, "logs": false}},
		"no permissions":    nil,
		"a string for logs": []map[string]any{{"tag_id": int64(stage.ID), "logs": "yes"}},
	} {
		resp := putIdentity(env.sam, body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		resp.Body.Close()
		if got := identityAccess(t, env); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s changed the stored access to %v", name, got)
		}
	}

	// An empty list is a real save: no access.
	resp = putIdentity(env.sam, []map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT with an empty list: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := identityAccess(t, env); len(got) != 0 {
		t.Errorf("access after an empty list = %v", got)
	}
}

func TestAgentIdentity_Audited(t *testing.T) {
	env := newSettingsEnv(t, false)
	prod, _ := env.st.CreateTag(t.Context(), "Prod", "red", false)
	resp := putIdentity(env.sam, []map[string]any{{"tag_id": int64(prod.ID), "logs": true}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	rows := settingsAuditRows(t, env.st, "agent_identity_update")
	if len(rows) != 1 {
		t.Fatalf("%d agent_identity_update rows, want exactly 1 (the store writes it)", len(rows))
	}
	tags, _ := rows[0].Details["tags"].(map[string]any)
	if rows[0].ActorUsername != "sam" || fmt.Sprint(tags[fmt.Sprint(int64(prod.ID))]) != "[View Logs]" {
		t.Errorf("audit row = %+v", rows[0])
	}
	// A refused save writes no row.
	resp = putIdentity(env.sam, []map[string]any{{"tag_id": 987654, "logs": false}})
	resp.Body.Close()
	if n := len(settingsAuditRows(t, env.st, "agent_identity_update")); n != 1 {
		t.Errorf("%d rows after a refused save, want still 1", n)
	}
}

func TestAgentIdentity_AbsentFromUsersList(t *testing.T) {
	env := newSettingsEnv(t, false)
	// Reading the settings creates the identity; it must not show among the users.
	if got := getSettings0(t, env.sam)["identity"].(map[string]any)["username"]; got != "harbormaster-agent" {
		t.Fatalf("identity = %v", got)
	}
	var users struct {
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
	}
	decode(t, env.sam.get("/api/users"), &users)
	names := []string{}
	for _, u := range users.Items {
		names = append(names, u.Username)
	}
	if len(names) != 2 || strings.Contains(strings.Join(names, ","), "harbormaster-agent") {
		t.Errorf("users = %v, want sam and omer only", names)
	}
}

func seedInvestigation(t *testing.T, env *settingsEnv, key model.ServiceKey, state, reason string, requested time.Time, runCostMicro int64) int64 {
	t.Helper()
	var runID *uuid.UUID
	if runCostMicro > 0 {
		id := uuid.New()
		runID = &id
		if _, err := env.st.Pool.Exec(t.Context(), `insert into agent_runs (id, role, provider, model, state, cost_micro_usd) values ($1, 'investigator', 'fake', 'fake-1', 'done', $2)`, id, runCostMicro); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	err := env.st.Pool.QueryRow(t.Context(), `insert into agent_investigations
		(subject_kind, subject_key, account_id, region, cluster_name, service_name, alert_kind, alert_title, alert_text, alert_opened_at,
		 state, reason, attempts, run_id, provider, model, summary, requested_at, finished_at)
		values ('service', $1, $2, $3, $4, $5, 'service_failing', $6, 'text', $7, $8, $9, 2, $10, 'fake', 'fake-1', $11, $7, $7) returning id`,
		"service/"+key.AccountID+"/"+key.Region+"/"+key.Cluster+"/"+key.Service, key.AccountID, key.Region, key.Cluster, key.Service,
		key.Service+": failing", requested, state, reason, runID, "summary of "+key.Service).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAgentInvestigations_AdminListWithCost(t *testing.T) {
	env := newSettingsEnv(t, false)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	key := func(name string) model.ServiceKey {
		return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "core"}, Service: name}
	}
	oldest := seedInvestigation(t, env, key("alpha"), "failed", "agent_unavailable", base, 0)
	// A run row that retention deleted: the investigation still points at it.
	if _, err := env.st.Pool.Exec(t.Context(), `update agent_investigations set run_id = $1 where id = $2`, uuid.New(), oldest); err != nil {
		t.Fatal(err)
	}
	middle := seedInvestigation(t, env, key("beta"), "done", "", base.Add(time.Minute), 1_500_000)
	newest := seedInvestigation(t, env, key("gamma"), "skipped", "budget", base.Add(2*time.Minute), 0)

	type page struct {
		Items []struct {
			ID      int64  `json:"id"`
			State   string `json:"state"`
			Reason  string `json:"reason"`
			Subject struct {
				Kind      string `json:"kind"`
				ServiceID string `json:"service_id"`
				Service   string `json:"service"`
				Cluster   string `json:"cluster"`
				ClusterID string `json:"cluster_id"`
				AccountID string `json:"account_id"`
				Region    string `json:"region"`
			} `json:"subject"`
			Alert struct {
				Kind  string `json:"kind"`
				Title string `json:"title"`
			} `json:"alert"`
			Summary  string `json:"summary"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Attempts int    `json:"attempts"`
			CostUSD  string `json:"cost_usd"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	var first page
	resp := env.sam.get("/api/agent/investigations?limit=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status %d", resp.StatusCode)
	}
	decode(t, resp, &first)
	if len(first.Items) != 2 || first.Items[0].ID != newest || first.Items[1].ID != middle || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want newest then middle and a cursor", first)
	}
	done := first.Items[1]
	if done.State != "done" || done.Provider != "fake" || done.Model != "fake-1" || done.Attempts != 2 || done.CostUSD != "1.50" ||
		done.Subject.Kind != "service" || done.Subject.Service != "beta" || done.Subject.Cluster != "core" ||
		done.Subject.ServiceID != key("beta").ID() || done.Subject.ClusterID != key("beta").ClusterKey.ID() ||
		done.Subject.AccountID != "111111111111" || done.Subject.Region != "us-east-1" ||
		done.Alert.Kind != "service_failing" || done.Alert.Title != "beta: failing" || done.Summary != "summary of beta" {
		t.Errorf("item = %+v", done)
	}
	if first.Items[0].State != "skipped" || first.Items[0].Reason != "budget" || first.Items[0].CostUSD != "" {
		t.Errorf("item without a run = %+v", first.Items[0])
	}

	var second page
	decode(t, env.sam.get("/api/agent/investigations?limit=2&cursor="+first.NextCursor), &second)
	if len(second.Items) != 1 || second.Items[0].ID != oldest || second.NextCursor != "" || second.Items[0].CostUSD != "" {
		t.Errorf("last page = %+v, want the oldest and an empty cursor", second)
	}

	// It carries no evidence, and a bad cursor or limit is refused.
	raw := env.sam.get("/api/agent/investigations")
	var body map[string]any
	decode(t, raw, &body)
	for _, item := range body["items"].([]any) {
		if _, ok := item.(map[string]any)["evidence"]; ok {
			t.Error("an item carries evidence")
		}
	}
	// A huge limit is clamped, not refused and not overflowed.
	for _, q := range []string{"limit=1000000", "limit=9223372036854775807"} {
		var big page
		resp := env.sam.get("/api/agent/investigations?" + q)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("?%s: status %d, want 200", q, resp.StatusCode)
		}
		decode(t, resp, &big)
		if len(big.Items) != 3 || big.NextCursor != "" {
			t.Errorf("?%s: %d items, cursor %q", q, len(big.Items), big.NextCursor)
		}
	}
	for _, q := range []string{"cursor=!!", "limit=0", "limit=abc"} {
		resp := env.sam.get("/api/agent/investigations?" + q)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("?%s: status %d, want 422", q, resp.StatusCode)
		}
		resp.Body.Close()
	}
	var empty page
	env2 := newSettingsEnv(t, false)
	decode(t, env2.sam.get("/api/agent/investigations"), &empty)
	if empty.Items == nil || empty.NextCursor != "" {
		t.Errorf("empty list = %+v, want [] and an empty cursor", empty)
	}
}
