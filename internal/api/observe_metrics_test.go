package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// recordingClients wraps the fake and remembers which CloudWatch namespaces were queried.
type recordingClients struct {
	awsx.Clients
	mu         sync.Mutex
	namespaces map[string]int
	// throttledRegions makes CloudWatch fail with a throttle for those regions only (the fake's knob is global).
	throttledRegions map[string]bool
}

func (c *recordingClients) throttleRegions(regions ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.throttledRegions = map[string]bool{}
	for _, region := range regions {
		c.throttledRegions[region] = true
	}
}

func (c *recordingClients) CloudWatch(ctx context.Context, t awsx.Target) (awsx.CloudWatchAPI, error) {
	c.mu.Lock()
	throttled := c.throttledRegions[t.Region]
	c.mu.Unlock()
	if throttled {
		return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded", Fault: smithy.FaultClient}
	}
	api, err := c.Clients.CloudWatch(ctx, t)
	if err != nil {
		return nil, err
	}
	return &recordingCloudWatch{CloudWatchAPI: api, owner: c}, nil
}

func (c *recordingClients) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.namespaces = map[string]int{}
}

func (c *recordingClients) queried(namespace string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.namespaces[namespace]
}

type recordingCloudWatch struct {
	awsx.CloudWatchAPI
	owner *recordingClients
}

func (r *recordingCloudWatch) GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	r.owner.mu.Lock()
	for _, q := range in.MetricDataQueries {
		if q.MetricStat != nil && q.MetricStat.Metric != nil {
			r.owner.namespaces[aws.ToString(q.MetricStat.Metric.Namespace)]++
		}
	}
	r.owner.mu.Unlock()
	return r.CloudWatchAPI.GetMetricData(ctx, in, opts...)
}

// testClock is a manually advanced clock shared by the API server and its metrics client.
type testClock struct{ nanos atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}
func (c *testClock) Now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) Advance(d time.Duration) { c.nanos.Add(int64(d)) }

type metricsHarness struct {
	srv   *httptest.Server
	fake  *fake.Fake
	store *store.Store
	clock *testClock
	cw    *recordingClients
}

