package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func insertCluster(t *testing.T, s *store.Store, k model.ClusterKey) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(),
		`insert into clusters (account_id, region, cluster_name, content_hash, raw, polled_at)
		 values ($1, $2, $3, 'h', '{}', now())`, k.AccountID, k.Region, k.Cluster)
	if err != nil {
		t.Fatalf("insert cluster: %v", err)
	}
}

func insertService(t *testing.T, s *store.Store, k model.ServiceKey) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(),
		`insert into services (account_id, region, cluster_name, service_name, content_hash, raw, polled_at)
		 values ($1, $2, $3, $4, 'h', '{}', now())`, k.AccountID, k.Region, k.Cluster, k.Service)
	if err != nil {
		t.Fatalf("insert service: %v", err)
	}
}

func clusterKey(name string) model.ClusterKey {
	return model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: name}
}

func serviceKey(cluster, name string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: clusterKey(cluster), Service: name}
}

func TestCreateTag_CaseInsensitiveClash(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.CreateTag(ctx, "Prod", "red"); err != nil {
		t.Fatalf("CreateTag Prod: %v", err)
	}
	if _, err := s.CreateTag(ctx, "prod", "blue"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("CreateTag prod error = %v, want ErrConflict", err)
	}
}

func TestDeleteTag_DevIsBuiltin(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	dev, err := s.DevTag(ctx)
	if err != nil {
		t.Fatalf("DevTag: %v", err)
	}
	if err := s.DeleteTag(ctx, dev.ID); !errors.Is(err, store.ErrBuiltin) {
		t.Fatalf("DeleteTag(Dev) error = %v, want ErrBuiltin", err)
	}
	if err := s.UpdateTag(ctx, dev.ID, "Development", "neutral"); !errors.Is(err, store.ErrBuiltin) {
		t.Fatalf("UpdateTag rename Dev error = %v, want ErrBuiltin", err)
	}
	if err := s.UpdateTag(ctx, dev.ID, "Dev", "blue"); err != nil {
		t.Fatalf("UpdateTag recolour Dev: %v", err)
	}
	if err := s.UpdateTag(ctx, 99999, "x", "blue"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateTag unknown error = %v, want ErrNotFound", err)
	}
}

