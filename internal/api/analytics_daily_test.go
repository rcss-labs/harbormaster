package api

import (
	"encoding/json"
	"io"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// withDailyRetention gives the server the production defaults of the daily rollup tables.
func withDailyRetention(s *Server) {
	withAnalyticsRetention(s)
	s.Cfg.Retention.DeploymentHistory = 2160 * time.Hour
	s.Cfg.Retention.AvailabilityDaily = 8760 * time.Hour
	s.Cfg.Retention.CapacityDaily = 8760 * time.Hour
	s.Cfg.Retention.UtilDaily = 2160 * time.Hour
}

func utcToday() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// seedAvailabilityDays writes one row per service and day for `daysAgo` in [first, last]. Rows of the last 30 days
// have 90 of 100 samples ok and 10 minutes below desired; older rows are fully available.
func seedAvailabilityDays(t *testing.T, st *store.Store, first, last int) {
	t.Helper()
	_, err := st.Pool.Exec(t.Context(),
		`insert into service_availability_daily (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops)
		 select s.account_id, s.region, s.cluster_name, s.service_name, $1::date - d, 100,
		   case when d <= 30 then 90 else 100 end, case when d <= 30 then 10 else 0 end, 0
		 from services s, generate_series($2::int, $3::int) d`, utcToday(), first, last)
	if err != nil {
		t.Fatal(err)
	}
}

func lineByKey(t *testing.T, series SeriesDTO, key string) SeriesLineDTO {
	t.Helper()
	for _, line := range series.Series {
		if line.Key == key {
			return line
		}
	}
	t.Fatalf("no line %q in %+v", key, series.Series)
	return SeriesLineDTO{}
}

func valueAt(t *testing.T, line SeriesLineDTO, index int) float64 {
	t.Helper()
	if index >= len(line.Values) || line.Values[index] == nil {
		t.Fatalf("line %s has no value at %d (len %d)", line.Key, index, len(line.Values))
	}
	return *line.Values[index]
}

func close2(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAnalyticsReliability_NinetyDayPanelsFromDaily(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	seedAvailabilityDays(t, st, 1, 60)
	services := len(visibleServices(t, st))

	admin := getReliability(t, login(t, srv, "sam", testPassword), "?range=7d")
	for name, available := range map[string]bool{"availability_90d": admin.Availability90d.Available, "below_desired_90d": admin.BelowDesired90d.Available, "getting_worse": admin.GettingWorse.Available} {
		if !available {
			t.Fatalf("%s is unavailable although the rollup has data", name)
		}
	}
	if admin.Availability90d.Data == nil {
		t.Fatal("available panel lacks data")
	}
	today := utcToday()
	if since := admin.Availability90d.DataSince; since == nil || !since.Equal(today.AddDate(0, 0, -60)) {
		t.Errorf("data_since = %v, want %v", since, today.AddDate(0, 0, -60))
	}

	availability := admin.Availability90d.Data
	if len(availability.Timestamps) != 90 || !availability.Timestamps[89].Equal(today) || !availability.Timestamps[0].Equal(today.AddDate(0, 0, -89)) {
		t.Fatalf("availability_90d timestamps = %d from %v to %v, want 90 daily points ending today", len(availability.Timestamps), availability.Timestamps[0], availability.Timestamps[len(availability.Timestamps)-1])
	}
	line := lineByKey(t, *availability, "availability")
	if got := valueAt(t, line, 88); !close2(got, 90) { // yesterday: 90 of 100 samples, as a percentage
		t.Errorf("availability yesterday = %v, want 90 (percent)", got)
	}
	if got := valueAt(t, line, 89-40); !close2(got, 100) {
		t.Errorf("availability 40 days ago = %v, want 100", got)
	}
	if line.Values[89] != nil || line.Values[0] != nil {
		t.Errorf("today and days before the rollup started must be null, got %v and %v", line.Values[89], line.Values[0])
	}

	below := lineByKey(t, *admin.BelowDesired90d.Data, "minutes_below")
	if got := valueAt(t, below, 88); !close2(got, float64(10*services)) {
		t.Errorf("minutes below yesterday = %v, want %d (10 per service, summed)", got, 10*services)
	}

	worse := admin.GettingWorse.Data
	if worse.Total != services || len(worse.Rows) != min(services, 50) {
		t.Fatalf("getting_worse total/rows = %d/%d, want %d services", worse.Total, len(worse.Rows), services)
	}
	row := worse.Rows[0]
	if !close2(row.Previous, 1) || !close2(row.Current, 0.9) || !close2(row.ChangePts, -10) {
		t.Errorf("row = previous %v current %v change_pts %v, want 1, 0.9 as fractions and -10 percentage points", row.Previous, row.Current, row.ChangePts)
	}

	sources := map[string]AnalyticsSourceDTO{}
	for _, src := range admin.Sources {
		sources[src.Table] = src
	}
	if src, ok := sources["service_availability_daily"]; !ok || src.Kept != "8760h" || src.Setting == nil || *src.Setting != "HM_RETENTION_AVAILABILITY_DAILY" {
		t.Errorf("service_availability_daily source = %+v (present %v)", src, ok)
	}

	// A viewer with fewer tags sees fewer services in the table and a smaller sum of minutes.
	omer := getReliability(t, login(t, srv, "omer", testPassword), "?range=7d")
	if omer.GettingWorse.Data == nil || omer.GettingWorse.Data.Total == 0 || omer.GettingWorse.Data.Total >= worse.Total {
		t.Errorf("omer sees %v services getting worse, admin %d; want a non-empty strict subset", omer.GettingWorse.Data, worse.Total)
	}
	omerBelow := lineByKey(t, *omer.BelowDesired90d.Data, "minutes_below")
	if valueAt(t, omerBelow, 88) >= valueAt(t, below, 88) {
		t.Errorf("omer minutes below %v not smaller than admin %v", valueAt(t, omerBelow, 88), valueAt(t, below, 88))
	}
}

func TestAnalyticsCapacity_ReservedVsUsedFromDaily(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	// Every cluster averages 2 vCPU, 4 GiB, 3 tasks (1 on spot); every service reserves 1 vCPU / 2 GiB and uses
	// 25% (p95 50%) of the CPU and p95 60% of the memory.
	_, err := st.Pool.Exec(t.Context(),
		`insert into cluster_capacity_daily (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
		 select c.account_id, c.region, c.cluster_name, $1::date - d, 4, 8, 3, 16, 5, 12, 4, 4
		 from clusters c, generate_series(1, 5) d`, utcToday())
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool.Exec(t.Context(),
		`insert into service_util_daily (account_id, region, cluster_name, service_name, day, cpu_avg, cpu_p95, mem_avg, mem_p95, cpu_reserved_units, mem_reserved_mib)
		 select s.account_id, s.region, s.cluster_name, s.service_name, $1::date - d, 25, 50, 30, 60, 1024, 2048
		 from services s, generate_series(1, 5) d`, utcToday())
	if err != nil {
		t.Fatal(err)
	}
	services := visibleServices(t, st)
	clusters, err := st.ListClusters(t.Context(), store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}

	got := getCapacity(t, login(t, srv, "sam", testPassword), "")
	for name, p := range map[string]bool{"reserved_vs_used_90d": got.ReservedVsUsed90d.Available, "spot_share_90d": got.SpotShare90d.Available, "memory_90d": got.Memory90d.Available, "over_reserved": got.OverReserved.Available} {
		if !p {
			t.Fatalf("%s is unavailable although its rollup has data", name)
		}
	}
	reserved := got.ReservedVsUsed90d.Data
	if len(reserved.Timestamps) != 90 || !reserved.Timestamps[89].Equal(utcToday()) {
		t.Fatalf("reserved_vs_used_90d has %d timestamps ending %v, want 90 daily ending today", len(reserved.Timestamps), reserved.Timestamps[len(reserved.Timestamps)-1])
	}
	n, c := float64(len(services)), float64(len(clusters))
	for key, want := range map[string]float64{"reserved": 2 * c, "used_avg": 0.25 * n, "used_p95": 0.5 * n} {
		if v := valueAt(t, lineByKey(t, *reserved, key), 88); !close2(v, want) {
			t.Errorf("reserved_vs_used %s yesterday = %v, want %v", key, v, want)
		}
	}
	if v := valueAt(t, lineByKey(t, *got.SpotShare90d.Data, "spot_share"), 88); !close2(v, 100.0/3) {
		t.Errorf("spot_share = %v, want 33.3 (percent of running tasks)", v)
	}
	memory := got.Memory90d.Data
	if v := valueAt(t, lineByKey(t, *memory, "reserved"), 88); !close2(v, 4*c) {
		t.Errorf("memory reserved = %v, want %v GiB", v, 4*c)
	}
	if v := valueAt(t, lineByKey(t, *memory, "used_p95"), 88); !close2(v, 1.2*n) {
		t.Errorf("memory used_p95 = %v, want %v GiB", v, 1.2*n)
	}

	over := got.OverReserved.Data
	if over.Total == 0 || len(over.Rows) == 0 {
		t.Fatalf("over_reserved = %+v, want services with running tasks", over)
	}
	top := over.Rows[0]
	if !close2(top.ReservedVCPU, 1) || !close2(top.UsedP95VCPU, 0.5) || !close2(top.ReservedMemGiB, 2) || !close2(top.UsedP95MemGiB, 1.2) {
		t.Errorf("top row = %+v, want per-task sizes 1/0.5 vCPU and 2/1.2 GiB", top)
	}
	for i := 1; i < len(over.Rows); i++ {
		if over.Rows[i].WastedVCPU > over.Rows[i-1].WastedVCPU {
			t.Fatalf("over_reserved not sorted by unused vCPU: %v before %v", over.Rows[i-1].WastedVCPU, over.Rows[i].WastedVCPU)
		}
	}
	if top.WastedVCPU <= 0 {
		t.Errorf("unused vCPU = %v, want (reserved - p95) x running > 0", top.WastedVCPU)
	}
	sources := map[string]AnalyticsSourceDTO{}
	for _, src := range got.Sources {
		sources[src.Table] = src
	}
	if src := sources["cluster_capacity_daily"]; src.Kept != "8760h" || src.Setting == nil || *src.Setting != "HM_RETENTION_CAPACITY_DAILY" {
		t.Errorf("cluster_capacity_daily source = %+v", src)
	}
	if src := sources["service_util_daily"]; src.Kept != "2160h" || src.Setting == nil || *src.Setting != "HM_RETENTION_UTIL_DAILY" {
		t.Errorf("service_util_daily source = %+v", src)
	}
}

func TestAnalyticsDelivery_FailureReasonsAndRecentFailures(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	services := visibleServices(t, st)
	byID := map[string]model.ServiceKey{}
	for _, svc := range services {
		byID[svc.Key.ID()] = svc.Key
	}
	// omer may see staging-web but not payments-api.
	payments, staging := byID[paymentsID()], byID[stagingWebID()]
	if payments.Service == "" || staging.Service == "" {
		t.Fatalf("estate lacks payments (%q) or staging web (%q)", payments.Service, staging.Service)
	}
	now := time.Now().UTC()
	record := func(key model.ServiceKey, id string, revision int, started time.Time, reason, detail string) {
		t.Helper()
		if _, err := st.InsertDeploymentIfNew(t.Context(), store.DeploymentRecord{Key: key, DeploymentID: id, Revision: revision, Kind: "deploy", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		var failure *store.FailureInfo
		if reason != "" {
			failure = &store.FailureInfo{Group: reason, Detail: detail}
		}
		if _, err := st.FinishDeployment(t.Context(), key, id, "failed", started.Add(4*time.Minute), failure); err != nil {
			t.Fatal(err)
		}
	}
	admin := login(t, srv, "sam", testPassword)
	getDelivery := func(c *apiClient) DeliveryDTO {
		var out DeliveryDTO
		decode(t, c.get("/api/analytics/delivery?range=7d"), &out)
		return out
	}

	// A failure recorded before reasons existed does not make the panels available.
	record(payments, "legacy", 2, now.Add(-30*time.Hour), "", "")
	before := getDelivery(admin)
	if before.FailureReasons.Available || before.RecentFailures.Available {
		t.Fatalf("panels available (%v, %v) with only reason-less failures", before.FailureReasons.Available, before.RecentFailures.Available)
	}

	first := now.Add(-26 * time.Hour)
	record(payments, "cb-1", 5, first, "circuit_breaker", "ECS deployment circuit breaker: tasks failed to start.")
	record(staging, "cb-2", 3, now.Add(-5*time.Hour), "circuit_breaker", "ECS deployment circuit breaker: tasks failed to start.")
	record(payments, "hc-1", 7, now.Add(-2*time.Hour), "health_check", "Task failed ELB health checks")

	got := getDelivery(admin)
	if !got.FailureReasons.Available || !got.RecentFailures.Available {
		t.Fatalf("failure panels unavailable after reasons were recorded: %+v %+v", got.FailureReasons, got.RecentFailures)
	}
	if since := got.FailureReasons.DataSince; since == nil || since.Sub(first).Abs() > time.Second {
		t.Errorf("data_since = %v, want the first rollout with a reason (%v)", since, first)
	}
	reasons := got.FailureReasons.Data
	if len(reasons.Timestamps) != 42 {
		t.Fatalf("failure_reasons has %d buckets, want the window's 42", len(reasons.Timestamps))
	}
	sum := func(line SeriesLineDTO) (total float64) {
		for _, v := range line.Values {
			if v == nil {
				t.Fatalf("count line %s has a null bucket", line.Key)
			}
			total += *v
		}
		return total
	}
	if n := sum(lineByKey(t, *reasons, "circuit_breaker")); n != 2 {
		t.Errorf("circuit_breaker failures = %v, want 2", n)
	}
	if n := sum(lineByKey(t, *reasons, "health_check")); n != 1 {
		t.Errorf("health_check failures = %v, want 1", n)
	}

	table := got.RecentFailures.Data
	if table.Total != 3 || len(table.Rows) != 3 {
		t.Fatalf("recent failures total/rows = %d/%d, want 3 (the reason-less one is left out)", table.Total, len(table.Rows))
	}
	if newest := table.Rows[0]; newest.Reason != "health_check" || newest.ToRevision != 7 || newest.Detail == "" || newest.RanForS == nil || !close2(*newest.RanForS, 240) {
		t.Errorf("newest = %+v, want health_check to :7 ran 240s with detail", newest)
	}
	if oldest := table.Rows[2]; oldest.ServiceID != payments.ID() || oldest.FromRevision == nil || *oldest.FromRevision != 2 || oldest.ToRevision != 5 {
		t.Errorf("oldest = %+v, want from :2 (the legacy failure) to :5 on %s", oldest, payments.ID())
	}

	omer := getDelivery(login(t, srv, "omer", testPassword))
	if omer.RecentFailures.Data == nil || omer.RecentFailures.Data.Total != 1 || omer.RecentFailures.Data.Rows[0].ServiceID != staging.ID() {
		t.Errorf("omer sees %+v, want only the staging-web failure", omer.RecentFailures.Data)
	}
	if n := sum(lineByKey(t, *omer.FailureReasons.Data, "circuit_breaker")); n != 1 {
		t.Errorf("omer circuit_breaker failures = %v, want 1", n)
	}
}

func TestDailyPanels_DataSinceReportedWhenPartial(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)

	// Nothing rolled up yet: every daily panel is unavailable.
	empty := getReliability(t, admin, "")
	if empty.Availability90d.Available || empty.Availability90d.Data != nil || empty.Availability90d.DataSince != nil {
		t.Fatalf("empty rollup = %+v, want unavailable", empty.Availability90d)
	}

	// The rollup began 10 days ago: the panel is available, reports when, and leaves the earlier days empty.
	seedAvailabilityDays(t, st, 1, 10)
	partial := getReliability(t, admin, "")
	want := utcToday().AddDate(0, 0, -10)
	if !partial.Availability90d.Available || partial.Availability90d.DataSince == nil || !partial.Availability90d.DataSince.Equal(want) {
		t.Fatalf("availability_90d = available %v since %v, want available since %v", partial.Availability90d.Available, partial.Availability90d.DataSince, want)
	}
	line := lineByKey(t, *partial.Availability90d.Data, "availability")
	nulls := 0
	for _, v := range line.Values {
		if v == nil {
			nulls++
		}
	}
	if nulls != 80 { // 90 days minus the 10 rolled-up days
		t.Errorf("null days = %d, want 80", nulls)
	}
	if got := partial.GettingWorse.Data; got == nil || got.Total != 0 || len(got.Rows) != 0 {
		t.Errorf("getting_worse = %+v, want an empty table (no previous 30 days to compare)", got)
	}

	// Capacity tables fill independently: spot/reserved turn on with cluster_capacity_daily alone.
	_, err := st.Pool.Exec(t.Context(),
		`insert into cluster_capacity_daily (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
		 select c.account_id, c.region, c.cluster_name, $1::date - 3, 2, 4, 2, 8, 4, 6, 3, 2 from clusters c`, utcToday())
	if err != nil {
		t.Fatal(err)
	}
	capacity := getCapacity(t, admin, "")
	if !capacity.ReservedVsUsed90d.Available || !capacity.SpotShare90d.Available {
		t.Fatalf("capacity panels = %v / %v, want available from cluster_capacity_daily", capacity.ReservedVsUsed90d.Available, capacity.SpotShare90d.Available)
	}
	if capacity.Memory90d.Available || capacity.OverReserved.Available {
		t.Errorf("memory/over_reserved available without service_util_daily: %v / %v", capacity.Memory90d.Available, capacity.OverReserved.Available)
	}
	used := lineByKey(t, *capacity.ReservedVsUsed90d.Data, "used_p95")
	for i, v := range used.Values {
		if v != nil {
			t.Fatalf("used_p95 has a value at %d with no utilisation rollup", i)
		}
	}
	if v := valueAt(t, lineByKey(t, *capacity.ReservedVsUsed90d.Data, "reserved"), 86); v <= 0 {
		t.Errorf("reserved 3 days ago = %v, want > 0", v)
	}
}

// A daily panel is an ordinary panel on the wire: available, data_since and data, nothing else, and its table is
// listed under "Data used on this page" whether or not it has rows yet.
func TestDailyPanels_PlainWireShapeAndSourcesAlwaysListed(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)

	for _, tc := range []struct {
		path   string
		panels []string
		tables []string
	}{
		{"delivery", []string{"failure_reasons", "recent_failures"}, nil},
		{"reliability", []string{"availability_90d", "below_desired_90d", "getting_worse"}, []string{"service_availability_daily"}},
		{"capacity", []string{"reserved_vs_used_90d", "spot_share_90d", "memory_90d", "over_reserved"}, []string{"cluster_capacity_daily", "service_util_daily"}},
	} {
		var body struct {
			Sources []AnalyticsSourceDTO `json:"sources"`
		}
		resp := admin.get("/api/analytics/" + tc.path)
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &body)
		for _, name := range tc.panels {
			var panel map[string]json.RawMessage
			if err := json.Unmarshal(top[name], &panel); err != nil {
				t.Fatalf("%s/%s: %v", tc.path, name, err)
			}
			keys := make([]string, 0, len(panel))
			for k := range panel {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, []string{"available", "data", "data_since"}) {
				t.Errorf("%s/%s keys = %v, want available, data, data_since", tc.path, name, keys)
			}
		}
		for _, table := range tc.tables {
			found := false
			for _, src := range body.Sources {
				found = found || src.Table == table
			}
			if !found {
				t.Errorf("%s sources miss %s before its first row: %+v", tc.path, table, body.Sources)
			}
		}
	}
}
