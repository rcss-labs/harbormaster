package telemetry

import (
	"strings"
	"testing"
)

func TestPrometheus_LeaseSeries(t *testing.T) {
	r := New()
	r.SetLeaseOwned(3, 17)
	r.CountLeaseEvent("slice", "acquired")
	r.CountLeaseEvent("slice", "acquired")
	r.CountLeaseEvent("slice", "takeover")
	r.CountLeaseEvent("coordinator", "lost")
	r.CountLeaseRenewFailure("slice")
	r.CountFencedWriteRejected()
	r.CountFencedWriteRejected()
	r.SetCoordinatorActive(true)
	r.SetPollMembers(map[string]int{"poller": 2, "coordinator": 1})
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_lease_slices_owned gauge",
		"harbormaster_lease_slices_owned 3",
		"# TYPE harbormaster_lease_weight_owned gauge",
		"harbormaster_lease_weight_owned 17",
		"# TYPE harbormaster_lease_events_total counter",
		`harbormaster_lease_events_total{kind="slice",event="acquired"} 2`,
		`harbormaster_lease_events_total{kind="slice",event="takeover"} 1`,
		`harbormaster_lease_events_total{kind="coordinator",event="lost"} 1`,
		"# TYPE harbormaster_lease_renew_failures_total counter",
		`harbormaster_lease_renew_failures_total{kind="slice"} 1`,
		"# TYPE harbormaster_fenced_writes_rejected_total counter",
		"harbormaster_fenced_writes_rejected_total 2",
		"# TYPE harbormaster_coordinator_active gauge",
		"harbormaster_coordinator_active 1",
		"# TYPE harbormaster_poll_members gauge",
		`harbormaster_poll_members{role="poller"} 2`,
		`harbormaster_poll_members{role="coordinator"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	r.SetCoordinatorActive(false)
	r.SetPollMembers(map[string]int{"poller": 1})
	out = render(t, r)
	if !strings.Contains(out, "harbormaster_coordinator_active 0") || strings.Contains(out, `role="coordinator"`) {
		t.Errorf("gauges not replaced:\n%s", out)
	}
}

func TestPrometheus_LeaseSeriesExistAtZero(t *testing.T) {
	out := render(t, New())
	for _, want := range []string{"harbormaster_lease_slices_owned 0", "harbormaster_fenced_writes_rejected_total 0", "harbormaster_coordinator_active 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("a fresh registry lacks %q (series must exist at zero so rate() works)", want)
		}
	}
}
