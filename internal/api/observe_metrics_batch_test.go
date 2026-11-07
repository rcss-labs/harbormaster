package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// targetGate sits under the harness's recording client. It throttles whole account x region
// targets (the harness knob is per region, which would also hit the second account in us-east-1)
// and remembers the ServiceName dimension of every Container Insights query.
type targetGate struct {
	awsx.Clients
	mu        sync.Mutex
	throttled map[awsx.Target]bool
	services  []string
}

func (g *targetGate) throttle(targets ...awsx.Target) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.throttled = map[awsx.Target]bool{}
	for _, t := range targets {
		g.throttled[t] = true
	}
}

func (g *targetGate) queriedServices() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.services...)
}

func (g *targetGate) CloudWatch(ctx context.Context, t awsx.Target) (awsx.CloudWatchAPI, error) {
	g.mu.Lock()
	throttled := g.throttled[t]
	g.mu.Unlock()
	if throttled {
		return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded", Fault: smithy.FaultClient}
	}
	api, err := g.Clients.CloudWatch(ctx, t)
	if err != nil {
		return nil, err
	}
	return &serviceRecordingCloudWatch{CloudWatchAPI: api, gate: g}, nil
}

type serviceRecordingCloudWatch struct {
	awsx.CloudWatchAPI
	gate *targetGate
}

func (r *serviceRecordingCloudWatch) GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	r.gate.mu.Lock()
	for _, q := range in.MetricDataQueries {
		if q.MetricStat == nil || q.MetricStat.Metric == nil || aws.ToString(q.MetricStat.Metric.Namespace) != "ECS/ContainerInsights" {
			continue
		}
		for _, d := range q.MetricStat.Metric.Dimensions {
			if aws.ToString(d.Name) == "ServiceName" {
				r.gate.services = append(r.gate.services, aws.ToString(d.Value))
			}
		}
	}
	r.gate.mu.Unlock()
	return r.CloudWatchAPI.GetMetricData(ctx, in, opts...)
}

func newBatchHarness(t *testing.T) (*metricsHarness, *targetGate) {
	t.Helper()
	h := newMetricsHarness(t, 0)
	gate := &targetGate{Clients: h.cw.Clients}
	h.cw.Clients = gate
	return h, gate
}

func notificationsID() string {
	return serviceID(homelabAccountID, "us-east-1", "core-services", "notifications")
}

func stagingAPIID() string {
	return serviceID(stagingAccountID, "us-east-1", "staging-web", "api")
}

func devEmailSenderID() string {
	return serviceID(homelabAccountID, "us-east-1", "background-jobs", "email-sender")
}

func batchPath(ids ...string) string {
	q := url.Values{}
	for _, id := range ids {
		q.Add("id", id)
	}
	return "/api/services/metrics?" + q.Encode()
}

func getBatch(t *testing.T, c *apiClient, ids ...string) ServicesMetricsDTO {
	t.Helper()
	resp := c.get(batchPath(ids...))
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("status %d (%s: %s), want 200", resp.StatusCode, code, msg)
	}
	var out ServicesMetricsDTO
	decode(t, resp, &out)
	return out
}

func metricsCalls(h *metricsHarness) uint64 {
	var n uint64
	for key, count := range h.fake.CallCounts() {
		if strings.Contains(key, "|cloudwatch|GetMetricData") {
			n += count
		}
	}
	return n
}

func TestServicesMetrics_BatchOneFetchPerTarget(t *testing.T) {
	h, _ := newBatchHarness(t)
	c := login(t, h.srv, "sam", testPassword)
	h.fake.ResetCallCounts()

	out := getBatch(t, c, paymentsID(), notificationsID(), stagingAPIID())

	if n := metricsCalls(h); n != 2 {
		t.Errorf("GetMetricData calls = %d; want 2 (one per account x region)", n)
	}
	for _, id := range []string{paymentsID(), notificationsID(), stagingAPIID()} {
		item, ok := out.Items[id]
		if !ok {
			t.Fatalf("items lacks %s", id)
		}
		if item.Insights == "disabled" || item.CPU == nil || item.Mem == nil {
			t.Fatalf("item %s = %+v; want insights on and cpu/mem values", id, item)
		}
		if *item.CPU < 0 || *item.CPU > 100 || *item.Mem < 0 || *item.Mem > 100 {
			t.Errorf("item %s cpu/mem = %v/%v; want within [0,100]", id, *item.CPU, *item.Mem)
		}
	}
	if len(out.Errors) != 0 || out.PolledAt.IsZero() {
		t.Errorf("errors = %+v, polled_at = %v; want no errors and a poll time", out.Errors, out.PolledAt)
	}
}

