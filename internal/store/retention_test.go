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

func TestRunRetention_AgesOutImagesAndKeepsCandidateRowsForLiveOnes(t *testing.T) {
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
	seedScanImages(t, s, now.Add(-100*day),
		scanSeen("sha256:aged", "ecr", "running"), scanSeen("sha256:live", "ecr", "running"),
		scanSeen("sha256:cand-only-aged", "ecr", "candidate"), scanSeen("sha256:cand-shared", "ecr", "candidate"))
	exec(`update scan_images set last_seen_running_at = $1 where digest = 'sha256:aged'`, now.Add(-95*day))
	exec(`update scan_images set last_seen_running_at = $1 where digest = 'sha256:live'`, now.Add(-1*day))
	for _, d := range []string{"sha256:aged", "sha256:live", "sha256:cand-only-aged", "sha256:cand-shared"} {
		exec(`insert into scan_findings (digest, vuln_id, package_name, package_version, severity, first_seen_at, last_seen_at, run_id)
			values ($1, 'CVE-0000-0001', 'p', '1', 'high', now(), now(), 1)`, d)
	}
	exec(`insert into scan_candidate_links values ('sha256:aged', 'sha256:cand-only-aged'), ('sha256:aged', 'sha256:cand-shared'), ('sha256:live', 'sha256:cand-shared')`)
	for _, d := range []string{"sha256:aged", "sha256:live"} {
		exec(`insert into scan_fix_candidates (running_digest, candidate_digest, tag, computed_at) values ($1, 'sha256:cand-shared', 't', now())`, d)
	}

	res, err := s.RunRetention(ctx, now, store.Retention{ScanImages: 90 * day})
	if err != nil {
		t.Fatal(err)
	}
	if res["scan_images_aged"] != 1 {
		t.Fatalf("scan_images_aged = %d, want 1 (%v)", res["scan_images_aged"], res)
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := s.Pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	aged := mustGetScanImage(t, s, "sha256:aged")
	if aged.Status != "aged_out" {
		t.Fatalf("aged image status = %q, want aged_out", aged.Status)
	}
	live := mustGetScanImage(t, s, "sha256:live")
	if live.Status != "queued" {
		t.Fatalf("live image status = %q, want untouched", live.Status)
	}
	if n := count(`select count(*) from scan_findings where digest in ('sha256:aged', 'sha256:cand-only-aged')`); n != 0 {
		t.Fatalf("findings of aged image and its unlinked candidate = %d, want 0", n)
	}
	if n := count(`select count(*) from scan_findings where digest in ('sha256:live', 'sha256:cand-shared')`); n != 2 {
		t.Fatalf("findings of live image and shared candidate = %d, want 2", n)
	}
	if n := count(`select count(*) from scan_images where digest = 'sha256:cand-only-aged'`); n != 0 {
		t.Fatalf("unlinked candidate rows = %d, want 0", n)
	}
	if n := count(`select count(*) from scan_images where digest = 'sha256:cand-shared'`); n != 1 {
		t.Fatalf("shared candidate rows = %d, want 1", n)
	}
	if n := count(`select count(*) from scan_candidate_links where running_digest = 'sha256:aged'`); n != 0 {
		t.Fatalf("links of aged image = %d, want 0", n)
	}
	if n := count(`select count(*) from scan_candidate_links where running_digest = 'sha256:live'`); n != 1 {
		t.Fatalf("links of live image = %d, want 1", n)
	}
	if n := count(`select count(*) from scan_fix_candidates where running_digest = 'sha256:aged'`); n != 0 {
		t.Fatalf("fix candidate of aged image = %d, want 0", n)
	}
	if n := count(`select count(*) from scan_fix_candidates where running_digest = 'sha256:live'`); n != 1 {
		t.Fatalf("fix candidate of live image = %d, want 1", n)
	}
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

func TestRunRetention_UnlinkedCandidateWithSBOMIsDeletedInTwoPasses(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	seedScanImages(t, s, now, scanSeen("sha256:cand", "ecr", "candidate"), scanSeen("sha256:busy", "ecr", "candidate"))
	for _, sql := range []string{
		`update scan_images set sbom_key = 'sbom/' || digest where role = 'candidate'`,
		`update scan_images set status = 'scanning' where digest = 'sha256:busy'`,
		`insert into scan_findings (digest, vuln_id, package_name, package_version, severity, first_seen_at, last_seen_at, run_id)
			select digest, 'CVE-0000-0001', 'p', '1', 'high', now(), now(), 1 from scan_images`,
	} {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	retention := store.Retention{ScanImages: 90 * 24 * time.Hour}

	if _, err := s.RunRetention(ctx, now, retention); err != nil {
		t.Fatal(err)
	}
	cand := mustGetScanImage(t, s, "sha256:cand")
	if cand.Status != "aged_out" {
		t.Fatalf("first pass candidate status = %q, want aged_out", cand.Status)
	}
	keys, err := s.AgedOutSBOMKeys(ctx, 10)
	if err != nil || len(keys) != 1 || keys[0].Digest != "sha256:cand" {
		t.Fatalf("aged out keys = %+v err %v, want the candidate", keys, err)
	}
	var findings int
	if err := s.Pool.QueryRow(ctx, `select count(*) from scan_findings where digest = 'sha256:cand'`).Scan(&findings); err != nil || findings != 0 {
		t.Fatalf("candidate findings = %d err %v, want 0", findings, err)
	}

	if err := s.ClearSBOMKey(ctx, "sha256:cand"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRetention(ctx, now, retention); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScanImage(ctx, "sha256:cand"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second pass: GetScanImage err = %v, want ErrNotFound", err)
	}
	// A candidate being scanned right now is not touched, even with no link yet.
	if busy := mustGetScanImage(t, s, "sha256:busy"); busy.Status != "scanning" {
		t.Fatalf("scanning candidate status = %q, want untouched", busy.Status)
	}
}
