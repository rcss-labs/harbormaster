package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// ErrMixedPeriods is returned when the queries of one Fetch disagree on Period.
var ErrMixedPeriods = errors.New("metrics: all queries in one fetch must use the same period")

const (
	maxQueriesPerCall = 500 // GetMetricData limit
	throttleReason    = "CloudWatch is throttling requests"
)

var queryIDPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)

// Client reads metrics from CloudWatch with a shared Postgres cache.
type Client struct {
	Clients  awsx.Clients
	Store    *store.Store // nil disables the cache
	TTL      time.Duration
	MaxStale time.Duration
	Now      func() time.Time
	Log      *slog.Logger
}

// New builds a Client; MaxStale defaults to 10 minutes.
func New(c awsx.Clients, s *store.Store, ttl time.Duration) *Client {
	return &Client{Clients: c, Store: s, TTL: ttl, MaxStale: 10 * time.Minute, Now: time.Now, Log: slog.Default()}
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// staleReasonFor is the short text shown in the UI: a fixed sentence for
// throttling, otherwise the API error message without SDK request plumbing.
func staleReasonFor(cause error) string {
	if awsx.IsThrottle(cause) {
		return throttleReason
	}
	var apiErr smithy.APIError
	if errors.As(cause, &apiErr) && apiErr.ErrorMessage() != "" {
		return apiErr.ErrorMessage()
	}
	return cause.Error()
}

// cachedPayload is the JSON stored in metrics_cache.payload: the series
// together with the grid it was aligned to, so it can be re-aligned to a later
// grid. Bucket i starts at Start + i*Period.
type cachedPayload struct {
	Start  time.Time  `json:"start"`
	Period int        `json:"period"`
	Values []*float64 `json:"values"`
}

// Fetch returns one series per query on a common grid ending at floor(now, period).
func (c *Client) Fetch(ctx context.Context, t awsx.Target, span Span, queries []Query) (*Result, error) {
	period, err := validate(queries)
	if err != nil {
		return nil, err
	}
	now := c.now()
	periodDur := time.Duration(period) * time.Second
	buckets := int((time.Duration(span) + periodDur - 1) / periodDur)
	end := now.Truncate(periodDur)
	start := end.Add(-time.Duration(buckets) * periodDur)
	res := &Result{Start: start, End: end, Period: period, FetchedAt: now,
		Timestamps: make([]time.Time, buckets), Series: make([]Series, len(queries))}
	for i := range res.Timestamps {
		res.Timestamps[i] = start.Add(time.Duration(i) * periodDur)
	}
	for i, q := range queries {
		res.Series[i] = Series{ID: q.ID, Label: q.Label}
	}

	keys := make([]string, len(queries))
	for i, q := range queries {
		keys[i] = CacheKey(t, span, period, q)
	}
	cached := c.readCache(ctx, keys)

	var missing []int // indexes of queries without a fresh cache row
	oldest := now
	for i := range queries {
		row, ok := cached[keys[i]]
		if ok && now.Sub(row.FetchedAt) < c.TTL {
			if values, ok := alignCached(row, start, period, buckets); ok {
				res.Series[i].Values = values
				oldest = minTime(oldest, row.FetchedAt)
				continue
			}
		}
		missing = append(missing, i)
	}

	if len(missing) > 0 {
		fetched, err := c.fetchFromCloudWatch(ctx, t, queries, missing, start, end, period, buckets)
		if err != nil {
			return c.serveStale(res, err, keys, cached, missing, now, start, period, buckets, oldest)
		}
		rows := make([]store.CachedSeries, 0, len(missing))
		for _, i := range missing {
			values := fetched[queries[i].ID]
			res.Series[i].Values = values
			payload, _ := json.Marshal(cachedPayload{Start: start, Period: period, Values: values})
			rows = append(rows, store.CachedSeries{Key: keys[i], Target: t, FetchedAt: now, Payload: payload})
		}
		c.writeCache(ctx, rows)
	}
	res.FetchedAt = oldest
	return res, nil
}

// serveStale fills the missing series from expired cache rows when every one
// of them has a row younger than MaxStale; otherwise it returns cause unchanged.
func (c *Client) serveStale(res *Result, cause error, keys []string, cached map[string]store.CachedSeries, missing []int,
	now, start time.Time, period, buckets int, oldest time.Time) (*Result, error) {
	staleValues := make(map[int][]*float64, len(missing))
	for _, i := range missing {
		row, ok := cached[keys[i]]
		if !ok || now.Sub(row.FetchedAt) >= c.MaxStale {
			return nil, cause
		}
		values, ok := alignCached(row, start, period, buckets)
		if !ok {
			return nil, cause
		}
		staleValues[i] = values
		oldest = minTime(oldest, row.FetchedAt)
	}
	for i, values := range staleValues {
		res.Series[i].Values = values
	}
	res.Stale = true
	res.StaleReason = staleReasonFor(cause)
	res.FetchedAt = oldest
	c.log().Warn("serving stale metrics", "reason", res.StaleReason, "series", len(missing))
	return res, nil
}

// fetchFromCloudWatch runs the queries at the given indexes in chunks of 500,
// follows NextToken, and returns grid-aligned values by query ID.
func (c *Client) fetchFromCloudWatch(ctx context.Context, t awsx.Target, queries []Query, indexes []int,
	start, end time.Time, period, buckets int) (map[string][]*float64, error) {
	api, err := c.Clients.CloudWatch(ctx, t)
	if err != nil {
		return nil, err
	}
	periodDur := time.Duration(period) * time.Second
	out := make(map[string][]*float64, len(indexes))
	for _, i := range indexes {
		out[queries[i].ID] = make([]*float64, buckets)
	}
	for chunk := range slices.Chunk(indexes, maxQueriesPerCall) {
		input := &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(start), EndTime: aws.Time(end),
			ScanBy:            cwtypes.ScanByTimestampAscending,
			MetricDataQueries: make([]cwtypes.MetricDataQuery, len(chunk)),
		}
		for n, i := range chunk {
			input.MetricDataQueries[n] = toMetricDataQuery(queries[i])
		}
		for {
			page, err := api.GetMetricData(ctx, input)
			if err != nil {
				return nil, err
			}
			for _, r := range page.MetricDataResults {
				values, ok := out[aws.ToString(r.Id)]
				if !ok {
					continue
				}
				for n, ts := range r.Timestamps {
					if n >= len(r.Values) || ts.Before(start) || !ts.Before(end) {
						continue
					}
					v := r.Values[n]
					values[int(ts.Sub(start)/periodDur)] = &v // later datapoints win
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

func toMetricDataQuery(q Query) cwtypes.MetricDataQuery {
	dims := make([]cwtypes.Dimension, len(q.Dimensions))
	for i, d := range q.Dimensions {
		dims[i] = cwtypes.Dimension{Name: aws.String(d.Name), Value: aws.String(d.Value)}
	}
	return cwtypes.MetricDataQuery{
		Id:    aws.String(q.ID),
		Label: aws.String(q.Label),
		MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String(q.Namespace), MetricName: aws.String(q.MetricName), Dimensions: dims},
			Period: aws.Int32(int32(q.Period)),
			Stat:   aws.String(q.Stat), // MetricStat.Stat also takes percentiles such as "p95"
		},
		ReturnData: aws.Bool(true),
	}
}

// validate checks IDs and periods and returns the common period.
func validate(queries []Query) (int, error) {
	if len(queries) == 0 {
		return 60, nil
	}
	period := queries[0].Period
	if period <= 0 || period%60 != 0 {
		return 0, fmt.Errorf("metrics: period %d is not a positive multiple of 60", period)
	}
	seen := make(map[string]bool, len(queries))
	for _, q := range queries {
		if q.Period != period {
			return 0, ErrMixedPeriods
		}
		if !queryIDPattern.MatchString(q.ID) {
			return 0, fmt.Errorf("metrics: query id %q is not CloudWatch-legal", q.ID)
		}
		if seen[q.ID] {
			return 0, fmt.Errorf("metrics: duplicate query id %q", q.ID)
		}
		seen[q.ID] = true
	}
	return period, nil
}

// alignCached re-places a cached series on the current grid. Buckets newer
// than the cached data come out nil; buckets that fell off the start are dropped.
func alignCached(row store.CachedSeries, start time.Time, period, buckets int) ([]*float64, bool) {
	var p cachedPayload
	if err := json.Unmarshal(row.Payload, &p); err != nil || p.Period != period {
		return nil, false
	}
	periodDur := time.Duration(period) * time.Second
	values := make([]*float64, buckets)
	for i, v := range p.Values {
		at := p.Start.Add(time.Duration(i) * periodDur)
		if at.Before(start) {
			continue
		}
		if idx := int(at.Sub(start) / periodDur); idx < buckets {
			values[idx] = v
		}
	}
	return values, true
}

func (c *Client) readCache(ctx context.Context, keys []string) map[string]store.CachedSeries {
	if c.Store == nil || len(keys) == 0 {
		return nil
	}
	rows, err := c.Store.GetMetricsCache(ctx, keys)
	if err != nil {
		c.log().Warn("metrics cache read failed", "err", err)
		return nil
	}
	return rows
}

func (c *Client) writeCache(ctx context.Context, rows []store.CachedSeries) {
	if c.Store == nil || len(rows) == 0 {
		return
	}
	if err := c.Store.PutMetricsCache(ctx, rows); err != nil {
		c.log().Warn("metrics cache write failed", "err", err)
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