// Review focus 7: an id outside the caller's scope never returns metrics, is never queried and is
// never named in an error.
func TestServicesMetrics_OmitsInvisibleIds(t *testing.T) {
	h, gate := newBatchHarness(t)
	c := login(t, h.srv, "omer", testPassword)

	out := getBatch(t, c, paymentsID(), devEmailSenderID())

	if _, leaked := out.Items[paymentsID()]; leaked {
		t.Error("a Prod service the caller cannot see came back with metrics")
	}
	if _, ok := out.Items[devEmailSenderID()]; !ok || len(out.Items) != 1 {
		t.Errorf("items = %v; want only the Dev service", out.Items)
	}
	if len(out.Errors) != 0 {
		t.Errorf("errors = %+v; want none", out.Errors)
	}
	for _, name := range gate.queriedServices() {
		if name == "payments-api" {
			t.Error("a Container Insights query named the service the caller cannot see")
		}
	}
}

// Even when the invisible service's own target is failing, nothing about it is reported.
func TestServicesMetrics_InvisibleIdNeverNamedInErrors(t *testing.T) {
	h, gate := newBatchHarness(t)
	gate.throttle(awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"})
	resp := login(t, h.srv, "omer", testPassword).get(batchPath(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 with nothing visible", resp.StatusCode)
	}
	var out ServicesMetricsDTO
	decode(t, resp, &out)
	if len(out.Items) != 0 || len(out.Errors) != 0 {
		t.Errorf("items = %v, errors = %+v; want both empty", out.Items, out.Errors)
	}
}

// An id the caller cannot see and an id that does not exist look the same: absent, never queried.
func TestServicesMetrics_UnknownIdOmitted(t *testing.T) {
	h, gate := newBatchHarness(t)
	ghost := serviceID(homelabAccountID, "us-east-1", "core-services", "ghost-service")
	out := getBatch(t, login(t, h.srv, "sam", testPassword), ghost, paymentsID())
	if len(out.Items) != 1 || out.Items[paymentsID()].CPU == nil {
		t.Errorf("items = %v; want only payments-api", out.Items)
	}
	for _, name := range gate.queriedServices() {
		if name == "ghost-service" {
			t.Error("an unknown service was queried")
		}
	}
}

func TestServicesMetrics_MalformedIdOmitted(t *testing.T) {
	h, _ := newBatchHarness(t)
	out := getBatch(t, login(t, h.srv, "sam", testPassword), "abc", paymentsID())
	if len(out.Items) != 1 || out.Items[paymentsID()].CPU == nil {
		t.Errorf("items = %v; want one item for payments-api", out.Items)
	}
}

// Container Insights off is reported as such, not as zero, and costs no Container Insights query.
func TestServicesMetrics_InsightsOffNoQuery(t *testing.T) {
	h, gate := newBatchHarness(t)
	h.cw.reset()
	c := login(t, h.srv, "sam", testPassword)

	resp := c.get(batchPath(edgeNginxID()))
	var raw struct {
		Items map[string]map[string]any `json:"items"`
	}
	decode(t, resp, &raw)
	item, ok := raw.Items[edgeNginxID()]
	if !ok || item["insights"] != "disabled" {
		t.Fatalf("item = %v (present %v); want insights disabled", item, ok)
	}
	for _, field := range []string{"cpu", "mem"} {
		if v, present := item[field]; !present || v != nil {
			t.Errorf("%s = %v (present %v); want an explicit null, not zero", field, v, present)
		}
	}
	if n := h.cw.queried("ECS/ContainerInsights"); n != 0 || len(gate.queriedServices()) != 0 {
		t.Errorf("%d Container Insights queries for a cluster with Insights off", n)
	}
}

func TestServicesMetrics_OneTargetThrottledStillServesOthers(t *testing.T) {
	h, gate := newBatchHarness(t)
	gate.throttle(awsx.Target{AccountID: stagingAccountID, Region: "us-east-1"})
	out := getBatch(t, login(t, h.srv, "sam", testPassword), paymentsID(), notificationsID(), stagingAPIID())

	if out.Items[paymentsID()].CPU == nil || out.Items[notificationsID()].CPU == nil {
		t.Errorf("items = %v; want the healthy target's services", out.Items)
	}
	if _, present := out.Items[stagingAPIID()]; present {
		t.Error("the throttled target's service must be absent, not zero")
	}
	if len(out.Errors) != 1 || out.Errors[0].AccountID != stagingAccountID || out.Errors[0].Region != "us-east-1" || out.Errors[0].Code != "throttled" {
		t.Errorf("errors = %+v; want one throttled entry for the staging account", out.Errors)
	}
}

// A failed target does not hide that Container Insights is off for a cluster elsewhere, and a
// cluster with Insights off on a failed target is still reported as off (no query was needed).
func TestServicesMetrics_PartialFailureKeepsDisabledReport(t *testing.T) {
	h, gate := newBatchHarness(t)
	gate.throttle(awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"})
	out := getBatch(t, login(t, h.srv, "sam", testPassword), paymentsID(), edgeNginxID())
	if out.Items[edgeNginxID()].Insights != "disabled" {
		t.Errorf("items = %v; want edge-nginx reported as disabled", out.Items)
	}
	if _, present := out.Items[paymentsID()]; present || len(out.Errors) != 1 {
		t.Errorf("items = %v, errors = %+v; want payments-api absent with one error", out.Items, out.Errors)
	}
}

func TestServicesMetrics_EveryTargetThrottledIs503(t *testing.T) {
	h, gate := newBatchHarness(t)
	gate.throttle(awsx.Target{AccountID: homelabAccountID, Region: "us-east-1"}, awsx.Target{AccountID: stagingAccountID, Region: "us-east-1"})
	resp := login(t, h.srv, "sam", testPassword).get(batchPath(paymentsID(), stagingAPIID()))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "throttled" {
		t.Errorf("code %q, want throttled", code)
	}
}

func TestServicesMetrics_CapAndEmpty422(t *testing.T) {
	h, _ := newBatchHarness(t)
	c := login(t, h.srv, "sam", testPassword)

	resp := c.get("/api/services/metrics")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no ids: status %d, want 422", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "invalid" || msg != "id is required." {
		t.Errorf("no ids: %q %q", code, msg)
	}

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = serviceID(homelabAccountID, "us-east-1", "core-services", "svc-"+strconv.Itoa(i))
	}
	resp = c.get(batchPath(ids...))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("101 ids: status %d, want 422", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); msg != "At most 100 ids per request." {
		t.Errorf("101 ids: message %q", msg)
	}
	if got := getBatch(t, c, ids[:100]...); len(got.Items) != 0 {
		t.Errorf("100 unknown ids: items = %v; want none", got.Items)
	}
}

func TestServicesMetrics_DuplicateIdsCountOnce(t *testing.T) {
	h, _ := newBatchHarness(t)
	h.fake.ResetCallCounts()
	out := getBatch(t, login(t, h.srv, "sam", testPassword), paymentsID(), paymentsID())
	if calls := metricsCalls(h); len(out.Items) != 1 || calls != 1 {
		t.Errorf("items = %d, calls = %d; want 1 and 1", len(out.Items), calls)
	}
}

// The batch asks for the same 1h series as the Metrics tab, so one warms the other.
func TestServicesMetrics_SharesCacheWithMetricsTab1h(t *testing.T) {
	h, _ := newBatchHarness(t)
	c := login(t, h.srv, "sam", testPassword)
	getServiceMetricsDTO(t, c, paymentsID(), "?range=1h")
	h.fake.ResetCallCounts()

	out := getBatch(t, c, paymentsID())
	if out.Items[paymentsID()].CPU == nil {
		t.Fatal("no cpu from the cached series")
	}
	if calls := metricsCalls(h); calls != 0 {
		t.Errorf("GetMetricData calls = %d; want 0 (served from the Metrics tab's cache)", calls)
	}
}

func TestServicesMetrics_ETag(t *testing.T) {
	h, _ := newBatchHarness(t)
	c := login(t, h.srv, "sam", testPassword)
	first := c.get(batchPath(paymentsID(), notificationsID()))
	etag := first.Header.Get("ETag")
	if first.StatusCode != http.StatusOK || etag == "" {
		t.Fatalf("status %d, etag %q", first.StatusCode, etag)
	}
	again := c.do(http.MethodGet, batchPath(paymentsID(), notificationsID()), nil, map[string]string{"If-None-Match": etag})
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("repeat with If-None-Match: status %d, want 304", again.StatusCode)
	}
}

func TestServicesMetrics_RequiresLogin(t *testing.T) {
	h, _ := newBatchHarness(t)
	resp, err := http.Get(h.srv.URL + batchPath(paymentsID()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}
