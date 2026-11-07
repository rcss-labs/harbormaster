package api

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func withCostAnalytics(s *Server) {
	withAnalyticsRetention(s)
	s.Cfg.Analytics.Util = true
	s.Cfg.Retention.UtilDaily = 90 * 24 * time.Hour
}

func getCost(t *testing.T, c *apiClient, query string) CostDTO {
	t.Helper()
	resp := c.get("/api/analytics/cost" + query)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("cost %q: status %d %s %s", query, resp.StatusCode, code, msg)
	}
	var out CostDTO
	decode(t, resp, &out)
	return out
}

func costService(t *testing.T, dto CostDTO, name string) CostServiceDTO {
	t.Helper()
	for _, svc := range dto.Rightsizing.Services {
		if svc.Service == name {
			return svc
		}
	}
	t.Fatalf("service %q missing from rightsizing.services", name)
	return CostServiceDTO{}
}

// forceProviders makes the capacity provider of every task in the fake estate deterministic: the seed picks
// FARGATE_SPOT at random.
func forceProviders(fk *fake.Fake, provider string) {
	fk.Estate.Mu.Lock()
	defer fk.Estate.Mu.Unlock()
	for i := range fk.Estate.Tasks {
		name := provider
		fk.Estate.Tasks[i].Task.CapacityProviderName = &name
	}
}

// editRunningTasks applies change to the RUNNING tasks of one service in the fake estate.
func editRunningTasks(fk *fake.Fake, service string, change func(index int, task *ecstypes.Task)) {
	fk.Estate.Mu.Lock()
	defer fk.Estate.Mu.Unlock()
	index := 0
	for i := range fk.Estate.Tasks {
		task := &fk.Estate.Tasks[i].Task
		if task.Group != nil && *task.Group == "service:"+service && task.LastStatus != nil && *task.LastStatus == "RUNNING" {
			change(index, task)
			index++
		}
	}
}

func sumGroups(groups []CostGroupDTO) float64 {
	var total float64
	for _, group := range groups {
		total += group.Monthly
	}
	return total
}

func sumServices(services []CostServiceDTO) float64 {
	var total float64
	for _, svc := range services {
		total += svc.Monthly
	}
	return total
}

func approx(got, want float64) bool { return math.Abs(got-want) < 0.01 }

// seededSizes are the task definition sizes (CPU units, MiB) of the services the tests give history.
var seededSizes = map[string][2]int{"report-worker": {1024, 2048}, "email-sender": {256, 512}, "payments-api": {512, 1024}}

// seedUtilHistory writes days of utilisation rows ending yesterday for one service, at the given use of its
// reservation (percent).
func seedUtilHistory(t *testing.T, st *store.Store, svc store.ServiceRow, days int, cpuPct, memPct float64) {
	t.Helper()
	cpu, mem := seededSizes[svc.Service.Key.Service][0], seededSizes[svc.Service.Key.Service][1]
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 1; i <= days; i++ {
		row := store.ServiceUtilRow{Key: svc.Service.Key, CPUAvg: &cpuPct, CPUP95: &cpuPct, MemAvg: &memPct, MemP95: &memPct,
			CPUReservedUnits: &cpu, MemReservedMiB: &mem}
		if err := st.UpsertServiceUtilDay(t.Context(), today.AddDate(0, 0, -i), []store.ServiceUtilRow{row}); err != nil {
			t.Fatal(err)
		}
	}
}

func serviceRow(t *testing.T, st *store.Store, name string) store.ServiceRow {
	t.Helper()
	for _, row := range visibleServices(t, st) {
		if row.Service.Key.Service == name {
			return row
		}
	}
	t.Fatalf("service %q not in the estate", name)
	return store.ServiceRow{}
}

