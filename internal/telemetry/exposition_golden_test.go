package telemetry

import (
	"bytes"
	"flag"
	"os"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/exposition.golden")

// TestWritePrometheus_Golden pins the whole /metrics output, so refactors of the histogram code cannot change it.
func TestWritePrometheus_Golden(t *testing.T) {
	r := New()
	r.SetBuildInfo("v1.2.3")
	r.SetRoleInfo("poller", "v1.2.3")
	r.CountAWSCall("111", "us-east-1", "ecs", "ListClusters", "ok", "poll")
	r.CountRegistryRequest("registry-1.docker.io", "ok")
	for _, d := range []time.Duration{50 * time.Millisecond, 400 * time.Millisecond, 3 * time.Second, 45 * time.Second} {
		r.ObserveLoop("111", "us-east-1", "fast", d, true)
	}
	r.ObserveLoop("111", "us-east-1", "fast", time.Second, false)
	r.SetLoopInterval("111", "us-east-1", "fast", 5*time.Second)
	r.CountSnapshotWrites("services", 4)
	r.CountNotification("slack", "delivered")
	r.CountLimitDecision("logsearch", "allowed", "kv")
	r.SetLeader(true)
	r.SetPollerIdle(true)
	r.SetLeaseOwned(2, 7)
	r.CountLeaseEvent("slice", "acquired")
	r.CountLeaseRenewFailure("slice")
	r.CountFencedWriteRejected()
	r.SetCoordinatorActive(true)
	r.SetPollMembers(map[string]int{"poller": 2, "web": 3})
	r.SetKVUp(true)
	for _, d := range []time.Duration{500 * time.Microsecond, 3 * time.Millisecond, 20 * time.Millisecond, 400 * time.Millisecond} {
		r.ObserveKVCommand("get", d)
	}
	r.CountKVError("set")
	r.CountFocusAdmission("admitted")
	r.CountPollNow("handled")
	r.SetFocusObjects(map[string]int{"service": 2})
	r.SetFocusInterval("111", "us-east-1", 2*time.Second)
	r.ObserveFocusCycle("service", 700*time.Millisecond)
	r.ObservePollNowLatency(120 * time.Millisecond)
	r.ObservePollNowLatency(12 * time.Second)
	r.CountSnapshotCache("cluster", "hit")

	var buf bytes.Buffer
	if err := r.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/exposition.golden"
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("exposition changed:\n--- got ---\n%s\n--- want ---\n%s", buf.Bytes(), want)
	}
}
