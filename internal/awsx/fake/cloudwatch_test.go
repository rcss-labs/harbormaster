package fake

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

var homelabUSEast1 = awsx.Target{AccountID: homelabID, Region: "us-east-1"}

func cloudWatchFor(t *testing.T, f *Fake, target awsx.Target) awsx.CloudWatchAPI {
	t.Helper()
	api, err := f.CloudWatch(context.Background(), target)
	if err != nil {
		t.Fatalf("CloudWatch(%v): %v", target, err)
	}
	return api
}

func serviceMetricQuery(id, metric, cluster, service string, period int32) cwtypes.MetricDataQuery {
	return cwtypes.MetricDataQuery{
		Id: aws.String(id),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{
				Namespace:  aws.String("ECS/ContainerInsights"),
				MetricName: aws.String(metric),
				Dimensions: []cwtypes.Dimension{
					{Name: aws.String("ClusterName"), Value: aws.String(cluster)},
					{Name: aws.String("ServiceName"), Value: aws.String(service)},
				},
			},
			Period: aws.Int32(period),
			Stat:   aws.String("Average"),
		},
	}
}

func paymentsQueries() []cwtypes.MetricDataQuery {
	return []cwtypes.MetricDataQuery{
		serviceMetricQuery("cpu", "CpuUtilized", "core-services", "payments-api", 300),
		serviceMetricQuery("mem", "MemoryUtilized", "core-services", "payments-api", 300),
		serviceMetricQuery("tasks", "RunningTaskCount", "core-services", "payments-api", 300),
	}
}

func sixHourInput(queries []cwtypes.MetricDataQuery) *cloudwatch.GetMetricDataInput {
	return &cloudwatch.GetMetricDataInput{
		MetricDataQueries: queries,
		StartTime:         aws.Time(fixedNow.Add(-6 * time.Hour)),
		EndTime:           aws.Time(fixedNow),
	}
}

func TestFakeGetMetricData_ReturnsOneResultPerQueryInOrder(t *testing.T) {
	api := cloudWatchFor(t, newFake(), homelabUSEast1)
	out, err := api.GetMetricData(context.Background(), sixHourInput(paymentsQueries()))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.MetricDataResults) != 3 {
		t.Fatalf("results = %d, want 3", len(out.MetricDataResults))
	}
	for i, id := range []string{"cpu", "mem", "tasks"} {
		r := out.MetricDataResults[i]
		if aws.ToString(r.Id) != id || r.StatusCode != cwtypes.StatusCodeComplete {
			t.Fatalf("result %d = %s/%s, want %s/Complete", i, aws.ToString(r.Id), r.StatusCode, id)
		}
		if len(r.Timestamps) != 72 || len(r.Values) != 72 {
			t.Fatalf("%s: %d timestamps, %d values, want 72", id, len(r.Timestamps), len(r.Values))
		}
		for _, v := range r.Values {
			switch id {
			case "tasks":
				if v != 3 {
					t.Fatalf("tasks value = %v, want 3", v)
				}
			default:
				if v < 0 || v > 100 {
					t.Fatalf("%s value %v outside [0,100]", id, v)
				}
			}
		}
	}
}

func TestFakeGetMetricData_IsDeterministic(t *testing.T) {
	api := cloudWatchFor(t, newFake(), homelabUSEast1)
	first, err := api.GetMetricData(context.Background(), sixHourInput(paymentsQueries()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := api.GetMetricData(context.Background(), sixHourInput(paymentsQueries()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.MetricDataResults, second.MetricDataResults) {
		t.Fatal("two identical calls returned different data")
	}
}

func TestFakeGetMetricData_RejectsOver500(t *testing.T) {
	api := cloudWatchFor(t, newFake(), homelabUSEast1)
	queries := make([]cwtypes.MetricDataQuery, 501)
	for i := range queries {
		queries[i] = serviceMetricQuery(fmt.Sprintf("q%d", i), "CpuUtilized", "core-services", "payments-api", 300)
	}
	_, err := api.GetMetricData(context.Background(), sixHourInput(queries))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want an error mentioning 500", err)
	}
}

func TestFakeGetMetricData_ThrottleFlag(t *testing.T) {
	f := newFake()
	f.ThrottleCloudWatch = 1
	api := cloudWatchFor(t, f, homelabUSEast1)
	if _, err := api.GetMetricData(context.Background(), sixHourInput(paymentsQueries())); !awsx.IsThrottle(err) {
		t.Fatalf("first call err = %v, want throttle", err)
	}
	if _, err := api.GetMetricData(context.Background(), sixHourInput(paymentsQueries())); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestFakeGetMetricData_PagesWhenOverPageSize(t *testing.T) {
	f := newFake()
	f.CloudWatchPageSize = 100 // 2 queries x 72 points = 144 > 100
	api := cloudWatchFor(t, f, homelabUSEast1)
	in := sixHourInput(paymentsQueries()[:2])
	counts := map[string]int{}
	pages := 0
	for {
		out, err := api.GetMetricData(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range out.MetricDataResults {
			counts[aws.ToString(r.Id)] += len(r.Values)
		}
		if out.NextToken == nil {
			break
		}
		in.NextToken = out.NextToken
	}
	if pages != 2 || counts["cpu"] != 72 || counts["mem"] != 72 {
		t.Fatalf("pages=%d counts=%v, want 2 pages and 72 points per query", pages, counts)
	}
}

func TestFakeGetMetricData_ELBUnhealthyHostCountFollowsSeed(t *testing.T) {
	api := cloudWatchFor(t, newFake(), awsx.Target{AccountID: homelabID, Region: "eu-west-1"})
	out, err := api.GetMetricData(context.Background(), sixHourInput([]cwtypes.MetricDataQuery{{
		Id: aws.String("healthy_hosts"),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String("AWS/ApplicationELB"), MetricName: aws.String("UnHealthyHostCount"),
				Dimensions: []cwtypes.Dimension{{Name: aws.String("TargetGroup"), Value: aws.String("targetgroup/catalog-api-tg/x")}}},
			Period: aws.Int32(300), Stat: aws.String("Average"),
		}}}))
	if err != nil {
		t.Fatal(err)
	}
	if v := out.MetricDataResults[0].Values; len(v) == 0 || v[0] != 1 {
		t.Fatalf("catalog-api UnHealthyHostCount = %v, want 1", v)
	}
}

func TestFakeGetMetricData_StoppedServiceReportsZeroCPUAndMemory(t *testing.T) {
	api := cloudWatchFor(t, newFake(), homelabUSEast1)
	out, err := api.GetMetricData(context.Background(), sixHourInput([]cwtypes.MetricDataQuery{
		serviceMetricQuery("cpu", "CpuUtilized", "background-jobs", "image-resizer", 300),
		serviceMetricQuery("mem", "MemoryUtilized", "background-jobs", "image-resizer", 300),
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.MetricDataResults {
		for _, v := range r.Values {
			if v != 0 {
				t.Fatalf("%s = %v, want 0 for a service with no running tasks", aws.ToString(r.Id), v)
			}
		}
	}
}
