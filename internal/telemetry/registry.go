// Package telemetry is a small goroutine-safe metrics registry with a
// hand-written Prometheus text exposition writer (no client library).
package telemetry

import (
	"strings"
	"sync"
	"time"
)

// loopBuckets are the upper bounds (seconds) of the poll loop histogram.
var loopBuckets = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

const keySep = "|"

type histogram struct {
	buckets []uint64 // non-cumulative per-bucket counts; last slot is +Inf overflow
	sum     float64
	count   uint64
}

// Registry holds all Harbormaster process metrics.
type Registry struct {
	mu           sync.Mutex
	awsCalls     map[string]uint64
	loopDuration map[string]*histogram
	loopRuns     map[string]uint64
	loopInterval map[string]time.Duration
	snapshotRows map[string]uint64
	leader       bool
	buildVersion string
	buildInfoSet bool
}

// Default is the process-wide registry.
var Default = New()

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		awsCalls:     map[string]uint64{},
		loopDuration: map[string]*histogram{},
		loopRuns:     map[string]uint64{},
		loopInterval: map[string]time.Duration{},
		snapshotRows: map[string]uint64{},
	}
}

func joinKey(parts ...string) string { return strings.Join(parts, keySep) }

// CountAWSCall records one AWS API call; result is "ok", "throttled" or "error".
func (r *Registry) CountAWSCall(account, region, service, operation, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.awsCalls[joinKey(account, region, service, operation, result)]++
}

// ObserveLoop records one poll loop run's duration and outcome.
func (r *Registry) ObserveLoop(account, region, tier string, d time.Duration, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := joinKey(account, region, tier)
	h := r.loopDuration[key]
	if h == nil {
		h = &histogram{buckets: make([]uint64, len(loopBuckets)+1)}
		r.loopDuration[key] = h
	}
	secs := d.Seconds()
	idx := len(loopBuckets) // overflow (+Inf only)
	for i, upper := range loopBuckets {
		if secs <= upper {
			idx = i
			break
		}
	}
	h.buckets[idx]++
	h.sum += secs
	h.count++

	result := "ok"
	if !ok {
		result = "error"
	}
	r.loopRuns[joinKey(account, region, tier, result)]++
}

// SetLoopInterval records the configured interval of a poll loop.
func (r *Registry) SetLoopInterval(account, region, tier string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loopInterval[joinKey(account, region, tier)] = d
}

// CountSnapshotWrites adds n to the rows-written counter of a table.
func (r *Registry) CountSnapshotWrites(table string, n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotRows[table] += uint64(n)
}

// SetLeader records whether this process currently holds poller leadership.
func (r *Registry) SetLeader(leader bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leader = leader
}

// SetBuildInfo records the build version exposed via harbormaster_build_info.
func (r *Registry) SetBuildInfo(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buildVersion = version
	r.buildInfoSet = true
}

// Snapshot is a copy of selected registry state for tests and the soak test.
type Snapshot struct {
	AWSCalls     map[string]uint64        // key "account|region|service|operation|result"
	LoopInterval map[string]time.Duration // key "account|region|tier"
	Leader       bool
}

// Snapshot returns a deep copy of the current state.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		AWSCalls:     make(map[string]uint64, len(r.awsCalls)),
		LoopInterval: make(map[string]time.Duration, len(r.loopInterval)),
		Leader:       r.leader,
	}
	for k, v := range r.awsCalls {
		s.AWSCalls[k] = v
	}
	for k, v := range r.loopInterval {
		s.LoopInterval[k] = v
	}
	return s
}
