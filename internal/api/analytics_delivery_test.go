package api

import (
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func deliveryKey(service string) model.ServiceKey {
	return model.ServiceKey{
		ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "core"},
		Service:    service,
	}
}

func deliveryFact(service, kind string, revision int, started time.Time, ran time.Duration, outcome string) store.DeploymentFact {
	fact := store.DeploymentFact{
		Key: deliveryKey(service), DeploymentID: service + "/" + started.Format(time.RFC3339),
		Kind: kind, Revision: revision, StartedAt: started, Outcome: outcome, TagID: 1,
	}
	if outcome != "" {
		finished := started.Add(ran)
		fact.FinishedAt = &finished
	}
	return fact
}

func approxEqual(got *float64, want float64) bool { return got != nil && math.Abs(*got-want) < 1e-9 }

func TestDeliveryAggregate_CFR_RollbackDetection_TTR_Percentiles(t *testing.T) {
	w := clampWindow(Range7d, 0, analyticsNow)
	t0 := w.from.Add(2 * time.Hour)
	facts := []store.DeploymentFact{
		// web: rev 5 -> 7 -> 6 is one rollback; the rev 7 rollout fails and the rev 6 one recovers it 18m later.
		deliveryFact("web", "deploy", 5, t0, time.Minute, "completed"),
		deliveryFact("web", "deploy", 7, t0.Add(time.Hour), 2*time.Minute, "failed"),
		deliveryFact("web", "deploy", 6, t0.Add(time.Hour+2*time.Minute+17*time.Minute), time.Minute, "completed"),
		// api: one superseded rollout and one still open.
		deliveryFact("api", "deploy", 1, t0, time.Minute, "superseded"),
		deliveryFact("api", "restart", 1, t0.Add(time.Hour), 0, ""),
	}
	// Ten completed rollouts of 1..10 minutes on another service for the percentiles.
	for i := 1; i <= 10; i++ {
		facts = append(facts, deliveryFact("batch", "deploy", i, t0.Add(time.Duration(i)*time.Hour), time.Duration(i)*time.Minute, "completed"))
	}

	got := aggregateDelivery(facts, w)

	if got.kpis.Rollouts != 15 {
		t.Errorf("rollouts = %d, want 15", got.kpis.Rollouts)
	}
	// failed 1 of (completed 2 + 10) + failed 1 = 13 finished with a verdict.
	if !approxEqual(got.kpis.ChangeFailureRate, 1.0/13.0) {
		t.Errorf("CFR = %v, want 1/13", got.kpis.ChangeFailureRate)
	}
	if got.kpis.Rollbacks != 1 {
		t.Errorf("rollbacks = %d, want 1", got.kpis.Rollbacks)
	}
	if got.kpis.Superseded != 1 {
		t.Errorf("superseded = %d, want 1", got.kpis.Superseded)
	}
	// The failure finishes at t0+1h2m; its recovery starts 17m later, runs 1m and finishes 18m after the failure.
	if !approxEqual(got.kpis.TimeToRecoverS, 18*60) {
		t.Errorf("TTR = %v, want 1080s", got.kpis.TimeToRecoverS)
	}
	// Completed durations in minutes: 1,1 (web) and 1..10 (batch) -> median 4.5m, p90 8.9m.
	if !approxEqual(got.kpis.MedianRolloutS, 4.5*60) || !approxEqual(got.kpis.P90RolloutS, 8.9*60) {
		t.Errorf("median/p90 = %v/%v, want 270/534", got.kpis.MedianRolloutS, got.kpis.P90RolloutS)
	}
	if len(got.services) != 3 {
		t.Fatalf("service stats = %d, want 3", len(got.services))
	}
}

func TestInterpolatedPercentile_InterpolatesLikePercentileCont(t *testing.T) {
	values := []float64{60, 120, 180, 240, 300, 360, 420, 480, 540, 600}
	if got := interpolatedPercentile(values, 0.5); got != 330 {
		t.Errorf("median = %v, want 330", got)
	}
	if got := interpolatedPercentile(values, 0.9); math.Abs(got-546) > 1e-9 {
		t.Errorf("p90 = %v, want 546", got)
	}
	if got := interpolatedPercentile([]float64{42}, 0.9); got != 42 {
		t.Errorf("single value p90 = %v, want 42", got)
	}
}

func TestDeliveryAggregate_EmptyWindowYieldsNullRates(t *testing.T) {
	w := clampWindow(Range30d, 0, analyticsNow)
	got := aggregateDelivery(nil, w)

	if got.kpis.Rollouts != 0 || got.kpis.RolloutsPerDay != 0 {
		t.Errorf("kpis = %+v, want zero counts", got.kpis)
	}
	if got.kpis.ChangeFailureRate != nil || got.kpis.MedianRolloutS != nil || got.kpis.P90RolloutS != nil || got.kpis.TimeToRecoverS != nil {
		t.Errorf("rates must be nil with no rollouts: %+v", got.kpis)
	}
	for _, line := range got.failureRate.Series {
		for i, v := range line.Values {
			if v != nil {
				t.Fatalf("failure_rate[%d] = %v, want nil", i, *v)
			}
		}
	}
	for _, line := range got.duration.Series {
		for _, v := range line.Values {
			if v != nil {
				t.Fatalf("duration line %s has a value, want nil", line.Key)
			}
		}
	}
	for _, line := range got.rollouts.Series {
		for i, v := range line.Values {
			if v == nil || *v != 0 {
				t.Fatalf("rollouts %s[%d] = %v, want 0", line.Key, i, v)
			}
		}
	}
	if len(got.rollouts.Timestamps) != w.buckets {
		t.Errorf("timestamps = %d, want %d", len(got.rollouts.Timestamps), w.buckets)
	}
}

