package telemetry

import (
	"bytes"
	"strings"
	"testing"
)

func TestInvestigationStepErrorsAreExposed(t *testing.T) {
	r := New()
	var before bytes.Buffer
	if err := r.WritePrometheus(&before); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before.String(), "harbormaster_investigation_step_errors_total 0\n") {
		t.Errorf("no zero series before any error:\n%s", before.String())
	}
	r.CountInvestigationStepError()
	r.CountInvestigationStepError()
	var after bytes.Buffer
	if err := r.WritePrometheus(&after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.String(), "harbormaster_investigation_step_errors_total 2\n") {
		t.Errorf("the two errors are not exposed:\n%s", after.String())
	}
}
