package metrics

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	nsContainerInsights = "ECS/ContainerInsights"
	nsALB               = "AWS/ApplicationELB"
)

func ciQuery(id, metric, stat string, period int, dims []Dim) Query {
	return Query{ID: id, Namespace: nsContainerInsights, MetricName: metric, Dimensions: dims, Stat: stat, Period: period, Label: id}
}

func cpuMemQueries(suffix string, period int, dims []Dim) []Query {
	return []Query{
		ciQuery("cpu_u"+suffix, "CpuUtilized", "Average", period, dims),
		ciQuery("cpu_r"+suffix, "CpuReserved", "Average", period, dims),
		ciQuery("mem_u"+suffix, "MemoryUtilized", "Average", period, dims),
		ciQuery("mem_r"+suffix, "MemoryReserved", "Average", period, dims),
	}
}

// ClusterQueries returns CPU and memory utilized/reserved for a cluster.
func ClusterQueries(cluster string, period int) []Query {
	return cpuMemQueries("", period, []Dim{{"ClusterName", cluster}})
}

// ServiceQueries returns CPU, memory, network and running-task-count queries for a service.
func ServiceQueries(cluster, service string, period int) []Query {
	dims := []Dim{{"ClusterName", cluster}, {"ServiceName", service}}
	qs := cpuMemQueries("", period, dims)
	return append(qs,
		ciQuery("net_rx", "NetworkRxBytes", "Average", period, dims),
		ciQuery("net_tx", "NetworkTxBytes", "Average", period, dims),
		ciQuery("tasks", "RunningTaskCount", "Average", period, dims),
	)
}

var illegalIDChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// TaskQueries returns per-task CPU and memory queries (enhanced Container
// Insights only). IDs carry the sanitized task ID so several tasks can share a Fetch.
func TaskQueries(cluster, family, taskID string, period int) []Query {
	dims := []Dim{{"ClusterName", cluster}, {"TaskDefinitionFamily", family}, {"TaskId", taskID}}
	return cpuMemQueries("_"+illegalIDChars.ReplaceAllString(taskID, "_"), period, dims)
}

// TargetGroupRef pairs a target group with the load balancer that fronts it.
type TargetGroupRef struct{ TargetGroupARN, LoadBalancerARN string }

// ALBQueries returns five queries per target group, suffixed with the group index.
// Groups whose ARNs cannot be parsed are skipped but keep their index.
func ALBQueries(groups []TargetGroupRef, period int) []Query {
	var qs []Query
	for i, g := range groups {
		tg, err1 := ARNSuffix(g.TargetGroupARN)
		lb, err2 := ARNSuffix(g.LoadBalancerARN)
		if err1 != nil || err2 != nil {
			continue
		}
		dims := []Dim{{"LoadBalancer", lb}, {"TargetGroup", tg}}
		add := func(prefix, metric, stat string) {
			id := prefix + "_" + strconv.Itoa(i)
			qs = append(qs, Query{ID: id, Namespace: nsALB, MetricName: metric, Dimensions: dims, Stat: stat, Period: period, Label: id})
		}
		add("req", "RequestCount", "Sum")
		add("p95", "TargetResponseTime", "p95")
		add("e5xx", "HTTPCode_Target_5XX_Count", "Sum")
		add("healthy", "HealthyHostCount", "Average")
		add("unhealthy", "UnHealthyHostCount", "Average")
	}
	return qs
}

// ARNSuffix extracts the CloudWatch dimension value from an ELBv2 ARN:
// "targetgroup/x/abc" for target groups, "app/x/abc" for load balancers.
func ARNSuffix(arn string) (string, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "elasticloadbalancing" {
		return "", fmt.Errorf("not an elasticloadbalancing ARN: %q", arn)
	}
	resource := parts[5]
	switch {
	case strings.HasPrefix(resource, "targetgroup/"):
		return resource, nil
	case strings.HasPrefix(resource, "loadbalancer/"):
		return strings.TrimPrefix(resource, "loadbalancer/"), nil
	}
	return "", fmt.Errorf("unsupported elasticloadbalancing resource: %q", arn)
}

// InsightsMode reports "disabled", "enabled" or "enhanced". The cluster's raw
// ECS settings are consulted first (encoding/json matches keys
// case-insensitively, so both SDK-cased and lower-case JSON work); the
// normalized flag is the fallback for "enabled".
func InsightsMode(c model.Cluster) string {
	var raw struct {
		Settings []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	if len(c.Raw) > 0 && json.Unmarshal(c.Raw, &raw) == nil {
		for _, s := range raw.Settings {
			if s.Name != "containerInsights" {
				continue
			}
			switch s.Value {
			case "enhanced", "enabled":
				return s.Value
			case "disabled":
				return "disabled"
			}
		}
	}
	if c.ContainerInsights {
		return "enabled"
	}
	return "disabled"
}

// ServiceTargetGroups lists the service's target groups with their load
// balancer ARN taken from target health; groups without one are skipped.
func ServiceTargetGroups(s model.Service, th []model.TargetHealth) []TargetGroupRef {
	lbByTG := map[string]string{}
	for _, h := range th {
		if h.LoadBalancerARN != "" {
			lbByTG[h.TargetGroupARN] = h.LoadBalancerARN
		}
	}
	var refs []TargetGroupRef
	for _, l := range s.LoadBalancers {
		if lb, ok := lbByTG[l.TargetGroupARN]; ok {
			refs = append(refs, TargetGroupRef{TargetGroupARN: l.TargetGroupARN, LoadBalancerARN: lb})
		}
	}
	return refs
}
