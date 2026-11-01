package fake

import (
	"context"
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	maxMetricQueries          = 500
	defaultCloudWatchPageSize = 100800
	cloudWatchTokenPrefix     = "fakecw-"
)

// cloudWatchClient is the fake CloudWatch API scoped to one account and region.
type cloudWatchClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.CloudWatchAPI = (*cloudWatchClient)(nil)

// GetMetricData answers ECS/ContainerInsights and AWS/ApplicationELB queries
// with deterministic series: a value depends only on the metric, its
// dimensions, the period and the timestamp, so overlapping windows agree.
// Like AWS, results are newest first unless ScanBy asks for ascending, and
// are paged once the datapoints exceed the page size.
func (c *cloudWatchClient) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	if err := c.f.beginThrottled("GetMetricData", &c.f.ThrottleCloudWatch); err != nil {
		return nil, err
	}
	if len(in.MetricDataQueries) > maxMetricQueries {
		return nil, &smithy.GenericAPIError{Code: "ValidationError",
			Message: "The collection MetricDataQueries must not have a size greater than " + strconv.Itoa(maxMetricQueries) + ".", Fault: smithy.FaultClient}
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()

	end := e.now()
	if in.EndTime != nil {
		end = *in.EndTime
	}
	start := end.Add(-3 * time.Hour)
	if in.StartTime != nil {
		start = *in.StartTime
	}

	pageSize := c.f.CloudWatchPageSize
	if pageSize <= 0 {
		pageSize = defaultCloudWatchPageSize
	}
	if in.MaxDatapoints != nil && *in.MaxDatapoints > 0 {
		pageSize = min(pageSize, int(*in.MaxDatapoints))
	}
	offset := 0
	if in.NextToken != nil && *in.NextToken != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(*in.NextToken, cloudWatchTokenPrefix))
		if err != nil || !strings.HasPrefix(*in.NextToken, cloudWatchTokenPrefix) || n < 0 {
			return nil, &smithy.GenericAPIError{Code: "InvalidNextToken", Message: "The next token is invalid.", Fault: smithy.FaultClient}
		}
		offset = n
	}
	ascending := in.ScanBy == cwtypes.ScanByTimestampAscending

	type series struct {
		query  cwtypes.MetricDataQuery
		stamps []time.Time
		values []float64
	}
	var all []series
	total := 0
	for _, q := range in.MetricDataQueries {
		if q.ReturnData != nil && !*q.ReturnData {
			continue
		}
		s := series{query: q}
		if q.MetricStat != nil && q.MetricStat.Metric != nil {
			period := time.Duration(aws.ToInt32(q.MetricStat.Period)) * time.Second
			if period <= 0 {
				period = time.Minute
			}
			for ts := start; ts.Before(end); ts = ts.Add(period) {
				s.stamps = append(s.stamps, ts)
			}
			s.values = c.valuesFor(q.MetricStat, s.stamps, period)
			if !ascending {
				slices.Reverse(s.stamps)
				slices.Reverse(s.values)
			}
		}
		all = append(all, s)
		total += len(s.stamps)
	}

	out := &cloudwatch.GetMetricDataOutput{}
	pageEnd := min(offset+pageSize, total)
	seen := 0
	for _, s := range all {
		from, to := max(offset-seen, 0), min(pageEnd-seen, len(s.stamps))
		seen += len(s.stamps)
		if to < from || (from == to && total > 0 && len(s.stamps) > 0) {
			continue
		}
		label := aws.ToString(s.query.Label)
		if label == "" && s.query.MetricStat != nil && s.query.MetricStat.Metric != nil {
			label = aws.ToString(s.query.MetricStat.Metric.MetricName)
		}
		status := cwtypes.StatusCodeComplete
		if to < len(s.stamps) {
			status = cwtypes.StatusCodePartialData
		}
		out.MetricDataResults = append(out.MetricDataResults, cwtypes.MetricDataResult{
			Id: s.query.Id, Label: aws.String(label), StatusCode: status,
			Timestamps: append([]time.Time(nil), s.stamps[from:to]...),
			Values:     append([]float64(nil), s.values[from:to]...),
		})
	}
	if pageEnd < total {
		out.NextToken = aws.String(cloudWatchTokenPrefix + strconv.Itoa(pageEnd))
	}
	return out, nil
}

// valuesFor computes one value per timestamp. Caller holds Mu.
func (c *cloudWatchClient) valuesFor(stat *cwtypes.MetricStat, stamps []time.Time, period time.Duration) []float64 {
	m := stat.Metric
	dims := map[string]string{}
	seedParts := []string{aws.ToString(m.Namespace), aws.ToString(m.MetricName), aws.ToString(stat.Stat), period.String()}
	for _, d := range m.Dimensions {
		dims[aws.ToString(d.Name)] = aws.ToString(d.Value)
		seedParts = append(seedParts, aws.ToString(d.Name)+"="+aws.ToString(d.Value))
	}
	h := fnv.New64a()
	h.Write([]byte(strings.Join(seedParts, "|")))
	seed := h.Sum64()

	var point func(ts time.Time, u float64) float64
	switch aws.ToString(m.Namespace) {
	case "ECS/ContainerInsights":
		point = c.ecsPoint(aws.ToString(m.MetricName), dims)
	case "AWS/ApplicationELB":
		point = c.elbPoint(aws.ToString(m.MetricName), aws.ToString(stat.Stat), dims, period)
	case "AWS/EFS": // plan 05
		point = c.efsPoint(aws.ToString(m.MetricName), dims, period)
	}
	values := make([]float64, len(stamps))
	if point == nil {
		return values
	}
	bucket := uint64(max(period.Milliseconds(), 1))
	for i, ts := range stamps {
		u := unitNoise(seed, uint64(ts.UnixMilli())/bucket)
		values[i] = point(ts, u)
	}
	return values
}

