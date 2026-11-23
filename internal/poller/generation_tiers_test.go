package poller

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
)

// Tokens expire after ten minutes; every pass over a cluster must put a missing one back and keep an existing one.
func TestGeneration_EveryTierKeepsTheTokensAlive(t *testing.T) {
	ctx := context.Background()
	tiers := map[string]func(l *Loop) error{
		"clusters":     func(l *Loop) error { return l.syncClusters(ctx) },
		"serviceList":  func(l *Loop) error { return l.syncServiceList(ctx) },
		"services":     func(l *Loop) error { return l.syncServices(ctx) },
		"runningTasks": func(l *Loop) error { return l.syncRunningTasks(ctx) },
		"stoppedTasks": func(l *Loop) error { return l.syncStoppedTasks(ctx) },
		"fullTasks":    func(l *Loop) error { return l.syncFullTasks(ctx) },
		"targetHealth": func(l *Loop) error {
			for _, d := range l.defs {
				if d.tier.name == tierTargetHealth {
					return d.run(ctx)
				}
			}
			t.Fatal("no target health tier")
			return nil
		},
	}
	for name, run := range tiers {
		t.Run(name, func(t *testing.T) {
			cache := newRecordingCache()
			h := genHarness(t, cache)
			h.runOnce(t)
			cache.mu.Lock()
			cache.tokens = map[snapcache.GenKey]string{} // everything expired
			cache.mu.Unlock()
			if err := run(h.loop); err != nil {
				t.Fatal(err)
			}
			tokens := cache.snapshot()
			if tokens[sliceGenKey] == "" {
				t.Errorf("the %s tier did not put the slice token back", name)
			}
			for _, cluster := range h.clusterNames(t) {
				if tokens[genKeyOf(cluster)] == "" {
					t.Errorf("the %s tier did not put the token of %s back", name, cluster)
				}
			}
		})
	}
}

func TestGeneration_ClusterRowChangeBumpsOnlyThatCluster(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	h.estate.Mu.Lock()
	for i := range h.estate.Clusters {
		if aws.ToString(h.estate.Clusters[i].Cluster.ClusterName) == "ci-tools" {
			h.estate.Clusters[i].Cluster.Status = aws.String("INACTIVE")
		}
	}
	h.estate.Mu.Unlock()
	if err := h.loop.syncClusters(context.Background()); err != nil {
		t.Fatal(err)
	}
	moved := changedKeys(before, cache.snapshot())
	if len(moved) != 2 || !hasKey(moved, genKeyOf("ci-tools")) || !hasKey(moved, sliceGenKey) {
		t.Fatalf("a change to one cluster row moved %v, want ci-tools and the slice", moved)
	}
}

// The database said a cluster row changed but this loop remembers writing the same content: it cannot tell which row,
// so it names every row rather than none.
func TestGeneration_UnknownClusterChangeNamesEveryDescribedCluster(t *testing.T) {
	h := genHarness(t, newRecordingCache())
	rows := []model.Cluster{{Key: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "a"}, Status: "ACTIVE"}, {Key: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "b"}, Status: "ACTIVE"}}
	h.loop.rememberClusterHashes(rows)
	if got := h.loop.changedClusterNames(rows); len(got) != 2 {
		t.Fatalf("changedClusterNames = %v, want both clusters", got)
	}
	rows[1].Status = "INACTIVE"
	if got := h.loop.changedClusterNames(rows); len(got) != 1 || got[0] != "b" {
		t.Fatalf("changedClusterNames = %v, want only b", got)
	}
}

