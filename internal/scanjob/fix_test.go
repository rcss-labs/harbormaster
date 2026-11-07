package scanjob

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// emptyManifest is a succeeded manifest listing the given images as scanned.
func scannedScript(t *testing.T, digest string) func(scan.Job) map[string][]byte {
	return func(job scan.Job) map[string][]byte {
		return map[string][]byte{
			scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{"run_id": job.RunID, "kind": "scan", "status": "succeeded",
				"images": []map[string]any{{"digest": digest, "status": "scanned"}}}),
			scan.ResultKey(job.RunID, digest): cleanResult(digest, "CVE-0000-0001", "low"),
		}
	}
}

func TestRunner_ImportedRunsTaskIsStopped(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner, running: true}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	script.script = scannedScript(t, digest)
	fx.cycle()
	fx.cycle()
	if got := script.Stops(); len(got) != 1 || got[0] != "arn:scripted/1" {
		t.Fatalf("stopped tasks = %v, want the finished run's task stopped once", got)
	}
	if fx.image(digest).Status != "scanned" || fx.run(1).Status != "succeeded" {
		t.Fatal("the run was not imported")
	}
}

func TestRunner_UnreadableManifestStillTimesOutAndStops(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner, running: true}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.runner.Bucket = deniedBucket{fx.scanner} // a 403 for a missing key
	fx.cycle()
	if fx.run(1).Status != "running" {
		t.Fatalf("run status = %q, want still running before the timeout", fx.run(1).Status)
	}
	fx.clock.Advance(31 * time.Minute)
	fx.cycle()
	if run := fx.run(1); run.Status != "timed_out" {
		t.Fatalf("run status = %q, want timed_out", run.Status)
	}
	if got := script.Stops(); len(got) != 1 {
		t.Fatalf("stopped tasks = %v, want the task stopped", got)
	}
	if img := fx.image(digest); img.Attempts != 1 {
		t.Fatalf("image attempts = %d, want the batch requeued", img.Attempts)
	}
}

func TestRunner_UnreadableManifestWithStoppedTaskEndsRun(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.runner.Bucket = deniedBucket{fx.scanner}
	fx.cycle()
	if run := fx.run(1); run.Status != "failed" {
		t.Fatalf("run status = %q, want failed so the slot is freed", run.Status)
	}
	if img := fx.image(digest); img.Attempts != 1 || img.FailureCode != scan.CodeTaskFailed {
		t.Fatalf("image = attempts %d code %q, want a counted task_failed", img.Attempts, img.FailureCode)
	}
}

// failUpdates makes updates matching the condition fail, as a persistent store error would.
func failUpdates(t *testing.T, fx *fixture, table, condition string) {
	t.Helper()
	for _, sql := range []string{
		`create function hm_test_refuse() returns trigger language plpgsql as $$ begin raise exception 'refused by test'; end $$`,
		`create trigger hm_test_refuse before update on ` + table + ` for each row when (` + condition + `) execute function hm_test_refuse()`,
	} {
		if _, err := fx.st.Pool.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunner_FailingToRecordTheTaskStopsIt(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner, running: true}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	failUpdates(t, fx, "scan_runs", "new.task_arn is distinct from old.task_arn")
	_ = fx.runner.Cycle(context.Background())
	if got := script.Stops(); len(got) != 1 || got[0] != "arn:scripted/1" {
		t.Fatalf("stopped tasks = %v, want the unrecorded task stopped", got)
	}
	if run := fx.run(1); run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if img := fx.image(digest); img.Status != "queued" || img.Attempts != 1 {
		t.Fatalf("image = %q attempts %d, want requeued with an attempt", img.Status, img.Attempts)
	}
}

func TestRunner_TaskTimeoutRetriesLargeThenFails(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner, running: true}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	fx.clock.Advance(31 * time.Minute)
	fx.cycle()
	launches := script.Launches()
	if len(launches) != 2 || launches[0].Large || !launches[1].Large {
		t.Fatalf("launches = %+v, want the timed-out batch relaunched once, large", launches)
	}
	fx.clock.Advance(31 * time.Minute)
	fx.cycle()
	img := fx.image(digest)
	if img.Status != "failed" || img.FailureCode != scan.CodeTimeout || len(script.Launches()) != 2 {
		t.Fatalf("image = %q %q after %d launches, want failed timeout after the large run", img.Status, img.FailureCode, len(script.Launches()))
	}
}

