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

// kvBuckets are the upper bounds (seconds) of the Valkey/Redis command histogram: 1, 5, 25, 100 and 250 ms.
var kvBuckets = []float64{0.001, 0.005, 0.025, 0.1, 0.25}

const keySep = "|"

type histogram struct {
	buckets []uint64 // non-cumulative per-bucket counts; last slot is +Inf overflow
	sum     float64
	count   uint64
}

// Registry holds all Harbormaster process metrics.
type Registry struct {
	mu                sync.Mutex
	awsCalls          map[string]uint64
	loopDuration      map[string]*histogram
	loopRuns          map[string]uint64
	loopInterval      map[string]time.Duration
	snapshotRows      map[string]uint64
	registryRequests  map[string]uint64
	notifyCounts      map[string]uint64
	limitDecisions    map[string]uint64
	leader            bool
	leaseSlices       int
	leaseWeight       int
	leaseEvents       map[string]uint64
	leaseRenewFails   map[string]uint64
	fencedRejected    uint64
	coordinatorOn     bool
	pollMembers       map[string]int
	idle              bool
	buildVersion      string
	buildInfoSet      bool
	roleName          string
	roleVersion       string
	roleInfoSet       bool
	kvUp              bool
	kvUpSet           bool
	kvErrors          map[string]uint64
	kvDuration        map[string]*histogram
	focusM            focusMetrics
	snapCache         map[string]uint64 // by joinKey(model, result)
	snapTokenFailures uint64
}

// Default is the process-wide registry.
var Default = New()

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		awsCalls:         map[string]uint64{},
		loopDuration:     map[string]*histogram{},
		loopRuns:         map[string]uint64{},
		loopInterval:     map[string]time.Duration{},
		snapshotRows:     map[string]uint64{},
		registryRequests: map[string]uint64{},
		notifyCounts:     map[string]uint64{},
		leaseEvents:      map[string]uint64{},
		leaseRenewFails:  map[string]uint64{},
		pollMembers:      map[string]int{},
		limitDecisions:   map[string]uint64{},
		kvErrors:         map[string]uint64{},
		kvDuration:       map[string]*histogram{},
	}
}

func joinKey(parts ...string) string { return strings.Join(parts, keySep) }

// CountAWSCall records one AWS API call; result is "ok", "throttled" or "error".
func (r *Registry) CountAWSCall(account, region, service, operation, result, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.awsCalls[joinKey(account, region, service, operation, result, source)]++
}

// CountRegistryRequest records one read of a public image registry; outcome is ok, not_found, denied, rate_limited,
// blocked or error. The host is one of the few allow-listed registry hosts, never a value from a task definition.
func (r *Registry) CountRegistryRequest(host, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registryRequests[joinKey(host, outcome)]++
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

// CountNotification records one notification attempt outcome; result is "delivered", "failed" or "retried".
func (r *Registry) CountNotification(destination, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifyCounts[joinKey(destination, result)]++
}

// CountLimitDecision records one shared-limit decision; result is "allowed" or "refused" and backend is "kv" or
// "memory" (the per-task fallback).
func (r *Registry) CountLimitDecision(limit, result, backend string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limitDecisions[joinKey(limit, result, backend)]++
}

// SetLeader records whether this process currently holds poller leadership.
func (r *Registry) SetLeader(leader bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leader = leader
}

// SetPollerIdle records whether the poller is in idle mode (no recent UI activity, slowed polling).
func (r *Registry) SetPollerIdle(idle bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idle = idle
}

// SetLeaseOwned records the slices this process owns and their total weight (clusters).
func (r *Registry) SetLeaseOwned(slices, weight int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leaseSlices, r.leaseWeight = slices, weight
}

// CountLeaseEvent records a lease event: kind is "slice" or "coordinator"; event is acquired, released, lost or takeover.
func (r *Registry) CountLeaseEvent(kind, event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leaseEvents[joinKey(kind, event)]++
}

// CountLeaseRenewFailure records a renewal that failed for a reason other than losing the lease.
func (r *Registry) CountLeaseRenewFailure(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leaseRenewFails[kind]++
}

// CountFencedWriteRejected records a poller write rejected because its lease epoch was gone.
func (r *Registry) CountFencedWriteRejected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fencedRejected++
}

// SetCoordinatorActive records whether this process holds the coordinator lease.
func (r *Registry) SetCoordinatorActive(active bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coordinatorOn = active
}

// SetPollMembers replaces the live member counts per role.
func (r *Registry) SetPollMembers(byRole map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pollMembers = make(map[string]int, len(byRole))
	for role, n := range byRole {
		r.pollMembers[role] = n
	}
}

// SetBuildInfo records the build version exposed via harbormaster_build_info.
func (r *Registry) SetBuildInfo(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buildVersion = version
	r.buildInfoSet = true
}

// SetRoleInfo records the role this process runs, exposed via harbormaster_role_info.
func (r *Registry) SetRoleInfo(role, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roleName, r.roleVersion, r.roleInfoSet = role, version, true
}

// SetKVUp records whether the Valkey/Redis store is reachable. The kv series appear once it has been called, so an
// install without a store exposes none of them.
func (r *Registry) SetKVUp(up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kvUp = up
	r.kvUpSet = true
}

// ClearKV forgets every kv series, as if no store had been configured. Closing the store calls it so a later
// run in the same process (tests) starts clean.
func (r *Registry) ClearKV() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kvUp, r.kvUpSet = false, false
	r.kvErrors = map[string]uint64{}
	r.kvDuration = map[string]*histogram{}
}

// CountKVError records one failed store command; op is the lower-case command name.
func (r *Registry) CountKVError(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kvErrors[op]++
}

// ObserveKVCommand records how long one store command took.
func (r *Registry) ObserveKVCommand(op string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.kvDuration[op]
	if h == nil {
		h = &histogram{buckets: make([]uint64, len(kvBuckets)+1)}
		r.kvDuration[op] = h
	}
	secs := d.Seconds()
	idx := len(kvBuckets)
	for i, upper := range kvBuckets {
		if secs <= upper {
			idx = i
			break
		}
	}
	h.buckets[idx]++
	h.sum += secs
	h.count++
}

// Snapshot is a copy of selected registry state for tests and the soak test.
type Snapshot struct {
	AWSCalls     map[string]uint64        // key "account|region|service|operation|result|source"
	LoopInterval map[string]time.Duration // key "account|region|tier"
	Leader       bool
	Idle         bool
}

// Snapshot returns a deep copy of the current state.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		AWSCalls:     make(map[string]uint64, len(r.awsCalls)),
		LoopInterval: make(map[string]time.Duration, len(r.loopInterval)),
		Leader:       r.leader,
		Idle:         r.idle,
	}
	for k, v := range r.awsCalls {
		s.AWSCalls[k] = v
	}
	for k, v := range r.loopInterval {
		s.LoopInterval[k] = v
	}
	return s
}
