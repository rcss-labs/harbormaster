package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func awsxTarget() awsx.Target {
	return awsx.Target{AccountID: "111111111111", Region: "eu-west-1"}
}

func TestRunRetention_DeletesByAge(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	day := 24 * time.Hour
	k := serviceKey("c", "api")

	_, err := s.UpsertTasks(ctx, []model.Task{
		stoppedTask("c", "arn:t/old", now.Add(-8*day)),
		stoppedTask("c", "arn:t/recent", now.Add(-6*day)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertServiceEvents(ctx, []model.ServiceEvent{
		{Key: k, ID: "old", CreatedAt: now.Add(-31 * day)},
		{Key: k, ID: "new", CreatedAt: now.Add(-1 * day)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCountsHistory(ctx, now.Add(-8*day), []model.Service{{Key: k}}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCountsHistory(ctx, now.Add(-1*day), []model.Service{{Key: k}}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, age := range []time.Duration{91 * day, 1 * day} {
		exec(`insert into operation_log (ts, account_id, region, cluster_name, source, text) values ($1, 'a', 'r', 'c', 'ecs', 'x')`, now.Add(-age))
	}
	for _, age := range []time.Duration{366 * day, 1 * day} {
		exec(`insert into audit_log (ts, action, summary) values ($1, 'a', 's')`, now.Add(-age))
	}
	for _, age := range []time.Duration{91 * day, 1 * day} {
		exec(`insert into deployment_history (account_id, region, cluster_name, service_name, deployment_id, kind, started_at)
		      values ('a', 'r', 'c', 'api', $1, 'deploy', $2)`, age.String(), now.Add(-age))
	}
	exec(`insert into login_attempts (username, ip, ts, ok) values ('u', 'i', $1, false), ('u', 'i', $2, false)`,
		now.Add(-25*time.Hour), now.Add(-time.Hour))

	if err := s.PutMetricsCache(ctx, []store.CachedSeries{
		{Key: "cpu|stale", Target: awsxTarget(), FetchedAt: now.Add(-11 * time.Minute), Payload: []byte(`{}`)},
		{Key: "cpu|fresh", Target: awsxTarget(), FetchedAt: now.Add(-9 * time.Minute), Payload: []byte(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}

	user, err := s.CreateUser(ctx, "alice", "hash", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for id, expires := range map[string]time.Time{"expired": now.Add(-time.Hour), "live": now.Add(time.Hour)} {
		if err := s.CreateSession(ctx, store.Session{IDHash: id, UserID: user.ID, CSRFToken: "c",
			CreatedAt: now, LastSeenAt: now, ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}

	dev, err := s.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertCluster(t, s, clusterKey("alive"))
	for _, name := range []string{"alive", "gone"} {
		if err := s.SetClusterTag(ctx, clusterKey(name), dev.ID); err != nil {
			t.Fatal(err)
		}
	}
	insertService(t, s, serviceKey("alive", "web"))
	for _, name := range []string{"web", "removed"} {
		tag := authz.TagID(dev.ID)
		if err := s.SetServiceTag(ctx, serviceKey("alive", name), &tag); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkOrphanedTags(ctx, now.Add(-31*day)); err != nil {
		t.Fatal(err)
	}

	res, err := s.RunRetention(ctx, now, store.Retention{
		StoppedTasks: 7 * day, ServiceEvents: 30 * day, CountsHistory: 7 * day, OperationLog: 90 * day,
		DeploymentHistory: 90 * day, AuditLog: 365 * day, OrphanTags: 30 * day,
	})
	if err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	want := store.RetentionResult{
		"tasks": 1, "service_events": 1, "service_counts_history": 1, "operation_log": 1,
		"deployment_history": 1, "audit_log": 1, "sessions": 1, "login_attempts": 1,
		"cluster_tags": 1, "service_tags": 1, "metrics_cache": 1,
	}
	for key, n := range want {
		if res[key] != n {
			t.Errorf("result[%q] = %d, want %d (full: %v)", key, res[key], n, res)
		}
	}
	remaining := map[string]int{"tasks": 1, "service_events": 1, "service_counts_history": 1, "operation_log": 1,
		"deployment_history": 1, "audit_log": 1, "sessions": 1, "login_attempts": 1, "cluster_tags": 1, "service_tags": 1, "metrics_cache": 1}
	for table, n := range remaining {
		var got int
		if err := s.Pool.QueryRow(ctx, `select count(*) from `+table).Scan(&got); err != nil || got != n {
			t.Errorf("%s rows left = %d, %v; want %d", table, got, err, n)
		}
	}
}

func TestRunRetention_ZeroDurationSkipsTable(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.UpsertTasks(ctx, []model.Task{stoppedTask("c", "arn:t/old", time.Now().Add(-1000*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRetention(ctx, time.Now(), store.Retention{}); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from tasks`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tasks = %d, %v; want 1", n, err)
	}
}