func TestAnalyticsCost_SpendUsesRegionPricesAndAddsUp(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	got := getCost(t, login(t, srv, "sam", testPassword), "")

	if edge := costService(t, got, "edge-nginx"); edge.Tasks != 2 || !approx(edge.Monthly, 20.72) {
		t.Errorf("edge-nginx = %d tasks %.4f/month, want 2 tasks 20.72 (eu-central-1 list price)", edge.Tasks, edge.Monthly)
	}
	if got.Tab != "cost" || got.PricesAsOf != "2026-10-19" || got.Window.RangeApplies {
		t.Errorf("tab %q prices_as_of %q window %+v", got.Tab, got.PricesAsOf, got.Window)
	}
	total := got.KPIs.MonthlyTotal
	if total <= 0 || !approx(sumGroups(got.ByCluster), total) || !approx(sumGroups(got.ByAccount), total) ||
		!approx(sumGroups(got.ByTag), total) || !approx(sumServices(got.Rightsizing.Services), total) {
		t.Errorf("total %.4f; by cluster %.4f, account %.4f, tag %.4f, services %.4f; want all equal",
			total, sumGroups(got.ByCluster), sumGroups(got.ByAccount), sumGroups(got.ByTag), sumServices(got.Rightsizing.Services))
	}
	for i := 1; i < len(got.ByCluster); i++ {
		if got.ByCluster[i].Monthly > got.ByCluster[i-1].Monthly {
			t.Errorf("by_cluster not sorted by monthly descending at %d", i)
		}
	}
	if len(got.Rightsizing.Services) != 15 {
		t.Errorf("%d services, want all 15 (no row cap, the totals sum them)", len(got.Rightsizing.Services))
	}
}

func TestAnalyticsCost_NonAdminTotalsOnlyVisibleServices(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	sam := getCost(t, login(t, srv, "sam", testPassword), "")
	omer := getCost(t, login(t, srv, "omer", testPassword), "")

	for _, svc := range omer.Rightsizing.Services {
		if svc.Tag.Name == "Prod" {
			t.Errorf("omer sees Prod service %s", svc.Service)
		}
	}
	for _, group := range omer.ByTag {
		if group.Label == "Prod" {
			t.Errorf("omer's by_tag lists Prod: %+v", group)
		}
	}
	if omer.KPIs.MonthlyTotal <= 0 || !approx(omer.KPIs.MonthlyTotal, sumServices(omer.Rightsizing.Services)) {
		t.Errorf("omer total %.4f, sum of his services %.4f", omer.KPIs.MonthlyTotal, sumServices(omer.Rightsizing.Services))
	}
	if omer.KPIs.MonthlyTotal >= sam.KPIs.MonthlyTotal {
		t.Errorf("omer total %.4f not below sam's %.4f", omer.KPIs.MonthlyTotal, sam.KPIs.MonthlyTotal)
	}
	for _, group := range sam.ByTag {
		narrowed := getCost(t, login(t, srv, "sam", testPassword), "?tag="+group.Key)
		if !approx(narrowed.KPIs.MonthlyTotal, group.Monthly) || !approx(narrowed.KPIs.MonthlyTotal, sumServices(narrowed.Rightsizing.Services)) {
			t.Errorf("?tag=%s total %.4f, want that tag's %.4f", group.Key, narrowed.KPIs.MonthlyTotal, group.Monthly)
		}
	}
}

func TestAnalyticsCost_EC2TaskHasNoDollars(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	before := getCost(t, login(t, srv, "sam", testPassword), "")

	srv2, fk2, st2, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st2)
	forceProviders(fk2, "FARGATE")
	editRunningTasks(fk2, "web-frontend", func(index int, task *ecstypes.Task) {
		if index == 0 {
			task.LaunchType = ecstypes.LaunchTypeEc2
			task.CapacityProviderName = nil
		}
	})
	pollAll(t, st2, fk2)
	after := getCost(t, login(t, srv2, "sam", testPassword), "")

	if after.KPIs.UnpricedTasks != before.KPIs.UnpricedTasks+1 {
		t.Errorf("unpriced_tasks %d, want %d", after.KPIs.UnpricedTasks, before.KPIs.UnpricedTasks+1)
	}
	oneTask := costService(t, before, "web-frontend").Monthly / 2
	if !approx(before.KPIs.MonthlyTotal-after.KPIs.MonthlyTotal, oneTask) {
		t.Errorf("total dropped by %.4f, want one task's share %.4f", before.KPIs.MonthlyTotal-after.KPIs.MonthlyTotal, oneTask)
	}
	web := costService(t, after, "web-frontend")
	if web.Status != "not_fargate" || len(web.Steps) != 0 || web.Steps == nil {
		t.Errorf("web-frontend status %q steps %#v, want not_fargate and an empty list", web.Status, web.Steps)
	}
	if !approx(web.Monthly, oneTask) {
		t.Errorf("web-frontend monthly %.4f, want only its Fargate task %.4f", web.Monthly, oneTask)
	}
}

