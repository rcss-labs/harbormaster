package store_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAppendOperations_WritesEveryRowLikeAppendOperation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	a, b := "payments-api", "notifications"
	ops := []store.Operation{
		{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services", Service: &a, Source: "harbormaster", Text: "one",
			Details: map[string]any{"k": "v"}},
		{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services", Service: &b, Source: "harbormaster", Text: "two"},
		{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services", Service: nil, Source: "harbormaster", Text: "three"},
	}
	if err := s.AppendOperations(ctx, ops); err != nil {
		t.Fatalf("append operations: %v", err)
	}
	if err := s.AppendOperations(ctx, nil); err != nil {
		t.Fatalf("append no operations: %v", err)
	}
	rows, err := s.RecentOperations(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	byText := map[string]store.OperationRow{}
	for _, row := range rows {
		byText[row.Text] = row
	}
	if len(rows) != 3 || byText["one"].Key.Service != a || byText["one"].Details["k"] != "v" ||
		byText["two"].Key.Service != b || byText["three"].Key.Service != "" {
		t.Fatalf("rows = %+v, want the three operations with their services and details", rows)
	}
}
