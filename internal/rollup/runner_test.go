package rollup_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const homelab = "482109375512"

var (
	target = awsx.Target{AccountID: homelab, Region: "us-east-1"}
	// 10:00 UTC on 2026-03-10: today is the 10th and yesterday, the 9th, is the last complete day.
	now = time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
)

func day(offset int) time.Time {
	return time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

type harness struct {
	st     *store.Store
	fake   *fake.Fake
	runner *rollup.Runner
	logs   *bytes.Buffer
	// regions the account is registered and polled in
	regions []string
}

// newHarness fills the store from the fake mockup estate and returns a runner clocked at `now`.
func newHarness(t *testing.T) *harness { return newHarnessRegions(t, target.Region) }

func newHarnessRegions(t *testing.T, regions ...string) *harness {
	t.Helper()
	ctx := context.Background()
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return now }))
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "homelab", AccountID: homelab,
		RoleARN: "arn:aws:iam::" + homelab + ":role/x", ExternalID: "e", Regions: regions, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, region := range regions {
		if err := poller.PollOnce(ctx, st, f, poller.Config{MaxInFlight: 4}, awsx.Target{AccountID: homelab, Region: region}); err != nil {
			t.Fatalf("poll mockup estate %s: %v", region, err)
		}
	}
	logs := &bytes.Buffer{}
	return &harness{st: st, fake: f, logs: logs, regions: regions, runner: &rollup.Runner{
		Store: st, Clients: f, Now: func() time.Time { return now }, UtilEnabled: true,
		SampleInterval: 5 * time.Minute, Log: slog.New(slog.NewTextHandler(logs, nil)),
	}}
}

func (h *harness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.st.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *harness) sample(t *testing.T, ts time.Time, service string, running, desired int) {
	t.Helper()
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelab, Region: target.Region, Cluster: "core-services"}, Service: service}
	if err := h.st.InsertCountsHistory(context.Background(), ts, []model.Service{{Key: key, Running: running, Desired: desired}}); err != nil {
		t.Fatal(err)
	}
}

