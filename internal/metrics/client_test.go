package metrics

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
)

var (
	homelab = awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	// 12:00:10 so the period floor is visible for 60 s and larger periods.
	testNow = time.Date(2026, 10, 31, 12, 0, 10, 0, time.UTC)
)

// stubCloudWatch answers GetMetricData through a test-supplied function and
// records the number of queries in each call.
type stubCloudWatch struct {
	mu     sync.Mutex
	sizes  []int
	answer func(in *cloudwatch.GetMetricDataInput) (*cloudwatch.GetMetricDataOutput, error)
}

func (s *stubCloudWatch) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	s.mu.Lock()
	s.sizes = append(s.sizes, len(in.MetricDataQueries))
	s.mu.Unlock()
	return s.answer(in)
}

// stubClients hands out one CloudWatch API; the other services are unused.
type stubClients struct {
	awsx.Clients
	cw awsx.CloudWatchAPI
}

func (s stubClients) CloudWatch(context.Context, awsx.Target) (awsx.CloudWatchAPI, error) {
	return s.cw, nil
}

func emptyAnswer(in *cloudwatch.GetMetricDataInput) (*cloudwatch.GetMetricDataOutput, error) {
	out := &cloudwatch.GetMetricDataOutput{}
	for _, q := range in.MetricDataQueries {
		out.MetricDataResults = append(out.MetricDataResults, cwtypes.MetricDataResult{Id: q.Id, StatusCode: cwtypes.StatusCodeComplete})
	}
	return out, nil
}

func newTestClient(c awsx.Clients) *Client {
	client := New(c, nil, 60*time.Second)
	client.Now = func() time.Time { return testNow }
	return client
}

func cpuQuery(id string) Query {
	return Query{ID: id, Namespace: "ECS/ContainerInsights", MetricName: "CpuUtilized", Stat: "Average", Period: 60,
		Dimensions: []Dim{{"ClusterName", "core-services"}, {"ServiceName", "payments-api"}}}
}

func newFakeClients() *fake.Fake {
	return fake.New(fake.MockupEstate(func() time.Time { return testNow }))
}

func TestFetch_BatchesOver500AndKeepsOrder(t *testing.T) {
	stub := &stubCloudWatch{answer: emptyAnswer}
	client := newTestClient(stubClients{cw: stub})
	queries := make([]Query, 1203)
	for i := range queries {
		queries[i] = cpuQuery(fmt.Sprintf("q%d", i))
		queries[i].MetricName = fmt.Sprintf("m%d", i)
	}
	res, err := client.Fetch(context.Background(), homelab, Span1h, queries)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stub.sizes) != "[500 500 203]" {
		t.Errorf("chunk sizes = %v, want [500 500 203]", stub.sizes)
	}
	if len(res.Series) != len(queries) {
		t.Fatalf("series = %d", len(res.Series))
	}
	for i, s := range res.Series {
		if s.ID != queries[i].ID {
			t.Fatalf("series[%d].ID = %s, want %s", i, s.ID, queries[i].ID)
		}
	}
}

func TestFetch_BatchesAgainstFakeRecordsThreeCalls(t *testing.T) {
	f := newFakeClients()
	client := newTestClient(f)
	queries := make([]Query, 1203)
	for i := range queries {
		queries[i] = cpuQuery(fmt.Sprintf("q%d", i))
		queries[i].MetricName = fmt.Sprintf("m%d", i)
	}
	if _, err := client.Fetch(context.Background(), homelab, Span1h, queries); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()["GetMetricData"]; got != 3 {
		t.Errorf("GetMetricData calls = %d, want 3", got)
	}
}

