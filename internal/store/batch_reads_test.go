package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestRecentServiceEventsPerService_LimitPerService(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	var events []model.ServiceEvent
	for _, svc := range []string{"a", "b"} {
		for i := 0; i < 5; i++ {
			events = append(events, model.ServiceEvent{Key: serviceKey("c", svc), ID: fmt.Sprintf("%s-%d", svc, i),
				CreatedAt: base.Add(time.Duration(i) * time.Minute), Message: "m"})
		}
	}
	events = append(events, model.ServiceEvent{Key: store2Other(), ID: "x", CreatedAt: base, Message: "other target"})
	if _, err := s.InsertServiceEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentServiceEventsPerService(ctx, store.TargetFilter{AccountID: "111111111111", Region: "eu-west-1"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("services with events = %d, want 2 (the other region excluded)", len(got))
	}
	for _, svc := range []string{"a", "b"} {
		list := got[serviceKey("c", svc)]
		if len(list) != 3 || list[0].ID != svc+"-4" || list[2].ID != svc+"-2" {
			t.Errorf("service %s events = %v, want its newest 3 first (4, 3, 2)", svc, ids(list))
		}
	}
}

func ids(events []model.ServiceEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func store2Other() model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: "c"}, Service: "a"}
}

func TestTargetHealthByService_ScopedToTargetAndCluster(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	polled := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	rows := []model.TargetHealth{
		{Key: serviceKey("c1", "a"), TargetGroupARN: "tg-2", LoadBalancerARN: "lb", Healthy: 1, PolledAt: polled},
		{Key: serviceKey("c1", "a"), TargetGroupARN: "tg-1", LoadBalancerARN: "lb", Healthy: 1, PolledAt: polled},
		{Key: serviceKey("c1", "b"), TargetGroupARN: "tg-3", LoadBalancerARN: "lb", Unhealthy: 2, PolledAt: polled},
		{Key: serviceKey("c2", "z"), TargetGroupARN: "tg-4", LoadBalancerARN: "lb", Healthy: 1, PolledAt: polled},
	}
	if err := s.UpsertTargetHealth(ctx, rows); err != nil {
		t.Fatal(err)
	}
	filter := store.TargetFilter{AccountID: "111111111111", Region: "eu-west-1"}

	all, err := s.TargetHealthByService(ctx, filter, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("whole target = %d services, %v; want 3", len(all), err)
	}
	if a := all[serviceKey("c1", "a")]; len(a) != 2 || a[0].TargetGroupARN != "tg-1" {
		t.Errorf("service a rows = %+v, want 2 ordered by target group ARN", a)
	}
	one, err := s.TargetHealthByService(ctx, filter, "c1")
	if err != nil || len(one) != 2 || len(one[serviceKey("c2", "z")]) != 0 {
		t.Fatalf("cluster c1 = %v, %v; want services a and b only", one, err)
	}
}

func TestListDeploymentsForCluster_GroupsByServiceNewestFirst(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	for i, r := range []store.DeploymentRecord{
		{Key: serviceKey("c1", "a"), DeploymentID: "d1", Kind: "deploy", StartedAt: day},
		{Key: serviceKey("c1", "a"), DeploymentID: "d2", Kind: "deploy", StartedAt: day.Add(time.Hour)},
		{Key: serviceKey("c1", "b"), DeploymentID: "d3", Kind: "restart", StartedAt: day},
		{Key: serviceKey("c1", "b"), DeploymentID: "d-old", Kind: "deploy", StartedAt: day.Add(-48 * time.Hour)},
		{Key: serviceKey("c2", "z"), DeploymentID: "d4", Kind: "deploy", StartedAt: day},
	} {
		if _, err := s.InsertDeploymentIfNew(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := s.ListDeploymentsForCluster(ctx, clusterKey("c1"), day.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[serviceKey("c2", "z")]) != 0 {
		t.Fatalf("services = %d, want a and b of c1 only", len(got))
	}
	if a := got[serviceKey("c1", "a")]; len(a) != 2 || a[0].DeploymentID != "d2" {
		t.Errorf("service a = %+v, want newest first", a)
	}
	if b := got[serviceKey("c1", "b")]; len(b) != 1 || b[0].DeploymentID != "d3" {
		t.Errorf("service b = %+v, want the deployment since the cutoff only", b)
	}
}

func TestReplaceAlertsAndStatusRuns_WritesBoth(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	target := awsxTarget()
	key := serviceKey("c", "a")
	alerts := []model.Alert{{Key: key, Kind: "service_failing", Severity: model.AlertDanger, Title: "t", Text: "x", At: time.Now(), Tab: "tasks"}}
	runs := []store.StatusRun{{Key: key, Status: model.StatusFailing, Since: time.Now(), Polls: 4}}

	if err := s.ReplaceAlertsAndStatusRuns(ctx, target, alerts, runs); err != nil {
		t.Fatal(err)
	}
	gotAlerts, err := s.ListAlerts(ctx, store.AlertFilter{AccountID: target.AccountID, Region: target.Region})
	if err != nil || len(gotAlerts) != 1 {
		t.Fatalf("alerts = %d, %v; want 1", len(gotAlerts), err)
	}
	gotRuns, err := s.StatusRuns(ctx, target)
	if err != nil || gotRuns[key].Polls != 4 {
		t.Fatalf("runs = %+v, %v; want polls 4", gotRuns, err)
	}

	// An invalid run (NULL status is impossible, so use a status that violates nothing) must not
	// leave alerts half-replaced: a failing write rolls the whole cycle back.
	bad := []store.StatusRun{{Key: key, Status: model.StatusSteady, Since: time.Now(), Polls: 1}}
	badKeyAlerts := []model.Alert{{Key: key, Kind: "x\x00bad", Severity: model.AlertDanger, Title: "t", Text: "x", At: time.Now(), Tab: "tasks"}}
	if err := s.ReplaceAlertsAndStatusRuns(ctx, target, badKeyAlerts, bad); err == nil {
		t.Fatal("expected an error for an alert with a NUL byte")
	}
	gotAlerts, _ = s.ListAlerts(ctx, store.AlertFilter{AccountID: target.AccountID, Region: target.Region})
	gotRuns, _ = s.StatusRuns(ctx, target)
	if len(gotAlerts) != 1 || gotAlerts[0].Kind != "service_failing" || gotRuns[key].Polls != 4 {
		t.Errorf("after a failed write alerts = %+v runs = %+v; want the previous state untouched", gotAlerts, gotRuns)
	}
}
