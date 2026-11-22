package telemetry

import "bufio"

// CountSnapshotCache records one cached read: model is cluster, service or slice; result is hit, miss, bypass or error.
func (r *Registry) CountSnapshotCache(model, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapCache == nil {
		r.snapCache = map[string]uint64{}
	}
	r.snapCache[joinKey(model, result)]++
}

// writeSnapshotCacheFamilies renders the snapshot cache series; the caller holds r.mu.
func (r *Registry) writeSnapshotCacheFamilies(bw *bufio.Writer) {
	writeHeader(bw, "harbormaster_snapshot_cache_requests_total", "Cached page reads by model and result (hit, miss, bypass, error).", "counter")
	for _, k := range sortedKeys(r.snapCache) {
		writeSample(bw, "harbormaster_snapshot_cache_requests_total", []string{"model", "result"}, k, "", utoa(r.snapCache[k]))
	}
	writeHeader(bw, "harbormaster_snapshot_cache_token_write_failures_total", "Generation token writes that failed (the cached pages stay stale until the retry succeeds).", "counter")
	writeSample(bw, "harbormaster_snapshot_cache_token_write_failures_total", nil, "", "", utoa(r.snapTokenFailures))
}

// CountSnapshotTokenFailure records a generation token write that failed. The cache is then stale until the poller's
// retry (the next pass over the cluster) succeeds, so a rising count means the store is hurting freshness.
func (r *Registry) CountSnapshotTokenFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapTokenFailures++
}