func TestAnalyticsCost_SpotIsReportedSeparately(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	editRunningTasks(fk, "edge-nginx", func(index int, task *ecstypes.Task) {
		if index == 0 {
			spot := "FARGATE_SPOT"
			task.CapacityProviderName = &spot
		}
	})
	pollAll(t, st, fk)
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if got.KPIs.SpotTasks != 1 || !approx(got.KPIs.SpotMonthly, 10.36) {
		t.Errorf("spot %d tasks %.4f/month, want 1 task at its list price 10.36", got.KPIs.SpotTasks, got.KPIs.SpotMonthly)
	}
	if !approx(costService(t, got, "edge-nginx").Monthly, 20.72) || got.KPIs.MonthlyTotal < got.KPIs.SpotMonthly {
		t.Errorf("spot must stay inside the total: edge-nginx %.4f, total %.4f", costService(t, got, "edge-nginx").Monthly, got.KPIs.MonthlyTotal)
	}
	if edge := costService(t, got, "edge-nginx"); edge.SpotTasks != 1 {
		t.Errorf("edge-nginx spot_tasks %d, want 1", edge.SpotTasks)
	}
}

func TestAnalyticsCost_UnknownRegionIsNotPriced(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	baseline := getCost(t, login(t, srv, "sam", testPassword), "")
	// The fake estate only hosts priced regions, so move one region's rows to a region without a rate.
	for _, table := range []string{"clusters", "services", "tasks", "task_definitions"} {
		if _, err := st.Pool.Exec(t.Context(), `update `+table+` set region = 'ap-south-1' where region = 'eu-west-1'`); err != nil {
			t.Fatal(err)
		}
	}
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if len(got.KPIs.UnpricedRegions) != 1 || got.KPIs.UnpricedRegions[0] != "ap-south-1" {
		t.Fatalf("unpriced_regions = %v, want [ap-south-1]", got.KPIs.UnpricedRegions)
	}
	catalog := costService(t, got, "catalog-api")
	if catalog.Status != "no_price" || catalog.Monthly != 0 {
		t.Errorf("catalog-api status %q monthly %.4f, want no_price and no dollars (no other region's price)", catalog.Status, catalog.Monthly)
	}
	if got.KPIs.UnpricedTasks <= baseline.KPIs.UnpricedTasks || got.KPIs.MonthlyTotal >= baseline.KPIs.MonthlyTotal {
		t.Errorf("unpriced %d -> %d, total %.2f -> %.2f", baseline.KPIs.UnpricedTasks, got.KPIs.UnpricedTasks, baseline.KPIs.MonthlyTotal, got.KPIs.MonthlyTotal)
	}
	found := false
	for _, group := range got.ByCluster {
		if group.Label == "eu-apps" {
			found = group.Monthly == 0 && group.UnpricedTasks > 0
		}
	}
	if !found {
		t.Errorf("by_cluster must list eu-apps with only unpriced tasks: %+v", got.ByCluster)
	}
}

func TestAnalyticsCost_RollupOffShowsSpendButNoSuggestions(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		withCostAnalytics(s)
		s.Cfg.Analytics.Util = false
	})
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	seedUtilHistory(t, st, serviceRow(t, st, "report-worker"), 30, 5, 5)
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if got.Rightsizing.State != "rollup_off" || got.KPIs.MonthlyTotal <= 0 {
		t.Errorf("state %q total %.2f, want rollup_off with spend still shown", got.Rightsizing.State, got.KPIs.MonthlyTotal)
	}
	for _, svc := range got.Rightsizing.Services {
		if svc.Status != "rollup_off" || len(svc.Steps) != 0 {
			t.Errorf("%s status %q steps %d, want rollup_off and none", svc.Service, svc.Status, len(svc.Steps))
		}
	}
}

func TestAnalyticsCost_ShortRetentionState(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, func(s *Server) {
		withCostAnalytics(s)
		s.Cfg.Retention.UtilDaily = 7 * 24 * time.Hour
	})
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if got.Rightsizing.State != "retention_short" || got.Rightsizing.MinHistoryDays != 14 || got.Rightsizing.DefaultHeadroomPct != 30 {
		t.Errorf("rightsizing = state %q min %d default %d", got.Rightsizing.State, got.Rightsizing.MinHistoryDays, got.Rightsizing.DefaultHeadroomPct)
	}
}

