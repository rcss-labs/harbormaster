package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func opsWindow() (from, to time.Time) {
	now := time.Now().UTC()
	return now.Add(-2 * time.Hour), now.Add(2 * time.Hour)
}

func mustUser(t *testing.T, s *store.Store, name string) int64 {
	t.Helper()
	u, err := s.CreateUser(context.Background(), name, "hash", false, false)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func appendOp(t *testing.T, s *store.Store, cluster string, service *string, actor *int64, text string, details map[string]any) {
	t.Helper()
	err := s.AppendOperation(context.Background(), store.Operation{
		AccountID: "111111111111", Region: "eu-west-1", Cluster: cluster, Service: service,
		ActorUserID: actor, Source: "harbormaster", Text: text, Details: details,
	})
	if err != nil {
		t.Fatalf("append %q: %v", text, err)
	}
}

// kindTotals sums AnalyticsOperations over all buckets.
func kindTotals(t *testing.T, s *store.Store, vis store.Visibility) map[string]int {
	t.Helper()
	from, to := opsWindow()
	buckets, err := s.AnalyticsOperations(context.Background(), store.TargetFilter{}, vis, from, to, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]int{}
	for _, b := range buckets {
		totals[b.Kind] += b.Count
	}
	return totals
}

func TestOpKindSQL_ClassifiesLegacyRows(t *testing.T) {
	s := storetest.Open(t)
	user := mustUser(t, s, "pavel")
	u := &user
	web := strp("web")
	// The historical shapes: no details.op, kind derived from text and detail keys.
	appendOp(t, s, "c1", web, u, "Deployed web:5.", map[string]any{"deployment_id": "d1", "from_revision": 4, "to_revision": 5})
	appendOp(t, s, "c1", web, u, "Rolled back web:4.", map[string]any{"deployment_id": "d2"})
	appendOp(t, s, "c1", web, u, "Restarted service (force new deployment) on revision 4.", map[string]any{"deployment_id": "d3"})
	appendOp(t, s, "c1", web, u, "Registered task definition web:6 (image x, 256 CPU / 512 MiB).", map[string]any{"revision": 6, "task_definition_arn": "arn"})
	appendOp(t, s, "c1", web, u, "Changed desired count from 3 to 5.", nil)
	appendOp(t, s, "c1", web, u, "Stopped task abcdef123456.", nil)
	appendOp(t, s, "c1", web, u, "Opened terminal session on task abc (web, /bin/sh).", map[string]any{"session_id": "s1"})
	// Excluded: terminal close, outcome rows (no actor), admin tag changes.
	appendOp(t, s, "c1", web, u, "Closed terminal session on task abc (web) after 3s.", map[string]any{"session_id": "s1"})
	appendOp(t, s, "c1", web, nil, "Deployment of web:5 completed.", map[string]any{"deployment_id": "d1", "outcome": "completed"})
	appendOp(t, s, "c1", nil, u, "Tagged cluster c1 as Prod.", nil)

	got := kindTotals(t, s, store.Visibility{Admin: true})
	want := map[string]int{"deploy": 1, "rollback": 1, "restart": 1, "task_definition": 1, "scale": 1, "stop_task": 1, "terminal": 1}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for kind, n := range want {
		if got[kind] != n {
			t.Errorf("kind %s = %d, want %d (all: %v)", kind, got[kind], n, got)
		}
	}
}

func TestOpKindSQL_PrefersDetailsOp(t *testing.T) {
	s := storetest.Open(t)
	user := mustUser(t, s, "pavel")
	// The text says "Deployed" but details.op wins; "outcome" and "terminal_close" are excluded.
	appendOp(t, s, "c1", strp("web"), &user, "Deployed web:5.", map[string]any{"op": "restart", "deployment_id": "d1"})
	appendOp(t, s, "c1", strp("web"), &user, "whatever", map[string]any{"op": "outcome"})
	appendOp(t, s, "c1", strp("web"), &user, "whatever", map[string]any{"op": "terminal_close", "session_id": "s"})

	got := kindTotals(t, s, store.Visibility{Admin: true})
	if len(got) != 1 || got["restart"] != 1 {
		t.Fatalf("kinds = %v, want only restart=1", got)
	}
}

func TestAnalyticsOperations_VisibilityAndWindow(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	user := mustUser(t, s, "pavel")
	prod, _ := s.CreateTag(ctx, "Prod", "red")
	if err := s.SetClusterTag(ctx, clusterKey("prod-cluster"), prod.ID); err != nil {
		t.Fatal(err)
	}
	appendOp(t, s, "prod-cluster", strp("web"), &user, "x", map[string]any{"op": "deploy"})
	appendOp(t, s, "dev-cluster", strp("web"), &user, "x", map[string]any{"op": "deploy"})
	// A standalone-task stop on the prod cluster has no service and takes the cluster tag.
	appendOp(t, s, "prod-cluster", nil, &user, "Stopped task t.", map[string]any{"op": "stop_task"})

	got := kindTotals(t, s, store.Visibility{TagIDs: []int64{int64(prod.ID)}})
	if got["deploy"] != 1 || got["stop_task"] != 1 {
		t.Fatalf("Prod viewer kinds = %v, want deploy=1 stop_task=1", got)
	}
	if all := kindTotals(t, s, store.Visibility{Admin: true}); all["deploy"] != 2 {
		t.Fatalf("admin kinds = %v, want deploy=2", all)
	}

	past := time.Now().UTC().Add(-48 * time.Hour)
	none, err := s.AnalyticsOperations(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, past.Add(-time.Hour), past, time.Hour)
	if err != nil || len(none) != 0 {
		t.Fatalf("window in the past = %v, %v; want nothing", none, err)
	}
}

func TestAnalyticsOperators_CountsPerKindAndLast(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	pavel, omer := mustUser(t, s, "pavel"), mustUser(t, s, "omer")
	appendOp(t, s, "c1", strp("web"), &pavel, "x", map[string]any{"op": "deploy"})
	appendOp(t, s, "c1", strp("web"), &pavel, "x", map[string]any{"op": "deploy"})
	appendOp(t, s, "c1", strp("web"), &pavel, "x", map[string]any{"op": "restart"})
	appendOp(t, s, "c1", strp("web"), &omer, "x", map[string]any{"op": "scale"})
	appendOp(t, s, "c1", strp("web"), &omer, "x", map[string]any{"op": "outcome"})

	from, to := opsWindow()
	got, err := s.AnalyticsOperators(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Username != "pavel" {
		t.Fatalf("operators = %+v, want pavel first then omer", got)
	}
	if got[0].Counts["deploy"] != 2 || got[0].Counts["restart"] != 1 || got[1].Counts["scale"] != 1 || len(got[1].Counts) != 1 {
		t.Fatalf("counts = %v / %v", got[0].Counts, got[1].Counts)
	}
	if got[0].LastAt.Before(from) || got[0].LastAt.After(to) {
		t.Fatalf("last_at = %v outside window", got[0].LastAt)
	}
}

func TestAnalyticsRestartedServices_DistinctPeople(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	pavel, omer := mustUser(t, s, "pavel"), mustUser(t, s, "omer")
	for _, actor := range []int64{pavel, pavel, omer} {
		a := actor
		appendOp(t, s, "c1", strp("flaky"), &a, "Restarted", map[string]any{"op": "restart"})
	}
	appendOp(t, s, "c1", strp("steady"), &pavel, "Restarted", map[string]any{"op": "restart"})
	appendOp(t, s, "c1", strp("steady"), &pavel, "Deployed", map[string]any{"op": "deploy"})

	from, to := opsWindow()
	got, err := s.AnalyticsRestartedServices(ctx, store.TargetFilter{}, store.Visibility{Admin: true}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("services = %+v, want 2", got)
	}
	if got[0].Key.Service != "flaky" || got[0].Restarts != 3 || got[0].People != 2 {
		t.Fatalf("first = %+v, want flaky 3 restarts by 2 people", got[0])
	}
	if got[1].Key.Service != "steady" || got[1].Restarts != 1 || got[1].People != 1 {
		t.Fatalf("second = %+v", got[1])
	}
	if got[0].TagID == 0 || !got[0].TagInherited {
		t.Fatalf("tag = %d inherited %v", got[0].TagID, got[0].TagInherited)
	}
}
