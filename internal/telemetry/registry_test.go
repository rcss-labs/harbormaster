package telemetry

import (
	"bytes"
	"fmt"
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

func TestWritePrometheus_RegistryRequestsByHostAndOutcome(t *testing.T) {
	r := New()
	r.CountRegistryRequest("ghcr.io", "ok")
	r.CountRegistryRequest("ghcr.io", "ok")
	r.CountRegistryRequest("registry-1.docker.io", "rate_limited")
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_registry_requests_total counter",
		`harbormaster_registry_requests_total{host="ghcr.io",outcome="ok"} 2`,
		`harbormaster_registry_requests_total{host="registry-1.docker.io",outcome="rate_limited"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestWritePrometheus_EscapesLabelValues(t *testing.T) {
	r := New()
	r.CountAWSCall("a", "r", "s", `a"b\c`, "ok")
	if out := render(t, r); !strings.Contains(out, `operation="a\"b\\c"`) {
		t.Fatalf("label not escaped:\n%s", out)
	}
}

func TestPrometheus_NotificationCounter(t *testing.T) {
	r := New()
	r.CountNotification("slack", "delivered")
	r.CountNotification("slack", "delivered")
	r.CountNotification("telegram", "failed")
	r.CountNotification("webhook", "retried")
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_notification_deliveries_total counter",
		`harbormaster_notification_deliveries_total{destination="slack",result="delivered"} 2`,
		`harbormaster_notification_deliveries_total{destination="telegram",result="failed"} 1`,
		`harbormaster_notification_deliveries_total{destination="webhook",result="retried"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
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

// blockingWriter blocks every Write until release is closed.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return len(p), nil
}

func TestWritePrometheus_SlowWriterDoesNotBlockRecording(t *testing.T) {
	r := New()
	for i := 0; i < 200; i++ { // enough output to exceed any internal write buffer
		r.CountAWSCall("acct", "us-east-1", "ecs", fmt.Sprintf("Op%d", i), "ok")
	}
	w := &blockingWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- r.WritePrometheus(w) }()
	defer func() { close(w.release); <-done }()

	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("WritePrometheus never wrote")
	}
	recorded := make(chan struct{})
	go func() {
		r.CountAWSCall("acct", "us-east-1", "ecs", "Late", "ok")
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("CountAWSCall blocked while a client write was stalled")
	}
}

func TestPrometheus_KVSeries(t *testing.T) {
	r := New()
	if out := render(t, r); strings.Contains(out, "harbormaster_kv_") {
		t.Fatalf("kv series must be absent until a store is configured:\n%s", out)
	}
	r.SetKVUp(false)
	r.CountKVError("get")
	r.CountKVError("get")
	r.ObserveKVCommand("get", 3*time.Millisecond)
	r.ObserveKVCommand("get", 400*time.Millisecond)
	r.SetKVUp(true)
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_kv_up gauge",
		"harbormaster_kv_up 1",
		"# TYPE harbormaster_kv_errors_total counter",
		`harbormaster_kv_errors_total{op="get"} 2`,
		"# TYPE harbormaster_kv_command_duration_seconds histogram",
		`harbormaster_kv_command_duration_seconds_bucket{op="get",le="0.001"} 0`,
		`harbormaster_kv_command_duration_seconds_bucket{op="get",le="0.005"} 1`,
		`harbormaster_kv_command_duration_seconds_bucket{op="get",le="0.25"} 1`,
		`harbormaster_kv_command_duration_seconds_bucket{op="get",le="+Inf"} 2`,
		`harbormaster_kv_command_duration_seconds_count{op="get"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}
