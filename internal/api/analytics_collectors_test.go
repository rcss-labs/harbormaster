package api

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// pollCountsAll runs one poll plus one counts-history tick for every account and region of the fake estate.
func pollCountsAll(t *testing.T, st *store.Store, f *fake.Fake) {
	t.Helper()
	for _, account := range f.Estate.Accounts {
		for _, region := range account.Regions {
			if err := poller.PollCountsOnce(t.Context(), st, f, poller.Config{MaxInFlight: 4}, awsx.Target{AccountID: account.ID, Region: region}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// These tests fill the daily panels the way production does: the real poller tick, the real rollup runner and the
// real outcome logger write the rows, and the API is asked afterwards. (The tests in analytics_daily_test.go seed
// the tables by SQL to pin the read side; the rollup and poller packages test the writers alone.)

func TestDailyPanelsCollected_ReliabilityAvailabilityFromNightlyRollup(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollCountsAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)
	if before := getReliability(t, admin, ""); before.Availability90d.Available {
		t.Fatal("availability_90d is available before any rollup ran: today is not a complete day")
	}

	// The nightly run, two days on: today's counts samples are now a complete day inside the backfill reach.
	runner := &rollup.Runner{Store: st, Clients: fk, Now: func() time.Time { return time.Now().Add(48 * time.Hour) }, CountsRetention: 7 * 24 * time.Hour}
	result, err := runner.RunDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	services := len(visibleServices(t, st))
	if result["service_availability_daily"] != services {
		t.Fatalf("rollup wrote %v rows, want one per service (%d)", result, services)
	}

	got := getReliability(t, admin, "")
	for name, panel := range map[string]bool{"availability_90d": got.Availability90d.Available, "below_desired_90d": got.BelowDesired90d.Available, "getting_worse": got.GettingWorse.Available} {
		if !panel {
			t.Errorf("%s is unavailable after the rollup ran", name)
		}
	}
	if since := got.Availability90d.DataSince; since == nil || !since.Equal(utcToday()) {
		t.Errorf("data_since = %v, want today %v (the day of the counts samples)", since, utcToday())
	}
	if v := valueAt(t, lineByKey(t, *got.Availability90d.Data, "availability"), 89); v <= 0 || v > 100 {
		t.Errorf("availability today = %v, want a percentage", v)
	}
}

func TestDailyPanelsCollected_CapacityFromCountsTickAndUtilRollup(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollCountsAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)
	before := getCapacity(t, admin, "")
	if before.Memory90d.Available || before.OverReserved.Available {
		t.Fatal("utilisation panels are available before the CloudWatch rollup ran")
	}

	// Cluster capacity is sampled on every counts tick, straight into today's row.
	var wantVCPU float64
	for _, cluster := range fk.Estate.Clusters {
		vcpu, _, _ := expectedReserved(fk, aws.ToString(cluster.Cluster.ClusterName))
		wantVCPU += vcpu
	}
	if !before.ReservedVsUsed90d.Available || !before.SpotShare90d.Available {
		t.Fatalf("reserved_vs_used_90d / spot_share_90d = %v / %v after one counts tick, want available", before.ReservedVsUsed90d.Available, before.SpotShare90d.Available)
	}
	if v := valueAt(t, lineByKey(t, *before.ReservedVsUsed90d.Data, "reserved"), 89); !close2(v, wantVCPU) {
		t.Errorf("reserved vCPU today = %v, want %v (sum of the estate's running tasks)", v, wantVCPU)
	}
	if v := valueAt(t, lineByKey(t, *before.SpotShare90d.Data, "spot_share"), 89); v < 0 || v > 100 {
		t.Errorf("spot share today = %v, want a percentage", v)
	}

	// The daily CloudWatch rollup (on by default) writes yesterday for clusters with Container Insights.
	runner := &rollup.Runner{Store: st, Clients: fk, Now: time.Now, UtilEnabled: true, CountsRetention: 7 * 24 * time.Hour}
	result, err := runner.RunDue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result["service_util_daily"] == 0 {
		t.Fatalf("rollup wrote %v, want service_util_daily rows", result)
	}
	got := getCapacity(t, admin, "")
	if !got.Memory90d.Available || !got.OverReserved.Available {
		t.Fatalf("memory_90d / over_reserved = %v / %v after the util rollup, want available", got.Memory90d.Available, got.OverReserved.Available)
	}
	if v := valueAt(t, lineByKey(t, *got.Memory90d.Data, "used_p95"), 88); v <= 0 {
		t.Errorf("memory used p95 yesterday = %v, want > 0", v)
	}
	if got.OverReserved.Data.Total == 0 || len(got.OverReserved.Data.Rows) == 0 {
		t.Errorf("over_reserved = %+v, want at least one service", got.OverReserved.Data)
	}
}

func TestDailyPanelsCollected_DeliveryFailureReasonsFromOutcomeLogger(t *testing.T) {
	srv, fk, st, _ := newServerCustom(t, true, withDailyRetention)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)
	getDelivery := func() DeliveryDTO {
		var out DeliveryDTO
		decode(t, admin.get("/api/analytics/delivery?range=30d"), &out)
		return out
	}
	if before := getDelivery(); before.FailureReasons.Available || before.RecentFailures.Available {
		t.Fatal("failure panels are available before any rollout failed")
	}

	// A rollout the poller recorded fails in ECS; the logger decides the outcome and why.
	open, err := st.OpenDeployments(t.Context())
	if err != nil || len(open) == 0 {
		t.Fatalf("open deployments = %d, %v; want the poller to have recorded the estate's rollouts", len(open), err)
	}
	failing := open[0]
	_, err = st.Pool.Exec(t.Context(),
		`update services set raw = jsonb_set(jsonb_set(raw, '{Deployments}', (
		     select jsonb_agg(case when d->>'Id' = $5 then d || '{"RolloutState":"FAILED","RolloutStateReason":"ECS deployment circuit breaker: tasks failed to start."}'::jsonb else d end)
		     from jsonb_array_elements(raw->'Deployments') d)), '{Status}', '"ACTIVE"')
		 where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`,
		failing.Key.AccountID, failing.Key.Region, failing.Key.Cluster, failing.Key.Service, failing.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	logger := deploy.NewOutcomeLogger(st, time.Second)
	if err := logger.Once(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := getDelivery()
	if !got.FailureReasons.Available || !got.RecentFailures.Available {
		t.Fatalf("failure panels = %v / %v after the logger recorded a failed rollout, want available", got.FailureReasons.Available, got.RecentFailures.Available)
	}
	var failures float64
	for _, v := range lineByKey(t, *got.FailureReasons.Data, "circuit_breaker").Values {
		if v != nil {
			failures += *v
		}
	}
	if failures != 1 {
		t.Errorf("circuit_breaker failures = %v, want 1", failures)
	}
	rows := got.RecentFailures.Data.Rows
	if len(rows) != 1 || rows[0].Service != failing.Key.Service || rows[0].Reason != "circuit_breaker" || rows[0].Detail == "" {
		t.Errorf("recent failures = %+v, want %s with reason circuit_breaker and its detail", rows, failing.Key.Service)
	}
}
