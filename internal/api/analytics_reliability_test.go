package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// withAnalyticsRetention gives the test server the production default retentions (testConfig leaves them 0).
func withAnalyticsRetention(s *Server) {
	s.Cfg.Retention.CountsHistory = 168 * time.Hour
	s.Cfg.Retention.StoppedTasks = 168 * time.Hour
	s.Cfg.Retention.ServiceEvents = 720 * time.Hour
	s.Cfg.Poll.CountsHistory = 5 * time.Minute
}

func getReliability(t *testing.T, c *apiClient, query string) ReliabilityDTO {
	t.Helper()
	resp := c.get("/api/analytics/reliability" + query)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("reliability %q: status %d %s %s", query, resp.StatusCode, code, msg)
	}
	var out ReliabilityDTO
	decode(t, resp, &out)
	return out
}

// visibleServices lists the estate's services with the tag each one resolves to.
func visibleServices(t *testing.T, st *store.Store) []store.ServiceRow {
	t.Helper()
	rows, err := st.ListServices(t.Context(), store.TargetFilter{}, "", store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// seedBelowDesired records one counts sample per service an hour ago; the given services run below desired.
func seedBelowDesired(t *testing.T, st *store.Store, below map[model.ServiceKey]bool) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Hour)
	var rows []model.Service
	for _, svc := range visibleServices(t, st) {
		row := model.Service{Key: svc.Key, Desired: 2, Running: 2}
		if below[svc.Key] {
			row.Running = 0
		}
		rows = append(rows, row)
	}
	if err := st.InsertCountsHistory(t.Context(), at, rows); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsReliability_ClampsNinetyDaysToRetention(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getReliability(t, login(t, srv, "sam", testPassword), "?range=90d")

	w := got.Window
	if w.Requested != "90d" || w.Used != "7d" || !w.Clamped || w.Bucket != "4h" || w.Retention != "168h" || !w.RangeApplies {
		t.Fatalf("window = %+v, want requested 90d used 7d clamped bucket 4h retention 168h", w)
	}
	if got.Tab != "reliability" || len(got.Availability.Timestamps) != 42 {
		t.Errorf("tab %q with %d timestamps, want reliability and 42", got.Tab, len(got.Availability.Timestamps))
	}
	sources := map[string]AnalyticsSourceDTO{}
	for _, src := range got.Sources {
		sources[src.Table] = src
	}
	events, ok := sources["service_events"]
	if !ok || events.WindowUsed == nil || *events.WindowUsed != "30d" {
		t.Errorf("service_events source = %+v, want window_used 30d", events)
	}
	if counts := sources["service_counts_history"]; counts.WindowUsed != nil || counts.Kept != "168h" {
		t.Errorf("service_counts_history source = %+v, want kept 168h and no window_used", counts)
	}
}

func TestAnalyticsReliability_ShortRangeIsNotClamped(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getReliability(t, login(t, srv, "sam", testPassword), "?range=24h")
	if got.Window.Used != "24h" || got.Window.Clamped || len(got.Availability.Timestamps) != 24 {
		t.Fatalf("window = %+v with %d timestamps, want 24h unclamped and 24", got.Window, len(got.Availability.Timestamps))
	}
	for _, src := range got.Sources {
		if src.WindowUsed != nil && src.Table == "service_events" {
			t.Errorf("service_events window_used = %q, want nil when the window matches the tab", *src.WindowUsed)
		}
	}
	if resp := login(t, srv, "sam", testPassword).get("/api/analytics/reliability?range=1y"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("range=1y status %d, want 422", resp.StatusCode)
	}
}

func TestAnalyticsReliability_DailyPanelsUnavailableBeforeFirstRollup(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getReliability(t, login(t, srv, "sam", testPassword), "")
	for name, available := range map[string]bool{
		"availability_90d":  got.Availability90d.Available,
		"below_desired_90d": got.BelowDesired90d.Available,
		"getting_worse":     got.GettingWorse.Available,
	} {
		if available {
			t.Errorf("%s is available before the first rollup", name)
		}
	}
}

func TestAnalyticsReliability_KPIsSeriesAndTable(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	services := visibleServices(t, st)
	bad := services[0]
	seedBelowDesired(t, st, map[model.ServiceKey]bool{bad.Key: true})

	stoppedAt := time.Now().UTC().Add(-2 * time.Hour)
	exit := 137
	if _, err := st.UpsertTasks(t.Context(), []model.Task{{
		Cluster: bad.Key.ClusterKey, ARN: "arn:aws:ecs:x:1:task/oom1", ServiceName: bad.Key.Service, LastStatus: "STOPPED",
		StoppedAt: &stoppedAt, StoppedReason: "OutOfMemoryError", AvailabilityZone: "eu-west-1a", PolledAt: stoppedAt,
		CreatedAt: stoppedAt.Add(-time.Hour), Containers: []model.Container{{Name: "app", ExitCode: &exit}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertServiceEvents(t.Context(), []model.ServiceEvent{
		{Key: bad.Key, ID: "pf1", CreatedAt: time.Now().Add(-time.Hour), Message: "(service x) was unable to place a task."}}); err != nil {
		t.Fatal(err)
	}

	got := getReliability(t, login(t, srv, "sam", testPassword), "?range=7d")
	k := got.KPIs
	if k.Availability == nil || *k.Availability >= 1 || *k.Availability <= 0 {
		t.Errorf("availability = %v, want between 0 and 1", k.Availability)
	}
	if k.ServicesBelow != 1 || k.MinutesBelowDesired != 5 {
		t.Errorf("services_below %d minutes_below %v, want 1 and 5", k.ServicesBelow, k.MinutesBelowDesired)
	}
	if k.TaskStops < 1 || k.UnexpectedStops < 1 || k.OutOfMemory < 1 || k.PlacementFailures < 1 {
		t.Errorf("kpis = %+v, want an OOM stop and one placement failure", k)
	}
	if len(got.LeastAvailable.Rows) != 1 || got.LeastAvailable.Rows[0].ServiceID != bad.Key.ID() || got.LeastAvailable.Total != 1 {
		t.Fatalf("least_available = %+v, want only the below-desired service", got.LeastAvailable)
	}
	row := got.LeastAvailable.Rows[0]
	if row.Availability != 0 || row.MinutesBelow != 5 || row.Tag.Name == "" {
		t.Errorf("row = %+v, want availability 0, 5 minutes below and a tag", row)
	}
	// Quiet stop-reason buckets are zero, never null; every group has a line and expected ones are flagged.
	if len(got.StopsByReason.Series) != 8 {
		t.Fatalf("stops_by_reason has %d lines, want the 8 groups", len(got.StopsByReason.Series))
	}
	for _, line := range got.StopsByReason.Series {
		for _, v := range line.Values {
			if v == nil {
				t.Fatalf("stops_by_reason line %q has a null value", line.Key)
			}
		}
		wantExpected := store.IsExpectedStopReason(line.Key)
		if line.Expected != wantExpected {
			t.Errorf("line %q expected = %v, want %v", line.Key, line.Expected, wantExpected)
		}
	}
	var zone, code bool
	for _, line := range got.UnexpectedByZone.Series {
		zone = zone || line.Key == "eu-west-1a"
	}
	for _, line := range got.ExitCodes.Series {
		code = code || line.Key == "137"
	}
	if !zone || !code {
		t.Errorf("zone line %v exit code line %v, want both present", zone, code)
	}
}

func TestAnalyticsReliability_NonAdminSeesOnlyVisible(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withAnalyticsRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	var prodSvc, devSvc *store.ServiceRow
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]string{}
	for _, tg := range tags {
		names[int64(tg.ID)] = tg.Name
	}
	services := visibleServices(t, st)
	for i := range services {
		switch names[int64(services[i].EffectiveTagID)] {
		case "Prod":
			if prodSvc == nil {
				prodSvc = &services[i]
			}
		case "Dev":
			if devSvc == nil {
				devSvc = &services[i]
			}
		}
	}
	if prodSvc == nil || devSvc == nil {
		t.Fatal("mock estate lacks a Prod or Dev service")
	}
	seedBelowDesired(t, st, map[model.ServiceKey]bool{prodSvc.Key: true, devSvc.Key: true})

	admin := getReliability(t, login(t, srv, "sam", testPassword), "")
	if admin.KPIs.ServicesBelow != 2 || len(admin.LeastAvailable.Rows) != 2 {
		t.Fatalf("admin sees %d services below, %d rows; want 2 and 2", admin.KPIs.ServicesBelow, len(admin.LeastAvailable.Rows))
	}
	omer := getReliability(t, login(t, srv, "omer", testPassword), "")
	if omer.KPIs.ServicesBelow != 1 || len(omer.LeastAvailable.Rows) != 1 || omer.LeastAvailable.Rows[0].ServiceID != devSvc.Key.ID() {
		t.Fatalf("omer sees %d below, rows %+v; want only the Dev service %s", omer.KPIs.ServicesBelow, omer.LeastAvailable.Rows, devSvc.Key.ID())
	}
}
