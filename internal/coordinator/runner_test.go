package coordinator_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/registry"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const homelab = "482109375512"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func timing() lease.Timing {
	return lease.Timing{
		TTL: 1500 * time.Millisecond, RenewEvery: 300 * time.Millisecond, Tick: 50 * time.Millisecond,
		MemberHeartbeat: 100 * time.Millisecond, MemberLive: 800 * time.Millisecond, SelfFenceAfter: time.Second,
		GiveBackEvery: 300 * time.Millisecond, GuardMinRemaining: 100 * time.Millisecond,
	}
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

// startManager runs a coordinator-only lease manager around the runner and returns a stop that waits for it.
func startManager(t *testing.T, st *store.Store, r *coordinator.Runner) (m *lease.Manager, stop func()) {
	t.Helper()
	m = &lease.Manager{Store: st, ID: uuid.New(), Roles: []string{"coordinator"}, Version: "test", Timing: timing(),
		Coordinator: r, TargetsEvery: 100 * time.Millisecond, Log: quiet()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return m, stop
}

func scanRunnerFor(st *store.Store, scanner *fake.Scanner) *scanjob.Runner {
	return &scanjob.Runner{
		Store: st, Launcher: scanner, Bucket: scanner, DB: scanner, Now: time.Now, Log: quiet(),
		Cfg: config.ScanConfig{Enabled: true, RoleName: "harbormaster-scan", Interval: 50 * time.Millisecond, TaskTimeout: 30 * time.Minute,
			DBRefresh: 24 * time.Hour, DBMaxAge: 120 * time.Hour, MaxTasks: 1, BatchSize: 25},
	}
}

func createHomelab(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.CreateAccount(context.Background(), store.AWSAccount{Name: "a", AccountID: homelab,
		RoleARN: "arn:aws:iam::" + homelab + ":role/x", ExternalID: "e", Regions: []string{"us-east-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func requestScan(t *testing.T, st *store.Store, estate *fake.Estate) {
	t.Helper()
	digest := estate.ImageDigestFor("edge-nginx:1.27.2")
	if _, _, err := st.UpsertRequestedScanImage(context.Background(), store.ScanImage{Digest: digest,
		ImageRef: homelab + ".dkr.ecr.us-east-1.amazonaws.com/edge-nginx", Tag: "1.27.2", RegistryKind: scan.KindECR,
		RegistryAccountID: homelab, RegistryRegion: "us-east-1", Repository: "edge-nginx"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Two coordinators on one database: the standby runs nothing until the holder stops, then takes over within a tick.
func TestCoordinator_TwoRunnersOneActive(t *testing.T) {
	st := storetest.Open(t)
	createHomelab(t, st)
	estate := fake.MockupEstate(time.Now)
	aScanner, bScanner := fake.NewScanner(estate), fake.NewScanner(estate)
	a := &coordinator.Runner{Store: st, Timing: timing(), Scan: scanRunnerFor(st, aScanner), Log: quiet()}
	b := &coordinator.Runner{Store: st, Timing: timing(), Scan: scanRunnerFor(st, bScanner), Log: quiet()}
	ma, stopA := startManager(t, st, a)
	waitFor(t, 5*time.Second, "a to coordinate", ma.HoldsCoordinator)
	mb, _ := startManager(t, st, b)

	requestScan(t, st, estate)
	waitFor(t, 10*time.Second, "the holder to launch the scan", func() bool { return len(aScanner.Launches()) == 1 })
	time.Sleep(400 * time.Millisecond) // several standby ticks and scan cycles
	if mb.HoldsCoordinator() || len(bScanner.Launches()) != 0 {
		t.Fatalf("the standby coordinates (holds=%v, launches=%d)", mb.HoldsCoordinator(), len(bScanner.Launches()))
	}

	stopA()
	waitFor(t, 5*time.Second, "b to take over", mb.HoldsCoordinator)
	if a.Scan.Guard != nil {
		t.Fatal("the stopped holder left its guard set")
	}
	// b's jobs are running: a second request is launched by b, not a.
	digest := estate.ImageDigestFor("edge-nginx:1.27.1")
	if _, _, err := st.UpsertRequestedScanImage(context.Background(), store.ScanImage{Digest: digest,
		ImageRef: homelab + ".dkr.ecr.us-east-1.amazonaws.com/edge-nginx", Tag: "1.27.1", RegistryKind: scan.KindECR,
		RegistryAccountID: homelab, RegistryRegion: "us-east-1", Repository: "edge-nginx"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := len(aScanner.Launches()); got != 1 {
		t.Fatalf("a launched %d tasks in total, want only the first", got)
	}
}

// blockingBucket holds the first job upload until released, as a stalled network call would.
type blockingBucket struct {
	awsx.Bucket
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBucket) Put(ctx context.Context, key string, body []byte) error {
	if strings.HasPrefix(key, "jobs/") && b.armed.CompareAndSwap(true, false) {
		close(b.entered)
		<-b.release
	}
	return b.Bucket.Put(ctx, key, body)
}

// A holder paused inside a job step (here, uploading the scan job) must not act once another process holds the lease.
func TestCoordinator_PausedHolderDoesNothingAfterTakeover(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	createHomelab(t, st)
	estate := fake.MockupEstate(time.Now)
	scanner := fake.NewScanner(estate)
	bucket := &blockingBucket{Bucket: scanner, entered: make(chan struct{}), release: make(chan struct{})}
	bucket.armed.Store(true)
	run := scanRunnerFor(st, scanner)
	run.Bucket = bucket
	a := &coordinator.Runner{Store: st, Timing: timing(), Scan: run, Log: quiet()}
	ma, _ := startManager(t, st, a)
	waitFor(t, 5*time.Second, "a to coordinate", ma.HoldsCoordinator)
	requestScan(t, st, estate)
	select {
	case <-bucket.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the holder never reached the job upload")
	}

	// Another process takes the lease over in one statement, so a's renewal cannot slip in between.
	var epoch int64
	if err := st.Pool.QueryRow(ctx, `update poll_leases set owner_id = $1, epoch = epoch + 1, expires_at = now() + interval '1 hour'
		where kind = 'coordinator' returning epoch`, uuid.New()).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	close(bucket.release)

	waitFor(t, 10*time.Second, "the paused holder to give up", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from scan_runs where status = 'failed' and error = 'coordinator lease lost'`).Scan(&n) == nil && n == 1
	})
	if got := scanner.Launches(); len(got) != 0 {
		t.Fatalf("the paused holder launched %d tasks after the takeover: %+v", len(got), got)
	}
}

type countedRegistry struct {
	inner *fake.Registry
	calls atomic.Int32
}

func (c *countedRegistry) Digest(ctx context.Context, ref registry.Ref) (string, error) {
	c.calls.Add(1)
	return c.inner.Digest(ctx, ref)
}

func (c *countedRegistry) Manifest(ctx context.Context, ref registry.Ref) ([]byte, string, error) {
	c.calls.Add(1)
	return c.inner.Manifest(ctx, ref)
}

type countingSender struct{ sent atomic.Int32 }

func (s *countingSender) Send(context.Context, store.NotificationRoute, notify.Secret, notify.Message) notify.Result {
	s.sent.Add(1)
	return notify.Result{Delivered: true, StatusCode: 200, Duration: time.Millisecond, Note: "ok"}
}

// Every job runs only on the process that holds the lease: two runners with all four jobs, one database, and a
// notification, a scan, an off-hours change and an image read each happen once and only on the holder.
func TestCoordinator_OnlyTheHolderRunsEveryJob(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	createHomelab(t, st)
	estate := fake.MockupEstate(time.Now)
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelab, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}

	// Off-hours: an always-awake schedule selecting the service.
	if _, err := storetest.Seed(st).UpsertServices(ctx, []model.Service{{Key: key, Status: "ACTIVE", Desired: 3, LaunchType: "FARGATE", TaskDefinitionARN: "arn:td/payments-api:1",
		Raw: []byte(`{"Tags":[{"Key":"env","Value":"staging"}]}`), PolledAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{Name: "always", Enabled: true, Selectors: []store.OffHoursSelector{{Kind: "aws", Key: "env", Value: "staging"}},
		Timezone: "UTC", Awake: strings.Repeat("1", 168)}, 0); err != nil {
		t.Fatal(err)
	}
	// Image platforms: a public image in the service's task definition.
	const image = "public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"
	raw, _ := json.Marshal(map[string]any{"ContainerDefinitions": []map[string]any{{"Name": "otel", "Image": image}}})
	if err := st.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{AccountID: homelab, Region: "us-east-1", ARN: "arn:td/payments-api:1", Family: "payments-api",
		Revision: 1, RequiresCompatibilities: []string{"FARGATE"}, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	// Notification: one route and one alert.
	box, err := secretbox.New("session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	secretJSON, _ := json.Marshal(notify.Secret{URL: "https://hooks.example/one"})
	sealed, err := box.Seal(secretJSON)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(notify.Kinds))
	for i, k := range notify.Kinds {
		kinds[i] = k.Kind
	}
	if _, err := st.CreateNotificationRoute(ctx, store.NotificationRoute{Name: "one", Enabled: true, Destination: notify.DestWebhook,
		Config: store.NotificationConfig{URLHost: "hooks.example"}, SecretEnc: sealed, MinSeverity: "warning", Kinds: kinds, QuietTimezone: "UTC", NotifyResolved: true}, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAlerts(ctx, awsx.Target{AccountID: homelab, Region: "us-east-1"}, []model.Alert{{Key: key, Kind: "service_failing",
		Severity: model.AlertDanger, Title: "payments-api failing", Text: "failing", At: time.Now(), Tab: "overview"}}); err != nil {
		t.Fatal(err)
	}
	requestScan(t, st, estate)

	type instance struct {
		runner  *coordinator.Runner
		scanner *fake.Scanner
		aws     *fake.Fake
		reader  *countedRegistry
		sender  *countingSender
	}
	build := func() *instance {
		in := &instance{scanner: fake.NewScanner(estate), aws: fake.New(fake.MockupEstate(time.Now)), reader: &countedRegistry{inner: fake.NewRegistry()}, sender: &countingSender{}}
		in.aws.Estate.Mu.Lock()
		in.aws.Estate.ScalingConfig[key] = &fake.ScalingSeed{Min: 2, Max: 6}
		in.aws.Estate.Mu.Unlock()
		in.runner = &coordinator.Runner{
			Store: st, Timing: timing(), Log: quiet(), Idle: func() bool { return false },
			Scan:           scanRunnerFor(st, in.scanner),
			OffHours:       &offhours.Reconciler{Applier: &offhours.Applier{Store: st, Clients: in.aws, Now: time.Now}, Store: st, Interval: 50 * time.Millisecond, Now: time.Now},
			ImagePlatforms: &platforms.Resolver{Store: st, Clients: in.aws, Public: in.reader, Interval: 50 * time.Millisecond, Recheck: time.Hour, Now: time.Now},
			Notify:         &notify.Dispatcher{Store: st, Senders: in.sender, Box: box, BaseURL: "https://hm.example.com", Interval: 50 * time.Millisecond, Now: time.Now},
			Rollups:        &rollup.Runner{Store: st, Clients: in.aws, Now: time.Now},
		}
		return in
	}
	holder, standby := build(), build()
	mh, _ := startManager(t, st, holder.runner)
	waitFor(t, 5*time.Second, "the first runner to coordinate", mh.HoldsCoordinator)
	ms, _ := startManager(t, st, standby.runner)

	waitFor(t, 15*time.Second, "the holder's jobs to act", func() bool {
		_, enrolled := st.GetOffHoursEnrolment(ctx, key)
		got, _ := st.ImagePlatformsFor(ctx, []string{image})
		return enrolled == nil && got[image].Known && len(holder.scanner.Launches()) == 1 && holder.sender.sent.Load() == 1
	})
	time.Sleep(500 * time.Millisecond) // standby ticks, further job cycles
	if ms.HoldsCoordinator() {
		t.Fatal("the standby holds the coordinator lease")
	}
	if n := len(standby.scanner.Launches()); n != 0 {
		t.Errorf("the standby launched %d scans", n)
	}
	if calls := standby.aws.Calls(); len(calls) != 0 {
		t.Errorf("the standby called AWS: %v", calls)
	}
	if n := standby.reader.calls.Load(); n != 0 {
		t.Errorf("the standby read the registry %d times", n)
	}
	if n := standby.sender.sent.Load(); n != 0 {
		t.Errorf("the standby sent %d notifications", n)
	}
	if n := holder.sender.sent.Load(); n != 1 {
		t.Errorf("the holder sent %d notifications, want exactly 1", n)
	}
	if holder.runner.ImagePlatforms.Idle == nil {
		t.Error("the image resolver was left without the idle flag")
	}
}

func TestCoordinator_RunsRollupsBeforeRetentionAtStart(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	sampledAt := today.AddDate(0, 0, -2).Add(12 * time.Hour)
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"}, Service: "api"}
	if err := storetest.Seed(st).InsertCountsHistory(ctx, sampledAt, []model.Service{{Key: key, Running: 1, Desired: 2}}); err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now().Add(-8 * 24 * time.Hour)
	if _, err := storetest.Seed(st).UpsertTasks(ctx, []model.Task{{
		Cluster: key.ClusterKey, ARN: "arn:t/old", ServiceName: "api", LastStatus: "STOPPED", DesiredStatus: "STOPPED", StoppedAt: &stoppedAt,
	}}); err != nil {
		t.Fatal(err)
	}
	// The hourly ticker never fires inside this test: whatever happened, happened at the start.
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: time.Hour,
		Rollups:   &rollup.Runner{Store: st, Clients: fake.New(fake.MockupEstate(time.Now)), Now: time.Now, SampleInterval: 5 * time.Minute, CountsRetention: 7 * 24 * time.Hour},
		Retention: store.Retention{CountsHistory: 7 * 24 * time.Hour, StoppedTasks: 7 * 24 * time.Hour}}
	holdFor(t, r)

	waitFor(t, 3*time.Second, "daily availability row", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from service_availability_daily`).Scan(&n) == nil && n == 1
	})
	waitFor(t, 3*time.Second, "old stopped task deleted", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from tasks`).Scan(&n) == nil && n == 0
	})
}

func TestCoordinator_RollupRunsBeforeRetention(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	// Three days old: inside the six-day backfill window but past the 24h counts retention, so the same tick
	// that deletes it must roll it up first.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	sampledAt := today.AddDate(0, 0, -3).Add(12 * time.Hour)
	key := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"}, Service: "api"}
	if err := storetest.Seed(st).InsertCountsHistory(ctx, sampledAt, []model.Service{
		{Key: key, Running: 1, Desired: 2}, {Key: model.ServiceKey{ClusterKey: key.ClusterKey, Service: "web"}, Running: 2, Desired: 2},
	}); err != nil {
		t.Fatal(err)
	}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: 50 * time.Millisecond,
		// The runner normally reads the same retention as the delete; widen only its backfill reach so a row that is
		// already past the 24h retention is still inside the window and the ordering is what the test observes.
		Rollups:   &rollup.Runner{Store: st, Clients: fake.New(fake.MockupEstate(time.Now)), Now: time.Now, SampleInterval: 5 * time.Minute, CountsRetention: 7 * 24 * time.Hour},
		Retention: store.Retention{CountsHistory: 24 * time.Hour}}
	holdFor(t, r)

	waitFor(t, 3*time.Second, "source rows deleted", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from service_counts_history`).Scan(&n) == nil && n == 0
	})
	var rows, below int
	if err := st.Pool.QueryRow(ctx,
		`select count(*), coalesce(sum(minutes_below), 0) from service_availability_daily where day = $1::date`,
		sampledAt.Format(time.DateOnly)).Scan(&rows, &below); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || below != 5 {
		t.Errorf("daily rows for %s = %d with %d minutes below, want 2 rows and 5 minutes", sampledAt.Format(time.DateOnly), rows, below)
	}
}

func TestCoordinator_RunsRetention(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	stoppedAt := time.Now().Add(-8 * 24 * time.Hour)
	exit := 137
	if _, err := storetest.Seed(st).UpsertTasks(ctx, []model.Task{{
		Cluster: model.ClusterKey{AccountID: "111111111111", Region: "eu-west-1", Cluster: "c"},
		ARN:     "arn:t/old", ServiceName: "api", LastStatus: "STOPPED", DesiredStatus: "STOPPED",
		StoppedAt: &stoppedAt, Containers: []model.Container{{Name: "app", ExitCode: &exit}},
	}}); err != nil {
		t.Fatal(err)
	}
	r := &coordinator.Runner{Store: st, Timing: timing(), Log: quiet(), RetentionEvery: 50 * time.Millisecond,
		Retention: store.Retention{StoppedTasks: 7 * 24 * time.Hour}}
	holdFor(t, r)

	waitFor(t, 3*time.Second, "old task deleted", func() bool {
		var n int
		return st.Pool.QueryRow(ctx, `select count(*) from tasks`).Scan(&n) == nil && n == 0
	})
}

// holdFor runs the runner's Hold until the test ends, under a real coordinator lease (retention checks it).
func holdFor(t *testing.T, r *coordinator.Runner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	if err := r.Store.EnsureCoordinatorLease(ctx); err != nil {
		t.Fatal(err)
	}
	l, ok, err := r.Store.ClaimLease(ctx, store.LeaseKey{Kind: store.LeaseCoordinator}, uuid.New(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim the coordinator lease: %v %v", ok, err)
	}
	go func() { defer wg.Done(); r.Hold(ctx, l) }()
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func TestRetentionFrom_MapsEveryPeriod(t *testing.T) {
	cfg := config.Config{Retention: config.RetentionConfig{
		AvailabilityDaily: 1 * time.Hour, CapacityDaily: 2 * time.Hour, UtilDaily: 3 * time.Hour, NotificationLog: 30 * 24 * time.Hour}}
	got := coordinator.RetentionFrom(cfg)
	if got.AvailabilityDaily != time.Hour || got.CapacityDaily != 2*time.Hour || got.UtilDaily != 3*time.Hour || got.NotificationLog != 30*24*time.Hour {
		t.Fatalf("retention = %+v, want the configured periods carried over", got)
	}
}