func TestRunner_MarkScanningFailureDefersTheBatch(t *testing.T) {
	fx := newFixture(t)
	fx.runner.Cfg.Interval = 15 * time.Second
	digest := fx.queue(edge, edgeTag)
	failUpdates(t, fx, "scan_images", "new.status = 'scanning'")
	_ = fx.runner.Cycle(context.Background())
	img := fx.image(digest)
	if img.Status != "queued" || img.Attempts != 0 || img.NextAttemptAt == nil || !img.NextAttemptAt.Equal(fx.clock.Now().Add(15*time.Second)) {
		t.Fatalf("image = %q attempts %d next %v, want deferred one interval with no attempt counted", img.Status, img.Attempts, img.NextAttemptAt)
	}
	_ = fx.runner.Cycle(context.Background())
	if fx.runCount() != 1 {
		t.Fatalf("runs = %d, want no new run while the image is deferred", fx.runCount())
	}
}

func TestRunner_ImportSkipsImagesOfANewerRun(t *testing.T) {
	fx := newFixture(t)
	script := &scriptedLauncher{bucket: fx.scanner, running: true}
	fx.runner.Launcher = script
	digest := fx.queue(edge, edgeTag)
	script.script = func(job scan.Job) map[string][]byte {
		return map[string][]byte{scan.ManifestKey(job.RunID): manifestBody(t, map[string]any{"run_id": job.RunID, "kind": "scan", "status": "succeeded",
			"images": []map[string]any{{"digest": digest, "status": "failed", "failure_code": "task_failed"}}})}
	}
	fx.cycle() // run 1 launched and wrote its manifest
	// Another run now scans the same image (as after a requeue with MaxTasks above 1).
	if _, err := fx.st.CreateScanRun(context.Background(), store.ScanRun{Kind: scan.ModeScan, BatchSize: 1, StartedAt: fx.clock.Now()}, []string{digest}); err != nil {
		t.Fatal(err)
	}
	fx.cycle()
	if img := fx.image(digest); img.Status != "scanning" || img.Attempts != 0 {
		t.Fatalf("image = %q attempts %d, want the older run's import to leave it to the newer run", img.Status, img.Attempts)
	}
	if fx.run(1).Status == "running" {
		t.Fatal("the older run was not finished")
	}
}

func TestRunner_RunIsForcedFailedAfterImportAttempts(t *testing.T) {
	fx := newFixture(t)
	digest := fx.queue(edge, edgeTag)
	fx.cycle()
	failUpdates(t, fx, "scan_runs", "new.status = 'succeeded'")
	for cycle := 2; cycle <= 6; cycle++ {
		_ = fx.runner.Cycle(context.Background())
		if status := fx.run(1).Status; status != "running" {
			t.Fatalf("run status after cycle %d = %q, want it retried while attempts are within the limit", cycle, status)
		}
	}
	_ = fx.runner.Cycle(context.Background())
	run := fx.run(1)
	if run.Status != "failed" || !strings.Contains(run.Error, "could not be recorded") {
		t.Fatalf("run = %+v, want failed once the import attempts ran out", run)
	}
	if run.ImagesOK != 1 || run.ImagesFailed != 0 {
		t.Fatalf("run counts = ok %d failed %d, want the replayed import to keep the truthful 1/0", run.ImagesOK, run.ImagesFailed)
	}
	if fx.image(digest).Status != "scanned" {
		t.Fatal("the image was disturbed by the replays")
	}
}
