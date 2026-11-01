package storage

import (
	"math"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/metrics"
)

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{21474836: "20 MiB", 41231686041: "38.4 GiB", 227204155801: "211.6 GiB", 0: "0 MiB"}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func pts(vals ...*float64) []metrics.Point {
	out := []metrics.Point{}
	for _, v := range vals {
		out = append(out, metrics.Point{V: v})
	}
	return out
}

func f(v float64) *float64 { return &v }

func TestGrowth(t *testing.T) {
	if pct, ok := Growth(pts(f(78), nil, f(100))); !ok || math.Abs(pct-28.2) > 0.1 {
		t.Errorf("78->100 = %v,%v want ~28.2,true", pct, ok)
	}
	if _, ok := Growth(pts(f(0), f(5))); ok {
		t.Error("first 0 must be not ok")
	}
	if _, ok := Growth(pts(f(5))); ok {
		t.Error("one point must be not ok")
	}
	if _, ok := Growth(pts(f(5), nil)); ok {
		t.Error("one non-nil point must be not ok")
	}
}
