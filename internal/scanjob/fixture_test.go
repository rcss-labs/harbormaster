package scanjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const (
	homelab = "482109375512"
	staging = "731022849906"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countingSource counts the reads of the fake DB source.
type countingSource struct {
	DBSource
	fetches, opens atomic.Int32
}

func (s *countingSource) FetchLatest(ctx context.Context) ([]byte, error) {
	s.fetches.Add(1)
	return s.DBSource.FetchLatest(ctx)
}

func (s *countingSource) OpenArchive(ctx context.Context, path string) (io.ReadCloser, error) {
	s.opens.Add(1)
	return s.DBSource.OpenArchive(ctx, path)
}

// failingSource is an upstream that cannot be reached.
type failingSource struct{}

func (failingSource) FetchLatest(context.Context) ([]byte, error) {
	return nil, errors.New("boom: upstream down")
}

func (failingSource) OpenArchive(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("boom: upstream down")
}

// errLauncher cannot start tasks.
type errLauncher struct{ calls atomic.Int32 }

func (l *errLauncher) RunTask(context.Context, awsx.LaunchSpec) (string, error) {
	l.calls.Add(1)
	return "", errors.New("ecs: capacity unavailable")
}

func (l *errLauncher) DescribeTask(context.Context, string) (awsx.TaskState, error) {
	return awsx.TaskState{}, awsx.ErrTaskNotFound
}

func (l *errLauncher) StopTask(context.Context, string, string) error { return nil }

// scriptedLauncher plays a hostile or odd scanner: it reads the run's job from the bucket and writes whatever the script
// returns, then reports the task as stopped with exit code 0.
type scriptedLauncher struct {
	bucket awsx.Bucket
	script func(job scan.Job) map[string][]byte

	mu       sync.Mutex
	launches []awsx.LaunchSpec
}

func (l *scriptedLauncher) RunTask(ctx context.Context, spec awsx.LaunchSpec) (string, error) {
	l.mu.Lock()
	l.launches = append(l.launches, spec)
	n := len(l.launches)
	l.mu.Unlock()
	data, err := l.bucket.Get(ctx, scan.JobKey(spec.RunID), scan.MaxManifestBytes)
	if err != nil {
		return "", err
	}
	var job scan.Job
	if err := json.Unmarshal(data, &job); err != nil {
		return "", err
	}
	for key, body := range l.script(job) {
		if err := l.bucket.Put(ctx, key, body); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("arn:scripted/%d", n), nil
}

func (l *scriptedLauncher) DescribeTask(context.Context, string) (awsx.TaskState, error) {
	exit := int32(0)
	return awsx.TaskState{LastStatus: "STOPPED", ExitCode: &exit}, nil
}

func (l *scriptedLauncher) StopTask(context.Context, string, string) error { return nil }

func (l *scriptedLauncher) Launches() []awsx.LaunchSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]awsx.LaunchSpec(nil), l.launches...)
}

func testConfig() config.ScanConfig {
	return config.ScanConfig{
		Enabled: true, RoleName: "harbormaster-scan", Interval: time.Hour, TaskTimeout: 30 * time.Minute,
		DBRefresh: 24 * time.Hour, DBMaxAge: 120 * time.Hour, MaxTasks: 1, BatchSize: 25, MaxImageMB: 4096,
		ClusterARN: "arn:aws:ecs:us-east-1:000000000000:cluster/harbormaster",
	}
}