func TestFetch_FollowsNextToken(t *testing.T) {
	f := newFakeClients()
	f.CloudWatchPageSize = 1000
	client := newTestClient(f)
	res, err := client.Fetch(context.Background(), homelab, Span72h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()["GetMetricData"]; got != 5 {
		t.Errorf("calls = %d, want 5 pages for 4320 points at 1000/page", got)
	}
	if len(res.Timestamps) != 4320 || len(res.Series[0].Values) != 4320 {
		t.Fatalf("len ts=%d values=%d, want 4320", len(res.Timestamps), len(res.Series[0].Values))
	}
	for i, v := range res.Series[0].Values {
		if v == nil {
			t.Fatalf("gap at bucket %d", i)
		}
	}
}

func TestFetch_GridIsFlooredToPeriod(t *testing.T) {
	client := newTestClient(stubClients{cw: &stubCloudWatch{answer: emptyAnswer}})
	q := cpuQuery("cpu")
	q.Period = 300
	res, err := client.Fetch(context.Background(), homelab, Span1h, []Query{q})
	if err != nil {
		t.Fatal(err)
	}
	wantEnd := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if !res.End.Equal(wantEnd) || !res.Start.Equal(wantEnd.Add(-time.Hour)) || len(res.Timestamps) != 12 {
		t.Errorf("grid start=%v end=%v n=%d", res.Start, res.End, len(res.Timestamps))
	}
}

func TestFetch_AlignsPointsToGrid_NullForGaps(t *testing.T) {
	var start, end time.Time
	stub := &stubCloudWatch{answer: func(in *cloudwatch.GetMetricDataInput) (*cloudwatch.GetMetricDataOutput, error) {
		start, end = *in.StartTime, *in.EndTime
		if in.ScanBy != cwtypes.ScanByTimestampAscending {
			t.Errorf("ScanBy = %v, want ascending", in.ScanBy)
		}
		return &cloudwatch.GetMetricDataOutput{MetricDataResults: []cwtypes.MetricDataResult{{
			Id: in.MetricDataQueries[0].Id, StatusCode: cwtypes.StatusCodeComplete,
			Timestamps: []time.Time{
				start.Add(time.Second),     // 1 s inside the window: bucket 0
				end.Add(-time.Minute),      // last bucket
				end.Add(time.Second),       // 1 s past End: dropped
				start.Add(-time.Second),    // before Start: dropped
				end.Add(-time.Minute + 10), // same bucket again: last wins
			},
			Values: []float64{1, 2, 99, 98, 3},
		}}}, nil
	}}
	client := newTestClient(stubClients{cw: stub})
	res, err := client.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	vals := res.Series[0].Values
	n := len(res.Timestamps)
	if n != 60 || len(vals) != n {
		t.Fatalf("n=%d values=%d", n, len(vals))
	}
	if !start.Equal(res.Start) || !end.Equal(res.End) {
		t.Errorf("request window %v-%v != grid %v-%v", start, end, res.Start, res.End)
	}
	if vals[0] == nil || *vals[0] != 1 || vals[n-1] == nil || *vals[n-1] != 3 {
		t.Errorf("first/last = %v / %v, want 1 / 3", vals[0], vals[n-1])
	}
	for i := 1; i < n-1; i++ {
		if vals[i] != nil {
			t.Fatalf("bucket %d = %v, want nil", i, *vals[i])
		}
	}
}

func TestFetch_NoDatapointsGivesNulls(t *testing.T) {
	client := newTestClient(stubClients{cw: &stubCloudWatch{answer: emptyAnswer}})
	res, err := client.Fetch(context.Background(), homelab, Span6h, []Query{cpuQuery("cpu")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series[0].Values) != len(res.Timestamps) || len(res.Timestamps) == 0 {
		t.Fatalf("values=%d timestamps=%d", len(res.Series[0].Values), len(res.Timestamps))
	}
	for _, v := range res.Series[0].Values {
		if v != nil {
			t.Fatal("expected all nil")
		}
	}
}

func TestFetch_ThrottleWithoutCacheReturnsThrottleErr(t *testing.T) {
	f := newFakeClients()
	f.ThrottleCloudWatch = 1
	client := newTestClient(f)
	_, err := client.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("cpu")})
	if err == nil || !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want a throttle error", err)
	}
}

func TestFetch_MixedPeriodsRejected(t *testing.T) {
	client := newTestClient(stubClients{cw: &stubCloudWatch{answer: emptyAnswer}})
	a, b := cpuQuery("a"), cpuQuery("b")
	b.Period = 300
	if _, err := client.Fetch(context.Background(), homelab, Span1h, []Query{a, b}); err != ErrMixedPeriods {
		t.Fatalf("err = %v, want ErrMixedPeriods", err)
	}
}

func TestFetch_RejectsDuplicateAndIllegalIDs(t *testing.T) {
	client := newTestClient(stubClients{cw: &stubCloudWatch{answer: emptyAnswer}})
	if _, err := client.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("a"), cpuQuery("a")}); err == nil {
		t.Error("duplicate id accepted")
	}
	if _, err := client.Fetch(context.Background(), homelab, Span1h, []Query{cpuQuery("Bad-Id")}); err == nil {
		t.Error("illegal id accepted")
	}
}
