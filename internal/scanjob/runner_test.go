package scanjob

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

const (
	edge     = "edge-nginx"
	edgeTag  = "1.27.2" // three findings in the fake
	edgeTag2 = "1.27.3"
)

func TestRunner_NothingQueuedLaunchesNothingAndFetchesNoDB(t *testing.T) {
	fx := newFixture(t)
	fx.cycle()
	if fx.source.fetches.Load() != 0 || fx.source.opens.Load() != 0 {
		t.Fatalf("source reads = %d/%d, want none", fx.source.fetches.Load(), fx.source.opens.Load())
	}
	if len(fx.scanner.Launches()) != 0 || len(fx.scanner.Writes()) != 0 || fx.runCount() != 0 {
		t.Fatalf("launches=%v writes=%v runs=%d, want nothing", fx.scanner.Launches(), fx.scanner.Writes(), fx.runCount())
	}
}

func TestRunner_RequestRefreshesDBThenLaunchesOneTask(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()

	writes := fx.scanner.Writes()
	indexOf := func(key string) int {
		for i, w := range writes {
			if w == key {
				return i
			}
		}
		return -1
	}
	archiveIndex := -1
	for i, w := range writes {
		if strings.HasPrefix(w, "grype-db/v6/") && w != scan.DBLatestKey {
			archiveIndex = i
		}
	}
	latestIndex, jobIndex := indexOf(scan.DBLatestKey), indexOf(scan.JobKey(1))
	if archiveIndex < 0 || latestIndex < archiveIndex || jobIndex < latestIndex {
		t.Fatalf("write order = %v, want archive, then latest.json, then the job", writes)
	}
	if got := fx.scanner.Launches(); len(got) != 1 || got[0].Mode != scan.ModeScan || got[0].Large {
		t.Fatalf("launches = %+v, want one normal scan task", got)
	}

	var latest scan.DBLatest
	if err := json.Unmarshal(fx.scanner.Objects()[scan.DBLatestKey], &latest); err != nil {
		t.Fatal(err)
	}
	job := fx.job(1)
	if job.DB.Checksum != latest.Checksum || job.DB.ArchiveKey != latest.ArchiveKey || job.DB.MaxAgeHours != 120 {
		t.Fatalf("job db = %+v, want latest's checksum and archive key and max age 120 h", job.DB)
	}
	if len(job.Images) != 1 || job.Images[0].Digest != digest || job.Kind != scan.ModeScan {
		t.Fatalf("job = %+v, want one image %s", job, digest)
	}
	if strings.Contains(strings.ToLower(string(fx.scanner.Objects()[scan.JobKey(1)])), "sbom") {
		t.Fatalf("job mentions an SBOM: %s", fx.scanner.Objects()[scan.JobKey(1)])
	}
	if got := fx.image(digest).Status; got != "scanning" {
		t.Fatalf("image status = %q, want scanning", got)
	}
	if run := fx.run(1); run.DBChecksum != latest.Checksum || run.ARN == "" {
		t.Fatalf("run = %+v, want the DB checksum and a task", run)
	}
}

func TestRunner_ImportsResultsAndRecordsScannedAt(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.clock.Advance(time.Minute)
	fx.cycle()

	img := fx.image(digest)
	if img.Status != "scanned" || img.ScannedAt == nil || !img.ScannedAt.Equal(fx.clock.Now()) {
		t.Fatalf("image = status %q scanned_at %v, want scanned at %v", img.Status, img.ScannedAt, fx.clock.Now())
	}
	run := fx.run(1)
	if img.MatchedDBBuiltAt == nil || run.DBBuiltAt == nil || !img.MatchedDBBuiltAt.Equal(*run.DBBuiltAt) {
		t.Fatalf("matched db time = %v, run db time = %v, want the run's", img.MatchedDBBuiltAt, run.DBBuiltAt)
	}
	findings, err := fx.st.ScanFindings(context.Background(), digest)
	if err != nil || len(findings) != 3 {
		t.Fatalf("findings = %d, err %v, want 3", len(findings), err)
	}
	if run.Status != "succeeded" || run.ImagesOK != 1 {
		t.Fatalf("run = %+v, want succeeded with one image", run)
	}
	state, err := fx.st.ScanDBState(context.Background())
	if err != nil || state.ScannerImageDigest == "" {
		t.Fatalf("db state = %+v, err %v, want the scanner image digest recorded", state, err)
	}
}

