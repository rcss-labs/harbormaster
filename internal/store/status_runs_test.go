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

func TestStatusRuns_RoundTrip(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	target := awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	api := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: target.AccountID, Region: target.Region, Cluster: "prod"}, Service: "api"}
	web := api
	web.Service = "web"
	since := time.Date(2026, 10, 31, 7, 0, 0, 0, time.UTC)

	runs := []store.StatusRun{
		{Key: api, Status: model.StatusDeploying, Since: since, Polls: 1},
		{Key: web, Status: model.StatusSteady, Since: since, Polls: 2},
	}
	if err := s.UpsertStatusRuns(ctx, runs); err != nil {
		t.Fatalf("UpsertStatusRuns: %v", err)
	}
	got, err := s.StatusRuns(ctx, target)
	if err != nil || len(got) != 2 {
		t.Fatalf("StatusRuns = %d runs, %v; want 2", len(got), err)
	}
	if got[api].Status != model.StatusDeploying || got[web].Polls != 2 {
		t.Errorf("StatusRuns = %+v; want api deploying, web polls 2", got)
	}

	runs[0].Polls = 3
	if err := s.UpsertStatusRuns(ctx, runs[:1]); err != nil {
		t.Fatalf("second UpsertStatusRuns: %v", err)
	}
	got, err = s.StatusRuns(ctx, target)
	if err != nil || got[api].Polls != 3 {
		t.Fatalf("after upsert api polls = %d, %v; want 3", got[api].Polls, err)
	}
}