func TestDeleteTag_CascadesAssignmentsAndPermissions(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	u, _ := s.CreateUser(ctx, "alice", "h", false, false)
	if err := s.SetClusterTag(ctx, clusterKey("c"), tag.ID); err != nil {
		t.Fatal(err)
	}
	svc := tag.ID
	if err := s.SetServiceTag(ctx, serviceKey("c", "web"), &svc); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPermission(ctx, u.ID, tag.ID, authz.ActionSet(authz.View)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTag(ctx, tag.ID); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	if _, ok, _ := s.ClusterTag(ctx, clusterKey("c")); ok {
		t.Error("cluster tag survived tag deletion")
	}
	if _, ok, _ := s.ServiceTag(ctx, serviceKey("c", "web")); ok {
		t.Error("service tag survived tag deletion")
	}
	perms, err := s.PermissionsOf(ctx, u.ID)
	if err != nil || len(perms) != 0 {
		t.Errorf("PermissionsOf = %v, %v; want empty", perms, err)
	}
	if err := s.DeleteTag(ctx, tag.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second DeleteTag error = %v, want ErrNotFound", err)
	}
}

func TestTagImpact_ListsClustersServicesUsers(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	tag, _ := s.CreateTag(ctx, "Prod", "red")
	bob, _ := s.CreateUser(ctx, "bob", "h", false, false)
	alice, _ := s.CreateUser(ctx, "alice", "h", false, false)
	carol, _ := s.CreateUser(ctx, "carol", "h", false, false)
	_ = carol
	_ = s.SetClusterTag(ctx, clusterKey("c1"), tag.ID)
	id := tag.ID
	_ = s.SetServiceTag(ctx, serviceKey("c2", "api"), &id)
	_ = s.SetPermission(ctx, bob.ID, tag.ID, authz.ActionSet(authz.View))
	_ = s.SetPermission(ctx, alice.ID, tag.ID, authz.ActionSet(authz.View|authz.Logs))
	_ = s.SetPermission(ctx, carol.ID, tag.ID, 0) // no row, so not listed

	imp, err := s.TagImpact(ctx, tag.ID)
	if err != nil {
		t.Fatalf("TagImpact: %v", err)
	}
	if len(imp.Clusters) != 1 || imp.Clusters[0] != clusterKey("c1") {
		t.Errorf("Clusters = %v", imp.Clusters)
	}
	if len(imp.Services) != 1 || imp.Services[0] != serviceKey("c2", "api") {
		t.Errorf("Services = %v", imp.Services)
	}
	if len(imp.Usernames) != 2 || imp.Usernames[0] != "alice" || imp.Usernames[1] != "bob" {
		t.Errorf("Usernames = %v, want [alice bob]", imp.Usernames)
	}
	empty, err := s.TagImpact(ctx, 99999)
	if err != nil || empty.Clusters == nil || empty.Services == nil || empty.Usernames == nil {
		t.Errorf("empty impact = %+v, %v; want non-nil empty slices", empty, err)
	}
}

func TestTagsSnapshot_InheritAndUntagged(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	dev, _ := s.DevTag(ctx)
	_ = s.SetClusterTag(ctx, clusterKey("tagged"), prod.ID)

	snap, err := s.TagsSnapshot(ctx)
	if err != nil {
		t.Fatalf("TagsSnapshot: %v", err)
	}
	if snap.DevTagID() != dev.ID {
		t.Errorf("DevTagID = %d, want %d", snap.DevTagID(), dev.ID)
	}
	if id, ok := snap.ClusterTag(clusterKey("tagged")); !ok || id != prod.ID {
		t.Errorf("ClusterTag(tagged) = %d, %v", id, ok)
	}
	if _, ok := snap.ClusterTag(clusterKey("other")); ok {
		t.Error("untagged cluster reported a tag")
	}
	if _, ok := snap.ServiceTag(serviceKey("tagged", "web")); ok {
		t.Error("service without row reported a tag")
	}
	if got := authz.EffectiveServiceTag(snap, serviceKey("tagged", "web")); got != prod.ID {
		t.Errorf("effective service tag = %d, want %d", got, prod.ID)
	}
	if got := authz.EffectiveClusterTag(snap, clusterKey("other")); got != dev.ID {
		t.Errorf("effective untagged cluster = %d, want Dev %d", got, dev.ID)
	}
}

func TestTagCounts_EffectiveUsesCoalesce(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	staging, _ := s.CreateTag(ctx, "Staging", "amber")
	dev, _ := s.DevTag(ctx)
	_ = s.SetClusterTag(ctx, clusterKey("c"), prod.ID)
	insertCluster(t, s, clusterKey("c"))
	insertService(t, s, serviceKey("c", "a"))
	insertService(t, s, serviceKey("c", "b"))
	sid := staging.ID
	_ = s.SetServiceTag(ctx, serviceKey("c", "b"), &sid)
	insertCluster(t, s, clusterKey("untagged"))
	insertService(t, s, serviceKey("untagged", "x"))

	counts, err := s.TagCounts(ctx)
	if err != nil {
		t.Fatalf("TagCounts: %v", err)
	}
	want := map[authz.TagID]store.TagCounts{
		prod.ID:    {Clusters: 1, ServicesDirect: 0, ServicesEffective: 1},
		staging.ID: {Clusters: 0, ServicesDirect: 1, ServicesEffective: 1},
		dev.ID:     {Clusters: 0, ServicesDirect: 0, ServicesEffective: 1},
	}
	for id, w := range want {
		if counts[id] != w {
			t.Errorf("counts[%d] = %+v, want %+v", id, counts[id], w)
		}
	}
	if len(counts) != 3 {
		t.Errorf("len(counts) = %d, want 3 (every tag)", len(counts))
	}
}

func TestMarkOrphanedTags_SetsAndClears(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	ck, sk := clusterKey("gone"), serviceKey("gone", "web")
	_ = s.SetClusterTag(ctx, ck, prod.ID)
	id := prod.ID
	_ = s.SetServiceTag(ctx, sk, &id)

	orphaned := func() (c, sv *time.Time) {
		t.Helper()
		err := s.Pool.QueryRow(ctx, `select (select orphaned_at from cluster_tags), (select orphaned_at from service_tags)`).Scan(&c, &sv)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkOrphanedTags(ctx, now); err != nil {
		t.Fatal(err)
	}
	c, sv := orphaned()
	if c == nil || sv == nil {
		t.Fatalf("orphaned_at not set: %v %v", c, sv)
	}
	// A second mark with a later time must not move the timestamp.
	_ = s.MarkOrphanedTags(ctx, now.Add(time.Hour))
	c2, _ := orphaned()
	if !c2.Equal(*c) {
		t.Errorf("orphaned_at moved from %v to %v", c, c2)
	}
	// Assignments survive and still resolve.
	if _, ok, _ := s.ClusterTag(ctx, ck); !ok {
		t.Error("orphaned cluster tag no longer resolves")
	}
	insertCluster(t, s, ck)
	insertService(t, s, sk)
	if err := s.MarkOrphanedTags(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, sv := orphaned(); c != nil || sv != nil {
		t.Errorf("orphaned_at not cleared: %v %v", c, sv)
	}
	// Retention delete.
	_, _ = s.Pool.Exec(ctx, `delete from clusters`)
	_, _ = s.Pool.Exec(ctx, `delete from services`)
	_ = s.MarkOrphanedTags(ctx, now)
	n, err := s.DeleteOrphanedTagsBefore(ctx, now.Add(time.Minute))
	if err != nil || n != 2 {
		t.Errorf("DeleteOrphanedTagsBefore = %d, %v; want 2", n, err)
	}
}
