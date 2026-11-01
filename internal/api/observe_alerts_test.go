package api

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type alertsBody struct {
	Items []struct {
		Kind      string `json:"kind"`
		Severity  string `json:"severity"`
		Title     string `json:"title"`
		ServiceID string `json:"service_id"`
		Service   string `json:"service"`
		Cluster   string `json:"cluster"`
		ClusterID string `json:"cluster_id"`
		Tab       string `json:"tab"`
	} `json:"items"`
	Counts struct {
		Danger  int `json:"danger"`
		Warning int `json:"warning"`
		Info    int `json:"info"`
	} `json:"counts"`
	PolledAt *time.Time `json:"polled_at"`
}

// targetAlert places one alert on one account x region target.
type targetAlert struct {
	target awsx.Target
	alert  model.Alert
}

// replaceAlerts makes rows the only alerts in the estate: every estate target is replaced, so the
// alerts the poller derived from the fake do not leak into the response.
func replaceAlerts(t *testing.T, st *store.Store, f *fake.Fake, rows ...targetAlert) {
	t.Helper()
	byTarget := map[awsx.Target][]model.Alert{}
	for _, row := range rows {
		byTarget[row.target] = append(byTarget[row.target], row.alert)
	}
	targets := map[awsx.Target]bool{}
	for _, account := range f.Estate.Accounts {
		for _, region := range account.Regions {
			targets[awsx.Target{AccountID: account.ID, Region: region}] = true
		}
	}
	for target := range byTarget {
		targets[target] = true
	}
	for target := range targets {
		if err := st.ReplaceAlerts(t.Context(), target, byTarget[target]); err != nil {
			t.Fatal(err)
		}
	}
}

func serviceAlert(account, region, cluster, service string, severity model.AlertSeverity, title string) targetAlert {
	return targetAlert{
		target: awsx.Target{AccountID: account, Region: region},
		alert: model.Alert{
			Key:      model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: region, Cluster: cluster}, Service: service},
			Kind:     "service_failing",
			Severity: severity,
			Title:    title,
			Text:     "detail",
			At:       time.Now().UTC(),
			Tab:      "tasks",
		},
	}
}

func getAlertsBody(t *testing.T, c *apiClient, query string) alertsBody {
	t.Helper()
	resp := c.get("/api/alerts" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/alerts%s: status %d, want 200", query, resp.StatusCode)
	}
	var body alertsBody
	decode(t, resp, &body)
	return body
}

func itemTitles(body alertsBody) []string {
	titles := make([]string, 0, len(body.Items))
	for _, item := range body.Items {
		titles = append(titles, item.Title)
	}
	return titles
}

func TestAlerts_AdminSeesAllSorted(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	replaceAlerts(t, st, fk,
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "payments-api", model.AlertInfo, "payments-api info"),
		serviceAlert(homelabAccountID, "us-east-1", "background-jobs", "image-resizer", model.AlertDanger, "image-resizer danger"),
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "web-frontend", model.AlertWarning, "web-frontend warning"),
	)

	body := getAlertsBody(t, login(t, srv, "pavel", testPassword), "")

	wantTitles := []string{"image-resizer danger", "web-frontend warning", "payments-api info"}
	if got := itemTitles(body); len(got) != len(wantTitles) || got[0] != wantTitles[0] || got[1] != wantTitles[1] || got[2] != wantTitles[2] {
		t.Fatalf("item titles = %v; want %v (danger, warning, info)", got, wantTitles)
	}
	if body.Counts.Danger != 1 || body.Counts.Warning != 1 || body.Counts.Info != 1 {
		t.Errorf("counts = %+v; want danger 1, warning 1, info 1", body.Counts)
	}
	if body.PolledAt == nil {
		t.Error("polled_at is null; want the alerts-tier poll time after pollAll")
	}
	if body.Items[0].Service != "image-resizer" || body.Items[0].Cluster != "background-jobs" {
		t.Errorf("first item service/cluster = %q/%q; want image-resizer/background-jobs", body.Items[0].Service, body.Items[0].Cluster)
	}
	if body.Items[0].ServiceID == "" || body.Items[0].ClusterID == "" {
		t.Errorf("first item service_id/cluster_id = %q/%q; want both set", body.Items[0].ServiceID, body.Items[0].ClusterID)
	}
}

