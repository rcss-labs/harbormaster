package metrics

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// Query id prefixes of UtilQueries; the service index follows.
const (
	UtilCPUAvg      = "cpu_avg_"
	UtilCPUP95      = "cpu_p95_"
	UtilMemAvg      = "mem_avg_"
	UtilMemP95      = "mem_p95_"
	UtilCPUReserved = "cpu_res_"
	UtilMemReserved = "mem_res_"
)

// UtilQueries returns the six Container Insights queries behind one service's daily utilisation row: average and
// p95 of CPU and memory utilised, plus average reserved CPU and memory. Ids are prefix+index so thousands of
// services can share one batch.
func UtilQueries(index int, cluster, service string, period int) []Query {
	dims := []Dim{{"ClusterName", cluster}, {"ServiceName", service}}
	n := strconv.Itoa(index)
	return []Query{
		ciQuery(UtilCPUAvg+n, "CpuUtilized", "Average", period, dims),
		ciQuery(UtilCPUP95+n, "CpuUtilized", "p95", period, dims),
		ciQuery(UtilMemAvg+n, "MemoryUtilized", "Average", period, dims),
		ciQuery(UtilMemP95+n, "MemoryUtilized", "p95", period, dims),
		ciQuery(UtilCPUReserved+n, "CpuReserved", "Average", period, dims),
		ciQuery(UtilMemReserved+n, "MemoryReserved", "Average", period, dims),
	}
}

// FetchValues runs queries against CloudWatch for [start, end), chunked at the 500-query limit and following
// NextToken, and returns the last datapoint of each query by id; queries without a datapoint are absent. It talks
// to CloudWatch directly: no cache and no span rules, for callers that fetch one fixed window once (the daily rollup).
func FetchValues(ctx context.Context, api awsx.CloudWatchAPI, queries []Query, start, end time.Time) (map[string]float64, error) {
	out := make(map[string]float64, len(queries))
	for chunk := range slices.Chunk(queries, maxQueriesPerCall) {
		input := &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(start), EndTime: aws.Time(end),
			ScanBy:            cwtypes.ScanByTimestampAscending,
			MetricDataQueries: make([]cwtypes.MetricDataQuery, len(chunk)),
		}
		for n, q := range chunk {
			input.MetricDataQueries[n] = toMetricDataQuery(q)
		}
		for {
			page, err := api.GetMetricData(ctx, input)
			if err != nil {
				return nil, err
			}
			for _, r := range page.MetricDataResults {
				if len(r.Values) > 0 {
					out[aws.ToString(r.Id)] = r.Values[len(r.Values)-1]
				}
			}
			if aws.ToString(page.NextToken) == "" {
				break
			}
			input.NextToken = page.NextToken
		}
	}
	return out, nil
}
