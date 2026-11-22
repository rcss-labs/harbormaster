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
}
