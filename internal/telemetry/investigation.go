package telemetry

import "bufio"

// CountInvestigationStepError records one error or panic of the dispatcher's end-of-cycle investigation step. The step
// never fails an alert send, so a rising count is the only sign that investigations are not being requested.
func (r *Registry) CountInvestigationStepError() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.investigationStepErrors++
}

// writeInvestigationFamilies renders the investigation step series; the caller holds r.mu.
func (r *Registry) writeInvestigationFamilies(bw *bufio.Writer) {
	writeHeader(bw, "harbormaster_investigation_step_errors_total", "Errors and panics of the dispatcher's investigation step (alert sends are not affected).", "counter")
	writeSample(bw, "harbormaster_investigation_step_errors_total", nil, "", "", utoa(r.investigationStepErrors))
}
