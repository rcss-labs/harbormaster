package storage

import "github.com/rcss-labs/harbormaster/internal/metrics"

// Growth is the percentage change from the first to the last non-nil point.
// ok is false with fewer than two non-nil points or a zero first value.
func Growth(points []metrics.Point) (pct float64, ok bool) {
	var first, last float64
	n := 0
	for _, p := range points {
		if p.V == nil {
			continue
		}
		if n == 0 {
			first = *p.V
		}
		last = *p.V
		n++
	}
	if n < 2 || first == 0 {
		return 0, false
	}
	return (last - first) / first * 100, true
}
