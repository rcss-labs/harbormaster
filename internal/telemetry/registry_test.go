package telemetry

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRegistry_CountersAccumulate(t *testing.T) {
	r := New()
	r.CountAWSCall("482109375512", "us-east-1", "ecs", "ListClusters", "ok")
	r.CountAWSCall("482109375512", "us-east-1", "ecs", "ListClusters", "ok")
	key := "482109375512|us-east-1|ecs|ListClusters|ok"
	if got := r.Snapshot().AWSCalls[key]; got != 2 {
		t.Fatalf("AWSCalls[%q] = %d, want 2", key, got)
	}
}

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWritePrometheus_Format(t *testing.T) {
	r := New()
	r.ObserveLoop("482109375512", "us-east-1", "services", 300*time.Millisecond, true)
	r.SetLeader(true)
	out := render(t, r)
	lbl := `account="482109375512",region="us-east-1",tier="services"`
	for _, want := range []string{
		"# TYPE harbormaster_poll_loop_duration_seconds histogram",
		"harbormaster_poll_loop_duration_seconds_bucket{" + lbl + `,le="0.5"} 1`,
		"harbormaster_poll_loop_duration_seconds_bucket{" + lbl + `,le="0.25"} 0`,
		"harbormaster_poll_loop_duration_seconds_bucket{" + lbl + `,le="+Inf"} 1`,
		"harbormaster_poll_loop_duration_seconds_count{" + lbl + "} 1",
		"harbormaster_poll_loop_runs_total{" + lbl + `,result="ok"} 1`,
		"harbormaster_poller_leader 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("output must end with newline")
	}
}

func TestWritePrometheus_EscapesLabelValues(t *testing.T) {
	r := New()
	r.CountAWSCall("a", "r", "s", `a"b\c`, "ok")
	if out := render(t, r); !strings.Contains(out, `operation="a\"b\\c"`) {
		t.Fatalf("label not escaped:\n%s", out)
	}
}

func TestWritePrometheus_DeterministicOrder(t *testing.T) {
	r := New()
	r.SetBuildInfo("1.2.3")
	r.CountSnapshotWrites("services", 3)
	r.CountAWSCall("b", "r", "ecs", "X", "ok")
	r.CountAWSCall("a", "r", "ecs", "Y", "error")
	r.ObserveLoop("a", "r", "t", time.Second, false)
	r.SetLoopInterval("a", "r", "t", 30*time.Second)
	if a, b := render(t, r), render(t, r); a != b {
		t.Fatalf("output differs between calls")
	}
}
