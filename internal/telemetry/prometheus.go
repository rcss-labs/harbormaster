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
	awsCallLabels = []string{"account", "region", "service", "operation", "result"}
	loopLabels    = []string{"account", "region", "tier"}
	loopRunLabels = []string{"account", "region", "tier", "result"}
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

	writeHeader(bw, "harbormaster_poll_loop_duration_seconds", "Poll loop run duration in seconds.", "histogram")
	for _, k := range sortedKeys(r.loopDuration) {
		h := r.loopDuration[k]
		var cum uint64
		for i, upper := range loopBuckets {
			cum += h.buckets[i]
			writeSample(bw, "harbormaster_poll_loop_duration_seconds_bucket", loopLabels, k,
				`le="`+strconv.FormatFloat(upper, 'g', -1, 64)+`"`, utoa(cum))
		}
		writeSample(bw, "harbormaster_poll_loop_duration_seconds_bucket", loopLabels, k, `le="+Inf"`, utoa(h.count))
		writeSample(bw, "harbormaster_poll_loop_duration_seconds_sum", loopLabels, k, "", strconv.FormatFloat(h.sum, 'g', -1, 64))
		writeSample(bw, "harbormaster_poll_loop_duration_seconds_count", loopLabels, k, "", utoa(h.count))
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

	writeHeader(bw, "harbormaster_poller_leader", "1 if this process holds poller leadership.", "gauge")
	leader := "0"
	if r.leader {
		leader = "1"
	}
	writeSample(bw, "harbormaster_poller_leader", nil, "", "", leader)

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

func utoa(n uint64) string { return strconv.FormatUint(n, 10) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
