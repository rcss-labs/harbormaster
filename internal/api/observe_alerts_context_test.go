package api

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type contextAlert struct {
	Kind     string `json:"kind"`
	Service  string `json:"service"`
	Cause    string `json:"cause"`
	NextStep struct {
		Label string `json:"label"`
		Href  string `json:"href"`
	} `json:"next_step"`
	JustBefore *struct {
		At     time.Time `json:"at"`
		Text   string    `json:"text"`
		Source string    `json:"source"`
	} `json:"just_before"`
}

func getContextAlerts(t *testing.T, c *apiClient) []contextAlert {
	t.Helper()
	resp := c.get("/api/alerts")
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Items []contextAlert `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode alerts: %v: %s", err, raw)
	}
	return body.Items
}

func alertOfKind(kind, account, region, cluster, service string, at time.Time) targetAlert {
	row := serviceAlert(account, region, cluster, service, model.AlertWarning, kind+" title")
	row.alert.Kind = kind
	row.alert.At = at
	return row
}

func TestAlerts_NextStepPerKind(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	now := time.Now().UTC()
	// One alert per kind on a different service each, so every kind is returned.
	services := map[string]string{
		"service_failing":     "image-resizer",
		"restart_loop":        "web-frontend",
		"deployment_failed":   "payments-api",
		"unhealthy_task":      "notifications",
		"service_degraded":    "artifact-proxy",
		"cannot_place":        "catalog",
		"rollout_in_progress": "edge-nginx",
	}
	clusters := map[string]string{
		"image-resizer": "background-jobs", "web-frontend": "core-services", "payments-api": "core-services",
		"notifications": "core-services", "artifact-proxy": "core-services", "catalog": "core-services", "edge-nginx": "core-services",
	}
	var rows []targetAlert
	for kind, service := range services {
		rows = append(rows, alertOfKind(kind, homelabAccountID, "us-east-1", clusters[service], service, now))
	}
	replaceAlerts(t, st, fk, rows...)

	wantTab := map[string]string{
		"service_failing": "logs", "restart_loop": "tasks", "deployment_failed": "deploy",
		"unhealthy_task": "tasks", "service_degraded": "overview", "cannot_place": "overview", "rollout_in_progress": "overview",
	}
	wantLabel := map[string]string{
		"logs": "Open logs", "tasks": "Open tasks", "deploy": "Open deploy", "overview": "Open service",
	}
	got := getContextAlerts(t, login(t, srv, "pavel", testPassword))
	if len(got) != len(services) {
		t.Fatalf("got %d alerts, want %d", len(got), len(services))
	}
	for _, item := range got {
		tab := wantTab[item.Kind]
		if !strings.HasSuffix(item.NextStep.Href, "/"+tab) || !strings.HasPrefix(item.NextStep.Href, "/services/") {
			t.Errorf("%s next_step.href = %q; want /services/<id>/%s", item.Kind, item.NextStep.Href, tab)
		}
		if item.NextStep.Label != wantLabel[tab] {
			t.Errorf("%s next_step.label = %q; want %q", item.Kind, item.NextStep.Label, wantLabel[tab])
		}
		if item.Cause == "" {
			t.Errorf("%s cause is empty; want the alert text", item.Kind)
		}
	}
}

func TestAlerts_AccountProblemNextStepIsAccounts(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	accountAlert := targetAlert{
		target: serviceAlert(homelabAccountID, "us-east-1", "core-services", "x", model.AlertDanger, "t").target,
		alert: model.Alert{
			Key:  model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1"}},
			Kind: "account_unreachable", Severity: model.AlertDanger, Title: "unreachable", Text: "AccessDenied", At: time.Now().UTC(),
		},
	}
	replaceAlerts(t, st, fk, accountAlert)

	got := getContextAlerts(t, login(t, srv, "pavel", testPassword))
	if len(got) != 1 || got[0].NextStep.Href != "/settings/accounts" || got[0].NextStep.Label != "Open AWS accounts" {
		t.Fatalf("alerts = %+v; want one account alert linking /settings/accounts", got)
	}
	if got[0].JustBefore != nil {
		t.Errorf("account alert just_before = %+v; want null", got[0].JustBefore)
	}
}

func appendOperationAt(t *testing.T, st *store.Store, cluster, service, source, text string, at time.Time) {
	t.Helper()
	_, err := st.Pool.Exec(t.Context(),
		`insert into operation_log (account_id, region, cluster_name, service_name, source, text, ts)
		 values ($1, 'us-east-1', $2, $3, $4, $5, $6)`,
		homelabAccountID, cluster, service, source, text, at)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAlerts_JustBeforeFromOperationLog(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	alertAt := time.Now().UTC().Add(-time.Minute)
	replaceAlerts(t, st, fk, alertOfKind("service_failing", homelabAccountID, "us-east-1", "background-jobs", "image-resizer", alertAt))
	appendOperationAt(t, st, "background-jobs", "image-resizer", "harbormaster", "Changed image-resizer desired count to 0.", alertAt.Add(-10*time.Minute))

	got := getContextAlerts(t, login(t, srv, "pavel", testPassword))
	if len(got) != 1 || got[0].JustBefore == nil {
		t.Fatalf("alerts = %+v; want one alert with just_before", got)
	}
	if jb := got[0].JustBefore; jb.Source != "harbormaster" || !strings.Contains(jb.Text, "desired count") {
		t.Errorf("just_before = %+v; want the harbormaster operation text", jb)
	}
}

func TestAlerts_JustBeforeOmittedWithoutChange(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	alertAt := time.Now().UTC().Add(-time.Minute)
	replaceAlerts(t, st, fk, alertOfKind("service_failing", homelabAccountID, "us-east-1", "background-jobs", "image-resizer", alertAt))
	// None of these may be reported: ECS event, outside the 24h window, and after the alert.
	appendOperationAt(t, st, "background-jobs", "image-resizer", "ecs", "ECS did something", alertAt.Add(-time.Minute))
	appendOperationAt(t, st, "background-jobs", "image-resizer", "harbormaster", "Too old", alertAt.Add(-25*time.Hour))
	appendOperationAt(t, st, "background-jobs", "image-resizer", "harbormaster", "Later", alertAt.Add(time.Minute))

	got := getContextAlerts(t, login(t, srv, "pavel", testPassword))
	if len(got) != 1 || got[0].JustBefore != nil {
		t.Fatalf("alerts = %+v; want one alert with just_before null", got)
	}
}

func TestAlerts_JustBeforeNeverLeaksInvisibleServiceChanges(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	alertAt := time.Now().UTC().Add(-time.Minute)
	// notifications is Prod (omer cannot view it); image-resizer is Dev.
	replaceAlerts(t, st, fk,
		alertOfKind("service_failing", homelabAccountID, "us-east-1", "core-services", "notifications", alertAt),
		alertOfKind("service_failing", homelabAccountID, "us-east-1", "background-jobs", "image-resizer", alertAt),
	)
	appendOperationAt(t, st, "core-services", "notifications", "harbormaster", "SECRET-PROD-CHANGE", alertAt.Add(-time.Minute))
	appendOperationAt(t, st, "background-jobs", "image-resizer", "harbormaster", "Dev change", alertAt.Add(-time.Minute))

	resp := login(t, srv, "omer", testPassword).get("/api/alerts")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "SECRET-PROD-CHANGE") || strings.Contains(string(raw), "notifications") {
		t.Fatalf("omer's alerts leak a Prod service or its change: %s", raw)
	}
	if !strings.Contains(string(raw), "Dev change") {
		t.Fatalf("omer's Dev alert lacks its change: %s", raw)
	}
}
