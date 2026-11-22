package telemetry

import (
	"strings"
	"testing"
)

func TestPrometheus_SnapshotCacheRequests(t *testing.T) {
	r := New()
	r.CountSnapshotCache("cluster", "hit")
	r.CountSnapshotCache("cluster", "hit")
	r.CountSnapshotCache("cluster", "miss")
	r.CountSnapshotCache("service", "bypass")
	r.CountSnapshotCache("slice", "error")
	out := render(t, r)
	for _, want := range []string{
		"# TYPE harbormaster_snapshot_cache_requests_total counter",
		`harbormaster_snapshot_cache_requests_total{model="cluster",result="hit"} 2`,
		`harbormaster_snapshot_cache_requests_total{model="cluster",result="miss"} 1`,
		`harbormaster_snapshot_cache_requests_total{model="service",result="bypass"} 1`,
		`harbormaster_snapshot_cache_requests_total{model="slice",result="error"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}