func TestAnalyticsCost_NewServiceHasNoSuggestion(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	seedUtilHistory(t, st, serviceRow(t, st, "report-worker"), 5, 5, 5)
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	if svc := costService(t, got, "report-worker"); svc.Status != "short_history" || len(svc.Steps) != 0 || svc.HistoryDays != 5 {
		t.Errorf("report-worker status %q steps %d history %d, want short_history, none, 5", svc.Status, len(svc.Steps), svc.HistoryDays)
	}
}

func TestAnalyticsCost_SuggestionAndCanEdit(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	seedUtilHistory(t, st, serviceRow(t, st, "report-worker"), 30, 5, 5)
	seedUtilHistory(t, st, serviceRow(t, st, "payments-api"), 30, 5, 5)

	dana := getCost(t, login(t, srv, "dana", testPassword), "")
	worker := costService(t, dana, "report-worker")
	if worker.Status != "ok" || len(worker.Steps) == 0 || worker.Steps[0].FromHeadroomPct != 10 || worker.Steps[0].Suggestion == nil {
		t.Fatalf("report-worker = status %q steps %+v, want ok with a ladder starting at 10 and a suggestion", worker.Status, worker.Steps)
	}
	suggestion := worker.Steps[0].Suggestion
	if suggestion.SavingMonthly <= 0 || suggestion.CPUUnits >= *worker.CPUUnits {
		t.Errorf("suggestion %+v against current %d/%d", suggestion, *worker.CPUUnits, *worker.MemoryMiB)
	}
	if worker.CPUUsedShare == nil || math.Abs(*worker.CPUUsedShare-0.05) > 1e-6 || !worker.CanEdit {
		t.Errorf("cpu_used_share %v can_edit %v, want 0.05 and true on Dev", worker.CPUUsedShare, worker.CanEdit)
	}
	if payments := costService(t, dana, "payments-api"); payments.CanEdit {
		t.Errorf("dana can_edit on a Prod service")
	}
	operator := authz.Presets["Operator"]
	wantOmer := operator.Has(authz.View) && operator.Has(authz.Edit)
	if omer := costService(t, getCost(t, login(t, srv, "omer", testPassword), ""), "report-worker"); omer.CanEdit != wantOmer {
		t.Errorf("omer (Operator on Dev) can_edit = %v, want %v from authz.Can(Edit)", omer.CanEdit, wantOmer)
	}
}

func TestAnalyticsCost_UnknownFloorsGetNoSuggestion(t *testing.T) {
	srv, fk, st, logs := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	forceProviders(fk, "FARGATE")
	pollAll(t, st, fk)
	seedUtilHistory(t, st, serviceRow(t, st, "report-worker"), 30, 5, 5)
	seedUtilHistory(t, st, serviceRow(t, st, "email-sender"), 30, 5, 5)
	// report-worker loses its container definitions entirely (floors unknown); email-sender's are unparsable.
	if _, err := st.Pool.Exec(t.Context(), `update task_definitions set raw = raw - 'ContainerDefinitions'
		where arn in (select task_definition_arn from services where service_name = 'report-worker')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(t.Context(), `update task_definitions set raw = jsonb_set(raw, '{ContainerDefinitions}', '"secret-token"')
		where arn in (select task_definition_arn from services where service_name = 'email-sender')`); err != nil {
		t.Fatal(err)
	}
	got := getCost(t, login(t, srv, "sam", testPassword), "")
	for _, name := range []string{"report-worker", "email-sender"} {
		svc := costService(t, got, name)
		if svc.Status != "no_size" || len(svc.Steps) != 0 || svc.Monthly <= 0 {
			t.Errorf("%s status %q steps %d monthly %.2f, want no_size, no steps, spend kept", name, svc.Status, len(svc.Steps), svc.Monthly)
		}
	}
	if strings.Contains(logs.String(), "secret-token") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("want a warning for the unparsable definition without its contents in the log:\n%s", logs.String())
	}
}

func TestAnalyticsCost_RejectsBadRange(t *testing.T) {
	srv, _, st, _ := newServerCustom(t, true, withCostAnalytics)
	seedMockupTags(t, st)
	c := login(t, srv, "sam", testPassword)
	if resp := c.get("/api/analytics/cost?range=1y"); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("range=1y status %d, want 422", resp.StatusCode)
	}
	if got := getCost(t, c, "?range=24h"); got.Window.Requested != "24h" || got.Window.RangeApplies {
		t.Errorf("window %+v, want requested 24h and range_applies false", got.Window)
	}
}
