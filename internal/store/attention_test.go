package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var attentionKey = model.ServiceKey{
	ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"},
	Service:    "web-frontend",
}

func insertOperationAt(t *testing.T, s *store.Store, source, text string, at time.Time) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(),
		`insert into operation_log (account_id, region, cluster_name, service_name, source, text, ts)
		 values ($1, $2, $3, $4, $5, $6, $7)`,
		attentionKey.AccountID, attentionKey.Region, attentionKey.Cluster, attentionKey.Service, source, text, at)
	if err != nil {
		t.Fatalf("insert operation: %v", err)
	}
}

func TestLastChangeBefore_PrefersNewestOfOperationOrDeployment(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	alertAt := time.Now().UTC().Truncate(time.Second)

	insertOperationAt(t, s, "harbormaster", "Scaled web-frontend to 4.", alertAt.Add(-2*time.Hour))
	if _, err := s.InsertDeploymentIfNew(ctx, store.DeploymentRecord{
		Key: attentionKey, DeploymentID: "d1", Revision: 7, Kind: "deploy", StartedAt: alertAt.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LastChangeBefore(ctx, attentionKey, alertAt, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Source != "deployment" || got.Text != "Deployed :7" || !got.At.Equal(alertAt.Add(-30*time.Minute)) {
		t.Fatalf("change = %+v; want the newer deployment 'Deployed :7'", got)
	}

	insertOperationAt(t, s, "harbormaster", "Restarted web-frontend.", alertAt.Add(-5*time.Minute))
	got, err = s.LastChangeBefore(ctx, attentionKey, alertAt, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Source != "harbormaster" || got.Text != "Restarted web-frontend." {
		t.Fatalf("change = %+v; want the newer operation", got)
	}
}

func TestLastChangeBefore_IgnoresECSEventsAndOutsideWindow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	alertAt := time.Now().UTC().Truncate(time.Second)

	insertOperationAt(t, s, "ecs", "service reached a steady state", alertAt.Add(-time.Minute))
	insertOperationAt(t, s, "harbormaster", "Too old.", alertAt.Add(-25*time.Hour))
	insertOperationAt(t, s, "harbormaster", "After the alert.", alertAt.Add(time.Minute))

	got, err := s.LastChangeBefore(ctx, attentionKey, alertAt, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("change = %+v; want nil: only ECS, too-old and later rows exist", got)
	}
}

func TestLastChangesBefore_ResolvesEachQueryIndependently(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	alertAt := time.Now().UTC().Truncate(time.Second)
	insertOperationAt(t, s, "harbormaster", "Older.", alertAt.Add(-3*time.Hour))
	insertOperationAt(t, s, "harbormaster", "Newer.", alertAt.Add(-time.Hour))

	early := store.ChangeQuery{Key: attentionKey, Before: alertAt.Add(-2 * time.Hour)}
	late := store.ChangeQuery{Key: attentionKey, Before: alertAt}
	got, err := s.LastChangesBefore(ctx, []store.ChangeQuery{early, late}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got[early] == nil || got[early].Text != "Older." || got[late] == nil || got[late].Text != "Newer." {
		t.Fatalf("changes = %+v", got)
	}
}
