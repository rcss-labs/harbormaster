package store_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// seedObserveRows writes one row of every plan 03 per-service table for the service.
func seedObserveRows(t *testing.T, s *store.Store, key model.ServiceKey) {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`insert into target_health (account_id, region, cluster_name, service_name, target_group_arn, load_balancer_arn,
		   healthy, unhealthy, targets, raw, polled_at) values ($1, $2, $3, $4, 'arn:tg', 'arn:lb', 0, 1, '[]', '{}', now())`,
		`insert into scaling (account_id, region, cluster_name, service_name, has_target, policies, scheduled_actions, activities, polled_at)
		   values ($1, $2, $3, $4, false, '[]', '[]', '[]', now())`,
		`insert into service_status_runs (account_id, region, cluster_name, service_name, status, since, polls)
		   values ($1, $2, $3, $4, 'failing', now(), 1)`,
		`insert into alerts (account_id, region, cluster_name, service_name, kind, severity, title, text, at, tab, polled_at)
		   values ($1, $2, $3, $4, 'service_failing', 'danger', 't', 'x', now(), 'tasks', now())`,
	} {
		if _, err := s.Pool.Exec(ctx, sql, key.AccountID, key.Region, key.Cluster, key.Service); err != nil {
			t.Fatalf("seed observe row: %v", err)
		}
	}
}

func observeRowCount(t *testing.T, s *store.Store, where string, args ...any) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"target_health", "scaling", "service_status_runs", "alerts"} {
		var n int
		if err := s.Pool.QueryRow(context.Background(), `select count(*) from `+table+` where `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func TestDeleteAccount_RemovesObserveRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	created, err := s.CreateAccount(ctx, newTestAccount("482109375512"))
	if err != nil {
		t.Fatal(err)
	}
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: created.AccountID, Region: "us-east-1", Cluster: "core"}, Service: "api"}
	seedObserveRows(t, s, key)
	if _, err := s.Pool.Exec(ctx,
		`insert into metrics_cache (key, account_id, region, fetched_at, payload) values ('k', $1, 'us-east-1', now(), '{}')`,
		created.AccountID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAccount(ctx, created.ID); err != nil {
		t.Fatal(err)
	}

	for table, n := range observeRowCount(t, s, `account_id = $1`, created.AccountID) {
		if n != 0 {
			t.Errorf("%s rows left after account delete = %d, want 0", table, n)
		}
	}
	var cached int
	if err := s.Pool.QueryRow(ctx, `select count(*) from metrics_cache where account_id = $1`, created.AccountID).Scan(&cached); err != nil || cached != 0 {
		t.Errorf("metrics_cache rows left = %d, %v; want 0", cached, err)
	}
}

func TestDeleteClustersNotIn_RemovesObserveRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.UpsertClusters(ctx, []model.Cluster{{Key: clusterKey("keep")}, {Key: clusterKey("drop")}}); err != nil {
		t.Fatal(err)
	}
	seedObserveRows(t, s, serviceKey("keep", "a"))
	seedObserveRows(t, s, serviceKey("drop", "b"))

	if _, err := s.DeleteClustersNotIn(ctx, awsxTarget(), []model.ClusterKey{clusterKey("keep")}); err != nil {
		t.Fatal(err)
	}

	for table, n := range observeRowCount(t, s, `cluster_name = 'drop'`) {
		if n != 0 {
			t.Errorf("%s rows of the removed cluster = %d, want 0", table, n)
		}
	}
	for table, n := range observeRowCount(t, s, `cluster_name = 'keep'`) {
		if n != 1 {
			t.Errorf("%s rows of the kept cluster = %d, want 1", table, n)
		}
	}
}

func TestDeleteServicesNotIn_RemovesObserveRows(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedObserveRows(t, s, serviceKey("c", "keep"))
	seedObserveRows(t, s, serviceKey("c", "drop"))

	if _, err := s.DeleteServicesNotIn(ctx, clusterKey("c"), []string{"keep"}); err != nil {
		t.Fatal(err)
	}

	for table, n := range observeRowCount(t, s, `service_name = 'drop'`) {
		if n != 0 {
			t.Errorf("%s rows of the removed service = %d, want 0", table, n)
		}
	}
	for table, n := range observeRowCount(t, s, `service_name = 'keep'`) {
		if n != 1 {
			t.Errorf("%s rows of the kept service = %d, want 1", table, n)
		}
	}
}