// A target group that leaves the slice's services is deleted by the tier; that is a change even though no row differs.
func TestGeneration_RemovedTargetGroupBumpsTheSliceFromTheTargetHealthTier(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	extra := "arn:aws:elasticloadbalancing:us-east-1:" + sandboxAccount + ":targetgroup/payments-api-extra/0123456789abcdef"
	h.estate.AddTargetGroup(paymentsAPI(), fake.TargetGroup{ARN: extra, Healthy: 1})
	h.runOnce(t)
	h.estate.RemoveTargetGroup(extra)
	if err := h.loop.syncServices(context.Background()); err != nil { // the service detaches the group
		t.Fatal(err)
	}
	before := cache.snapshot()
	for _, d := range h.loop.defs {
		if d.tier.name == tierTargetHealth {
			if err := d.run(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if moved := changedKeys(before, cache.snapshot()); !hasKey(moved, sliceGenKey) {
		t.Fatalf("deleting a target group's rows moved %v, want the slice", moved)
	}
}

// The page of a removed cluster is told by its own token, or a direct link would keep serving it until the TTL.
func TestGeneration_RemovedClusterBumpsItsOwnToken(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	h.estate.Mu.Lock()
	kept := h.estate.Clusters[:0]
	for _, c := range h.estate.Clusters {
		if aws.ToString(c.Cluster.ClusterName) != "ci-tools" || c.AccountID != loopTarget.AccountID || c.Region != loopTarget.Region {
			kept = append(kept, c)
		}
	}
	h.estate.Clusters = kept
	h.estate.Mu.Unlock()
	// A loop that has just started knows nothing about what it stored before: it must ask the database.
	h.loop.mu.Lock()
	h.loop.clusters = nil
	h.loop.mu.Unlock()
	if err := h.loop.syncClusters(context.Background()); err != nil {
		t.Fatal(err)
	}
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, genKeyOf("ci-tools")) || !hasKey(moved, sliceGenKey) {
		t.Fatalf("removing ci-tools moved %v, want its own token and the slice's", moved)
	}
	for _, k := range moved {
		if k != genKeyOf("ci-tools") && k != sliceGenKey {
			t.Errorf("removing ci-tools also moved %+v", k)
		}
	}
}

// A loop that has already run a pass knows the stored clusters from it: no database read is needed to name the removed one.
func TestGeneration_RemovedClusterBumpsItsOwnTokenOnARunningLoop(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	h.estate.Mu.Lock()
	kept := h.estate.Clusters[:0]
	for _, c := range h.estate.Clusters {
		if aws.ToString(c.Cluster.ClusterName) != "ci-tools" || c.AccountID != loopTarget.AccountID || c.Region != loopTarget.Region {
			kept = append(kept, c)
		}
	}
	h.estate.Clusters = kept
	h.estate.Mu.Unlock()
	if err := h.loop.syncClusters(context.Background()); err != nil {
		t.Fatal(err)
	}
	moved := changedKeys(before, cache.snapshot())
	if !hasKey(moved, genKeyOf("ci-tools")) || !hasKey(moved, sliceGenKey) {
		t.Fatalf("removing ci-tools moved %v, want its own token and the slice's", moved)
	}
	for _, k := range moved {
		if k != genKeyOf("ci-tools") && k != sliceGenKey {
			t.Errorf("removing ci-tools also moved %+v", k)
		}
	}
}

func TestGeneration_RemovedClusterBumpsTheSlice(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	before := cache.snapshot()
	h.estate.Mu.Lock()
	kept := h.estate.Clusters[:0]
	for _, c := range h.estate.Clusters {
		if aws.ToString(c.Cluster.ClusterName) != "ci-tools" || c.AccountID != loopTarget.AccountID || c.Region != loopTarget.Region {
			kept = append(kept, c)
		}
	}
	h.estate.Clusters = kept
	h.estate.Mu.Unlock()
	if err := h.loop.syncClusters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if moved := changedKeys(before, cache.snapshot()); !hasKey(moved, sliceGenKey) {
		t.Fatalf("removing a cluster moved %v, want at least the slice", moved)
	}
}

// A pass over every cluster keeps the tokens alive with one cache command, not one per cluster.
func TestGeneration_ClusterPassTouchesTheCacheOnce(t *testing.T) {
	cache := newRecordingCache()
	h := genHarness(t, cache)
	h.runOnce(t)
	if n := len(h.clusterNames(t)); n < 2 {
		t.Fatalf("the estate has %d clusters in the slice; the test needs several", n)
	}
	cache.mu.Lock()
	cache.touchCalls = 0
	cache.mu.Unlock()
	if err := h.loop.syncServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.touchCalls != 1 {
		t.Fatalf("one services pass made %d touch calls, want 1", cache.touchCalls)
	}
}
