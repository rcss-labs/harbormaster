package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var deliveryBase = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

func seedDeployment(t *testing.T, s *store.Store, cluster, service, id string, rev int, started time.Time, outcome string) {
	t.Helper()
	finished := started.Add(2 * time.Minute)
	record := store.DeploymentRecord{
		Key: serviceKey(cluster, service), DeploymentID: id, Revision: rev, Kind: "deploy",
		StartedAt: started, Outcome: outcome,
	}
	if outcome != "" {
		record.FinishedAt = &finished
	}
	if _, err := s.InsertDeploymentIfNew(context.Background(), record); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func factIDs(facts []store.DeploymentFact) []string {
	ids := []string{}
	for _, f := range facts {
		ids = append(ids, f.DeploymentID)
	}
	return ids
}

func TestAnalyticsDeployments_WindowAndScope(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedDeployment(t, s, "c1", "web", "before", 1, deliveryBase.Add(-time.Hour), "completed")
	seedDeployment(t, s, "c1", "web", "in", 2, deliveryBase.Add(time.Hour), "completed")
	seedDeployment(t, s, "c1", "web", "at-to", 3, deliveryBase.Add(24*time.Hour), "completed")
	other := store.DeploymentRecord{
		Key:          serviceKey("c1", "api"),
		DeploymentID: "other-region", Kind: "deploy", StartedAt: deliveryBase.Add(2 * time.Hour),
	}
	other.Key.Region = "us-east-1"
	if _, err := s.InsertDeploymentIfNew(ctx, other); err != nil {
		t.Fatal(err)
	}

	admin := store.Visibility{Admin: true}
	to := deliveryBase.Add(24 * time.Hour)
	facts, err := s.AnalyticsDeployments(ctx, store.TargetFilter{}, admin, deliveryBase, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := factIDs(facts); len(got) != 2 {
		t.Fatalf("facts = %v, want only rows with started_at in [from,to)", got)
	}
	scoped, err := s.AnalyticsDeployments(ctx, store.TargetFilter{Region: "eu-west-1"}, admin, deliveryBase, to)
	if err != nil {
		t.Fatal(err)
	}
	if got := factIDs(scoped); len(got) != 1 || got[0] != "in" {
		t.Fatalf("region-filtered facts = %v, want [in]", got)
	}
	if scoped[0].Revision != 2 || scoped[0].Outcome != "completed" || scoped[0].FinishedAt == nil {
		t.Fatalf("fact = %+v", scoped[0])
	}
}

func TestAnalyticsDeployments_VisibilityHidesOtherTags(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	if err := s.SetClusterTag(ctx, clusterKey("prod-cluster"), prod.ID); err != nil {
		t.Fatal(err)
	}
	seedDeployment(t, s, "prod-cluster", "web", "prod-1", 1, deliveryBase.Add(time.Hour), "completed")
	seedDeployment(t, s, "dev-cluster", "web", "dev-1", 1, deliveryBase.Add(time.Hour), "completed")

	facts, err := s.AnalyticsDeployments(ctx, store.TargetFilter{}, store.Visibility{TagIDs: []int64{int64(prod.ID)}}, deliveryBase, deliveryBase.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := factIDs(facts); len(got) != 1 || got[0] != "prod-1" {
		t.Fatalf("viewer with Prod sees %v, want [prod-1]", got)
	}
	if facts[0].TagID != int64(prod.ID) || !facts[0].TagInherited {
		t.Fatalf("tag = %d inherited=%v, want %d inherited", facts[0].TagID, facts[0].TagInherited, prod.ID)
	}
}

func TestAnalyticsDeployments_TagFilterNarrowsAdmin(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	if err := s.SetClusterTag(ctx, clusterKey("prod-cluster"), prod.ID); err != nil {
		t.Fatal(err)
	}
	seedDeployment(t, s, "prod-cluster", "web", "prod-1", 1, deliveryBase.Add(time.Hour), "completed")
	seedDeployment(t, s, "dev-cluster", "web", "dev-1", 1, deliveryBase.Add(time.Hour), "completed")

	// scopeFrom turns an admin's ?tag=Prod into a non-admin visibility of exactly that tag.
	narrowed := store.Visibility{Admin: false, TagIDs: []int64{int64(prod.ID)}}
	facts, err := s.AnalyticsDeployments(ctx, store.TargetFilter{}, narrowed, deliveryBase, deliveryBase.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := factIDs(facts); len(got) != 1 || got[0] != "prod-1" {
		t.Fatalf("narrowed admin sees %v, want [prod-1]", got)
	}
	all, err := s.AnalyticsDeployments(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, deliveryBase, deliveryBase.Add(24*time.Hour))
	if err != nil || len(all) != 2 {
		t.Fatalf("admin without filter sees %d (%v), want 2", len(all), err)
	}
}