func TestAlerts_FiltersInvisibleServices(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	// notifications sits in core-services, which is tagged Prod; omer only has Dev and Staging.
	replaceAlerts(t, st, fk,
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "notifications", model.AlertDanger, "notifications danger"),
		serviceAlert(homelabAccountID, "us-east-1", "background-jobs", "image-resizer", model.AlertWarning, "image-resizer warning"),
	)

	omer := getAlertsBody(t, login(t, srv, "omer", testPassword), "")
	if got := itemTitles(omer); len(got) != 1 || got[0] != "image-resizer warning" {
		t.Fatalf("omer item titles = %v; want only the Dev alert", got)
	}
	if omer.Counts.Danger != 0 || omer.Counts.Warning != 1 {
		t.Errorf("omer counts = %+v; want danger 0 and warning 1 after filtering", omer.Counts)
	}

	pavel := getAlertsBody(t, login(t, srv, "pavel", testPassword), "")
	if len(pavel.Items) != 2 || pavel.Counts.Danger != 1 {
		t.Errorf("pavel sees %d items, danger %d; want 2 items and danger 1", len(pavel.Items), pavel.Counts.Danger)
	}
}

func TestAlerts_AccountRegionFilter(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	replaceAlerts(t, st, fk,
		serviceAlert(homelabAccountID, "us-east-1", "core-services", "web-frontend", model.AlertWarning, "homelab warning"),
		serviceAlert(stagingAccountID, "us-east-1", "staging-web", "web", model.AlertDanger, "staging danger"),
	)
	pavel := login(t, srv, "pavel", testPassword)

	homelab := getAlertsBody(t, pavel, "?account="+homelabAccountID)
	if got := itemTitles(homelab); len(got) != 1 || got[0] != "homelab warning" {
		t.Fatalf("account filter item titles = %v; want only the homelab alert", got)
	}
	if homelab.Counts.Danger != 0 || homelab.Counts.Warning != 1 {
		t.Errorf("account filter counts = %+v; want counts recomputed over the homelab alert only", homelab.Counts)
	}

	empty := getAlertsBody(t, pavel, "?account="+homelabAccountID+"&region=eu-west-1")
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Errorf("region with no alerts: items = %#v; want an empty non-nil list", empty.Items)
	}
}

func TestAlerts_AccountUnreachableVisibleWhenAnyClusterVisible(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	unreachable := func(account, region string) targetAlert {
		return targetAlert{
			target: awsx.Target{AccountID: account, Region: region},
			alert: model.Alert{
				Key:      model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: region}},
				Kind:     "account_unreachable",
				Severity: model.AlertDanger,
				Title:    "unreachable " + account + " " + region,
				At:       time.Now().UTC(),
			},
		}
	}
	// omer (Dev Operator, Staging Viewer) sees homelab us-east-1 and eu-west-1 through their Dev clusters,
	// but not staging-example eu-west-1, which has no cluster at all.
	// oncall (Staging and Prod only) sees homelab us-east-1 through core-services (Prod), but not eu-west-1,
	// whose only cluster eu-apps is Dev.
	setPassword(t, st, "oncall")
	replaceAlerts(t, st, fk,
		unreachable(homelabAccountID, "us-east-1"),
		unreachable(homelabAccountID, "eu-west-1"),
		unreachable(stagingAccountID, "eu-west-1"),
	)
	homelabUSEast := "unreachable " + homelabAccountID + " us-east-1"
	homelabEUWest := "unreachable " + homelabAccountID + " eu-west-1"

	omer := getAlertsBody(t, login(t, srv, "omer", testPassword), "")
	if got := itemTitles(omer); len(got) != 2 || !slices.Contains(got, homelabUSEast) || !slices.Contains(got, homelabEUWest) {
		t.Fatalf("omer item titles = %v; want the two homelab accounts only", got)
	}
	if omer.Counts.Danger != 2 {
		t.Errorf("omer counts.danger = %d; want 2", omer.Counts.Danger)
	}
	if omer.Items[0].Service != "" || omer.Items[0].ServiceID != "" {
		t.Errorf("account alert service/service_id = %q/%q; want empty", omer.Items[0].Service, omer.Items[0].ServiceID)
	}

	oncall := getAlertsBody(t, login(t, srv, "oncall", testPassword), "")
	if got := itemTitles(oncall); len(got) != 1 || got[0] != homelabUSEast {
		t.Fatalf("oncall item titles = %v; want only the us-east-1 account", got)
	}
	if oncall.Counts.Danger != 1 {
		t.Errorf("oncall counts.danger = %d; want 1 after excluding the invisible account", oncall.Counts.Danger)
	}

	pavel := getAlertsBody(t, login(t, srv, "pavel", testPassword), "")
	if len(pavel.Items) != 3 || pavel.Counts.Danger != 3 {
		t.Errorf("pavel sees %d items, danger %d; want all three account alerts", len(pavel.Items), pavel.Counts.Danger)
	}
}
