package api

import (
	"fmt"
	"net/http"
	"time"
)

// Window, range and bucket helpers shared by the four analytics tabs. They are pure (no DB) so the
// clamping rules can be tested directly; see docs/analytics_design/PLAN.md sections 1.1 and 2.

// AnalyticsRange is the ?range= a tab was asked for.
type AnalyticsRange string

const (
	Range24h AnalyticsRange = "24h"
	Range7d  AnalyticsRange = "7d"
	Range30d AnalyticsRange = "30d"
	Range90d AnalyticsRange = "90d"

	defaultAnalyticsRange = Range30d

	// analyticsPollStep is the counts-history cadence; truncating "now" to it keeps the ETag stable within a poll.
	analyticsPollStep = 5 * time.Minute
)

// rangeSpecs lists the ranges from shortest to longest; clampWindow walks it backwards to find the largest fit.
var rangeSpecs = []struct {
	name     AnalyticsRange
	span     time.Duration
	bucket   time.Duration
	buckets  int
	bucketID string
}{
	{Range24h, 24 * time.Hour, time.Hour, 24, "1h"},
	{Range7d, 7 * 24 * time.Hour, 4 * time.Hour, 42, "4h"},
	{Range30d, 30 * 24 * time.Hour, 24 * time.Hour, 30, "1d"},
	{Range90d, 90 * 24 * time.Hour, 48 * time.Hour, 45, "2d"},
}

// analyticsRangeFrom reads ?range= (default 30d); anything but 24h, 7d, 30d or 90d is a 422.
func analyticsRangeFrom(r *http.Request) (AnalyticsRange, error) {
	name := AnalyticsRange(r.URL.Query().Get("range"))
	if name == "" {
		return defaultAnalyticsRange, nil
	}
	for _, spec := range rangeSpecs {
		if spec.name == name {
			return name, nil
		}
	}
	return "", invalid("range must be one of 24h, 7d, 30d or 90d.")
}

// windowSpec is the time window a tab queries, split into equal buckets. The last bucket may be partial in
// data terms (it ends at "now") but not in width.
type windowSpec struct {
	from, to  time.Time // [from, to)
	bucket    time.Duration
	buckets   int
	bucketID  string
	requested AnalyticsRange
	used      AnalyticsRange
	clamped   bool
}

// clampWindow limits the requested range to what the governing table still holds. retention 0 means the
// table is kept forever, so nothing is clamped. The used range is the largest standard range that fits in
// the retention (at least 24h), so the bucket size always stays one of the four standard ones.
func clampWindow(requested AnalyticsRange, retention time.Duration, now time.Time) windowSpec {
	chosen := 0
	for i, spec := range rangeSpecs {
		if spec.name == requested {
			chosen = i
		}
	}
	used := chosen
	if retention > 0 {
		for used > 0 && rangeSpecs[used].span > retention {
			used--
		}
	}
	spec := rangeSpecs[used]
	to := now.UTC().Truncate(analyticsPollStep)
	return windowSpec{
		from:      to.Add(-spec.span),
		to:        to,
		bucket:    spec.bucket,
		buckets:   spec.buckets,
		bucketID:  spec.bucketID,
		requested: requested,
		used:      spec.name,
		clamped:   used != chosen,
	}
}

// bucketName is the bucket width as the API names it: "1h", "4h", "1d" or "2d".
func (w windowSpec) bucketName() string { return w.bucketID }

// dto renders the window block of a response. retention is the governing table's retention.
func (w windowSpec) dto(retention time.Duration, rangeApplies bool) AnalyticsWindowDTO {
	return AnalyticsWindowDTO{
		Requested:    string(w.requested),
		Used:         string(w.used),
		Clamped:      w.clamped,
		RangeApplies: rangeApplies,
		Bucket:       w.bucketID,
		From:         w.from,
		To:           w.to,
		Retention:    formatRetention(retention),
	}
}

// formatRetention renders a retention like the config does: whole hours as "168h", zero (kept forever) as "".
func formatRetention(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return d.String()
}

// bucketIndex is the bucket ts falls in, or -1 when it is outside [from, to).
func bucketIndex(ts time.Time, w windowSpec) int {
	if ts.Before(w.from) || !ts.Before(w.to) {
		return -1
	}
	index := int(ts.Sub(w.from) / w.bucket)
	if index >= w.buckets {
		return w.buckets - 1
	}
	return index
}

// bucketTimestamps is the start of every bucket.
func bucketTimestamps(w windowSpec) []time.Time {
	stamps := make([]time.Time, w.buckets)
	for i := range stamps {
		stamps[i] = w.from.Add(time.Duration(i) * w.bucket)
	}
	return stamps
}

// newBucketedSeries is an empty series whose timestamps cover the window.
func newBucketedSeries(w windowSpec) SeriesDTO {
	return SeriesDTO{Timestamps: bucketTimestamps(w), Series: []SeriesLineDTO{}}
}

// zeroLine is a count line: every bucket starts at 0 so quiet buckets are 0, not null.
func zeroLine(w windowSpec, key, label string, expected bool) SeriesLineDTO {
	values := make([]*float64, w.buckets)
	for i := range values {
		zero := 0.0
		values[i] = &zero
	}
	return SeriesLineDTO{Key: key, Label: label, Expected: expected, Values: values}
}

// nullLine is a rate line: buckets stay null until a sample is set.
func nullLine(w windowSpec, key, label string) SeriesLineDTO {
	return SeriesLineDTO{Key: key, Label: label, Values: make([]*float64, w.buckets)}
}

// add increases bucket index of a zero-filled line by v; an index outside the window (-1) is ignored.
func (l *SeriesLineDTO) add(index int, v float64) {
	if index < 0 || index >= len(l.Values) {
		return
	}
	if l.Values[index] == nil {
		zero := 0.0
		l.Values[index] = &zero
	}
	*l.Values[index] += v
}

// set stores a rate sample in bucket index; an index outside the window is ignored.
func (l *SeriesLineDTO) set(index int, v float64) {
	if index < 0 || index >= len(l.Values) {
		return
	}
	l.Values[index] = &v
}
