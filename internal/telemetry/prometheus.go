package telemetry

import (
	"bufio"
	"bytes"
	"io"
	"sort"
	"strconv"
	"strings"
)

var (
	awsCallLabels = []string{"account", "region", "service", "operation", "result", "source"}
	loopLabels    = []string{"account", "region", "tier"}
	loopRunLabels = []string{"account", "region", "tier", "result"}
	kvLabels      = []string{"op"}
)

// WritePrometheus renders all families in the Prometheus text exposition
// format, sorted by family name and then by label values.
func (r *Registry) WritePrometheus(w io.Writer) error {
	// Render into memory under the lock, then write after releasing it so a
	// slow client cannot block metric recording.
	var buf bytes.Buffer
	r.mu.Lock()
	bw := bufio.NewWriter(&buf)

	// Families in alphabetical order by name.
	writeHeader(bw, "harbormaster_aws_api_calls_total", "AWS API calls made by Harbormaster.", "counter")
	for _, k := range sortedKeys(r.awsCalls) {
		writeSample(bw, "harbormaster_aws_api_calls_total", awsCallLabels, k, "", utoa(r.awsCalls[k]))
	}

	writeHeader(bw, "harbormaster_build_info", "Build information; value is always 1.", "gauge")
	if r.buildInfoSet {
		writeSample(bw, "harbormaster_build_info", []string{"version"}, r.buildVersion, "", "1")
	}

	writeHeader(bw, "harbormaster_coordinator_active", "1 if this process holds the coordinator lease.", "gauge")
	writeSample(bw, "harbormaster_coordinator_active", nil, "", "", boolValue(r.coordinatorOn))

	r.writeFocusFamilies(bw)
	r.writeSnapshotCacheFamilies(bw)
	r.writeInvestigationFamilies(bw)

	writeHeader(bw, "harbormaster_fenced_writes_rejected_total", "Poller writes rejected because the slice lease was lost.", "counter")
	writeSample(bw, "harbormaster_fenced_writes_rejected_total", nil, "", "", utoa(r.fencedRejected))

	if r.kvUpSet {
		writeHeader(bw, "harbormaster_kv_command_duration_seconds", "Valkey/Redis command duration in seconds.", "histogram")
		for _, k := range sortedKeys(r.kvDuration) {
			writeHistogram(bw, "harbormaster_kv_command_duration_seconds", kvLabels, k, r.kvDuration[k])
		}

		writeHeader(bw, "harbormaster_kv_errors_total", "Failed Valkey/Redis commands by command.", "counter")
		for _, k := range sortedKeys(r.kvErrors) {
			writeSample(bw, "harbormaster_kv_errors_total", kvLabels, k, "", utoa(r.kvErrors[k]))
		}

		writeHeader(bw, "harbormaster_kv_up", "1 if the Valkey/Redis store is reachable.", "gauge")
		writeSample(bw, "harbormaster_kv_up", nil, "", "", boolValue(r.kvUp))
	}

	writeHeader(bw, "harbormaster_lease_events_total", "Lease events by kind (slice, coordinator) and event (acquired, released, lost, takeover).", "counter")
	for _, k := range sortedKeys(r.leaseEvents) {
		writeSample(bw, "harbormaster_lease_events_total", []string{"kind", "event"}, k, "", utoa(r.leaseEvents[k]))
	}

	writeHeader(bw, "harbormaster_lease_renew_failures_total", "Lease renewals that failed for a reason other than losing the lease.", "counter")
	for _, k := range sortedKeys(r.leaseRenewFails) {
		writeSample(bw, "harbormaster_lease_renew_failures_total", []string{"kind"}, k, "", utoa(r.leaseRenewFails[k]))
	}

	writeHeader(bw, "harbormaster_lease_slices_owned", "Account x region slices this process owns.", "gauge")
	writeSample(bw, "harbormaster_lease_slices_owned", nil, "", "", strconv.Itoa(r.leaseSlices))

	writeHeader(bw, "harbormaster_lease_weight_owned", "Total weight (clusters) of the slices this process owns.", "gauge")
	writeSample(bw, "harbormaster_lease_weight_owned", nil, "", "", strconv.Itoa(r.leaseWeight))

	writeHeader(bw, "harbormaster_limit_decisions_total", "Shared-limit decisions by limit, result and backend.", "counter")
	for _, k := range sortedKeys(r.limitDecisions) {
		writeSample(bw, "harbormaster_limit_decisions_total", []string{"limit", "result", "backend"}, k, "", utoa(r.limitDecisions[k]))
	}

	writeHeader(bw, "harbormaster_notification_deliveries_total", "Notification delivery attempts by destination and result.", "counter")
	for _, k := range sortedKeys(r.notifyCounts) {
		writeSample(bw, "harbormaster_notification_deliveries_total", []string{"destination", "result"}, k, "", utoa(r.notifyCounts[k]))
	}

	writeHeader(bw, "harbormaster_poll_loop_duration_seconds", "Poll loop run duration in seconds.", "histogram")
	for _, k := range sortedKeys(r.loopDuration) {
		writeHistogram(bw, "harbormaster_poll_loop_duration_seconds", loopLabels, k, r.loopDuration[k])
	}

	writeHeader(bw, "harbormaster_poll_loop_interval_seconds", "Configured poll loop interval in seconds.", "gauge")
	for _, k := range sortedKeys(r.loopInterval) {
		writeSample(bw, "harbormaster_poll_loop_interval_seconds", loopLabels, k, "",
			strconv.FormatFloat(r.loopInterval[k].Seconds(), 'g', -1, 64))
	}

	writeHeader(bw, "harbormaster_poll_loop_runs_total", "Poll loop runs by result.", "counter")
	for _, k := range sortedKeys(r.loopRuns) {
		writeSample(bw, "harbormaster_poll_loop_runs_total", loopRunLabels, k, "", utoa(r.loopRuns[k]))
	}

	writeHeader(bw, "harbormaster_poll_members", "Live members (processes) by role, from the member heartbeats.", "gauge")
	for _, role := range sortedKeys(r.pollMembers) {
		writeSample(bw, "harbormaster_poll_members", []string{"role"}, role, "", strconv.Itoa(r.pollMembers[role]))
	}

	writeHeader(bw, "harbormaster_poller_leader", "1 if this process holds the coordinator lease (kept under its old name for existing alerts).", "gauge")
	writeSample(bw, "harbormaster_poller_leader", nil, "", "", boolValue(r.leader))

	writeHeader(bw, "harbormaster_poller_idle", "1 if polling is slowed because no one has used the UI recently.", "gauge")
	writeSample(bw, "harbormaster_poller_idle", nil, "", "", boolValue(r.idle))

	writeHeader(bw, "harbormaster_registry_requests_total", "Reads of public image registries by host and outcome.", "counter")
	for _, k := range sortedKeys(r.registryRequests) {
		writeSample(bw, "harbormaster_registry_requests_total", []string{"host", "outcome"}, k, "", utoa(r.registryRequests[k]))
	}

	writeHeader(bw, "harbormaster_role_info", "The role this process runs (all, web, poller or coordinator); value is always 1.", "gauge")
	if r.roleInfoSet {
		writeSample(bw, "harbormaster_role_info", []string{"role", "version"}, joinKey(r.roleName, r.roleVersion), "", "1")
	}

	writeHeader(bw, "harbormaster_snapshot_rows_written_total", "Snapshot rows written by table.", "counter")
	for _, k := range sortedKeys(r.snapshotRows) {
		writeSample(bw, "harbormaster_snapshot_rows_written_total", []string{"table"}, k, "", utoa(r.snapshotRows[k]))
	}

	err := bw.Flush()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

func writeHistogram(bw *bufio.Writer, name string, labels []string, key string, h *histogram) {
	var cum uint64
	for i, upper := range h.bounds {
		cum += h.buckets[i]
		writeSample(bw, name+"_bucket", labels, key, `le="`+strconv.FormatFloat(upper, 'g', -1, 64)+`"`, utoa(cum))
	}
	writeSample(bw, name+"_bucket", labels, key, `le="+Inf"`, utoa(h.count))
	writeSample(bw, name+"_sum", labels, key, "", strconv.FormatFloat(h.sum, 'g', -1, 64))
	writeSample(bw, name+"_count", labels, key, "", utoa(h.count))
}

func writeHeader(bw *bufio.Writer, name, help, typ string) {
	bw.WriteString("# HELP " + name + " " + help + "\n")
	bw.WriteString("# TYPE " + name + " " + typ + "\n")
}

// writeSample emits one line. key holds label values joined by keySep; extra is
// a pre-formatted trailing label such as `le="0.5"`.
func writeSample(bw *bufio.Writer, name string, labels []string, key, extra, value string) {
	bw.WriteString(name)
	if len(labels) > 0 || extra != "" {
		values := strings.Split(key, keySep)
		bw.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				bw.WriteByte(',')
			}
			bw.WriteString(l + `="` + escapeLabel(values[i]) + `"`)
		}
		if extra != "" {
			if len(labels) > 0 {
				bw.WriteByte(',')
			}
			bw.WriteString(extra)
		}
		bw.WriteByte('}')
	}
	bw.WriteString(" " + value + "\n")
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(v string) string { return labelEscaper.Replace(v) }

func boolValue(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func utoa(n uint64) string { return strconv.FormatUint(n, 10) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
