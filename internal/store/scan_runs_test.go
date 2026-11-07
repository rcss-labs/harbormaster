package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func newScanRun(t *testing.T, s *store.Store, kind string, digests ...string) int64 {
	t.Helper()
	id, err := s.CreateScanRun(context.Background(), store.ScanRun{
		Kind: kind, ClusterARN: "arn:cluster", BatchSize: len(digests), StartedAt: time.Now(),
		DBChecksum: "sum", GrypeVersion: "",
	}, digests)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFinishScanRun_ClaimsOnce(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	id := newScanRun(t, s, "scan", "sha256:a")
	if err := s.SetScanRunTask(ctx, id, "arn:task/1"); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveScanRuns(ctx)
	if err != nil || len(active) != 1 || active[0].Status != "running" || active[0].TaskARN != "arn:task/1" {
		t.Fatalf("active runs = %+v, err %v, want one running with the task", active, err)
	}
	exit := 0
	claimed, err := s.FinishScanRun(ctx, id, "succeeded", store.ScanRunPatch{ExitCode: &exit, ImagesOK: 1, GrypeVersion: "v1"})
	if err != nil || !claimed {
		t.Fatalf("first finish claimed=%v err=%v, want true", claimed, err)
	}
	claimed, err = s.FinishScanRun(ctx, id, "failed", store.ScanRunPatch{Error: "late"})
	if err != nil || claimed {
		t.Fatalf("second finish claimed=%v err=%v, want false", claimed, err)
	}
	run, err := s.LatestScanRun(ctx, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" || run.Error != "" || run.FinishedAt == nil || run.ImagesOK != 1 || run.DBChecksum != "sum" {
		t.Fatalf("run after double finish = %+v", run)
	}
	digests, _ := s.ScanRunDigests(ctx, id)
	if len(digests) != 1 || digests[0] != "sha256:a" {
		t.Fatalf("run digests = %v", digests)
	}
	if _, err := s.Pool.Exec(ctx, `insert into scan_runs (kind, batch_size, status, started_at) values ('rematch', 1, 'running', now())`); err == nil {
		t.Fatal("inserting a rematch run succeeded, want the kind check to refuse it")
	}
}

func TestScanRunsSummary_FailingSinceStartsAtFirstFailureAfterSuccess(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	base := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	insert := func(status, kind, errText string, finished time.Time) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, `insert into scan_runs (kind, batch_size, status, started_at, finished_at, error)
			values ($1, 1, $2, $3, $3, nullif($4, ''))`, kind, status, finished, errText); err != nil {
			t.Fatal(err)
		}
	}
	insert("failed", "scan", "early failure", base)                  // before the success: not counted
	insert("succeeded", "scan", "", base.Add(time.Hour))             // last success
	insert("failed", "scan", "first failure", base.Add(2*time.Hour)) // failing since here
	insert("timed_out", "scan", "latest failure", base.Add(3*time.Hour))
	if _, err := s.Pool.Exec(ctx, `insert into scan_runs (kind, batch_size, status, started_at) values ('scan', 3, 'running', $1)`, base.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into scan_run_images select id, 'sha256:x' from scan_runs where status = 'running'`); err != nil {
		t.Fatal(err)
	}

	sum, err := s.ScanRunsSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Running != 1 || sum.RunningImages != 1 {
		t.Fatalf("running=%d images=%d, want 1 1", sum.Running, sum.RunningImages)
	}
	if sum.LastSuccessAt == nil || !sum.LastSuccessAt.Equal(base.Add(time.Hour)) || sum.LastSuccessKind != "scan" {
		t.Fatalf("last success = %v %q", sum.LastSuccessAt, sum.LastSuccessKind)
	}
	if sum.FailingSince == nil || !sum.FailingSince.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("failing since = %v, want %v", sum.FailingSince, base.Add(2*time.Hour))
	}
	if sum.LastError != "latest failure" {
		t.Fatalf("last error = %q", sum.LastError)
	}

	// A later success ends the failing streak.
	insert("succeeded", "scan", "", base.Add(5*time.Hour))
	sum, _ = s.ScanRunsSummary(ctx)
	if sum.FailingSince != nil || sum.LastError != "" {
		t.Fatalf("after success failing since = %v error %q, want none", sum.FailingSince, sum.LastError)
	}
}

func TestScanDBState_RoundTrip(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	empty, err := s.ScanDBState(ctx)
	if err != nil || empty.BuiltAt != nil || empty.Checksum != "" {
		t.Fatalf("empty state = %+v err %v, want zero", empty, err)
	}
	built := time.Now().Add(-time.Hour).Truncate(time.Second)
	want := store.ScanDBState{BuiltAt: &built, Checksum: "sha256:abc", ArchiveKey: "grype-db/v6/x.tar.zst",
		ImportedAt: &built, LastRefreshAt: &built, LastRefreshError: "boom", ScannerImageDigest: "sha256:img", RefreshingSince: &built}
	if err := s.SetScanDBState(ctx, want); err != nil {
		t.Fatal(err)
	}
	want.Checksum = "sha256:def"
	want.LastRefreshError = ""
	if err := s.SetScanDBState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ScanDBState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checksum != "sha256:def" || got.ArchiveKey != want.ArchiveKey || got.LastRefreshError != "" ||
		got.BuiltAt == nil || !got.BuiltAt.Equal(built) || got.ScannerImageDigest != "sha256:img" ||
		got.RefreshingSince == nil || got.ImportedAt == nil || got.LastRefreshAt == nil {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
}

func TestScanUserForeignKeys_DoNotBlockUserDeletion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "bob", "hash", false, false)
	if err != nil {
		t.Fatal(err)
	}
	seedScanImages(t, s, time.Now(), "sha256:a")
	if _, err := s.Pool.Exec(ctx, `update scan_images set requested_by = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `insert into scan_risk_acceptances (digest, vuln_id, package_name, package_version, account_id, region,
		  cluster_name, service_name, reason, accepted_by, expires_at, revoked_by)
		values ('sha256:a', 'CVE-0000-0001', 'p', '1', '111111111111', 'eu-west-1', 'c', 's', 'r', $1, now(), $1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `delete from users where id = $1`, user.ID); err != nil {
		t.Fatalf("user delete blocked: %v", err)
	}
	img, _ := s.GetScanImage(ctx, "sha256:a")
	if img.RequestedBy != nil {
		t.Fatalf("requested_by = %v, want null", *img.RequestedBy)
	}
}
