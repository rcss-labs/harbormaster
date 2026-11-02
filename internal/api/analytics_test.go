package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var analyticsNow = time.Date(2026, 10, 31, 12, 3, 41, 0, time.UTC)

func TestAnalyticsRangeFrom_DefaultIs30d(t *testing.T) {
	got, err := analyticsRangeFrom(httptest.NewRequest("GET", "/api/analytics/delivery", nil))
	if err != nil || got != Range30d {
		t.Fatalf("got %q, %v; want 30d", got, err)
	}
}

func TestAnalyticsRangeFrom_AcceptsEachRange(t *testing.T) {
	for _, name := range []string{"24h", "7d", "30d", "90d"} {
		got, err := analyticsRangeFrom(httptest.NewRequest("GET", "/api/analytics/delivery?range="+name, nil))
		if err != nil || string(got) != name {
			t.Errorf("range=%s: got %q, %v", name, got, err)
		}
	}
}

func TestAnalyticsRangeFrom_RejectsUnknown(t *testing.T) {
	_, err := analyticsRangeFrom(httptest.NewRequest("GET", "/api/analytics/delivery?range=1y", nil))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v, want an APIError", err)
	}
	if apiErr.Status != http.StatusUnprocessableEntity || apiErr.Code != "invalid" || apiErr.Message != "range must be one of 24h, 7d, 30d or 90d." {
		t.Fatalf("got %d %s %q", apiErr.Status, apiErr.Code, apiErr.Message)
	}
}

func TestClampWindow_ClampsToRetention(t *testing.T) {
	spec := clampWindow(Range30d, 168*time.Hour, analyticsNow)
	if spec.used != Range7d || !spec.clamped {
		t.Fatalf("used %q clamped %v; want 7d clamped", spec.used, spec.clamped)
	}
	if got := spec.to.Sub(spec.from); got != 7*24*time.Hour {
		t.Fatalf("window is %v, want 168h", got)
	}
}

func TestClampWindow_ZeroRetentionKeepsRequested(t *testing.T) {
	spec := clampWindow(Range90d, 0, analyticsNow)
	if spec.used != Range90d || spec.clamped {
		t.Fatalf("used %q clamped %v; want 90d unclamped", spec.used, spec.clamped)
	}
}

func TestClampWindow_RetentionLongerThanRangeDoesNotClamp(t *testing.T) {
	spec := clampWindow(Range7d, 720*time.Hour, analyticsNow)
	if spec.used != Range7d || spec.clamped {
		t.Fatalf("used %q clamped %v; want 7d unclamped", spec.used, spec.clamped)
	}
}

func TestClampWindow_ToIsNowTruncatedToFiveMinutes(t *testing.T) {
	spec := clampWindow(Range24h, 0, analyticsNow)
	want := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if !spec.to.Equal(want) {
		t.Fatalf("to = %v, want %v", spec.to, want)
	}
	if !spec.from.Equal(want.Add(-24 * time.Hour)) {
		t.Fatalf("from = %v", spec.from)
	}
}

func TestClampWindow_BucketSizes(t *testing.T) {
	cases := []struct {
		rng     AnalyticsRange
		bucket  time.Duration
		buckets int
		name    string
	}{
		{Range24h, time.Hour, 24, "1h"},
		{Range7d, 4 * time.Hour, 42, "4h"},
		{Range30d, 24 * time.Hour, 30, "1d"},
		{Range90d, 48 * time.Hour, 45, "2d"},
	}
	for _, c := range cases {
		spec := clampWindow(c.rng, 0, analyticsNow)
		if spec.bucket != c.bucket || spec.buckets != c.buckets || spec.bucketName() != c.name {
			t.Errorf("%s: bucket %v x%d %q; want %v x%d %q", c.rng, spec.bucket, spec.buckets, spec.bucketName(), c.bucket, c.buckets, c.name)
		}
	}
}

func TestBucketIndex(t *testing.T) {
	spec := clampWindow(Range7d, 0, analyticsNow)
	cases := []struct {
		name string
		ts   time.Time
		want int
	}{
		{"first instant", spec.from, 0},
		{"just before second bucket", spec.from.Add(4*time.Hour - time.Second), 0},
		{"second bucket", spec.from.Add(4 * time.Hour), 1},
		{"last instant inside window", spec.to.Add(-time.Second), 41},
		{"before window", spec.from.Add(-time.Second), -1},
		{"at to (exclusive)", spec.to, -1},
	}
	for _, c := range cases {
		if got := bucketIndex(c.ts, spec); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestZeroFilledSeries_CountsAreZeroNotNull(t *testing.T) {
	spec := clampWindow(Range24h, 0, analyticsNow)
	series := newBucketedSeries(spec)
	if len(series.Timestamps) != 24 || !series.Timestamps[0].Equal(spec.from) || !series.Timestamps[23].Equal(spec.to.Add(-time.Hour)) {
		t.Fatalf("timestamps: %d, first %v", len(series.Timestamps), series.Timestamps[0])
	}
	line := zeroLine(spec, "failed", "Failed", false)
	line.add(bucketIndex(spec.from.Add(90*time.Minute), spec), 2)
	line.add(bucketIndex(spec.from.Add(100*time.Minute), spec), 1)
	line.add(-1, 5) // outside the window: ignored
	if len(line.Values) != 24 {
		t.Fatalf("%d values", len(line.Values))
	}
	for i, v := range line.Values {
		if v == nil {
			t.Fatalf("value %d is null", i)
		}
		want := 0.0
		if i == 1 {
			want = 3
		}
		if *v != want {
			t.Errorf("value %d = %v, want %v", i, *v, want)
		}
	}
}

func TestNullLine_StartsWithNoSamples(t *testing.T) {
	line := nullLine(clampWindow(Range24h, 0, analyticsNow), "availability", "Availability")
	if len(line.Values) != 24 {
		t.Fatalf("%d values", len(line.Values))
	}
	for i, v := range line.Values {
		if v != nil {
			t.Fatalf("value %d = %v, want null", i, *v)
		}
	}
	line.set(3, 99.5)
	line.set(-1, 1) // outside the window: ignored
	if line.Values[3] == nil || *line.Values[3] != 99.5 {
		t.Fatalf("set did not store the sample")
	}
}

func TestWindowDTO_RendersSpec(t *testing.T) {
	spec := clampWindow(Range30d, 168*time.Hour, analyticsNow)
	dto := spec.dto(168*time.Hour, true)
	if dto.Requested != "30d" || dto.Used != "7d" || !dto.Clamped || !dto.RangeApplies || dto.Bucket != "4h" || dto.Retention != "168h" {
		t.Fatalf("%+v", dto)
	}
	if !dto.From.Equal(spec.from) || !dto.To.Equal(spec.to) {
		t.Fatalf("from/to %v %v", dto.From, dto.To)
	}
	if got := clampWindow(Range7d, 0, analyticsNow).dto(0, false); got.Retention != "" || got.RangeApplies {
		t.Fatalf("%+v", got)
	}
}

func TestFormatRetention(t *testing.T) {
	for in, want := range map[time.Duration]string{168 * time.Hour: "168h", 8760 * time.Hour: "8760h", 90 * time.Minute: "1h30m0s", 0: ""} {
		if got := formatRetention(in); got != want {
			t.Errorf("%v: got %q, want %q", in, got, want)
		}
	}
}