func TestDeliveryAggregate_RolloutsPerDayAndBuckets(t *testing.T) {
	w := clampWindow(Range7d, 0, analyticsNow)
	facts := []store.DeploymentFact{
		deliveryFact("web", "deploy", 1, w.from.Add(time.Hour), time.Minute, "completed"),
		deliveryFact("web", "deploy", 2, w.from.Add(5*time.Hour), time.Minute, "failed"),
	}
	got := aggregateDelivery(facts, w)
	if math.Abs(got.kpis.RolloutsPerDay-2.0/7.0) > 1e-9 {
		t.Errorf("per day = %v, want 2/7", got.kpis.RolloutsPerDay)
	}
	byKey := map[string]SeriesLineDTO{}
	for _, line := range got.rollouts.Series {
		byKey[line.Key] = line
	}
	if *byKey["completed"].Values[0] != 1 || *byKey["failed"].Values[1] != 1 {
		t.Errorf("bucket placement wrong: completed=%v failed=%v", *byKey["completed"].Values[0], *byKey["failed"].Values[1])
	}
	rate := got.failureRate.Series[0]
	if rate.Values[0] == nil || *rate.Values[0] != 0 || rate.Values[1] == nil || *rate.Values[1] != 100 || rate.Values[2] != nil {
		t.Errorf("failure rate = %v %v %v, want 0, 100, nil", rate.Values[0], rate.Values[1], rate.Values[2])
	}
}

func TestAnalyticsDelivery_EndToEnd(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	setPassword(t, st, "omer")

	services, err := st.ListServices(t.Context(), store.TargetFilter{}, "", store.Visibility{Admin: true})
	if err != nil || len(services) == 0 {
		t.Fatalf("services = %d, %v", len(services), err)
	}
	now := time.Now().UTC()
	for i, svc := range services {
		outcome := "completed"
		if i%2 == 0 {
			outcome = "failed"
		}
		finished := now.Add(-2*time.Hour + time.Minute)
		if _, err := st.InsertDeploymentIfNew(t.Context(), store.DeploymentRecord{
			Key: svc.Key, DeploymentID: "ecs-svc/" + svc.Key.Service, Revision: 3, Kind: "deploy",
			StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, Outcome: outcome,
		}); err != nil {
			t.Fatal(err)
		}
	}

	windowEnd := now.Truncate(analyticsPollStep) // the handler's window ends at "now" truncated to 5 minutes
	// The poll itself records the estate's rollouts, so compare with what the store holds.
	stored, err := st.AnalyticsDeployments(t.Context(), store.TargetFilter{}, store.Visibility{Admin: true}, windowEnd.Add(-7*24*time.Hour), windowEnd)
	if err != nil || len(stored) < len(services) {
		t.Fatalf("stored rollouts = %d, %v; want at least %d", len(stored), err, len(services))
	}
	admin := login(t, srv, "pavel", testPassword)
	var all DeliveryDTO
	resp := admin.get("/api/analytics/delivery?range=7d")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	decode(t, resp, &all)
	if all.Tab != "delivery" || all.Window.Used != "7d" || all.KPIs.Rollouts != len(stored) {
		t.Fatalf("admin delivery = tab %q used %q rollouts %d, want delivery 7d %d", all.Tab, all.Window.Used, all.KPIs.Rollouts, len(stored))
	}
	if all.Services.Total == 0 || len(all.Services.Rows) == 0 {
		t.Errorf("services total/rows = %d/%d", all.Services.Total, len(all.Services.Rows))
	}
	if all.FailureReasons.Available || all.RecentFailures.Available {
		t.Error("failure panels must stay unavailable until a reason is recorded")
	}
	if len(all.Sources) == 0 || all.Sources[0].Table != "deployment_history" {
		t.Errorf("sources = %+v", all.Sources)
	}

	if etag == "" {
		t.Fatal("no ETag")
	}
	again := admin.do(http.MethodGet, "/api/analytics/delivery?range=7d", nil, map[string]string{"If-None-Match": etag})
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("repeat with If-None-Match = %d, want 304", again.StatusCode)
	}

	omer := login(t, srv, "omer", testPassword)
	var narrow DeliveryDTO
	decode(t, omer.get("/api/analytics/delivery?range=7d"), &narrow)
	if narrow.KPIs.Rollouts >= all.KPIs.Rollouts {
		t.Errorf("omer sees %d rollouts, admin %d; omer must see only his tags", narrow.KPIs.Rollouts, all.KPIs.Rollouts)
	}

	bad := admin.get("/api/analytics/delivery?range=1y")
	if code, _ := errorCode(t, bad); code != "invalid" || bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("range=1y -> %d %q, want 422 invalid", bad.StatusCode, code)
	}
}