func TestRunner_ReusesFreshDBWithoutRefetch(t *testing.T) {
	fx := newFixture(t)
	fx.queue(edge, edgeTag)
	fx.cycle()
	fx.cycle()
	fetches := fx.source.fetches.Load()
	if fetches != 1 {
		t.Fatalf("fetches after the first request = %d, want 1", fetches)
	}

	fx.clock.Advance(2 * time.Hour)
	fx.queue("payments-api", "1.18.2")
	fx.cycle()
	if got := fx.source.fetches.Load(); got != fetches || fx.source.opens.Load() != 1 {
		t.Fatalf("source reads after the second request = %d/%d, want no more than %d/1", got, fx.source.opens.Load(), fetches)
	}
	if len(fx.scanner.Launches()) != 2 {
		t.Fatalf("launches = %d, want 2", len(fx.scanner.Launches()))
	}
}

func TestRunner_MirrorModeNeverFetches(t *testing.T) {
	fx := newFixture(t)
	fx.runner.Cfg.DBRefresh = 0
	latest := putMirrorDB(t, fx.scanner, fx.clock.Now().Add(-48*time.Hour)) // old, but a mirror is trusted until DBMaxAge
	fx.queue(edge, edgeTag)
	fx.cycle()
	if fx.source.fetches.Load() != 0 || fx.source.opens.Load() != 0 {
		t.Fatalf("source reads = %d/%d, want none in mirror mode", fx.source.fetches.Load(), fx.source.opens.Load())
	}
	if len(fx.scanner.Launches()) != 1 || fx.run(1).DBChecksum != latest.Checksum {
		t.Fatalf("launches = %d, run = %+v, want one run against the mirrored DB", len(fx.scanner.Launches()), fx.run(1))
	}
}

func TestRunner_StaleDBDefersAndReports(t *testing.T) {
	fx := newFixture(t)
	fx.runner.DB = failingSource{}
	putMirrorDB(t, fx.scanner, fx.clock.Now().Add(-144*time.Hour)) // 6 days
	fresh := fx.queue(edge, edgeTag)
	old := fx.queueFor(homelab, "payments-api", "1.18.2", fx.clock.Now().Add(-121*time.Hour))
	fx.cycle()

	if fx.runCount() != 0 {
		t.Fatalf("runs = %d, want none against stale data", fx.runCount())
	}
	img := fx.image(fresh)
	wantNext := fx.clock.Now().Add(10 * time.Minute)
	if img.Status != "queued" || img.FailureCode != scan.CodeDBStale || img.NextAttemptAt == nil || !img.NextAttemptAt.Equal(wantNext) ||
		!strings.Contains(img.FailureDetail, "boom") {
		t.Fatalf("recent image = status %q code %q next %v detail %q, want queued, db_stale, in 10 min, naming the refresh error",
			img.Status, img.FailureCode, img.NextAttemptAt, img.FailureDetail)
	}
	if img := fx.image(old); img.Status != "failed" || img.FailureCode != scan.CodeDBStale {
		t.Fatalf("image queued longer than the max age = status %q code %q, want failed db_stale", img.Status, img.FailureCode)
	}
	state, _ := fx.st.ScanDBState(context.Background())
	if !strings.Contains(state.LastRefreshError, "boom") || state.RefreshingSince != nil {
		t.Fatalf("db state = %+v, want the refresh error recorded and no refresh in progress", state)
	}
}

func TestRunner_NotFoundMeansNoDBButOtherBucketErrorsAreRefreshFailures(t *testing.T) {
	fx := newFixture(t)
	fx.runner.Cfg.DBRefresh = 0
	fx.runner.Bucket = deniedBucket{fx.scanner}
	fx.queue(edge, edgeTag)
	fx.cycle()
	state, _ := fx.st.ScanDBState(context.Background())
	if !strings.Contains(state.LastRefreshError, "AccessDenied") {
		t.Fatalf("last refresh error = %q, want the AccessDenied recorded", state.LastRefreshError)
	}
}

