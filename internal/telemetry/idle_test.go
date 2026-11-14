package telemetry

import (
	"strings"
	"testing"
)

func TestWritePrometheus_PollerIdle(t *testing.T) {
	r := New()
	if out := render(t, r); !strings.Contains(out, "harbormaster_poller_idle 0") {
		t.Errorf("default output missing idle 0\n%s", out)
	}
	r.SetPollerIdle(true)
	out := render(t, r)
	if !strings.Contains(out, "# TYPE harbormaster_poller_idle gauge") || !strings.Contains(out, "harbormaster_poller_idle 1") {
		t.Errorf("output missing idle 1\n%s", out)
	}
	if !r.Snapshot().Idle {
		t.Error("Snapshot.Idle = false, want true")
	}
}
