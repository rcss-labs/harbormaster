package telemetry

import (
	"strings"
	"testing"
	"time"
)

func TestPrometheus_FocusSeries(t *testing.T) {
	r := New()
	r.CountFocusAdmission("admitted")
	r.CountFocusAdmission("admitted")
	r.CountFocusAdmission("capped")
	r.CountPollNow("handled")
	r.CountPollNow("not_owner")
	r.SetFocusObjects(map[string]int{"cluster": 1, "service": 3})
	r.SetFocusInterval("482109375512", "us-east-1", 10*time.Second)
	r.ObserveFocusCycle("service", 300*time.Millisecond)
	r.ObservePollNowLatency(2 * time.Second)
	out := render(t, r)
	for _, want := range []string{
		`harbormaster_focus_admissions_total{result="admitted"} 2`,
		`harbormaster_focus_admissions_total{result="capped"} 1`,
		`harbormaster_pollnow_signals_total{result="handled"} 1`,
		`harbormaster_pollnow_signals_total{result="not_owner"} 1`,
		`harbormaster_focus_objects{kind="service"} 3`,
		`harbormaster_focus_objects{kind="cluster"} 1`,
		`harbormaster_focus_interval_seconds{account="482109375512",region="us-east-1"} 10`,
		`harbormaster_focus_cycle_duration_seconds_bucket{kind="service",le="0.5"} 1`,
		`harbormaster_focus_cycle_duration_seconds_count{kind="service"} 1`,
		`harbormaster_pollnow_latency_seconds_bucket{le="2.5"} 1`,
		`harbormaster_pollnow_latency_seconds_count 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}
