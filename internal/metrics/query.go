// Package metrics reads CloudWatch metrics through GetMetricData: it batches
// queries, follows paging, aligns datapoints to a fixed time grid and keeps a
// short-lived shared cache in Postgres.
package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// Dim is one CloudWatch dimension.
type Dim struct{ Name, Value string }

// Query describes one metric series to fetch.
type Query struct {
	ID         string // result id, CloudWatch-legal: ^[a-z][a-zA-Z0-9_]*$; unique within a Fetch
	Namespace  string // "ECS/ContainerInsights" | "AWS/ApplicationELB" | "AWS/EFS" | ...
	MetricName string
	Dimensions []Dim  // order as given; CacheKey sorts a copy
	Stat       string // "Average" | "Sum" | "Maximum" | "SampleCount" | "p95" ...
	Period     int    // seconds, multiple of 60
	Label      string // free text carried to Series.Label
}

// Span is how far back from now a Fetch looks.
type Span time.Duration

const (
	Span1h  = Span(time.Hour)
	Span6h  = Span(6 * time.Hour)
	Span24h = Span(24 * time.Hour)
	Span72h = Span(72 * time.Hour)
)

// ParseRange maps the API range parameter to a Span.
func ParseRange(s string) (Span, bool) {
	switch s {
	case "1h":
		return Span1h, true
	case "6h":
		return Span6h, true
	case "24h":
		return Span24h, true
	case "72h":
		return Span72h, true
	}
	return 0, false
}

// DefaultPeriod returns the period in seconds that gives roughly 60-72 points
// for the standard spans; for other spans the smallest multiple of 60 that
// yields at most 100 points (minimum 60).
func DefaultPeriod(s Span) int {
	switch s {
	case Span1h:
		return 60
	case Span6h:
		return 300
	case Span24h:
		return 1200
	case Span72h:
		return 3600
	}
	seconds := int(time.Duration(s) / time.Second)
	const maxPoints = 100
	period := (seconds + maxPoints - 1) / maxPoints // ceil
	period = (period + 59) / 60 * 60                // round up to a minute
	return max(period, 60)
}

// Series is one query's values, one entry per grid bucket; nil means no datapoint.
type Series struct {
	ID, Label string
	Values    []*float64
}

// Result is the outcome of one Fetch.
type Result struct {
	Start, End  time.Time // End = floor(now, period); Start = End - span rounded up to whole periods
	Period      int
	Timestamps  []time.Time // bucket starts, ascending, len == len(Series[i].Values)
	Series      []Series    // in query order
	FetchedAt   time.Time   // oldest fetch time among the series served
	Stale       bool
	StaleReason string // set when served from an expired cache row because CloudWatch failed
}

// Point is one bucket of a series.
type Point struct {
	T time.Time
	V *float64
}

// Points zips the series with the given ID with Timestamps; nil when unknown.
func (r *Result) Points(id string) []Point {
	for _, s := range r.Series {
		if s.ID != id {
			continue
		}
		points := make([]Point, len(r.Timestamps))
		for i, ts := range r.Timestamps {
			points[i] = Point{T: ts}
			if i < len(s.Values) {
				points[i].V = s.Values[i]
			}
		}
		return points
	}
	return nil
}

// CacheKey is a stable sha256 hex over target, span, period and the metric
// identity of the query. The query ID, label and dimension order do not matter.
func CacheKey(t awsx.Target, span Span, period int, q Query) string {
	type dim struct{ N, V string }
	dims := make([]dim, len(q.Dimensions))
	for i, d := range q.Dimensions {
		dims[i] = dim{d.Name, d.Value}
	}
	slices.SortFunc(dims, func(a, b dim) int {
		if a.N != b.N {
			return compareStrings(a.N, b.N)
		}
		return compareStrings(a.V, b.V)
	})
	canonical := struct {
		Acct   string `json:"acct"`
		Region string `json:"region"`
		Span   int64  `json:"span"`
		Period int    `json:"period"`
		NS     string `json:"ns"`
		Metric string `json:"metric"`
		Dims   []dim  `json:"dims"`
		Stat   string `json:"stat"`
	}{t.AccountID, t.Region, int64(time.Duration(span) / time.Second), period, q.Namespace, q.MetricName, dims, q.Stat}
	raw, _ := json.Marshal(canonical) // plain strings and numbers: cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
