package metrics

import (
	"strconv"
	"time"
)

const nsEFS = "AWS/EFS"

// Each EFS builder uses one period, so each set is a valid single Fetch
// (Fetch rejects mixed periods with ErrMixedPeriods).
const (
	EFSSizeSpan       = Span(30 * 24 * time.Hour) // period 86400
	EFSThroughputSpan = Span24h                   // period 1200
	EFSConnectionSpan = Span(10 * time.Minute)    // period 60
)

// efsQueries builds one query per file system. IDs are index-based because
// file system IDs contain "-", which GetMetricData query IDs reject.
func efsQueries(prefix, metric, stat string, period int, withStorageClass bool, fsIDs []string) []Query {
	qs := make([]Query, 0, len(fsIDs))
	for i, fsID := range fsIDs {
		dims := []Dim{{"FileSystemId", fsID}}
		if withStorageClass {
			dims = append(dims, Dim{"StorageClass", "Total"})
		}
		id := prefix + "_" + strconv.Itoa(i)
		qs = append(qs, Query{ID: id, Namespace: nsEFS, MetricName: metric, Dimensions: dims, Stat: stat, Period: period, Label: id})
	}
	return qs
}

// EFSStorageQueries asks for total stored bytes, daily.
func EFSStorageQueries(fsIDs []string) []Query {
	return efsQueries("sz", "StorageBytes", "Average", 86400, true, fsIDs)
}

// EFSThroughputQueries asks for metered I/O bytes per 20 minutes.
func EFSThroughputQueries(fsIDs []string) []Query {
	return efsQueries("io", "MeteredIOBytes", "Sum", 1200, false, fsIDs)
}

// EFSConnectionQueries asks for client connections per minute.
func EFSConnectionQueries(fsIDs []string) []Query {
	return efsQueries("cc", "ClientConnections", "Sum", 60, false, fsIDs)
}

// BytesPerPeriodToMiBps converts a per-period byte sum to MiB per second.
func BytesPerPeriodToMiBps(sum float64, periodSeconds int) float64 {
	if periodSeconds <= 0 {
		return 0
	}
	return sum / float64(periodSeconds) / (1 << 20)
}