func TestRunner_StaleRefreshingSinceIsIgnored(t *testing.T) {
	t.Run("older than 15 minutes", func(t *testing.T) {
		fx := newFixture(t)
		since := fx.clock.Now().Add(-20 * time.Minute)
		if err := fx.st.SetScanDBState(context.Background(), storeState(&since)); err != nil {
			t.Fatal(err)
		}
		fx.queue(edge, edgeTag)
		fx.cycle()
		if len(fx.scanner.Launches()) != 1 {
			t.Fatalf("launches = %d, want the refresh to run and one task to start", len(fx.scanner.Launches()))
		}
		if state, _ := fx.st.ScanDBState(context.Background()); state.RefreshingSince != nil {
			t.Fatalf("refreshing_since = %v, want cleared", state.RefreshingSince)
		}
	})
	t.Run("a recent one is respected", func(t *testing.T) {
		fx := newFixture(t)
		since := fx.clock.Now().Add(-time.Minute)
		if err := fx.st.SetScanDBState(context.Background(), storeState(&since)); err != nil {
			t.Fatal(err)
		}
		digest := fx.queue(edge, edgeTag)
		fx.cycle()
		if fx.source.fetches.Load() != 0 || len(fx.scanner.Launches()) != 0 {
			t.Fatalf("fetches=%d launches=%d, want a refresh in progress elsewhere to be left alone", fx.source.fetches.Load(), len(fx.scanner.Launches()))
		}
		if img := fx.image(digest); img.Status != "queued" || img.FailureCode != scan.CodeDBStale {
			t.Fatalf("image = %q/%q, want queued and db_stale", img.Status, img.FailureCode)
		}
	})
}

func TestRunner_ReconcileImportsFinishedRunOnce(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	other := fx.newRunner()
	fx.cycle() // the first runner imports
	// Gone from the bucket: a second import would find no result and fail the image.
	_ = fx.scanner.Delete(context.Background(), scan.ResultKey(1, digest))
	_ = fx.scanner.Delete(context.Background(), scan.ManifestKey(1))
	if err := other.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if img := fx.image(digest); img.Status != "scanned" || img.Attempts != 0 {
		t.Fatalf("image = status %q attempts %d, want scanned once and left alone", img.Status, img.Attempts)
	}

	t.Run("two runners at the same moment", func(t *testing.T) {
		fx := newFixture(t)
		digest := fx.queue(edge, edgeTag)
		fx.cycle()
		runners := []*Runner{fx.runner, fx.newRunner(), fx.newRunner()}
		var wg sync.WaitGroup
		for _, r := range runners {
			wg.Add(1)
			go func() { defer wg.Done(); _ = r.Cycle(context.Background()) }()
		}
		wg.Wait()
		findings, _ := fx.st.ScanFindings(context.Background(), digest)
		if img := fx.image(digest); img.Status != "scanned" || len(findings) != 3 || fx.run(1).ImagesOK != 1 {
			t.Fatalf("image %q findings %d run %+v, want one clean import", img.Status, len(findings), fx.run(1))
		}
	})
}

func TestRunner_StuckTaskIsStoppedAndRequeued(t *testing.T) {
	fx := newFixture(t)
	fx.scanner.Delay = time.Hour
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	arn := fx.run(1).ARN

	fx.cycle() // not yet past the timeout
	if fx.run(1).Status != "running" {
		t.Fatalf("run status = %q, want still running", fx.run(1).Status)
	}
	fx.clock.Advance(31 * time.Minute)
	fx.cycle()

	state, err := fx.scanner.DescribeTask(context.Background(), arn)
	if err != nil || state.LastStatus != "STOPPED" {
		t.Fatalf("task = %+v, %v, want it stopped", state, err)
	}
	if run := fx.run(1); run.Status != "timed_out" {
		t.Fatalf("run status = %q, want timed_out", run.Status)
	}
	img := fx.image(digest)
	if img.Attempts != 1 || img.FailureCode != scan.CodeTimeout || !img.Large {
		t.Fatalf("image = %q attempts %d code %q large %v, want one attempt as large with code timeout",
			img.Status, img.Attempts, img.FailureCode, img.Large)
	}
	if launches := fx.scanner.Launches(); len(launches) != 2 || !launches[1].Large {
		t.Fatalf("launches = %+v, want the retry launched large", launches)
	}
}