type fixture struct {
	t       *testing.T
	st      *store.Store
	estate  *fake.Estate
	scanner *fake.Scanner
	clock   *clock
	source  *countingSource
	runner  *Runner
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := storetest.Open(t)
	clk := &clock{t: time.Now().Truncate(time.Second)}
	estate := fake.MockupEstate(clk.Now)
	scanner := fake.NewScanner(estate)
	fx := &fixture{t: t, st: st, estate: estate, scanner: scanner, clock: clk, source: &countingSource{DBSource: scanner}}
	fx.runner = fx.newRunner()
	for account, external := range map[string]string{homelab: "ext-home", staging: "ext-stage"} {
		_, err := st.CreateAccount(context.Background(), store.AWSAccount{
			Name: "acct-" + account, AccountID: account, RoleARN: "arn:aws:iam::" + account + ":role/harbormaster-access",
			ExternalID: external, Regions: []string{"us-east-1"}, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

func (fx *fixture) newRunner() *Runner {
	return &Runner{Store: fx.st, Launcher: fx.scanner, Bucket: fx.scanner, DB: fx.source, Cfg: testConfig(), Now: fx.clock.Now, Log: quietLog()}
}

func (fx *fixture) cycle() {
	fx.t.Helper()
	if err := fx.runner.Cycle(context.Background()); err != nil {
		fx.t.Fatalf("Cycle: %v", err)
	}
}

// queue asks for a scan of repository:tag on behalf of one service in the homelab account.
func (fx *fixture) queue(repository, tag string) string {
	return fx.queueFor(homelab, repository, tag, fx.clock.Now())
}

func (fx *fixture) queueFor(account, repository, tag string, at time.Time) string {
	fx.t.Helper()
	ctx := context.Background()
	digest := fx.estate.ImageDigestFor(repository + ":" + tag)
	image := fmt.Sprintf("%s.dkr.ecr.us-east-1.amazonaws.com/%s", account, repository)
	_, _, err := fx.st.UpsertRequestedScanImage(ctx, store.ScanImage{
		Digest: digest, ImageRef: image, Tag: tag, RegistryKind: scan.KindECR, RegistryAccountID: account,
		RegistryRegion: "us-east-1", Repository: repository, Platform: "linux/amd64"}, at)
	if err != nil {
		fx.t.Fatal(err)
	}
	_, err = fx.st.AddScanRequest(ctx, store.ScanRequest{Digest: digest,
		Key:       model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: "us-east-1", Cluster: "core-services"}, Service: "svc-" + repository},
		Container: "app", Image: image + ":" + tag, Tag: tag, Source: "manual", RequestedAt: at})
	if err != nil {
		fx.t.Fatal(err)
	}
	return digest
}

func (fx *fixture) image(digest string) store.ScanImage {
	fx.t.Helper()
	img, err := fx.st.GetScanImage(context.Background(), digest)
	if err != nil {
		fx.t.Fatal(err)
	}
	return img
}

type runRow struct {
	Status                 string
	ImagesOK, ImagesFailed int
	DBBuiltAt              *time.Time
	DBChecksum, Error, ARN string
	ObjectsDeleted         bool
}

func (fx *fixture) run(id int64) runRow {
	fx.t.Helper()
	var r runRow
	err := fx.st.Pool.QueryRow(context.Background(), `select status, coalesce(images_ok, 0), coalesce(images_failed, 0), db_built_at,
		  coalesce(db_checksum, ''), coalesce(error, ''), coalesce(task_arn, ''), objects_deleted_at is not null from scan_runs where id = $1`, id).
		Scan(&r.Status, &r.ImagesOK, &r.ImagesFailed, &r.DBBuiltAt, &r.DBChecksum, &r.Error, &r.ARN, &r.ObjectsDeleted)
	if err != nil {
		fx.t.Fatal(err)
	}
	return r
}

func (fx *fixture) runCount() int {
	fx.t.Helper()
	var n int
	if err := fx.st.Pool.QueryRow(context.Background(), `select count(*) from scan_runs`).Scan(&n); err != nil {
		fx.t.Fatal(err)
	}
	return n
}

func (fx *fixture) job(runID int64) scan.Job {
	fx.t.Helper()
	data, ok := fx.scanner.Objects()[scan.JobKey(runID)]
	if !ok {
		fx.t.Fatalf("no job object for run %d", runID)
	}
	var job scan.Job
	if err := json.Unmarshal(data, &job); err != nil {
		fx.t.Fatal(err)
	}
	return job
}

// putMirrorDB writes a latest.json as an operator mirror would.
func putMirrorDB(t *testing.T, scanner *fake.Scanner, builtAt time.Time) scan.DBLatest {
	t.Helper()
	latest := scan.DBLatest{BuiltAt: builtAt, Checksum: "sha256:" + strings.Repeat("a", 64), ArchiveKey: "grype-db/v6/mirror.tar.zst"}
	data, _ := json.Marshal(latest)
	if err := scanner.Put(context.Background(), scan.DBLatestKey, data); err != nil {
		t.Fatal(err)
	}
	return latest
}

func manifestBody(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func cleanResult(digest string, vulnID, severity string) []byte {
	data, _ := json.Marshal(scan.Result{Digest: digest, PlatformDigest: digest, Platform: "linux/amd64", Status: scan.StatusScanned,
		Packages: 5, Findings: []scan.Finding{{VulnID: vulnID, Severity: severity, PackageName: "libx", PackageVersion: "1.0"}}})
	return data
}

// deniedBucket reads nothing: every Get is denied.
type deniedBucket struct{ awsx.Bucket }

func (deniedBucket) Get(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("operation error S3: GetObject, AccessDenied")
}

func storeState(refreshingSince *time.Time) store.ScanDBState {
	return store.ScanDBState{RefreshingSince: refreshingSince}
}
