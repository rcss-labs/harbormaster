package telemetry

import (
	"bufio"
	"strconv"
	"time"
)

// focusMetrics are the series of live page refresh (focus). They live under Registry.mu; maps are made on first use.
type focusMetrics struct {
	admissions     map[string]uint64
	pollNow        map[string]uint64
	objects        map[string]int
	interval       map[string]time.Duration
	cycleDuration  map[string]*histogram
	pollNowLatency *histogram
}

// CountFocusAdmission records the result of one focus heartbeat: admitted, refreshed, capped, user_capped,
// unavailable or not_found.
func (r *Registry) CountFocusAdmission(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.focusM.admissions == nil {
		r.focusM.admissions = map[string]uint64{}
	}
	r.focusM.admissions[result]++
}

// CountPollNow records what a poller did with a poll-now signal: handled, coalesced or not_owner.
func (r *Registry) CountPollNow(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.focusM.pollNow == nil {
		r.focusM.pollNow = map[string]uint64{}
	}
	r.focusM.pollNow[result]++
}

// SetFocusObjects replaces the number of focus loops this process runs, by kind.
func (r *Registry) SetFocusObjects(byKind map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.focusM.objects = make(map[string]int, len(byKind))
	for k, v := range byKind {
		r.focusM.objects[k] = v
	}
}

// SetFocusInterval records the effective focus interval of a slice.
func (r *Registry) SetFocusInterval(account, region string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.focusM.interval == nil {
		r.focusM.interval = map[string]time.Duration{}
	}
	r.focusM.interval[joinKey(account, region)] = d
}

// ObserveFocusCycle records how long one focus cycle took.
func (r *Registry) ObserveFocusCycle(kind string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.focusM.cycleDuration == nil {
		r.focusM.cycleDuration = map[string]*histogram{}
	}
	h := r.focusM.cycleDuration[kind]
	if h == nil {
		h = newHistogram(loopBuckets)
		r.focusM.cycleDuration[kind] = h
	}
	h.observe(d)
}

// ObservePollNowLatency records the time from a poll-now signal to the end of the cycle it caused.
func (r *Registry) ObservePollNowLatency(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.focusM.pollNowLatency == nil {
		r.focusM.pollNowLatency = newHistogram(loopBuckets)
	}
	r.focusM.pollNowLatency.observe(d)
}

// writeFocusFamilies renders the focus series; the caller holds r.mu.
func (r *Registry) writeFocusFamilies(bw *bufio.Writer) {
	f := &r.focusM
	writeHeader(bw, "harbormaster_focus_admissions_total", "Focus heartbeats by result.", "counter")
	for _, k := range sortedKeys(f.admissions) {
		writeSample(bw, "harbormaster_focus_admissions_total", []string{"result"}, k, "", utoa(f.admissions[k]))
	}
	writeHeader(bw, "harbormaster_focus_cycle_duration_seconds", "Duration of focus cycles by kind.", "histogram")
	for _, k := range sortedKeys(f.cycleDuration) {
		writeHistogram(bw, "harbormaster_focus_cycle_duration_seconds", []string{"kind"}, k, f.cycleDuration[k])
	}
	writeHeader(bw, "harbormaster_focus_interval_seconds", "Effective focus interval of a slice in seconds.", "gauge")
	for _, k := range sortedKeys(f.interval) {
		writeSample(bw, "harbormaster_focus_interval_seconds", []string{"account", "region"}, k, "",
			strconv.FormatFloat(f.interval[k].Seconds(), 'g', -1, 64))
	}
	writeHeader(bw, "harbormaster_focus_objects", "Focus loops running in this process, by kind.", "gauge")
	for _, k := range sortedKeys(f.objects) {
		writeSample(bw, "harbormaster_focus_objects", []string{"kind"}, k, "", strconv.Itoa(f.objects[k]))
	}
	writeHeader(bw, "harbormaster_pollnow_latency_seconds", "Time from a poll-now signal to the end of the cycle it caused.", "histogram")
	if f.pollNowLatency != nil {
		writeHistogram(bw, "harbormaster_pollnow_latency_seconds", nil, "", f.pollNowLatency)
	}
	writeHeader(bw, "harbormaster_pollnow_signals_total", "Poll-now signals by what the poller did with them.", "counter")
	for _, k := range sortedKeys(f.pollNow) {
		writeSample(bw, "harbormaster_pollnow_signals_total", []string{"result"}, k, "", utoa(f.pollNow[k]))
	}
}