func TestRunner_ThreeFailuresGiveUp(t *testing.T) {
	fx := newFixture(t)
	launcher := &errLauncher{}
	fx.runner.Launcher = launcher
	digest := fx.queue(edge, edgeTag)

	fx.cycle()
	img := fx.image(digest)
	if img.Status != "queued" || img.Attempts != 1 || img.NextAttemptAt == nil || !img.NextAttemptAt.Equal(fx.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("after attempt 1: %q attempts %d next %v, want queued and retry in 5 min", img.Status, img.Attempts, img.NextAttemptAt)
	}
	fx.cycle() // not due yet
	if launcher.calls.Load() != 1 {
		t.Fatalf("launch calls = %d, want the backoff respected", launcher.calls.Load())
	}
	fx.clock.Advance(5 * time.Minute)
	fx.cycle()
	img = fx.image(digest)
	if img.Status != "queued" || img.Attempts != 2 || img.NextAttemptAt == nil || !img.NextAttemptAt.Equal(fx.clock.Now().Add(30*time.Minute)) {
		t.Fatalf("after attempt 2: %q attempts %d next %v, want queued and retry in 30 min", img.Status, img.Attempts, img.NextAttemptAt)
	}
	fx.clock.Advance(30 * time.Minute)
	fx.cycle()
	img = fx.image(digest)
	if img.Status != "failed" || img.FailureCode != scan.CodeTaskFailed || launcher.calls.Load() != 3 {
		t.Fatalf("after attempt 3: %q code %q after %d launches, want failed task_failed after 3", img.Status, img.FailureCode, launcher.calls.Load())
	}
	if run := fx.run(1); run.Status != "failed" {
		t.Fatalf("run 1 status = %q, want failed", run.Status)
	}
}

func TestRunner_PullDeniedFailsAtOnce(t *testing.T) {
	fx := newFixture(t)
	fx.scanner.FailPull(edge)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.cycle()
	img := fx.image(digest)
	if img.Status != "failed" || img.FailureCode != scan.CodePullDenied || img.Attempts != 0 {
		t.Fatalf("image = %q code %q attempts %d, want failed pull_denied with no retry", img.Status, img.FailureCode, img.Attempts)
	}
	fx.cycle()
	if len(fx.scanner.Launches()) != 1 {
		t.Fatalf("launches = %d, want 1", len(fx.scanner.Launches()))
	}
}

func TestRunner_TimeoutRetriesLarge(t *testing.T) {
	fx := newFixture(t)
	fx.scanner.FailTimeout(edge)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.cycle() // imports the timeout and relaunches large
	fx.cycle() // imports the large result

	launches := fx.scanner.Launches()
	if len(launches) != 2 || launches[0].Large || !launches[1].Large {
		t.Fatalf("launches = %+v, want a normal one and then a large one", launches)
	}
	if img := fx.image(digest); img.Status != "scanned" {
		t.Fatalf("image = %q (%q), want scanned by the large retry", img.Status, img.FailureCode)
	}
}

func TestRunner_UnconnectedRegistryAccountFails(t *testing.T) {
	fx := newFixture(t)
	if _, err := fx.st.Pool.Exec(context.Background(), `delete from aws_accounts where account_id = $1`, homelab); err != nil {
		t.Fatal(err)
	}
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	img := fx.image(digest)
	if img.Status != "failed" || img.FailureCode != scan.CodePullDenied || img.FailureDetail != "account not connected" {
		t.Fatalf("image = %q %q %q, want failed pull_denied \"account not connected\"", img.Status, img.FailureCode, img.FailureDetail)
	}
	if len(fx.scanner.Launches()) != 0 {
		t.Fatalf("launches = %d, want none for an unconnected account", len(fx.scanner.Launches()))
	}
}

func TestRunner_JobCarriesOnlyTheBatchAccounts(t *testing.T) {
	fx := newFixture(t)
	fx.runner.Cfg.BatchSize = 1
	fx.queueFor(homelab, edge, edgeTag, fx.clock.Now().Add(-2*time.Hour))
	fx.queueFor(staging, edge, edgeTag2, fx.clock.Now().Add(-time.Hour))
	fx.cycle()

	job := fx.job(1)
	registry, ok := job.Registries[homelab]
	if len(job.Registries) != 1 || !ok || registry.RoleARN != "arn:aws:iam::"+homelab+":role/harbormaster-scan" || registry.ExternalID != "ext-home" {
		t.Fatalf("registries = %+v, want only the homelab account with its scan role and external id", job.Registries)
	}
	if len(job.Images) != 1 || job.Images[0].AccountID != homelab {
		t.Fatalf("images = %+v, want the older request alone", job.Images)
	}
}

