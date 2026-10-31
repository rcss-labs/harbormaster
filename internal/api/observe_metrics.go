package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// StaleDTO says the series were served from an expired cache row because CloudWatch failed.
type StaleDTO struct {
	Reason    string    `json:"reason"`
	FetchedAt time.Time `json:"fetched_at"`
}

// MarkerDTO is a deployment marker on a usage chart. Service is set only on cluster charts.
type MarkerDTO struct {
	At      time.Time `json:"at"`
	Label   string    `json:"label"`
	Kind    string    `json:"kind"`
	Failed  bool      `json:"failed"`
	Service string    `json:"service,omitempty"`
}

type ServiceSeriesDTO struct {
	CPU       []*float64 `json:"cpu"`
	Mem       []*float64 `json:"mem"`
	NetRxKBps []*float64 `json:"net_rx_kbps"`
	NetTxKBps []*float64 `json:"net_tx_kbps"`
	Tasks     []*float64 `json:"tasks"`
}

type ServiceNowDTO struct {
	CPU      *float64 `json:"cpu"`
	Mem      *float64 `json:"mem"`
	CPUUnits int      `json:"cpu_units"`
	MemMiB   int      `json:"mem_mib"`
	Running  int      `json:"running"`
}

type HourlySeriesDTO struct {
	Timestamps []time.Time `json:"timestamps"`
	Values     []*float64  `json:"values"`
}

type ServiceALBDTO struct {
	ReqPerMin     []*float64      `json:"req_per_min"`
	P95MS         []*float64      `json:"p95_ms"`
	Err5xxPerHour HourlySeriesDTO `json:"err5xx_per_hour"`
	ReqNow        *float64        `json:"req_now"`
	P95Now        *float64        `json:"p95_now"`
	Err5xxToday   float64         `json:"err5xx_today"`
}

type TaskNowDTO struct {
	TaskID string   `json:"task_id"`
	CPU    *float64 `json:"cpu"`
	Mem    *float64 `json:"mem"`
}

// ServiceMetricsDTO is the body of GET /api/services/{serviceId}/metrics.
type ServiceMetricsDTO struct {
	Range      string            `json:"range"`
	Period     int               `json:"period"`
	Insights   string            `json:"insights"`
	Timestamps []time.Time       `json:"timestamps"`
	Series     *ServiceSeriesDTO `json:"series"`
	Now        *ServiceNowDTO    `json:"now"`
	ALB        *ServiceALBDTO    `json:"alb"`
	TasksNow   []TaskNowDTO      `json:"tasks_now"`
	Markers    []MarkerDTO       `json:"markers"`
	Stale      *StaleDTO         `json:"stale"`
	PolledAt   *time.Time        `json:"polled_at"`
}

type CPUMemSeriesDTO struct {
	CPU []*float64 `json:"cpu"`
	Mem []*float64 `json:"mem"`
}

type CPUMemNowDTO struct {
	CPU *float64 `json:"cpu"`
	Mem *float64 `json:"mem"`
}

type ALBNowDTO struct {
	ReqNow      *float64 `json:"req_now"`
	P95Now      *float64 `json:"p95_now"`
	Err5xxToday float64  `json:"err5xx_today"`
}

type ClusterServiceMetricsDTO struct {
	CPU    []*float64 `json:"cpu"`
	Mem    []*float64 `json:"mem"`
	CPUNow *float64   `json:"cpu_now"`
	MemNow *float64   `json:"mem_now"`
	ALB    *ALBNowDTO `json:"alb"`
}

// ClusterMetricsDTO is the body of GET /api/clusters/{clusterId}/metrics.
type ClusterMetricsDTO struct {
	Range      string                              `json:"range"`
	Period     int                                 `json:"period"`
	Insights   string                              `json:"insights"`
	Timestamps []time.Time                         `json:"timestamps"`
	Cluster    *CPUMemSeriesDTO                    `json:"cluster"`
	Now        *CPUMemNowDTO                       `json:"now"`
	Markers    []MarkerDTO                         `json:"markers"`
	Services   map[string]ClusterServiceMetricsDTO `json:"services"`
	Stale      *StaleDTO                           `json:"stale"`
}

