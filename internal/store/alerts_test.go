package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAlerts_ReplaceIsPerTarget(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	targetA := awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	targetB := awsx.Target{AccountID: "123456789012", Region: "us-east-1"}
	at := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	keyA := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: targetA.AccountID, Region: targetA.Region, Cluster: "prod"}, Service: "api"}
	keyB := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: targetB.AccountID, Region: targetB.Region, Cluster: "prod"}, Service: "api"}

	alertsA := []model.Alert{
		{Key: keyA, Kind: "unhealthy_target", Severity: model.AlertDanger, Title: "2 unhealthy", Text: "api-tg", At: at, Tab: "targets"},
		{Key: keyA, Kind: "scaling_activity", Severity: model.AlertWarning, Title: "scale out", Text: "cpu", At: at, Tab: "scaling"},
	}
	alertsB := []model.Alert{
		{Key: keyB, Kind: "account_unreachable", Severity: model.AlertInfo, Title: "unreachable", Text: "sts", At: at, Tab: "accounts"},
	}
	if err := s.ReplaceAlerts(ctx, targetA, alertsA); err != nil {
		t.Fatalf("ReplaceAlerts A: %v", err)
	}
	if err := s.ReplaceAlerts(ctx, targetB, alertsB); err != nil {
		t.Fatalf("ReplaceAlerts B: %v", err)
	}
	all, err := s.ListAlerts(ctx, store.AlertFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAlerts({}) = %d, %v; want 3", len(all), err)
	}

	if err := s.ReplaceAlerts(ctx, targetA, nil); err != nil {
		t.Fatalf("ReplaceAlerts A empty: %v", err)
	}
	forA, err := s.ListAlerts(ctx, store.AlertFilter{AccountID: targetA.AccountID, Region: targetA.Region})
	if err != nil || len(forA) != 0 {
		t.Fatalf("ListAlerts(A) = %d, %v; want 0", len(forA), err)
	}
	all, err = s.ListAlerts(ctx, store.AlertFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAlerts({}) after replacing A = %d, %v; want 1", len(all), err)
	}
}