func TestRunner_BucketKeysComeFromTheLeader(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	script.script = func(job scan.Job) map[string][]byte {
		return map[string][]byte{
			scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{
				"run_id": job.RunID, "kind": "scan", "status": "succeeded", "result_key": "results/evil.json",
				"images": []map[string]any{{"digest": digest, "status": "scanned", "result_key": "results/evil.json", "key": "results/evil.json"}},
			}),
			scan.ResultKey(job.RunID, digest): cleanResult(digest, "CVE-0000-0001", "high"),
			"results/evil.json":               cleanResult(digest, "CVE-0000-9999", "critical"),
		}
	}
	fx.cycle()
	fx.cycle()
	findings, err := fx.st.ScanFindings(context.Background(), digest)
	if err != nil || len(findings) != 1 || findings[0].VulnID != "CVE-0000-0001" {
		t.Fatalf("findings = %+v, %v, want only the one at the key the leader computed", findings, err)
	}
}

func TestRunner_UnexpectedDigestInManifestIsIgnored(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner}
	fx.runner.Launcher = script
	inJob := fx.queueFor(homelab, edge, edgeTag, fx.clock.Now().Add(-2*time.Hour))
	outside := fx.queueFor(homelab, "payments-api", "1.18.2", fx.clock.Now().Add(-time.Hour))
	// The second image is queued but not part of the run: make it wait, so the first run takes only the first.
	fx.runner.Cfg.BatchSize = 1
	script.script = func(job scan.Job) map[string][]byte {
		return map[string][]byte{
			scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{
				"run_id": job.RunID, "kind": "scan", "status": "succeeded",
				"images": []map[string]any{{"digest": inJob, "status": "scanned"}, {"digest": outside, "status": "scanned"}},
			}),
			scan.ResultKey(job.RunID, inJob):   cleanResult(inJob, "CVE-0000-0001", "low"),
			scan.ResultKey(job.RunID, outside): cleanResult(outside, "CVE-0000-0002", "low"),
		}
	}
	fx.cycle()
	if job := fx.job(1); len(job.Images) != 1 || job.Images[0].Digest != inJob {
		t.Fatalf("job images = %+v, want only %s", job.Images, inJob)
	}
	fx.cycle()
	if img := fx.image(inJob); img.Status != "scanned" {
		t.Fatalf("job image = %q, want scanned", img.Status)
	}
	findings, _ := fx.st.ScanFindings(context.Background(), outside)
	if len(findings) != 0 {
		t.Fatalf("an image outside the job received findings: %+v", findings)
	}
}

func TestRunner_UnknownFailureCodeCountsAnAttempt(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner}
	fx.runner.Launcher = script
	unknownCode := fx.queue(edge, edgeTag)
	noCode := fx.queue("payments-api", "1.18.2")
	script.script = func(job scan.Job) map[string][]byte {
		return map[string][]byte{scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{
			"run_id": job.RunID, "kind": "scan", "status": "succeeded",
			"images": []map[string]any{
				{"digest": unknownCode, "status": "failed", "failure_code": "registry_not_connected"},
				{"digest": noCode, "status": "failed"},
			}})}
	}
	fx.cycle()
	fx.cycle()
	for _, digest := range []string{unknownCode, noCode} {
		if img := fx.image(digest); img.Status != "queued" || img.Attempts != 1 || img.FailureCode != scan.CodeTaskFailed {
			t.Fatalf("%s = %q attempts %d code %q, want queued, one attempt, task_failed", digest, img.Status, img.Attempts, img.FailureCode)
		}
	}
}