// newMetricsHarness serves the API over the mockup estate (plus extraClusters Insights-enabled
// clusters), with the dev seed users, one full poll, a 1 minute metrics cache and a manual clock.
func newMetricsHarness(t *testing.T, extraClusters int) *metricsHarness {
	t.Helper()
	st := storetest.Open(t)
	log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	cfg := testConfig()
	cfg.MetricsCacheTTL = time.Minute
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name  string
		admin bool
	}{{"sam", true}, {"omer", false}} {
		if _, err := st.CreateUser(t.Context(), u.name, hash, u.admin, false); err != nil {
			t.Fatal(err)
		}
	}
	setup, err := auth.NewSetup(t.Context(), st, log)
	if err != nil {
		t.Fatal(err)
	}
	fk := fake.New(fake.MockupEstate(time.Now))
	for i := range extraClusters {
		name := "extra-" + string(rune('a'+i))
		fk.Estate.Mu.Lock()
		fk.Estate.Clusters = append(fk.Estate.Clusters, fake.Cluster{AccountID: homelabAccountID, Region: "us-east-1", Cluster: ecstypes.Cluster{
			ClusterArn: aws.String("arn:aws:ecs:us-east-1:" + homelabAccountID + ":cluster/" + name), ClusterName: aws.String(name),
			Status:   aws.String("ACTIVE"),
			Settings: []ecstypes.ClusterSetting{{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enabled")}},
		}})
		fk.Estate.Mu.Unlock()
	}
	clock := newTestClock()
	cw := &recordingClients{Clients: fk, namespaces: map[string]int{}}
	api := New(Server{
		Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
		Clients: cw, Prober: fk, Cfg: cfg, Log: log, Now: clock.Now,
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	seedMockupTags(t, st)
	setPassword(t, st, "dana")
	pollAll(t, st, fk)
	return &metricsHarness{srv: srv, fake: fk, store: st, clock: clock, cw: cw}
}

func catalogID() string {
	return serviceID(homelabAccountID, "eu-west-1", "eu-apps", "catalog-api")
}

func edgeNginxID() string {
	return serviceID(homelabAccountID, "eu-central-1", "edge-proxy", "edge-nginx")
}

func coreServicesID() string { return clusterID(homelabAccountID, "us-east-1", "core-services") }

func getServiceMetricsDTO(t *testing.T, c *apiClient, id, query string) ServiceMetricsDTO {
	t.Helper()
	resp := c.get("/api/services/" + id + "/metrics" + query)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("status %d (%s: %s), want 200", resp.StatusCode, code, msg)
	}
	var out ServiceMetricsDTO
	decode(t, resp, &out)
	return out
}

func TestServiceMetrics_ViewerGetsSeriesAndALB(t *testing.T) {
	h := newMetricsHarness(t, 0)
	out := getServiceMetricsDTO(t, login(t, h.srv, "omer", testPassword), catalogID(), "")

	if out.Range != "24h" || out.Period != 1200 || out.Insights != "enabled" {
		t.Errorf("range/period/insights = %q/%d/%q; want 24h/1200/enabled", out.Range, out.Period, out.Insights)
	}
	if out.Series == nil || len(out.Timestamps) != 72 || len(out.Series.CPU) != 72 || len(out.Series.Mem) != 72 || len(out.Series.Tasks) != 72 {
		t.Fatalf("timestamps = %d, series = %+v; want 72 points each", len(out.Timestamps), out.Series)
	}
	if out.Now == nil || out.Now.CPUUnits != 512 || out.Now.MemMiB != 1024 || out.Now.Running != 2 || out.Now.CPU == nil {
		t.Errorf("now = %+v; want cpu_units 512, mem_mib 1024, running 2 and a cpu value", out.Now)
	}
	if out.ALB == nil {
		t.Fatal("alb is null for a service behind a load balancer")
	}
	if len(out.ALB.Err5xxPerHour.Values) != 24 || len(out.ALB.Err5xxPerHour.Timestamps) != 24 || out.ALB.Err5xxToday <= 0 {
		t.Errorf("err5xx_per_hour = %d values, today = %v; want 24 values and a positive total",
			len(out.ALB.Err5xxPerHour.Values), out.ALB.Err5xxToday)
	}
	if len(out.ALB.ReqPerMin) != 72 || out.ALB.ReqNow == nil || out.ALB.P95Now == nil {
		t.Errorf("alb req/p95 = %d points, req_now %v, p95_now %v", len(out.ALB.ReqPerMin), out.ALB.ReqNow, out.ALB.P95Now)
	}
	if out.TasksNow != nil {
		t.Errorf("tasks_now = %v for a non-enhanced cluster; want null", out.TasksNow)
	}
	if out.Stale != nil {
		t.Errorf("stale = %+v on a fresh fetch", out.Stale)
	}
}

// The seed starts catalog-api's current deployment (revision 23) 30 hours ago, so the :23 marker
// is outside the default 24 h range and appears with ?range=72h.
func TestServiceMetrics_DeployMarkerAppearsWhenInRange(t *testing.T) {
	h := newMetricsHarness(t, 0)
	c := login(t, h.srv, "omer", testPassword)

	if day := getServiceMetricsDTO(t, c, catalogID(), ""); len(day.Markers) != 0 {
		t.Errorf("24h markers = %+v; want none (deployment is 30 h old)", day.Markers)
	}
	out := getServiceMetricsDTO(t, c, catalogID(), "?range=72h")
	if out.Period != 3600 || len(out.Timestamps) != 72 {
		t.Errorf("72h period/points = %d/%d; want 3600/72", out.Period, len(out.Timestamps))
	}
	if !slices.ContainsFunc(out.Markers, func(m MarkerDTO) bool { return m.Label == ":23" && m.Kind == "deploy" && !m.Failed }) {
		t.Errorf("markers = %+v; want a deploy marker labelled :23", out.Markers)
	}
}

func TestServiceMetrics_InsightsOffSkipsInsights(t *testing.T) {
	h := newMetricsHarness(t, 0)
	h.cw.reset()
	out := getServiceMetricsDTO(t, login(t, h.srv, "omer", testPassword), edgeNginxID(), "")

	if out.Insights != "disabled" || out.Series != nil || out.Now != nil {
		t.Errorf("insights/series/now = %q/%v/%v; want disabled/null/null", out.Insights, out.Series, out.Now)
	}
	if out.ALB == nil {
		t.Error("alb is null; the load balancer is still fetched when Insights is off")
	}
	if n := h.cw.queried("ECS/ContainerInsights"); n != 0 {
		t.Errorf("%d Container Insights queries were sent for a cluster with Insights disabled", n)
	}
	if h.cw.queried("AWS/ApplicationELB") == 0 {
		t.Error("no ApplicationELB queries were sent")
	}
}

func TestServiceMetrics_EnhancedAddsTasksNow(t *testing.T) {
	h := newMetricsHarness(t, 0)
	out := getServiceMetricsDTO(t, login(t, h.srv, "sam", testPassword), paymentsID(), "")

	if out.Insights != "enhanced" || len(out.TasksNow) != 3 {
		t.Fatalf("insights %q, tasks_now = %d; want enhanced and 3", out.Insights, len(out.TasksNow))
	}
	for _, task := range out.TasksNow {
		if task.TaskID == "" || task.CPU == nil || task.Mem == nil {
			t.Errorf("task %+v lacks id, cpu or mem", task)
		}
	}
}

func TestServiceMetrics_NoViewIs404(t *testing.T) {
	h := newMetricsHarness(t, 0)
	resp := login(t, h.srv, "omer", testPassword).get("/api/services/" + paymentsID() + "/metrics")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "not_found" {
		t.Errorf("code %q, want not_found", code)
	}
}

func TestServiceMetrics_BadRangeIs422(t *testing.T) {
	h := newMetricsHarness(t, 0)
	resp := login(t, h.srv, "sam", testPassword).get("/api/services/" + paymentsID() + "/metrics?range=2h")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "invalid" {
		t.Errorf("code %q, want invalid", code)
	}
}

func TestServiceMetrics_ThrottledColdCacheIs503(t *testing.T) {
	h := newMetricsHarness(t, 0)
	atomic.StoreInt32(&h.fake.ThrottleCloudWatch, 1000)
	resp := login(t, h.srv, "sam", testPassword).get("/api/services/" + paymentsID() + "/metrics")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "throttled" {
		t.Errorf("code %q, want throttled", code)
	}
}

func TestServiceMetrics_StaleWhenThrottledWarmCache(t *testing.T) {
	h := newMetricsHarness(t, 0)
	c := login(t, h.srv, "sam", testPassword)
	getServiceMetricsDTO(t, c, paymentsID(), "")

	atomic.StoreInt32(&h.fake.ThrottleCloudWatch, 1000)
	h.clock.Advance(5 * time.Minute)
	out := getServiceMetricsDTO(t, c, paymentsID(), "")

	if out.Stale == nil || out.Stale.Reason == "" || out.Stale.FetchedAt.IsZero() {
		t.Fatalf("stale = %+v; want a reason and fetched_at", out.Stale)
	}
	if out.Series == nil || len(out.Series.CPU) != 72 {
		t.Errorf("series = %+v; want the cached 72 points", out.Series)
	}
}

// Review focus: a service with two target groups sums requests and 5xx across the groups and
// averages p95 latency (seconds on the wire, milliseconds in the response).
func TestServiceMetrics_TwoTargetGroupsSumRequestsAndAverageP95(t *testing.T) {
	h := newMetricsHarness(t, 0)
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	h.fake.Estate.AddTargetGroup(key, fake.TargetGroup{Healthy: 3})
	pollAll(t, h.store, h.fake)

	groups, err := h.store.TargetHealthForService(t.Context(), key)
	if err != nil || len(groups) != 2 {
		t.Fatalf("target health groups = %d, err %v; want 2", len(groups), err)
	}
	// Expected values: fetch each group on its own through an independent client on the same clock.
	direct := metrics.New(h.fake, nil, 0)
	direct.Now = h.clock.Now
	target := awsx.Target{AccountID: key.AccountID, Region: key.Region}
	lastOf := func(group model.TargetHealth, period int, span metrics.Span, id string) metrics.Series {
		res, err := direct.Fetch(t.Context(), target, span, metrics.ALBQueries(
			[]metrics.TargetGroupRef{{TargetGroupARN: group.TargetGroupARN, LoadBalancerARN: group.LoadBalancerARN}}, period))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range res.Series {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("series %s missing", id)
		return metrics.Series{}
	}
	var wantReq, wantP95, wantErr float64
	for _, g := range groups {
		wantReq += *metrics.Last(metrics.PerMinute(lastOf(g, 1200, metrics.Span24h, "req_0"), 1200))
		wantP95 += *metrics.Last(lastOf(g, 1200, metrics.Span24h, "p95_0")) * 1000 / 2
		wantErr += metrics.SumAll(lastOf(g, 3600, metrics.Span24h, "e5xx_0"))
	}

	out := getServiceMetricsDTO(t, login(t, h.srv, "sam", testPassword), paymentsID(), "")
	if out.ALB == nil || out.ALB.ReqNow == nil || out.ALB.P95Now == nil {
		t.Fatalf("alb = %+v", out.ALB)
	}
	if math.Abs(*out.ALB.ReqNow-wantReq) > 0.01 {
		t.Errorf("req_now = %v, want sum of both groups %v", *out.ALB.ReqNow, wantReq)
	}
	if math.Abs(*out.ALB.P95Now-wantP95) > 0.01 {
		t.Errorf("p95_now = %v, want mean of both groups %v ms", *out.ALB.P95Now, wantP95)
	}
	if math.Abs(out.ALB.Err5xxToday-wantErr) > 0.5 {
		t.Errorf("err5xx_today = %v, want sum of both groups %v", out.ALB.Err5xxToday, wantErr)
	}
}

func TestClusterMetrics_OnlyVisibleServices(t *testing.T) {
	h := newMetricsHarness(t, 0)
	// Hide notifications from dana with a tag she has no permission on.
	secret, err := h.store.CreateTag(t.Context(), "Secret", "red")
	if err != nil {
		t.Fatal(err)
	}
	hidden := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "notifications"}
	tagID := secret.ID
	if err := h.store.SetServiceTag(t.Context(), hidden, &tagID); err != nil {
		t.Fatal(err)
	}

	resp := login(t, h.srv, "dana", testPassword).get("/api/clusters/" + coreServicesID() + "/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out ClusterMetricsDTO
	decode(t, resp, &out)

	want := []string{"auth-service", "payments-api", "web-frontend"}
	var got []string
	for id := range out.Services {
		key, err := model.ParseServiceID(id)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, key.Service)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("services = %v, want only the visible Prod services %v", got, want)
	}
	if out.Insights != "enhanced" || out.Cluster == nil || len(out.Cluster.CPU) != 72 || out.Now == nil || out.Now.CPU == nil {
		t.Errorf("insights %q cluster %+v now %+v; want enhanced with 72 points", out.Insights, out.Cluster, out.Now)
	}
	payments := out.Services[paymentsID()]
	if len(payments.CPU) != 72 || payments.CPUNow == nil || payments.ALB == nil || payments.ALB.ReqNow == nil {
		t.Errorf("payments-api entry = %+v; want series, cpu_now and alb", payments)
	}
	if auth := out.Services[serviceID(homelabAccountID, "us-east-1", "core-services", "auth-service")]; auth.ALB == nil {
		t.Error("auth-service (behind a load balancer) has no alb")
	}

	omer := login(t, h.srv, "omer", testPassword).get("/api/clusters/" + coreServicesID() + "/metrics")
	defer omer.Body.Close()
	if omer.StatusCode != http.StatusNotFound {
		t.Errorf("omer status %d, want 404", omer.StatusCode)
	}
}

func TestClustersMetrics_PerVisibleCluster(t *testing.T) {
	h := newMetricsHarness(t, 0)
	resp := login(t, h.srv, "omer", testPassword).get("/api/clusters/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out ClustersMetricsDTO
	decode(t, resp, &out)

	if _, ok := out.Items[coreServicesID()]; ok {
		t.Error("core-services (Prod) is listed for omer")
	}
	euApps := out.Items[model.ClusterKey{AccountID: homelabAccountID, Region: "eu-west-1", Cluster: "eu-apps"}.ID()]
	if euApps.Insights != "enabled" || len(euApps.CPU) != 48 || len(euApps.Mem) != 48 || euApps.CPUNow == nil || euApps.MemNow == nil {
		t.Errorf("eu-apps = insights %q, %d cpu points, now %v/%v; want enabled with 48 points", euApps.Insights, len(euApps.CPU), euApps.CPUNow, euApps.MemNow)
	}
	edge, ok := out.Items[model.ClusterKey{AccountID: homelabAccountID, Region: "eu-central-1", Cluster: "edge-proxy"}.ID()]
	if !ok || edge.Insights != "disabled" || len(edge.CPU) != 0 || edge.CPUNow != nil {
		t.Errorf("edge-proxy = %+v (listed %v); want disabled with empty series", edge, ok)
	}
}

// Slices are never null in the API (01 convention): the SPA reads cpu/mem as arrays even for Insights-off clusters.
func TestClustersMetrics_DisabledClusterSerialisesEmptyArrays(t *testing.T) {
	h := newMetricsHarness(t, 0)
	resp := login(t, h.srv, "omer", testPassword).get("/api/clusters/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var raw struct {
		Items map[string]map[string]json.RawMessage `json:"items"`
	}
	decode(t, resp, &raw)
	edge := raw.Items[model.ClusterKey{AccountID: homelabAccountID, Region: "eu-central-1", Cluster: "edge-proxy"}.ID()]
	for _, key := range []string{"cpu", "mem"} {
		if got := string(edge[key]); got != "[]" {
			t.Errorf("edge-proxy %s = %s, want []", key, got)
		}
	}
}

func TestClusterMetrics_DisabledClusterServicesSerialiseEmptyArrays(t *testing.T) {
	h := newMetricsHarness(t, 0)
	edgeID := model.ClusterKey{AccountID: homelabAccountID, Region: "eu-central-1", Cluster: "edge-proxy"}.ID()
	resp := login(t, h.srv, "omer", testPassword).get("/api/clusters/" + edgeID + "/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var raw struct {
		Services map[string]map[string]json.RawMessage `json:"services"`
	}
	decode(t, resp, &raw)
	if len(raw.Services) == 0 {
		t.Fatal("edge-proxy has no services entries")
	}
	for id, entry := range raw.Services {
		for _, key := range []string{"cpu", "mem"} {
			if got := string(entry[key]); got != "[]" {
				t.Errorf("service %s %s = %s, want []", id, key, got)
			}
		}
	}
}

func TestOverviewMetrics_TopSevenPlusOthers(t *testing.T) {
	h := newMetricsHarness(t, 5) // 5 mockup clusters with Insights + 5 extra = 10
	resp := login(t, h.srv, "sam", testPassword).get("/api/overview/metrics")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out OverviewMetricsDTO
	decode(t, resp, &out)

	for name, list := range map[string][]ClusterSeriesDTO{"cpu": out.CPUByCluster, "mem": out.MemByCluster} {
		if len(list) != 8 || list[7].Label != "others" {
			t.Errorf("%s_by_cluster = %d entries, last %q; want 8 ending in others", name, len(list), list[len(list)-1].Label)
			continue
		}
		for _, s := range list {
			if len(s.Values) != 48 {
				t.Errorf("%s series %s has %d points, want 48", name, s.Label, len(s.Values))
			}
		}
	}
	if len(out.Timestamps) != 48 || out.Avg.CPU == nil || out.Avg.Mem == nil {
		t.Errorf("timestamps %d, avg %+v; want 48 and non-null averages", len(out.Timestamps), out.Avg)
	}
}

func TestTargetHealth_Groups(t *testing.T) {
	h := newMetricsHarness(t, 0)
	c := login(t, h.srv, "omer", testPassword)

	resp := c.get("/api/services/" + catalogID() + "/target-health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out TargetHealthDTO
	decode(t, resp, &out)
	if len(out.Groups) != 1 || out.Healthy != 1 || out.Unhealthy != 1 || out.PolledAt == nil {
		t.Fatalf("health = %+v; want one group, 1 healthy, 1 unhealthy, polled", out)
	}
	group := out.Groups[0]
	if group.Name != "catalog-api-tg" || group.Healthy != 1 || group.Unhealthy != 1 {
		t.Errorf("group = %+v; want catalog-api-tg 1/1", group)
	}
	var unhealthy *TargetDTO
	for i := range group.Targets {
		if group.Targets[i].State == "unhealthy" {
			unhealthy = &group.Targets[i]
			break
		}
	}
	if unhealthy == nil || unhealthy.Reason != "Target.ResponseCodeMismatch" {
		t.Errorf("unhealthy target = %+v; want reason Target.ResponseCodeMismatch", unhealthy)
	}
}

func TestTargetHealth_NoLoadBalancerIsEmpty(t *testing.T) {
	h := newMetricsHarness(t, 0)
	id := serviceID(homelabAccountID, "eu-west-1", "eu-apps", "search-indexer")
	resp := login(t, h.srv, "omer", testPassword).get("/api/services/" + id + "/target-health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out TargetHealthDTO
	decode(t, resp, &out)
	if out.Groups == nil || len(out.Groups) != 0 || out.Healthy != 0 || out.Unhealthy != 0 || out.PolledAt != nil {
		t.Errorf("health = %+v; want empty groups (not null), zero counts and null polled_at", out)
	}
}

// A target that is throttled with a cold cache degrades only its own clusters; the rest keep their series.
func TestClustersMetrics_OneThrottledTargetStillServesOthers(t *testing.T) {
	h := newMetricsHarness(t, 0)
	h.cw.throttleRegions("eu-west-1")
	resp := login(t, h.srv, "sam", testPassword).get("/api/clusters/metrics")
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("status %d (%s: %s), want 200", resp.StatusCode, code, msg)
	}
	var out ClustersMetricsDTO
	decode(t, resp, &out)

	core := out.Items[coreServicesID()]
	if len(core.CPU) == 0 || core.CPUNow == nil {
		t.Errorf("core-services (healthy target) = %d cpu points, now %v; want its series", len(core.CPU), core.CPUNow)
	}
	euApps := out.Items[model.ClusterKey{AccountID: homelabAccountID, Region: "eu-west-1", Cluster: "eu-apps"}.ID()]
	if len(euApps.CPU) != 0 || euApps.CPUNow != nil {
		t.Errorf("eu-apps (throttled target) = %d cpu points, now %v; want empty", len(euApps.CPU), euApps.CPUNow)
	}
	if len(out.Errors) != 1 || out.Errors[0].AccountID != homelabAccountID || out.Errors[0].Region != "eu-west-1" || out.Errors[0].Code != "throttled" {
		t.Errorf("errors = %+v; want one throttled entry for eu-west-1", out.Errors)
	}
}

func TestOverviewMetrics_OneThrottledTargetStillServesOthers(t *testing.T) {
	h := newMetricsHarness(t, 0)
	h.cw.throttleRegions("eu-west-1")
	resp := login(t, h.srv, "sam", testPassword).get("/api/overview/metrics")
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("status %d (%s: %s), want 200", resp.StatusCode, code, msg)
	}
	var out OverviewMetricsDTO
	decode(t, resp, &out)

	if len(out.CPUByCluster) == 0 || out.Avg.CPU == nil {
		t.Errorf("cpu_by_cluster = %d series, avg %+v; want the healthy target's clusters", len(out.CPUByCluster), out.Avg)
	}
	if len(out.Errors) != 1 || out.Errors[0].Region != "eu-west-1" {
		t.Errorf("errors = %+v; want one entry for eu-west-1", out.Errors)
	}
}

// When no target can be read there is nothing to show, so the whole response keeps failing as before.
func TestClustersMetrics_EveryTargetThrottledIs503(t *testing.T) {
	h := newMetricsHarness(t, 0)
	h.cw.throttleRegions("us-east-1", "eu-west-1")
	resp := login(t, h.srv, "sam", testPassword).get("/api/clusters/metrics")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
}

func TestClustersMetrics_NoErrorsSerialisesEmptyArray(t *testing.T) {
	h := newMetricsHarness(t, 0)
	resp := login(t, h.srv, "sam", testPassword).get("/api/clusters/metrics")
	var raw map[string]json.RawMessage
	decode(t, resp, &raw)
	if got := string(raw["errors"]); got != "[]" {
		t.Errorf("errors = %s, want []", got)
	}
}
