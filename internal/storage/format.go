package storage

import (
	"fmt"

	"github.com/rcss-labs/harbormaster/internal/metrics"
)

const (
	mib = 1 << 20
	gib = 1 << 30
)

// FormatSize renders bytes as "20 MiB" below 1 GiB, else "38.4 GiB".
func FormatSize(bytes int64) string {
	if bytes < gib {
		return fmt.Sprintf("%.0f MiB", float64(bytes)/mib)
	}
	return fmt.Sprintf("%.1f GiB", float64(bytes)/gib)
}

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
