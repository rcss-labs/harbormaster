package metrics

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func TestParseRange_Table(t *testing.T) {
	for in, want := range map[string]Span{"1h": Span1h, "6h": Span6h, "24h": Span24h, "72h": Span72h} {
		got, ok := ParseRange(in)
		if !ok || got != want {
			t.Errorf("ParseRange(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"7d", "", "2h", "1H"} {
		if _, ok := ParseRange(in); ok {
			t.Errorf("ParseRange(%q) ok, want false", in)
		}
	}
}

func TestDefaultPeriod_Table(t *testing.T) {
	cases := []struct {
		span Span
		want int
	}{
		{Span1h, 60}, {Span6h, 300}, {Span24h, 1200}, {Span72h, 3600},
		{Span(30 * 24 * time.Hour), 25920}, {Span(10 * time.Minute), 60},
	}
	for _, c := range cases {
		if got := DefaultPeriod(c.span); got != c.want {
			t.Errorf("DefaultPeriod(%v) = %d, want %d", time.Duration(c.span), got, c.want)
		}
	}
}

func TestCacheKey_IgnoresDimensionOrderAndID(t *testing.T) {
	target := awsx.Target{AccountID: "111", Region: "eu-west-1"}
	a := Query{ID: "a", Namespace: "AWS/ECS", MetricName: "CPU", Stat: "Average", Period: 60,
		Dimensions: []Dim{{"ClusterName", "c"}, {"ServiceName", "s"}}, Label: "x"}
	b := a
	b.ID, b.Label = "other", "y"
	b.Dimensions = []Dim{{"ServiceName", "s"}, {"ClusterName", "c"}}
	if CacheKey(target, Span1h, 60, a) != CacheKey(target, Span1h, 60, b) {
		t.Error("key differs for reordered dimensions / different ID")
	}
	c := a
	c.Stat = "Maximum"
	if CacheKey(target, Span1h, 60, a) == CacheKey(target, Span1h, 60, c) {
		t.Error("key equal for different stat")
	}
	if CacheKey(target, Span1h, 60, a) == CacheKey(target, Span6h, 60, a) {
		t.Error("key equal for different span")
	}
	if CacheKey(target, Span1h, 60, a) == CacheKey(awsx.Target{AccountID: "222", Region: "eu-west-1"}, Span1h, 60, a) {
		t.Error("key equal for different account")
	}
}

func TestResult_Points(t *testing.T) {
	v := 2.0
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r := &Result{Timestamps: []time.Time{t0, t0.Add(time.Minute)},
		Series: []Series{{ID: "cpu", Values: []*float64{nil, &v}}}}
	pts := r.Points("cpu")
	if len(pts) != 2 || !pts[0].T.Equal(t0) || pts[0].V != nil || pts[1].V == nil || *pts[1].V != 2 {
		t.Errorf("Points = %+v", pts)
	}
	if r.Points("nope") != nil {
		t.Error("unknown id should give nil")
	}
}
