package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestMetricsCache_PutGetPrune(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	target := awsx.Target{AccountID: "482109375512", Region: "us-east-1"}
	fetched := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	rows := []store.CachedSeries{
		{Key: "cpu|svc-a", Target: target, FetchedAt: fetched, Payload: json.RawMessage(`{"points":[1]}`)},
		{Key: "mem|svc-a", Target: target, FetchedAt: fetched, Payload: json.RawMessage(`{"points":[2]}`)},
	}
	if err := s.PutMetricsCache(ctx, rows); err != nil {
		t.Fatalf("PutMetricsCache: %v", err)
	}

	got, err := s.GetMetricsCache(ctx, []string{"cpu|svc-a", "mem|svc-a", "missing"})
	if err != nil {
		t.Fatalf("GetMetricsCache: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetMetricsCache returned %d rows, want 2", len(got))
	}
	if got["cpu|svc-a"].Target != target || !got["cpu|svc-a"].FetchedAt.Equal(fetched) {
		t.Errorf("round trip lost target or fetched_at: %+v", got["cpu|svc-a"])
	}

	pruned, err := s.PruneMetricsCache(ctx, 0)
	if err != nil || pruned != 2 {
		t.Fatalf("PruneMetricsCache(0) = %d, %v; want 2, nil", pruned, err)
	}
	got, err = s.GetMetricsCache(ctx, []string{"cpu|svc-a", "mem|svc-a"})
	if err != nil || len(got) != 0 {
		t.Fatalf("GetMetricsCache after prune = %d rows, %v; want 0", len(got), err)
	}
}