func availabilityDays(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.st.Pool.Query(context.Background(),
		`select distinct to_char(day, 'YYYY-MM-DD') from service_availability_daily order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		days = append(days, d)
	}
	return days
}

func TestRunner_BackfillsSixDaysOnEmptyTable(t *testing.T) {
	h := newHarness(t)
	for offset := -8; offset <= 0; offset++ { // noon of today-8 .. today
		h.sample(t, day(offset).Add(12*time.Hour), "gateway", 1, 1)
	}
	res, err := h.runner.RunDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-03-04", "2026-03-05", "2026-03-06", "2026-03-07", "2026-03-08", "2026-03-09"}
	got := availabilityDays(t, h)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("days rolled up = %v, want the six complete days %v (not today, nothing older)", got, want)
	}
	if res[store.TableServiceAvailabilityDaily] != 6 {
		t.Errorf("result = %v, want 6 availability rows", res)
	}
	since, err := h.st.DailyDataSince(context.Background())
	if err != nil || since[store.TableServiceAvailabilityDaily] == nil || !since[store.TableServiceAvailabilityDaily].Equal(day(-6)) {
		t.Errorf("data since = %v (err %v), want %s", since[store.TableServiceAvailabilityDaily], err, day(-6))
	}
}

func TestRunner_RerunIsIdempotentAndContinuesFromWatermark(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.sample(t, day(-3).Add(time.Hour), "gateway", 1, 1)
	if _, err := h.runner.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	rows := h.count(t, `select count(*) from service_availability_daily`)

	again, err := h.runner.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again[store.TableServiceAvailabilityDaily] != 0 || h.count(t, `select count(*) from service_availability_daily`) != rows {
		t.Errorf("second run result = %v, rows %d -> %d, want no change", again,
			rows, h.count(t, `select count(*) from service_availability_daily`))
	}

	// A later day arrives; only that day is rolled up.
	h.sample(t, day(-1).Add(time.Hour), "gateway", 1, 1)
	third, err := h.runner.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third[store.TableServiceAvailabilityDaily] != 1 {
		t.Errorf("third run result = %v, want 1 row for yesterday", third)
	}
}

func TestRunner_LogsGapAndDoesNotFabricateRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The last rollup was 10 days ago; the leader was down since.
	if _, err := h.st.Pool.Exec(ctx,
		`insert into service_availability_daily values ($1, $2, 'core-services', 'gateway', $3::date, 10, 10, 0, 0)`,
		homelab, target.Region, day(-10).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	h.sample(t, day(-8).Add(time.Hour), "gateway", 0, 1) // older than the backfill window, still present
	h.sample(t, day(-2).Add(time.Hour), "gateway", 1, 1)
	if _, err := h.runner.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if got := availabilityDays(t, h); strings.Join(got, ",") != "2026-02-28,2026-03-08" {
		t.Errorf("days = %v, want the old row and 2026-03-08 only (no fabricated rows for the gap)", got)
	}
	if !strings.Contains(h.logs.String(), "gap") {
		t.Errorf("log does not mention the gap:\n%s", h.logs.String())
	}
}

// insightsServices counts the stored services whose cluster has Container Insights on, across the harness regions.
func insightsServices(t *testing.T, h *harness) (withInsights, without int) {
	t.Helper()
	ctx := context.Background()
	for _, region := range h.regions {
		filter := store.TargetFilter{AccountID: homelab, Region: region}
		clusters, err := h.st.ListClusters(ctx, filter, store.Visibility{Admin: true})
		if err != nil {
			t.Fatal(err)
		}
		on := map[string]bool{}
		for _, c := range clusters {
			on[c.Key.Cluster] = metrics.InsightsMode(c.Cluster) != "disabled"
		}
		services, err := h.st.ListServices(ctx, filter, "", store.Visibility{Admin: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range services {
			if on[s.Key.Cluster] {
				withInsights++
			} else {
				without++
			}
		}
	}
	return withInsights, without
}

func getMetricDataCalls(h *harness) uint64 {
	return h.fake.CallCounts()[homelab+"|us-east-1|cloudwatch|GetMetricData"]
}

func TestUtilDay_OneLogicalBatchPerTarget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Scale up to 600 insights services so the batch has to be chunked at 500 queries.
	extra := make([]model.Service, 0, 600)
	for i := 0; i < 600; i++ {
		key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelab, Region: target.Region, Cluster: "core-services"}, Service: fmt.Sprintf("bulk-%03d", i)}
		extra = append(extra, model.Service{Key: key, Running: 1, Desired: 1, PolledAt: now})
	}
	if _, err := h.st.UpsertServices(ctx, extra); err != nil {
		t.Fatal(err)
	}
	withInsights, _ := insightsServices(t, h)
	h.fake.ResetCallCounts()

	res, err := h.runner.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queries := withInsights * 6
	wantCalls := uint64((queries + 499) / 500)
	if got := getMetricDataCalls(h); got != wantCalls {
		t.Errorf("GetMetricData calls = %d, want %d for %d queries", got, wantCalls, queries)
	}
	if res[store.TableServiceUtilDaily] != withInsights || h.count(t, `select count(*) from service_util_daily where day = $1::date`, day(-1).Format(time.DateOnly)) != withInsights {
		t.Errorf("util rows = %d (result %v), want %d for yesterday", h.count(t, `select count(*) from service_util_daily`), res, withInsights)
	}
}

func TestUtilDay_WritesPercentagesAndReservedUnits(t *testing.T) {
	h := newHarness(t)
	if _, err := h.runner.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var bad, withReserved int
	err := h.st.Pool.QueryRow(context.Background(),
		`select count(*) filter (where cpu_avg is null or cpu_p95 is null or mem_avg is null or mem_p95 is null
		        or cpu_avg not between 0 and 100 or mem_p95 not between 0 and 100),
		   count(*) filter (where cpu_reserved_units > 0 and mem_reserved_mib > 0)
		 from service_util_daily`).Scan(&bad, &withReserved)
	if err != nil {
		t.Fatal(err)
	}
	if bad != 0 || withReserved == 0 {
		t.Errorf("rows with missing or out-of-range percentages = %d, rows with reserved units = %d (want 0 and > 0)", bad, withReserved)
	}
	var viaTaskDef int
	if err := h.st.Pool.QueryRow(context.Background(),
		`select count(*) from service_util_daily u
		 join services s using (account_id, region, cluster_name, service_name)
		 join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 where u.cpu_reserved_units = td.cpu::int and u.mem_reserved_mib = td.memory::int`).Scan(&viaTaskDef); err != nil || viaTaskDef != withReserved {
		t.Errorf("rows matching the current task definition = %d (err %v), want %d", viaTaskDef, err, withReserved)
	}
}

func TestUtilDay_SkipsClustersWithoutInsights(t *testing.T) {
	h := newHarnessRegions(t, "us-east-1", "eu-central-1") // edge-proxy lives in eu-central-1
	withInsights, without := insightsServices(t, h)
	if without == 0 {
		t.Fatal("mockup estate has no service in a cluster without Container Insights; the test needs one")
	}
	if _, err := h.runner.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.count(t, `select count(*) from service_util_daily`); got != withInsights {
		t.Errorf("util rows = %d, want %d (services without Insights get none)", got, withInsights)
	}
	if got := h.count(t, `select count(*) from service_util_daily where cluster_name = 'edge-proxy'`); got != 0 {
		t.Errorf("edge-proxy has Container Insights disabled but got %d rows", got)
	}
}

func TestUtilDay_ThrottleLeavesDayUndone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.ThrottleCloudWatch = 1
	if _, err := h.runner.RunDue(ctx); err != nil {
		t.Fatalf("a throttle is retried next tick, not an error: %v", err)
	}
	if got := h.count(t, `select count(*) from service_util_daily`); got != 0 {
		t.Fatalf("util rows after throttle = %d, want 0", got)
	}
	res, err := h.runner.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res[store.TableServiceUtilDaily] == 0 || h.count(t, `select count(*) from service_util_daily`) == 0 {
		t.Errorf("second run result = %v, want util rows written", res)
	}
}

func TestUtilDay_DoneDayIsNotFetchedAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.runner.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	h.fake.ResetCallCounts()
	rows := h.count(t, `select count(*) from service_util_daily`)
	res, err := h.runner.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if getMetricDataCalls(h) != 0 || res[store.TableServiceUtilDaily] != 0 || h.count(t, `select count(*) from service_util_daily`) != rows {
		t.Errorf("second run made %d GetMetricData calls, result %v", getMetricDataCalls(h), res)
	}
}

func TestRunner_UtilDisabledWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.runner.UtilEnabled = false
	h.fake.ResetCallCounts()
	if _, err := h.runner.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := getMetricDataCalls(h); got != 0 {
		t.Errorf("GetMetricData calls = %d with util disabled, want 0", got)
	}
	if got := h.count(t, `select count(*) from service_util_daily`); got != 0 {
		t.Errorf("util rows = %d with util disabled, want 0", got)
	}
}
