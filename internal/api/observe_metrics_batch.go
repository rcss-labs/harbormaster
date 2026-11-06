package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const maxServicesMetricsIDs = 100

// getServicesMetrics answers GET /api/services/metrics?id=&id=: the latest CPU and memory of up to
// 100 services in one request, for the rows of one page of the Services list.
//
// Cost: one CloudWatch fetch per account x region (four queries per service), over the 1h span at
// its default period, which are the very series the service Metrics tab asks for at range=1h, so
// the two share cache rows.
//
// Scope: the caller's visibility is applied by intersecting the requested ids with ListServices.
// An id that is invisible, unknown or malformed is dropped before anything is queried, and it is
// never named in the response or in an error entry. Account, region and tag params are ignored:
// the ids are the scope.
//
// Failure: a target that cannot be read is listed in errors and its services are absent; the rest of
// the response stands. Only when nothing at all can be served (every target needed a fetch and all
// failed) is the whole request an error. A cluster with Container Insights off is reported as "disabled" with null figures and costs
// no query, whether or not its target is healthy.
func (s *Server) getServicesMetrics(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query()["id"]
	if len(requested) == 0 {
		s.fail(w, invalid("id is required."))
		return
	}
	unique := slices.Clone(requested)
	slices.Sort(unique)
	unique = slices.Compact(unique)
	if len(unique) > maxServicesMetricsIDs {
		s.fail(w, invalid("At most 100 ids per request."))
		return
	}
	wanted := make(map[model.ServiceKey]bool, len(unique))
	for _, id := range unique {
		if key, err := model.ParseServiceID(id); err == nil {
			wanted[key] = true
		}
	}

	ctx := r.Context()
	vis := visibilityOf(auth.SubjectFrom(ctx))
	visible, err := s.Store.ListServices(ctx, store.TargetFilter{}, "", vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	var keys []model.ServiceKey
	for _, row := range visible {
		if wanted[row.Key] {
			keys = append(keys, row.Key)
		}
	}
	out := ServicesMetricsDTO{Items: make(map[string]ServiceNowMetricsDTO, len(keys)), Errors: []TargetErrorDTO{}}
	if len(keys) == 0 {
		out.PolledAt = s.now()
		writeJSON(w, http.StatusOK, out)
		return
	}

	clusters, err := s.Store.ListClusters(ctx, store.TargetFilter{}, vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	insightsOf := make(map[model.ClusterKey]string, len(clusters))
	for _, c := range clusters {
		insightsOf[c.Key] = metrics.InsightsMode(c.Cluster)
	}

	var targets []awsx.Target
	byTarget := map[awsx.Target][]model.ServiceKey{}
	for _, key := range keys {
		mode, ok := insightsOf[key.ClusterKey]
		if !ok {
			continue
		}
		if mode == "disabled" {
			out.Items[key.ID()] = ServiceNowMetricsDTO{Insights: mode}
			continue
		}
		target := awsx.Target{AccountID: key.AccountID, Region: key.Region}
		if _, seen := byTarget[target]; !seen {
			targets = append(targets, target)
		}
		byTarget[target] = append(byTarget[target], key)
	}

	period := metrics.DefaultPeriod(metrics.Span1h)
	var results []*metrics.Result
	var firstErr error
	for _, target := range targets {
		group := byTarget[target]
		var queries []metrics.Query
		for i, key := range group {
			queries = append(queries, withPrefix(fmt.Sprintf("s%d_", i), metrics.ServiceQueries(key.Cluster, key.Service, period), cpuMemOnly)...)
		}
		res, err := s.fetchMetrics(ctx, target, metrics.Span1h, queries)
		if err != nil {
			if ctx.Err() != nil {
				s.fail(w, err)
				return
			}
			if firstErr == nil {
				firstErr = err
			}
			out.Errors = append(out.Errors, targetErrorOf(target, err))
			continue
		}
		results = append(results, res)
		idx := indexOf(res)
		for i, key := range group {
			prefix := fmt.Sprintf("s%d_", i)
			out.Items[key.ID()] = ServiceNowMetricsDTO{
				Insights: insightsOf[key.ClusterKey],
				CPU:      lastOf(idx.percent("cpu", prefix, "")),
				Mem:      lastOf(idx.percent("mem", prefix, "")),
			}
		}
	}
	if firstErr != nil && len(out.Items) == 0 {
		s.fail(w, firstErr)
		return
	}
	out.Stale = staleOf(results...)
	// polled_at is the age of the oldest series served, so it stays put while the cache row does.
	out.PolledAt = s.now()
	for _, res := range results {
		if !res.FetchedAt.IsZero() && res.FetchedAt.Before(out.PolledAt) {
			out.PolledAt = res.FetchedAt
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// targetErrorOf turns a failed fetch of one target into its entry in errors[].
func targetErrorOf(target awsx.Target, err error) TargetErrorDTO {
	entry := TargetErrorDTO{AccountID: target.AccountID, Region: target.Region, Code: "aws_error", Message: err.Error()}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		entry.Code, entry.Message = apiErr.Code, apiErr.Message
	}
	return entry
}
