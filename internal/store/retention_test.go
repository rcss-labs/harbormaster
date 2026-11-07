package store_test

import (
	"context"
	"errors"
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
	for _, age := range []time.Duration{366 * day, 1 * day} {
		exec(`insert into service_availability_daily (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops)
		      values ('a', 'r', 'c', 'api', $1::date, 1, 1, 0, 0)`, now.Add(-age))
		exec(`insert into cluster_capacity_daily (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum)
		      values ('a', 'r', 'c', $1::date, 1, 1, 1, 1, 1, 1, 1, 0)`, now.Add(-age))
	}
	for _, age := range []time.Duration{91 * day, 1 * day} {
		exec(`insert into service_util_daily (account_id, region, cluster_name, service_name, day) values ('a', 'r', 'c', 'api', $1::date)`, now.Add(-age))
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
		AvailabilityDaily: 365 * day, CapacityDaily: 365 * day, UtilDaily: 90 * day,
	})
	if err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	want := store.RetentionResult{
		"tasks": 1, "service_events": 1, "service_counts_history": 1, "operation_log": 1,
		"deployment_history": 1, "audit_log": 1, "sessions": 1, "login_attempts": 1,
		"cluster_tags": 1, "service_tags": 1, "metrics_cache": 1,
		"service_availability_daily": 1, "cluster_capacity_daily": 1, "service_util_daily": 1,
	}
	for key, n := range want {
		if res[key] != n {
			t.Errorf("result[%q] = %d, want %d (full: %v)", key, res[key], n, res)
		}
	}
	remaining := map[string]int{"tasks": 1, "service_events": 1, "service_counts_history": 1, "operation_log": 1,
		"deployment_history": 1, "audit_log": 1, "sessions": 1, "login_attempts": 1, "cluster_tags": 1, "service_tags": 1, "metrics_cache": 1,
		"service_availability_daily": 1, "cluster_capacity_daily": 1, "service_util_daily": 1}
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
	for _, sql := range []string{
		`insert into service_availability_daily (account_id, region, cluster_name, service_name, day, samples, samples_ok, minutes_below, unexpected_stops) values ('a', 'r', 'c', 'api', '2000-01-01', 1, 1, 0, 0)`,
		`insert into cluster_capacity_daily (account_id, region, cluster_name, day, samples, vcpu_sum, vcpu_max, mem_gib_sum, mem_gib_max, tasks_sum, tasks_max, spot_sum) values ('a', 'r', 'c', '2000-01-01', 1, 1, 1, 1, 1, 1, 1, 0)`,
		`insert into service_util_daily (account_id, region, cluster_name, service_name, day) values ('a', 'r', 'c', 'api', '2000-01-01')`,
	} {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if _, err := s.RunRetention(ctx, time.Now(), store.Retention{}); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	for _, table := range []string{"tasks", "service_availability_daily", "cluster_capacity_daily", "service_util_daily"} {
		var n int
		if err := s.Pool.QueryRow(ctx, `select count(*) from `+table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s = %d, %v; want 1", table, n, err)
		}
	}
}