func TestRunner_EndlessSkipsEndInFailure(t *testing.T) {
	t.Run("a scanner that always skips", func(t *testing.T) {
		fx := newFixture(t)
		script := &scriptedLauncher{bucket: fx.scanner}
		fx.runner.Launcher = script
		digest := fx.queue(edge, edgeTag)
		script.script = func(job scan.Job) map[string][]byte {
			return map[string][]byte{scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{
				"run_id": job.RunID, "kind": "scan", "status": "succeeded",
				"images": []map[string]any{{"digest": digest, "status": "skipped"}}})}
		}
		for cycle := 1; cycle <= 3; cycle++ {
			fx.cycle()
			if img := fx.image(digest); img.Status == "failed" {
				t.Fatalf("failed after cycle %d, want the fourth", cycle)
			}
		}
		fx.cycle()
		img := fx.image(digest)
		if img.Status != "failed" || img.FailureCode != scan.CodeTaskFailed || img.FailureDetail != "scanner kept skipping this image" {
			t.Fatalf("after cycle 4: %q %q %q, want failed task_failed \"scanner kept skipping this image\"", img.Status, img.FailureCode, img.FailureDetail)
		}
		if got := len(script.Launches()); got != 3 {
			t.Fatalf("launches = %d, want 3", got)
		}
	})
	t.Run("a scanner that always says the DB is stale", func(t *testing.T) {
		fx := newFixture(t)
		fx.runner.Cfg.DBRefresh = 0
		putMirrorDB(t, fx.scanner, fx.clock.Now().Add(-time.Hour)) // fresh as far as the leader can tell
		fx.scanner.ForceStale(true)
		digest := fx.queue(edge, edgeTag)
		for range 4 {
			fx.cycle()
		}
		img := fx.image(digest)
		if img.Status != "failed" || img.FailureCode != scan.CodeDBStale {
			t.Fatalf("after cycle 4: %q %q, want failed db_stale", img.Status, img.FailureCode)
		}
		if state, _ := fx.st.ScanDBState(context.Background()); state.LastRefreshError != "stale" {
			t.Fatalf("last refresh error = %q, want \"stale\"", state.LastRefreshError)
		}
	})
}

func TestRunner_StoreFailureDoesNotWedgeTheSlot(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	// Findings can no longer be written, so the scanned result cannot be recorded.
	if _, err := fx.st.Pool.Exec(context.Background(), `alter table scan_findings rename to scan_findings_held`); err != nil {
		t.Fatal(err)
	}
	fx.cycle()

	if img := fx.image(digest); img.Status != "failed" || img.FailureCode != scan.CodeTaskFailed {
		t.Fatalf("image = %q %q, want failed task_failed", img.Status, img.FailureCode)
	}
	if run := fx.run(1); run.Status == "running" || run.Status == "launching" {
		t.Fatalf("run status = %q, want the run finished", run.Status)
	}
	second := fx.queue("payments-api", "1.18.2")
	fx.cycle()
	if len(fx.scanner.Launches()) != 2 || fx.image(second).Status != "scanning" {
		t.Fatalf("launches = %d, second image %q, want new work launched", len(fx.scanner.Launches()), fx.image(second).Status)
	}
}

func TestRunner_AgeOutDeletesRunObjects(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.cycle()
	objects := fx.scanner.Objects()
	for _, key := range []string{scan.JobKey(1), scan.ManifestKey(1), scan.ResultKey(1, digest)} {
		if _, ok := objects[key]; !ok {
			t.Fatalf("%s missing before age-out", key)
		}
	}
	fx.cycle()
	if _, ok := fx.scanner.Objects()[scan.JobKey(1)]; !ok {
		t.Fatal("job deleted before 24 h")
	}
	fx.clock.Advance(25 * time.Hour)
	fx.cycle()
	objects = fx.scanner.Objects()
	for _, key := range []string{scan.JobKey(1), scan.ManifestKey(1), scan.ResultKey(1, digest)} {
		if _, ok := objects[key]; ok {
			t.Fatalf("%s still present after age-out", key)
		}
	}
	if _, ok := objects[scan.DBLatestKey]; !ok {
		t.Fatal("the DB was deleted with the run")
	}
	if !fx.run(1).ObjectsDeleted {
		t.Fatal("run not marked objects-deleted")
	}
}

func TestRunner_KickWakesTheLoop(t *testing.T) {
	fx := newFixture(t)
	cycles := make(chan struct{}, 8)
	fx.runner.afterCycle = func() { cycles <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); fx.runner.Run(ctx) }()
	defer func() { cancel(); <-done }()

	wait := func(what string) {
		select {
		case <-cycles:
		case <-time.After(5 * time.Second):
			t.Fatalf("no cycle: %s", what)
		}
	}
	wait("the first cycle runs at once")
	fx.queue(edge, edgeTag)
	fx.runner.Kick()
	fx.runner.Kick() // a second kick while one is pending must not block
	wait("a kick runs a cycle long before the hour-long interval")
	if len(fx.scanner.Launches()) != 1 {
		t.Fatalf("launches = %d, want the kicked cycle to launch the request", len(fx.scanner.Launches()))
	}
}
