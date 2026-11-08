package fake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
)

func TestScanner_FindingsFollowTheTagAheadOfTheRunningOne(t *testing.T) {
	s := NewScanner(newFake().Estate)
	count := func(repository, tag string) int { return len(s.FindingsFor(repository, tag)) }

	if got := count("edge-nginx", "1.27.2"); got != 3 {
		t.Fatalf("edge-nginx 1.27.2 has %d findings, want 3", got)
	}
	if got := count("edge-nginx", "1.27.3"); got != 0 {
		t.Fatalf("edge-nginx 1.27.3 has %d findings, want it clean", got)
	}
	if got := count("payments-api", "1.18.2"); got != 2 {
		t.Fatalf("payments-api running tag has %d findings, want 2", got)
	}
	if got := count("payments-api", "1.18.3"); got != 1 {
		t.Fatalf("payments-api one tag ahead has %d findings, want 1", got)
	}
	if got := count("image-resizer", "0.4.2"); got != 0 {
		t.Fatalf("an unlisted repository has %d findings, want none", got)
	}
}

func TestScanner_RunTaskWritesTheManifestAndResultsOfTheJob(t *testing.T) {
	s := NewScanner(newFake().Estate)
	s.FailPull("cert-renewer")
	ctx := context.Background()
	const host = homelabID + ".dkr.ecr.us-east-1.amazonaws.com/"
	edge := s.Estate.ImageDigestFor("edge-nginx:1.27.2")
	renewer := s.Estate.ImageDigestFor("cert-renewer:0.2.1")
	job := scan.Job{RunID: 9, Kind: scan.ModeScan, Images: []scan.JobImage{
		{Digest: edge, ImageRef: host + "edge-nginx", Tag: "1.27.2"},
		{Digest: renewer, ImageRef: host + "cert-renewer", Tag: "0.2.1"},
	}}
	data, _ := json.Marshal(job)
	if err := s.Put(ctx, scan.JobKey(9), data); err != nil {
		t.Fatal(err)
	}

	arn, err := s.RunTask(ctx, awsx.LaunchSpec{RunID: 9, Mode: scan.ModeScan})
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.DescribeTask(ctx, arn)
	if err != nil || state.LastStatus != "STOPPED" || state.ExitCode == nil || *state.ExitCode != 0 {
		t.Fatalf("task state = %+v, %v; want STOPPED with exit 0", state, err)
	}

	manifestData, err := s.Get(ctx, scan.ManifestKey(9), scan.MaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := scan.ParseManifest(manifestData, 9, []string{edge, renewer})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != scan.ManifestSucceeded || len(manifest.Images) != 2 ||
		manifest.Images[0].Status != scan.OutcomeScanned ||
		manifest.Images[1].Status != scan.OutcomeFailed || manifest.Images[1].FailureCode != scan.CodePullDenied {
		t.Fatalf("manifest = %+v, want edge-nginx scanned and cert-renewer pull_denied", manifest)
	}
	resultData, err := s.Get(ctx, scan.ResultKey(9, edge), scan.MaxResultBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scan.ParseResult(resultData, edge)
	if err != nil || len(result.Findings) != 3 {
		t.Fatalf("result = %+v, %v; want 3 findings", result, err)
	}
	if _, err := s.Get(ctx, scan.ResultKey(9, renewer), scan.MaxResultBytes); !errors.Is(err, awsx.ErrObjectNotFound) {
		t.Fatalf("a failed image has a result object (err = %v)", err)
	}
	for _, key := range s.Writes() {
		if strings.HasPrefix(key, "sbom/") {
			t.Fatalf("wrote %s, the fake writes no SBOM objects", key)
		}
	}
}

func TestScanner_DelayKeepsTheTaskRunningUntilItElapses(t *testing.T) {
	s := NewScanner(newFake().Estate)
	s.Delay = 20 * time.Millisecond
	ctx := context.Background()
	data, _ := json.Marshal(scan.Job{RunID: 3, Kind: scan.ModeScan})
	_ = s.Put(ctx, scan.JobKey(3), data)

	arn, _ := s.RunTask(ctx, awsx.LaunchSpec{RunID: 3, Mode: scan.ModeScan})
	if state, _ := s.DescribeTask(ctx, arn); state.LastStatus != "RUNNING" {
		t.Fatalf("status = %s right after the start, want RUNNING", state.LastStatus)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := s.DescribeTask(ctx, arn); state.LastStatus == "STOPPED" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the task never stopped")
}

func TestScanner_FetchLatestIsSelfConsistentAcrossAClockTick(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	e := newFake().Estate
	e.Now = func() time.Time { return now }
	s := NewScanner(e)
	ctx := context.Background()

	listing, err := s.FetchLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)

	upstream, err := scan.ParseUpstreamDB(listing, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upstream.Path, upstream.Built.Format(fakeDBPathTimeFormat)) {
		t.Fatalf("path %q does not carry the build time %s", upstream.Path, upstream.Built)
	}
	archive, err := s.OpenArchive(ctx, upstream.Path)
	if err != nil {
		t.Fatalf("OpenArchive of the issued path after the clock moved: %v", err)
	}
	archive.Close()
	again, _ := s.FetchLatest(ctx)
	if string(again) != string(listing) {
		t.Fatalf("listing changed with the clock: %s then %s", listing, again)
	}
}

func TestScanner_LargeLaunchEscapesFailTimeout(t *testing.T) {
	s := NewScanner(newFake().Estate)
	s.FailTimeout("edge-nginx")
	ctx := context.Background()
	const host = homelabID + ".dkr.ecr.us-east-1.amazonaws.com/"
	digest := s.Estate.ImageDigestFor("edge-nginx:1.27.2")
	for _, runID := range []int64{1, 2} {
		data, _ := json.Marshal(scan.Job{RunID: runID, Kind: scan.ModeScan, Images: []scan.JobImage{
			{Digest: digest, ImageRef: host + "edge-nginx", Tag: "1.27.2"}}})
		_ = s.Put(ctx, scan.JobKey(runID), data)
	}
	normal := awsx.LaunchSpec{RunID: 1, Mode: scan.ModeScan}
	large := awsx.LaunchSpec{RunID: 2, Mode: scan.ModeScan, Large: true}
	for _, spec := range []awsx.LaunchSpec{normal, large} {
		if _, err := s.RunTask(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}

	status := func(runID int64) string {
		data, err := s.Get(ctx, scan.ManifestKey(runID), scan.MaxManifestBytes)
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := scan.ParseManifest(data, runID, []string{digest})
		if err != nil || len(m.Images) != 1 {
			t.Fatalf("manifest %d: %+v, %v", runID, m, err)
		}
		return m.Images[0].Status + "/" + m.Images[0].FailureCode
	}
	if got := status(1); got != "failed/timeout" {
		t.Fatalf("normal launch = %s, want failed/timeout", got)
	}
	if got := status(2); got != "scanned/" {
		t.Fatalf("large launch = %s, want scanned", got)
	}
	if got := s.Launches(); len(got) != 2 || got[0] != normal || got[1] != large {
		t.Fatalf("Launches() = %+v, want both specs in order", got)
	}
}