func TestRunRetention_SAMLTables(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	for _, r := range []store.SAMLRequest{
		{ID: "_old", Purpose: "login", ReturnTo: "/", BindingHash: "h", ExpiresAt: now.Add(-time.Minute)},
		{ID: "_live", Purpose: "login", ReturnTo: "/", BindingHash: "h", ExpiresAt: now.Add(time.Hour)},
	} {
		if err := s.CreateSAMLRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RememberAssertionID(ctx, "_a_old", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberAssertionID(ctx, "_a_live", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"now() - interval '8 days'", "now() - interval '1 day'", "null"} {
		if _, err := s.Pool.Exec(ctx, `insert into saml_sp_keys (cert_pem, key_pem_enc, retired_at) values ('c', '\x01', `+retired+`)`); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.RunRetention(ctx, now, store.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if res["saml_requests"] != 1 || res["saml_assertion_ids"] != 1 || res["saml_sp_keys"] != 1 {
		t.Fatalf("result = %v, want one row deleted from each saml table", res)
	}
	if _, err := s.ConsumeSAMLRequest(ctx, "_live", now); err != nil {
		t.Fatalf("live request was deleted: %v", err)
	}
	keys, _ := s.ListSPKeys(ctx)
	if len(keys) != 2 {
		t.Fatalf("keys left = %d, want 2", len(keys))
	}
}

func TestRunRetention_DeletesExpiredScanImagesWithFindingsAndRequests(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	day := 24 * time.Hour
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	seedScanImages(t, s, now.Add(-100*day), "sha256:expired", "sha256:queued-old")
	seedScanImages(t, s, now.Add(-1*day), "sha256:fresh")
	exec(`update scan_images set status = 'scanned' where digest in ('sha256:expired', 'sha256:fresh')`)
	for _, d := range []string{"sha256:expired", "sha256:fresh"} {
		exec(`insert into scan_findings (digest, vuln_id, package_name, package_version, severity, first_seen_at, last_seen_at, run_id)
			values ($1, 'CVE-0000-0001', 'p', '1', 'high', now(), now(), 1)`, d)
		exec(`insert into scan_risk_acceptances (digest, vuln_id, package_name, package_version, reason, expires_at)
			values ($1, 'CVE-0000-0001', 'p', '1', 'r', now())`, d)
		exec(`insert into scan_requests (digest, account_id, region, cluster_name, service_name, container, image, source, requested_at)
			values ($1, '111111111111', 'eu-west-1', 'c', 's', 'app', 'img', 'manual', now())`, d)
	}

	res, err := s.RunRetention(ctx, now, store.Retention{ScanImages: 90 * day})
	if err != nil {
		t.Fatal(err)
	}
	if res["scan_images_expired"] != 1 {
		t.Fatalf("scan_images_expired = %d, want 1 (%v)", res["scan_images_expired"], res)
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := s.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := s.GetScanImage(ctx, "sha256:expired"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired image err = %v, want ErrNotFound", err)
	}
	for _, table := range []string{"scan_findings", "scan_risk_acceptances", "scan_requests"} {
		if n := count(`select count(*) from ` + table + ` where digest = 'sha256:expired'`); n != 0 {
			t.Fatalf("%s rows of expired image = %d, want 0", table, n)
		}
		if n := count(`select count(*) from ` + table + ` where digest = 'sha256:fresh'`); n != 1 {
			t.Fatalf("%s rows of fresh image = %d, want 1", table, n)
		}
	}
	// A queued row stays however old its request is: it has not been scanned yet.
	if queued := mustGetScanImage(t, s, "sha256:queued-old"); queued.Status != "queued" {
		t.Fatalf("queued image status = %q, want untouched", queued.Status)
	}
	mustGetScanImage(t, s, "sha256:fresh")
}

func TestRunRetention_DeletesFinishedRunsWithTheirImages(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	day := 24 * time.Hour
	oldRun := newScanRun(t, s, "scan", "sha256:a")
	recentRun := newScanRun(t, s, "scan", "sha256:b")
	activeRun := newScanRun(t, s, "scan", "sha256:c")
	for _, id := range []int64{oldRun, recentRun} {
		if _, err := s.FinishScanRun(ctx, id, "succeeded", store.ScanRunPatch{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `update scan_runs set finished_at = $2 where id = $1`, oldRun, now.Add(-40*day)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `update scan_runs set started_at = $2 where id = $1`, activeRun, now.Add(-40*day)); err != nil {
		t.Fatal(err)
	}

	res, err := s.RunRetention(ctx, now, store.Retention{ScanRuns: 30 * day})
	if err != nil {
		t.Fatal(err)
	}
	if res["scan_runs"] != 1 {
		t.Fatalf("scan_runs deleted = %d, want 1 (%v)", res["scan_runs"], res)
	}
	var runs, batchRows int
	if err := s.Pool.QueryRow(ctx, `select count(*) from scan_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `select count(*) from scan_run_images where run_id = $1`, oldRun).Scan(&batchRows); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || batchRows != 0 {
		t.Fatalf("runs left = %d (want 2), batch rows of deleted run = %d (want 0)", runs, batchRows)
	}
}
