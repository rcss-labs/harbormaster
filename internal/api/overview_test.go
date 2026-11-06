package api

import (
	"net/http"
	"testing"
)

func getOverview(t *testing.T, c *apiClient, query string) OverviewDTO {
	t.Helper()
	resp := c.get("/api/overview" + query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview %q: status %d", query, resp.StatusCode)
	}
	var out OverviewDTO
	decode(t, resp, &out)
	return out
}

func TestOverview_AdminTotals(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getOverview(t, login(t, srv, "sam", testPassword), "")

	if len(got.Clusters) != 6 || got.Services.Total != 15 {
		t.Fatalf("clusters %d services %d, want 6 and 15", len(got.Clusters), got.Services.Total)
	}
	if got.Accounts != 2 || got.Regions != 3 {
		t.Errorf("accounts %d regions %d, want 2 and 3", got.Accounts, got.Regions)
	}
	if got.Tasks.Running != 21 || got.Tasks.Desired != 23 {
		t.Errorf("tasks running %d desired %d, want 21 and 23", got.Tasks.Running, got.Tasks.Desired)
	}
	if got.TaskHealth.Missing != got.Tasks.Desired-got.Tasks.Running || got.TaskHealth.Missing != 2 {
		t.Errorf("missing %d, want 2", got.TaskHealth.Missing)
	}
	if got.TaskHealth.RollingOut != 1 || got.TaskHealth.Unhealthy != 1 || got.Tasks.Unhealthy != 1 {
		t.Errorf("task health %+v unhealthy %d, want rolling_out 1 unhealthy 1", got.TaskHealth, got.Tasks.Unhealthy)
	}
	if got.TaskHealth.Healthy != 21-1-1 {
		t.Errorf("healthy %d, want 19", got.TaskHealth.Healthy)
	}
	for status, want := range map[string]int{"failing": 1, "deploying": 1, "unhealthy_task": 1} {
		if n := got.Services.ByStatus[statusOf(status)]; n != want {
			t.Errorf("by_status[%s] = %d, want %d", status, n, want)
		}
	}
	if len(got.StopsPerHour) != 24 || len(got.DeploymentsPerDay) != 14 {
		t.Errorf("buckets: %d hours %d days, want 24 and 14", len(got.StopsPerHour), len(got.DeploymentsPerDay))
	}
	if len(got.PerCluster) != 6 || got.PolledAt.IsZero() {
		t.Errorf("per_cluster %d polled_at %v", len(got.PerCluster), got.PolledAt)
	}
	if len(got.Events) == 0 || len(got.Events) > 10 {
		t.Errorf("events %d, want 1..10", len(got.Events))
	}
}

func TestOverview_FilteredByAccount(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getOverview(t, login(t, srv, "sam", testPassword), "?account="+stagingAccountID)
	if len(got.Clusters) != 1 || got.Clusters[0].Name != "staging-web" || got.Accounts != 1 || got.Regions != 1 {
		t.Fatalf("filtered overview = %+v", got.Clusters)
	}
}

func TestOverview_UserSeesOnlyVisible(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getOverview(t, login(t, srv, "omer", testPassword), "")
	if len(got.Clusters) != 5 {
		t.Fatalf("omer sees %d clusters, want 5", len(got.Clusters))
	}
	for _, c := range got.Clusters {
		if c.Name == "core-services" {
			t.Error("core-services leaked to omer")
		}
	}
	for _, e := range got.Events {
		if e.Service.Name == "payments-api" || e.Cluster.Name == "core-services" {
			t.Errorf("event from an invisible cluster: %+v", e)
		}
	}
}

func TestOverview_JSONNeverNull(t *testing.T) {
	srv, _, _ := newTestServer(t) // nothing polled
	resp := login(t, srv, "sam", testPassword).get("/api/overview")
	var raw map[string]any
	decode(t, resp, &raw)
	for _, key := range []string{"clusters", "per_cluster", "stops_per_hour", "deployments_per_day", "stop_reasons", "events"} {
		if raw[key] == nil {
			t.Errorf("%s is null", key)
		}
	}
	if tasks, _ := raw["tasks"].(map[string]any); tasks["history_72h"] == nil {
		t.Error("tasks.history_72h is null")
	}
}

func TestOverview_PerClusterHealth(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getOverview(t, login(t, srv, "sam", testPassword), "")

	byName := map[string]PerClusterDTO{}
	var unhealthy, rollingOut, missing int
	for _, c := range got.PerCluster {
		byName[c.Name] = c
		unhealthy += c.Unhealthy
		rollingOut += c.RollingOut
		missing += c.Missing
	}
	if byName["eu-apps"].Unhealthy != 1 {
		t.Errorf("eu-apps unhealthy %d, want 1", byName["eu-apps"].Unhealthy)
	}
	if byName["background-jobs"].Missing != 2 {
		t.Errorf("background-jobs missing %d, want 2", byName["background-jobs"].Missing)
	}
	if byName["core-services"].RollingOut != 1 {
		t.Errorf("core-services rolling_out %d, want 1", byName["core-services"].RollingOut)
	}
	if s := byName["staging-web"]; s.Unhealthy != 0 || s.RollingOut != 0 || s.Missing != 0 {
		t.Errorf("staging-web %+v, want all zero", s)
	}
	if unhealthy != got.TaskHealth.Unhealthy || rollingOut != got.TaskHealth.RollingOut || missing != got.TaskHealth.Missing {
		t.Errorf("per-cluster sums %d/%d/%d, want totals %+v", unhealthy, rollingOut, missing, got.TaskHealth)
	}
}
