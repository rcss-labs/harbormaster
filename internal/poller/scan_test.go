package poller

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func scanRunnerFor(st *store.Store, scanner *fake.Scanner) *scanjob.Runner {
	return &scanjob.Runner{
		Store: st, Launcher: scanner, Bucket: scanner, DB: scanner, Now: time.Now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg: config.ScanConfig{Enabled: true, RoleName: "harbormaster-scan", Interval: 50 * time.Millisecond, TaskTimeout: 30 * time.Minute,
			DBRefresh: 24 * time.Hour, DBMaxAge: 120 * time.Hour, MaxTasks: 1, BatchSize: 25},
	}
}

func TestSupervisor_RunsTheScanLoopOnTheLeaderOnly(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	const account = "111111111111"
	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "a", AccountID: account, RoleARN: "arn:aws:iam::" + account + ":role/x",
		ExternalID: "e", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	estate := fake.MockupEstate(time.Now)
	leaderScanner, followerScanner := fake.NewScanner(estate), fake.NewScanner(estate)
	key := randomLockKey()

	leader := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, Scan: scanRunnerFor(st, leaderScanner)}, key)
	leader.loopFactory = newLoopRecorder().factory
	startRun(t, leader)
	waitFor(t, 3*time.Second, "the first poller to lead", leader.IsLeader)

	follower := newTestPoller(st, Config{AccountsRefresh: time.Hour, RetentionEvery: time.Hour, Scan: scanRunnerFor(st, followerScanner)}, key)
	follower.loopFactory = newLoopRecorder().factory
	startRun(t, follower)

	digest := estate.ImageDigestFor("edge-nginx:1.27.2")
	if _, _, err := st.UpsertRequestedScanImage(ctx, store.ScanImage{Digest: digest, ImageRef: account + ".dkr.ecr.us-east-1.amazonaws.com/edge-nginx",
		Tag: "1.27.2", RegistryKind: scan.KindECR, RegistryAccountID: account, RegistryRegion: "us-east-1", Repository: "edge-nginx"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the leader's loop to launch the requested scan", func() bool { return len(leaderScanner.Launches()) == 1 })
	time.Sleep(300 * time.Millisecond)
	if got := len(followerScanner.Launches()); got != 0 {
		t.Fatalf("the follower launched %d tasks, want none", got)
	}
}
