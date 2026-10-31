package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestAppendOperation_StandaloneHasNullService(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	err := s.AppendOperation(ctx, store.Operation{
		AccountID: "482109375512",
		Region:    "us-east-1",
		Cluster:   "core-services",
		Service:   nil,
		Source:    "harbormaster",
		Text:      "Stopped standalone task",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	rows, err := s.RecentOperations(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(rows) != 1 || rows[0].Text != "Stopped standalone task" {
		t.Fatalf("recent rows = %+v, want the one standalone operation", rows)
	}
	if rows[0].Key.Service != "" {
		t.Fatalf("standalone row Key.Service = %q, want empty", rows[0].Key.Service)
	}
}

func TestListOperations_MergesECSEventsAndCursor(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	key := model.ServiceKey{
		ClusterKey: model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services"},
		Service:    "notifications",
	}
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	service := key.Service

	for _, op := range []store.Operation{
		{Service: &service, Source: "harbormaster", Text: "op1"},
		{Service: &service, Source: "harbormaster", Text: "op2"},
		{Service: &service, Source: "harbormaster", Text: "op3"},
	} {
		op.AccountID, op.Region, op.Cluster = key.AccountID, key.Region, key.Cluster
		if err := s.AppendOperation(ctx, op); err != nil {
			t.Fatalf("append %q: %v", op.Text, err)
		}
	}
	// Give the operations explicit timestamps 1, 2 and 3 minutes after base, in creation order.
	if _, err := s.Pool.Exec(ctx,
		`update operation_log set ts = $1::timestamptz + (id - (select min(id) from operation_log) + 1) * interval '1 minute'`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx,
		`insert into service_events (account_id, region, cluster_name, service_name, event_id, created_at, message, content_hash, raw, polled_at)
		 values ($1, $2, $3, $4, 'ev1', $5, 'ev1', 'h', '{}', now()),
		        ($1, $2, $3, $4, 'ev2', $6, 'ev2', 'h', '{}', now())`,
		key.AccountID, key.Region, key.Cluster, key.Service,
		base.Add(90*time.Second), base.Add(150*time.Second)); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListOperations(ctx, key, true, nil, 0)
	if err != nil {
		t.Fatalf("list with ECS: %v", err)
	}
	if got := texts(all); got != "op3,ev2,op2,ev1,op1" {
		t.Fatalf("with ECS = %s, want op3,ev2,op2,ev1,op1", got)
	}
	if all[1].Source != "ecs" || all[1].Actor != "ECS" {
		t.Fatalf("event row = source %q actor %q, want ecs/ECS", all[1].Source, all[1].Actor)
	}

	withoutECS, err := s.ListOperations(ctx, key, false, nil, 0)
	if err != nil {
		t.Fatalf("list without ECS: %v", err)
	}
	if got := texts(withoutECS); got != "op3,op2,op1" {
		t.Fatalf("without ECS = %s, want op3,op2,op1", got)
	}

	before := base.Add(2 * time.Minute)
	page, err := s.ListOperations(ctx, key, true, &before, 0)
	if err != nil {
		t.Fatalf("list with cursor: %v", err)
	}
	if got := texts(page); got != "ev1,op1" {
		t.Fatalf("cursor page = %s, want ev1,op1", got)
	}
}

func texts(rows []store.OperationRow) string {
	out := ""
	for i, r := range rows {
		if i > 0 {
			out += ","
		}
		out += r.Text
	}
	return out
}

func TestInTx_CommitsOnNilAndRollsBackOnError(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	op := func(text string) store.Operation {
		return store.Operation{AccountID: "482109375512", Region: "us-east-1", Cluster: "core-services", Source: "harbormaster", Text: text}
	}
	if err := s.InTx(ctx, func(tx store.Tx) error { return tx.AppendOperation(ctx, op("kept")) }); err != nil {
		t.Fatal(err)
	}
	failure := context.DeadlineExceeded
	if err := s.InTx(ctx, func(tx store.Tx) error {
		if err := tx.AppendOperation(ctx, op("rolled back")); err != nil {
			return err
		}
		return failure
	}); err != failure {
		t.Fatalf("InTx() = %v, want the callback error", err)
	}
	rows, err := s.RecentOperations(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, 10)
	if err != nil || len(rows) != 1 || rows[0].Text != "kept" {
		t.Fatalf("operations = %v, %v; want only the committed one", rows, err)
	}
}