// unitNoise maps (seed, bucket) to a stable value in [0,1).
func unitNoise(seed, bucket uint64) float64 {
	z := seed ^ (bucket+1)*0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return float64(z>>11) / float64(1<<53)
}

// noisy returns base +/- spread driven by u, never below zero.
func noisy(base, spread, u float64) float64 { return math.Max(0, base+(u-0.5)*2*spread) }

func percent(v float64) float64 { return math.Min(100, math.Max(0, v)) }

var defaultProfile = MetricProfile{CPUBase: 25, MemBase: 45, NetInBase: 25, NetOutBase: 18, RPMScale: 1, Running: true}

// ecsPoint builds the value function for a Container Insights metric. CPU and
// memory "utilized" are percentages of the reservation, which is reported as
// 100, so utilized/reserved is the percentage the UI shows. Caller holds Mu.
func (c *cloudWatchClient) ecsPoint(metric string, dims map[string]string) func(time.Time, float64) float64 {
	e := c.f.Estate
	cluster, service := dims["ClusterName"], dims["ServiceName"]
	profile, running, desired := defaultProfile, 0, 0
	if service != "" {
		key := serviceKeyOf(c.target.AccountID, c.target.Region, cluster, service)
		if p, ok := e.MetricProfiles[key]; ok {
			profile = p
		}
		running, desired = e.runningAndDesired(key)
	} else {
		running, desired = e.clusterCounts(model.ClusterKey{AccountID: c.target.AccountID, Region: c.target.Region, Cluster: cluster})
	}
	if dims["TaskId"] != "" {
		running, desired = 1, 1
	}
	// A service with no running tasks reports zero CPU and memory.
	utilization := func(base, spread, u float64) float64 {
		if !profile.Running {
			return 0
		}
		return percent(noisy(base, spread, u))
	}
	switch metric {
	case "CpuUtilized":
		return func(_ time.Time, u float64) float64 { return utilization(profile.CPUBase, 9, u) }
	case "MemoryUtilized":
		return func(_ time.Time, u float64) float64 { return utilization(profile.MemBase, 5, u) }
	case "CpuReserved", "MemoryReserved":
		return func(time.Time, float64) float64 { return 100 }
	case "NetworkRxBytes":
		return func(_ time.Time, u float64) float64 {
			return noisy(profile.NetInBase, profile.NetInBase*0.25, u) * 1024
		}
	case "NetworkTxBytes":
		return func(_ time.Time, u float64) float64 {
			return noisy(profile.NetOutBase, profile.NetOutBase*0.25, u) * 1024
		}
	case "RunningTaskCount":
		return func(time.Time, float64) float64 { return float64(running) }
	case "DesiredTaskCount":
		return func(time.Time, float64) float64 { return float64(desired) }
	case "PendingTaskCount":
		return func(time.Time, float64) float64 { return 0 }
	}
	return func(_ time.Time, u float64) float64 { return noisy(10, 5, u) }
}

// elbPoint builds the value function for an Application Load Balancer metric.
// Caller holds Mu.
func (c *cloudWatchClient) elbPoint(metric, stat string, dims map[string]string, period time.Duration) func(time.Time, float64) float64 {
	e := c.f.Estate
	key, tg, ok := c.targetGroupByDimension(dims["TargetGroup"])
	if !ok {
		return nil
	}
	profile := e.MetricProfiles[key]
	now := e.now()
	switch metric {
	case "RequestCount":
		perPeriod := period.Minutes()
		return func(_ time.Time, u float64) float64 {
			return math.Round(noisy(50*profile.RPMScale, 10*profile.RPMScale, u) * perPeriod)
		}
	case "TargetResponseTime":
		latencyMs := 35 + 85*unitNoise(hashSeed(key.Service, 11), 0)
		spread := 8.0
		if profile.Unhealthy {
			spread *= 6
		}
		return func(_ time.Time, u float64) float64 { return noisy(latencyMs, spread, u) / 1000 }
	case "HTTPCode_Target_5XX_Count":
		return func(ts time.Time, u float64) float64 {
			perHour := 3 * u
			if key.Service == "catalog-api" && ts.After(now.Add(-4*time.Hour)) {
				perHour = 6 + 14*u
			}
			return math.Floor(perHour*period.Hours() + unitNoise(hashSeed(key.Service, 13), uint64(ts.UnixNano())))
		}
	case "HealthyHostCount":
		return func(time.Time, float64) float64 { return float64(tg.Healthy) }
	case "UnHealthyHostCount":
		return func(time.Time, float64) float64 { return float64(tg.Unhealthy) }
	}
	return nil
}

// targetGroupByDimension resolves a "targetgroup/<name>/<id>" dimension value
// to the service and target group in this target, matching on the name.
// Caller holds Mu.
func (c *cloudWatchClient) targetGroupByDimension(dim string) (model.ServiceKey, TargetGroup, bool) {
	parts := strings.Split(dim, "/")
	if len(parts) < 2 {
		return model.ServiceKey{}, TargetGroup{}, false
	}
	needle := "targetgroup/" + parts[1] + "/"
	for _, entry := range c.f.Estate.allTargetGroups() {
		if entry.key.AccountID == c.target.AccountID && entry.key.Region == c.target.Region && strings.Contains(entry.group.ARN, needle) {
			return entry.key, entry.group, true
		}
	}
	return model.ServiceKey{}, TargetGroup{}, false
}
