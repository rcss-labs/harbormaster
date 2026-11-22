//go:build soak

package poller

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxClusterModelGzip is the most a cluster model may weigh once compressed. The design estimates about 15 KB for 15
// services and 45 tasks; the limit leaves room for estates far fatter than that and catches a model that grew by an
// order of magnitude, which would make every miss and every hit expensive.
const maxClusterModelGzip = 256 << 10

// TestSoak_ModelSizes polls one slice of the target-scale estate (20 clusters, 15 services and 45 tasks each) and
// measures the compressed read models the snapshot cache would store.
func TestSoak_ModelSizes(t *testing.T) {
	env := newSoakEnv(t, fake.TargetScale)
	ctx := context.Background()
	target := env.targets[0]
	if err := PollOnce(ctx, env.st, env.fake, env.cfg, target); err != nil {
		t.Fatal(err)
	}
	clusters, err := env.st.ListClusters(ctx, store.TargetFilter{AccountID: target.AccountID, Region: target.Region}, store.Visibility{Admin: true})
	if err != nil || len(clusters) == 0 {
		t.Fatalf("no clusters polled: %v", err)
	}
	size := func(v any) int {
		body, err := snapcache.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return len(body)
	}
	var largestCluster, largestService int
	var raw int
	for _, c := range clusters {
		m, err := snapcache.LoadCluster(ctx, env.st, c.Key)
		if err != nil {
			t.Fatal(err)
		}
		largestCluster = max(largestCluster, size(m))
		raw = max(raw, len(m.Services)+len(m.Tasks))
		for _, svc := range m.Services[:min(len(m.Services), 3)] {
			sm, err := snapcache.LoadService(ctx, env.st, svc.Key)
			if err != nil {
				t.Fatal(err)
			}
			largestService = max(largestService, size(sm))
		}
	}
	slice, err := snapcache.LoadSlice(ctx, env.st, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("largest cluster model: %d bytes gzip (%d services + tasks); largest of the sampled service models: %d bytes gzip; slice summary of %d clusters: %d bytes gzip",
		largestCluster, raw, largestService, len(clusters), size(slice))
	if largestCluster > maxClusterModelGzip {
		t.Fatalf("a cluster model is %d bytes gzip, over the %d byte limit", largestCluster, maxClusterModelGzip)
	}
}