type ClusterListMetricsDTO struct {
	Insights string     `json:"insights"`
	CPU      []*float64 `json:"cpu"`
	Mem      []*float64 `json:"mem"`
	CPUNow   *float64   `json:"cpu_now"`
	MemNow   *float64   `json:"mem_now"`
}

// ClustersMetricsDTO is the body of GET /api/clusters/metrics.
type ClustersMetricsDTO struct {
	Items map[string]ClusterListMetricsDTO `json:"items"`
	Stale *StaleDTO                        `json:"stale"`
}

type ClusterSeriesDTO struct {
	ID     string     `json:"id"`
	Label  string     `json:"label"`
	Values []*float64 `json:"values"`
}

// OverviewMetricsDTO is the body of GET /api/overview/metrics.
type OverviewMetricsDTO struct {
	Timestamps   []time.Time        `json:"timestamps"`
	CPUByCluster []ClusterSeriesDTO `json:"cpu_by_cluster"`
	MemByCluster []ClusterSeriesDTO `json:"mem_by_cluster"`
	Avg          CPUMemNowDTO       `json:"avg"`
	Stale        *StaleDTO          `json:"stale"`
}

type TargetDTO struct {
	ID          string `json:"id"`
	Port        int    `json:"port"`
	AZ          string `json:"az"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	Description string `json:"description"`
}

type TargetGroupHealthDTO struct {
	TargetGroupARN string      `json:"target_group_arn"`
	Name           string      `json:"name"`
	Healthy        int         `json:"healthy"`
	Unhealthy      int         `json:"unhealthy"`
	Targets        []TargetDTO `json:"targets"`
}

// TargetHealthDTO is the body of GET /api/services/{serviceId}/target-health.
type TargetHealthDTO struct {
	Groups    []TargetGroupHealthDTO `json:"groups"`
	Healthy   int                    `json:"healthy"`
	Unhealthy int                    `json:"unhealthy"`
	PolledAt  *time.Time             `json:"polled_at"`
}

const (
	defaultMetricsRange = "24h"
	overviewKeepTop     = 7
	// listMetricsPeriod gives 48 points over 24 h for the clusters page and the overview.
	listMetricsPeriod = 1800
	errorBucketPeriod = 3600
)

// ---- shared helpers ---------------------------------------------------------------------------

// rangeFrom reads ?range= (default 24h); anything but 1h, 6h, 24h or 72h is a 422.
func rangeFrom(r *http.Request) (string, metrics.Span, error) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = defaultMetricsRange
	}
	span, ok := metrics.ParseRange(name)
	if !ok {
		return "", 0, invalid("range must be one of 1h, 6h, 24h or 72h.")
	}
	return name, span, nil
}

// fetchMetrics runs one CloudWatch fetch and maps its failures: throttling is 503 "throttled",
// anything else 502 "aws_error". Queries are optional: no queries yields an empty grid-less result.
func (s *Server) fetchMetrics(ctx context.Context, t awsx.Target, span metrics.Span, queries []metrics.Query) (*metrics.Result, error) {
	if len(queries) == 0 {
		return &metrics.Result{Timestamps: []time.Time{}}, nil
	}
	res, err := s.Metrics.Fetch(ctx, t, span, queries)
	if err == nil {
		return res, nil
	}
	if awsx.IsThrottle(err) {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Code: "throttled", Message: "CloudWatch is throttling requests. Try again shortly."}
	}
	s.log().Warn("cloudwatch fetch failed", "account", t.AccountID, "region", t.Region, "error", err)
	return nil, &APIError{Status: http.StatusBadGateway, Code: "aws_error", Message: "CloudWatch returned an error."}
}

// staleOf reports the oldest stale result among results, or nil when all are fresh.
func staleOf(results ...*metrics.Result) *StaleDTO {
	var out *StaleDTO
	for _, res := range results {
		if res == nil || !res.Stale {
			continue
		}
		if out == nil {
			out = &StaleDTO{Reason: res.StaleReason, FetchedAt: res.FetchedAt}
		} else if res.FetchedAt.Before(out.FetchedAt) {
			out.FetchedAt = res.FetchedAt
		}
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// values converts a series to its JSON form: never null, nil buckets stay null, rounded to 2 places.
func values(s metrics.Series) []*float64 {
	out := make([]*float64, len(s.Values))
	for i, v := range s.Values {
		if v != nil {
			r := round2(*v)
			out[i] = &r
		}
	}
	return out
}

// lastOf is the rounded last non-nil value of a series, nil when it has none.
func lastOf(s metrics.Series) *float64 {
	v := metrics.Last(s)
	if v == nil {
		return nil
	}
	r := round2(*v)
	return &r
}

func scaleSeries(s metrics.Series, factor float64) metrics.Series {
	out := metrics.Series{ID: s.ID, Label: s.Label, Values: make([]*float64, len(s.Values))}
	for i, v := range s.Values {
		if v != nil {
			scaled := *v * factor
			out.Values[i] = &scaled
		}
	}
	return out
}

// seriesIndex finds a series of res by query ID; a missing one is an empty series.
type seriesIndex map[string]metrics.Series

func indexOf(res *metrics.Result) seriesIndex {
	idx := make(seriesIndex, len(res.Series))
	for _, s := range res.Series {
		idx[s.ID] = s
	}
	return idx
}

func (idx seriesIndex) get(id string) metrics.Series {
	if s, ok := idx[id]; ok {
		return s
	}
	return metrics.Series{ID: id}
}

// percent is 100*used/reserved for the "cpu" or "mem" query pair; the ids are prefix+kind+"_u"+suffix.
func (idx seriesIndex) percent(kind, prefix, suffix string) metrics.Series {
	return metrics.Percent(idx.get(prefix+kind+"_u"+suffix), idx.get(prefix+kind+"_r"+suffix))
}

// group collects the per-target-group series name_0..name_{n-1} that the fetch returned.
func (idx seriesIndex) group(prefix, name string, n int) []metrics.Series {
	var out []metrics.Series
	for i := range n {
		if s, ok := idx[prefix+name+"_"+strconv.Itoa(i)]; ok {
			out = append(out, s)
		}
	}
	return out
}

// withPrefix copies queries with their IDs (and labels) prefixed, keeping those keep accepts, so
// several services can share one fetch.
func withPrefix(prefix string, queries []metrics.Query, keep func(id string) bool) []metrics.Query {
	var out []metrics.Query
	for _, q := range queries {
		if keep != nil && !keep(q.ID) {
			continue
		}
		q.ID, q.Label = prefix+q.ID, prefix+q.Label
		out = append(out, q)
	}
	return out
}

func hasAnyPrefix(id string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func cpuMemOnly(id string) bool { return hasAnyPrefix(id, "cpu_", "mem_") }
func requestsAndLatency(id string) bool {
	return hasAnyPrefix(id, "req_", "p95_")
}
func errorsOnly(id string) bool { return strings.HasPrefix(id, "e5xx_") }

// albSummary reduces a service's target-group series to the numbers of the cluster page.
func albSummary(idx, errIdx seriesIndex, prefix string, groups, period int) *ALBNowDTO {
	req := metrics.PerMinute(metrics.SumSeries("req", idx.group(prefix, "req", groups)...), period)
	p95 := scaleSeries(metrics.MeanSeries("p95", idx.group(prefix, "p95", groups)...), 1000)
	errs := metrics.SumSeries("e5xx", errIdx.group(prefix, "e5xx", groups)...)
	return &ALBNowDTO{ReqNow: lastOf(req), P95Now: lastOf(p95), Err5xxToday: round2(metrics.SumAll(errs))}
}

// deploymentMarkers lists the service's deployments since start as chart markers, oldest first.
func (s *Server) deploymentMarkers(ctx context.Context, key model.ServiceKey, since time.Time, withService bool) ([]MarkerDTO, error) {
	records, err := s.Store.ListDeployments(ctx, key, since)
	if err != nil {
		return nil, err
	}
	markers := make([]MarkerDTO, 0, len(records))
	for _, rec := range records {
		marker := MarkerDTO{At: rec.StartedAt, Label: ":" + strconv.Itoa(rec.Revision), Kind: rec.Kind, Failed: rec.Outcome == "failed"}
		if rec.Kind == "restart" {
			marker.Label = "restart"
		}
		if withService {
			marker.Service = key.Service
		}
		markers = append(markers, marker)
	}
	sort.SliceStable(markers, func(i, j int) bool { return markers[i].At.Before(markers[j].At) })
	return markers, nil
}

// ---- service metrics --------------------------------------------------------------------------

// getServiceMetrics answers GET /api/services/{serviceId}/metrics?range=.
func (s *Server) getServiceMetrics(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	rangeName, span, err := rangeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	row, err := s.Store.GetServiceView(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	cluster, err := s.Store.GetCluster(ctx, key.ClusterKey, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	health, err := s.Store.TargetHealthForService(ctx, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	insights := metrics.InsightsMode(cluster.Cluster)
	period := metrics.DefaultPeriod(span)
	target := awsx.Target{AccountID: key.AccountID, Region: key.Region}
	refs := metrics.ServiceTargetGroups(row.Service, health)

	var queries []metrics.Query
	if insights != "disabled" {
		queries = append(queries, metrics.ServiceQueries(key.Cluster, key.Service, period)...)
	}
	queries = append(queries, metrics.ALBQueries(refs, period)...)
	var taskIDs []string // ECS task ids parallel to taskQuerySuffixes
	var taskSuffixes []string
	if insights == "enhanced" {
		tasks, err := s.serviceTasks(ctx, key, sc.Vis)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, task := range tasks {
			if task.LastStatus != "RUNNING" || task.Family == "" {
				continue
			}
			taskQueries := metrics.TaskQueries(key.Cluster, task.Family, task.ShortID(), period)
			queries = append(queries, taskQueries...)
			taskIDs = append(taskIDs, task.ShortID())
			taskSuffixes = append(taskSuffixes, strings.TrimPrefix(taskQueries[0].ID, "cpu_u"))
		}
	}
	res, err := s.fetchMetrics(ctx, target, span, queries)
	if err != nil {
		s.fail(w, err)
		return
	}
	idx := indexOf(res)

	out := ServiceMetricsDTO{
		Range: rangeName, Period: period, Insights: insights, Timestamps: res.Timestamps,
		Markers: []MarkerDTO{}, PolledAt: nil,
	}
	if out.Timestamps == nil {
		out.Timestamps = []time.Time{}
	}
	if !res.FetchedAt.IsZero() {
		fetchedAt := res.FetchedAt
		out.PolledAt = &fetchedAt
	}
	results := []*metrics.Result{res}

	if insights != "disabled" {
		cpu, mem := idx.percent("cpu", "", ""), idx.percent("mem", "", "")
		out.Series = &ServiceSeriesDTO{
			CPU: values(cpu), Mem: values(mem),
			NetRxKBps: values(metrics.ToKBps(idx.get("net_rx"))), NetTxKBps: values(metrics.ToKBps(idx.get("net_tx"))),
			Tasks: values(idx.get("tasks")),
		}
		td, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, row.TaskDefinitionARN)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return
		}
		cpuUnits, _ := strconv.Atoi(td.CPU)
		memMiB, _ := strconv.Atoi(td.Memory)
		out.Now = &ServiceNowDTO{CPU: lastOf(cpu), Mem: lastOf(mem), CPUUnits: cpuUnits, MemMiB: memMiB, Running: row.Running}
	}
	if insights == "enhanced" {
		out.TasksNow = make([]TaskNowDTO, 0, len(taskIDs))
		for i, id := range taskIDs {
			out.TasksNow = append(out.TasksNow, TaskNowDTO{
				TaskID: id, CPU: lastOf(idx.percent("cpu", "", taskSuffixes[i])), Mem: lastOf(idx.percent("mem", "", taskSuffixes[i])),
			})
		}
	}

	if len(refs) > 0 {
		errRes, err := s.fetchMetrics(ctx, target, metrics.Span24h, filterQueries(metrics.ALBQueries(refs, errorBucketPeriod), errorsOnly))
		if err != nil {
			s.fail(w, err)
			return
		}
		results = append(results, errRes)
		errs := metrics.SumSeries("e5xx", indexOf(errRes).group("", "e5xx", len(refs))...)
		req := metrics.PerMinute(metrics.SumSeries("req", idx.group("", "req", len(refs))...), period)
		p95 := scaleSeries(metrics.MeanSeries("p95", idx.group("", "p95", len(refs))...), 1000)
		out.ALB = &ServiceALBDTO{
			ReqPerMin: values(req), P95MS: values(p95),
			Err5xxPerHour: HourlySeriesDTO{Timestamps: nonNilTimes(errRes.Timestamps), Values: values(errs)},
			ReqNow:        lastOf(req), P95Now: lastOf(p95), Err5xxToday: round2(metrics.SumAll(errs)),
		}
	}

	since := res.Start
	if since.IsZero() {
		since = s.now().Add(-time.Duration(span))
	}
	if out.Markers, err = s.deploymentMarkers(ctx, key, since, false); err != nil {
		s.fail(w, err)
		return
	}
	out.Stale = staleOf(results...)
	writeJSON(w, http.StatusOK, out)
}

func filterQueries(queries []metrics.Query, keep func(id string) bool) []metrics.Query {
	return withPrefix("", queries, keep)
}

// ---- cluster metrics --------------------------------------------------------------------------

// getClusterMetrics answers GET /api/clusters/{clusterId}/metrics?range=; only visible services appear.
func (s *Server) getClusterMetrics(w http.ResponseWriter, r *http.Request) {
	key, err := model.ParseClusterID(r.PathValue("clusterId"))
	if err != nil {
		s.fail(w, errNotFound)
		return
	}
	rangeName, span, err := rangeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	cluster, err := s.Store.GetCluster(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	services, err := s.Store.ListServices(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region}, key.Cluster, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	insights := metrics.InsightsMode(cluster.Cluster)
	period := metrics.DefaultPeriod(span)
	target := awsx.Target{AccountID: key.AccountID, Region: key.Region}

	var queries, errQueries []metrics.Query
	groupCounts := make([]int, len(services))
	if insights != "disabled" {
		queries = append(queries, metrics.ClusterQueries(key.Cluster, period)...)
	}
	for i, svc := range services {
		prefix := fmt.Sprintf("s%d_", i)
		if insights != "disabled" {
			queries = append(queries, withPrefix(prefix, metrics.ServiceQueries(key.Cluster, svc.Key.Service, period), cpuMemOnly)...)
		}
		health, err := s.Store.TargetHealthForService(ctx, svc.Key)
		if err != nil {
			s.fail(w, err)
			return
		}
		refs := metrics.ServiceTargetGroups(svc.Service, health)
		groupCounts[i] = len(refs)
		queries = append(queries, withPrefix(prefix, metrics.ALBQueries(refs, period), requestsAndLatency)...)
		errQueries = append(errQueries, withPrefix(prefix, metrics.ALBQueries(refs, errorBucketPeriod), errorsOnly)...)
	}
	res, err := s.fetchMetrics(ctx, target, span, queries)
	if err != nil {
		s.fail(w, err)
		return
	}
	errRes, err := s.fetchMetrics(ctx, target, metrics.Span24h, errQueries)
	if err != nil {
		s.fail(w, err)
		return
	}
	idx, errIdx := indexOf(res), indexOf(errRes)

	out := ClusterMetricsDTO{
		Range: rangeName, Period: period, Insights: insights, Timestamps: res.Timestamps,
		Services: make(map[string]ClusterServiceMetricsDTO, len(services)), Stale: staleOf(res, errRes),
	}
	if out.Timestamps == nil {
		out.Timestamps = []time.Time{}
	}
	if insights != "disabled" {
		cpu, mem := idx.percent("cpu", "", ""), idx.percent("mem", "", "")
		out.Cluster = &CPUMemSeriesDTO{CPU: values(cpu), Mem: values(mem)}
		out.Now = &CPUMemNowDTO{CPU: lastOf(cpu), Mem: lastOf(mem)}
	}
	since := res.Start
	if since.IsZero() {
		since = s.now().Add(-time.Duration(span))
	}
	out.Markers = []MarkerDTO{}
	for i, svc := range services {
		prefix := fmt.Sprintf("s%d_", i)
		entry := ClusterServiceMetricsDTO{}
		if insights != "disabled" {
			cpu, mem := idx.percent("cpu", prefix, ""), idx.percent("mem", prefix, "")
			entry.CPU, entry.Mem, entry.CPUNow, entry.MemNow = values(cpu), values(mem), lastOf(cpu), lastOf(mem)
		}
		if groupCounts[i] > 0 {
			entry.ALB = albSummary(idx, errIdx, prefix, groupCounts[i], period)
		}
		out.Services[svc.Key.ID()] = entry
		markers, err := s.deploymentMarkers(ctx, svc.Key, since, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		out.Markers = append(out.Markers, markers...)
	}
	sort.SliceStable(out.Markers, func(i, j int) bool { return out.Markers[i].At.Before(out.Markers[j].At) })
	writeJSON(w, http.StatusOK, out)
}

// ---- clusters list and overview ---------------------------------------------------------------

// clusterUtilisation is one cluster's CPU and memory percentage series.
type clusterUtilisation struct {
	Insights string
	CPU, Mem metrics.Series
}

// fetchClusterUtilisation fetches 24 h CPU and memory at listMetricsPeriod for every Insights
// cluster in rows, one fetch per account and region. Clusters with Insights disabled are
// reported with no series and cost no query.
func (s *Server) fetchClusterUtilisation(ctx context.Context, rows []store.ClusterRow) (map[model.ClusterKey]clusterUtilisation, []time.Time, *StaleDTO, error) {
	byTarget := map[awsx.Target][]store.ClusterRow{}
	var targets []awsx.Target
	for _, row := range rows {
		target := awsx.Target{AccountID: row.Key.AccountID, Region: row.Key.Region}
		if _, seen := byTarget[target]; !seen {
			targets = append(targets, target)
		}
		byTarget[target] = append(byTarget[target], row)
	}
	out := make(map[model.ClusterKey]clusterUtilisation, len(rows))
	timestamps := []time.Time{}
	var results []*metrics.Result
	for _, target := range targets {
		var queries []metrics.Query
		for i, row := range byTarget[target] {
			mode := metrics.InsightsMode(row.Cluster)
			out[row.Key] = clusterUtilisation{Insights: mode}
			if mode != "disabled" {
				queries = append(queries, withPrefix(fmt.Sprintf("c%d_", i), metrics.ClusterQueries(row.Key.Cluster, listMetricsPeriod), nil)...)
			}
		}
		res, err := s.fetchMetrics(ctx, target, metrics.Span24h, queries)
		if err != nil {
			return nil, nil, nil, err
		}
		results = append(results, res)
		if len(res.Timestamps) > len(timestamps) {
			timestamps = res.Timestamps
		}
		idx := indexOf(res)
		for i, row := range byTarget[target] {
			if out[row.Key].Insights == "disabled" {
				continue
			}
			prefix := fmt.Sprintf("c%d_", i)
			util := clusterUtilisation{Insights: out[row.Key].Insights, CPU: idx.percent("cpu", prefix, ""), Mem: idx.percent("mem", prefix, "")}
			util.CPU.ID, util.CPU.Label = row.Key.ID(), row.Key.Cluster
			util.Mem.ID, util.Mem.Label = row.Key.ID(), row.Key.Cluster
			out[row.Key] = util
		}
	}
	return out, timestamps, staleOf(results...), nil
}

// getClustersMetrics answers GET /api/clusters/metrics?account=&region= for the clusters page.
func (s *Server) getClustersMetrics(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.Store.ListClusters(r.Context(), sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	util, _, stale, err := s.fetchClusterUtilisation(r.Context(), rows)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ClustersMetricsDTO{Items: make(map[string]ClusterListMetricsDTO, len(rows)), Stale: stale}
	for _, row := range rows {
		u := util[row.Key]
		item := ClusterListMetricsDTO{Insights: u.Insights, CPU: []*float64{}, Mem: []*float64{}}
		if u.Insights != "disabled" {
			item.CPU, item.Mem, item.CPUNow, item.MemNow = values(u.CPU), values(u.Mem), lastOf(u.CPU), lastOf(u.Mem)
		}
		out.Items[row.Key.ID()] = item
	}
	writeJSON(w, http.StatusOK, out)
}

// getOverviewMetrics answers GET /api/overview/metrics?account=&region=: the top seven clusters
// by latest utilisation plus the rest folded into "others".
func (s *Server) getOverviewMetrics(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.Store.ListClusters(r.Context(), sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	util, timestamps, stale, err := s.fetchClusterUtilisation(r.Context(), rows)
	if err != nil {
		s.fail(w, err)
		return
	}
	var cpu, mem []metrics.Series
	for _, row := range rows {
		u := util[row.Key]
		if u.Insights == "disabled" {
			continue
		}
		cpu, mem = append(cpu, u.CPU), append(mem, u.Mem)
	}
	if timestamps == nil {
		timestamps = []time.Time{}
	}
	out := OverviewMetricsDTO{
		Timestamps: timestamps, Stale: stale,
		CPUByCluster: topSeries(cpu), MemByCluster: topSeries(mem),
		Avg: CPUMemNowDTO{CPU: meanOfLasts(cpu), Mem: meanOfLasts(mem)},
	}
	writeJSON(w, http.StatusOK, out)
}

func topSeries(in []metrics.Series) []ClusterSeriesDTO {
	top := metrics.TopWithOthers(in, overviewKeepTop, "others", metrics.MeanSeries)
	out := make([]ClusterSeriesDTO, 0, len(top))
	for _, s := range top {
		out = append(out, ClusterSeriesDTO{ID: s.ID, Label: s.Label, Values: values(s)})
	}
	return out
}

// meanOfLasts averages the latest value of each series that has one.
func meanOfLasts(in []metrics.Series) *float64 {
	sum, n := 0.0, 0
	for _, s := range in {
		if v := metrics.Last(s); v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	mean := round2(sum / float64(n))
	return &mean
}

// ---- target health ----------------------------------------------------------------------------

// getTargetHealth answers GET /api/services/{serviceId}/target-health.
func (s *Server) getTargetHealth(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if _, err := s.Store.GetServiceView(ctx, key, sc.Vis); err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.Store.TargetHealthForService(ctx, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := TargetHealthDTO{Groups: make([]TargetGroupHealthDTO, 0, len(rows))}
	for _, row := range rows {
		group := TargetGroupHealthDTO{
			TargetGroupARN: row.TargetGroupARN, Name: targetGroupName(row.TargetGroupARN),
			Healthy: row.Healthy, Unhealthy: row.Unhealthy, Targets: make([]TargetDTO, 0, len(row.Targets)),
		}
		for _, t := range row.Targets {
			group.Targets = append(group.Targets, TargetDTO{ID: t.ID, Port: t.Port, AZ: t.AZ, State: t.State, Reason: t.Reason, Description: t.Description})
		}
		out.Groups = append(out.Groups, group)
		out.Healthy += row.Healthy
		out.Unhealthy += row.Unhealthy
		if out.PolledAt == nil || row.PolledAt.After(*out.PolledAt) {
			polledAt := row.PolledAt
			out.PolledAt = &polledAt
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// nonNilTimes keeps an empty timestamp list serialising as [] rather than null.
func nonNilTimes(in []time.Time) []time.Time {
	if in == nil {
		return []time.Time{}
	}
	return in
}
