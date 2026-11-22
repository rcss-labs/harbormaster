package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestKnownTaskARNsForService(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	cluster := clusterKey("core")
	tasks := []model.Task{
		{Cluster: cluster, ARN: "arn:t/api-1", ServiceName: "api", LastStatus: "RUNNING"},
		{Cluster: cluster, ARN: "arn:t/api-2", ServiceName: "api", LastStatus: "PENDING"},
		{Cluster: cluster, ARN: "arn:t/api-old", ServiceName: "api", LastStatus: "STOPPED"},
		{Cluster: cluster, ARN: "arn:t/web-1", ServiceName: "web", LastStatus: "RUNNING"},
		{Cluster: cluster, ARN: "arn:t/standalone", LastStatus: "RUNNING"},
	}
	if _, err := s.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	api := serviceKey("core", "api")

	live, err := s.KnownTaskARNsForService(ctx, api, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"arn:t/api-1", "arn:t/api-2"}; !slices.Equal(live, want) {
		t.Fatalf("non-stopped = %v, want %v", live, want)
	}
	stopped, err := s.KnownTaskARNsForService(ctx, api, "STOPPED")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"arn:t/api-old"}; !slices.Equal(stopped, want) {
		t.Fatalf("stopped = %v, want %v", stopped, want)
	}
	other, err := s.KnownTaskARNsForService(ctx, serviceKey("other", "api"), "")
	if err != nil || len(other) != 0 {
		t.Fatalf("same service name in another cluster = %v, %v; want none", other, err)
	}
}
