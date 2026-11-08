package poller

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const homelabAccount = "482109375512"

var (
	loopTarget = awsx.Target{AccountID: homelabAccount, Region: "us-east-1"}
	loopNow    = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
)

type loopHarness struct {
	loop   *Loop
	fake   *fake.Fake
	estate *fake.Estate
	store  *store.Store
}

func newHarness(t *testing.T, estate *fake.Estate) *loopHarness {
	t.Helper()
	st := storetest.Open(t)
	f := fake.New(estate)
	p := New(st, f, Config{MaxInFlight: 4}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &loopHarness{loop: newLoop(p, loopTarget), fake: f, estate: estate, store: st}
}

func mockupHarness(t *testing.T) *loopHarness {
	return newHarness(t, fake.MockupEstate(func() time.Time { return loopNow }))
}

func (h *loopHarness) runOnce(t *testing.T) {
	t.Helper()
	if err := h.loop.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
}

func (h *loopHarness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.store.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// estateTasks returns the estate's tasks of the loop target whose last status matches (any when "").
func (h *loopHarness) estateTasks(status string) []fake.Task {
	h.estate.Mu.Lock()
	defer h.estate.Mu.Unlock()
	var out []fake.Task
	for _, task := range h.estate.Tasks {
		if task.AccountID == loopTarget.AccountID && task.Region == loopTarget.Region &&
			(status == "" || aws.ToString(task.Task.LastStatus) == status) {
			out = append(out, task)
		}
	}
	return out
}

func TestLoop_FirstCycleFillsSnapshots(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)

	h.estate.Mu.Lock()
	var wantClusters, wantServices int
	for _, c := range h.estate.Clusters {
		if c.AccountID == loopTarget.AccountID && c.Region == loopTarget.Region {
			wantClusters++
		}
	}
	for _, s := range h.estate.Services {
		if s.AccountID == loopTarget.AccountID && s.Region == loopTarget.Region {
			wantServices++
		}
	}
	wantARNs := map[string]bool{}
	for _, s := range h.estate.Services {
		if s.AccountID != loopTarget.AccountID || s.Region != loopTarget.Region {
			continue
		}
		for _, d := range s.Service.Deployments {
			wantARNs[aws.ToString(d.TaskDefinition)] = true
		}
	}
	h.estate.Mu.Unlock()
	allTasks := h.estateTasks("")
	for _, task := range allTasks {
		wantARNs[aws.ToString(task.Task.TaskDefinitionArn)] = true
	}

	if wantClusters != 3 || wantServices != 9 {
		t.Fatalf("estate sanity: clusters/services = %d/%d, want 3/9", wantClusters, wantServices)
	}
	if got := h.count(t, `select count(*) from clusters`); got != wantClusters {
		t.Errorf("clusters stored = %d, want %d", got, wantClusters)
	}
	if got := h.count(t, `select count(*) from services`); got != wantServices {
		t.Errorf("services stored = %d, want %d", got, wantServices)
	}
	if got := h.count(t, `select count(*) from tasks where last_status = 'RUNNING'`); got != len(h.estateTasks("RUNNING")) {
		t.Errorf("running tasks stored = %d, want %d", got, len(h.estateTasks("RUNNING")))
	}
	if got := h.count(t, `select count(*) from tasks where last_status = 'STOPPED'`); got != len(h.estateTasks("STOPPED")) || got == 0 {
		t.Errorf("stopped tasks stored = %d, want %d (>0)", got, len(h.estateTasks("STOPPED")))
	}
	if got := h.count(t, `select count(*) from service_events`); got == 0 {
		t.Error("no service events stored")
	}
	for arn := range wantARNs {
		ok, err := h.store.HasTaskDefinition(context.Background(), loopTarget.AccountID, loopTarget.Region, arn)
		if err != nil || !ok {
			t.Errorf("task definition %s not cached (err=%v)", arn, err)
		}
	}
}

func TestLoop_RunningTasks_PaginatesAndChunks(t *testing.T) {
	h := mockupHarness(t)
	h.estate.AddRunningTasks("core-services", "payments-api", 250)
	h.runOnce(t)

	calls := h.fake.Calls()
	if calls["ListTasks"] < 3 || calls["DescribeTasks"] < 3 {
		t.Fatalf("ListTasks/DescribeTasks calls = %d/%d, want both >= 3", calls["ListTasks"], calls["DescribeTasks"])
	}
	want := len(h.estateTasks("RUNNING"))
	if want < 250 {
		t.Fatalf("estate has only %d running tasks", want)
	}
	if got := h.count(t, `select count(*) from tasks where last_status = 'RUNNING'`); got != want {
		t.Fatalf("running tasks stored = %d, want %d", got, want)
	}
}

func TestLoop_DescribeServicesBatchesOfTen(t *testing.T) {
	estate := &fake.Estate{
		Now:      func() time.Time { return loopNow },
		Accounts: []fake.Account{{ID: homelabAccount, Name: "homelab-admin", Regions: []string{"us-east-1"}}},
		Events:   map[string][]ecstypes.ServiceEvent{},
	}
	clusterARN := "arn:aws:ecs:us-east-1:" + homelabAccount + ":cluster/bulk"
	estate.Clusters = []fake.Cluster{{AccountID: homelabAccount, Region: "us-east-1",
		Cluster: ecstypes.Cluster{ClusterArn: aws.String(clusterARN), ClusterName: aws.String("bulk"), Status: aws.String("ACTIVE")}}}
	for _, name := range []string{"s01", "s02", "s03", "s04", "s05", "s06", "s07", "s08", "s09", "s10", "s11", "s12"} {
		estate.Services = append(estate.Services, fake.Service{AccountID: homelabAccount, Region: "us-east-1", ClusterName: "bulk",
			Service: ecstypes.Service{ServiceName: aws.String(name), Status: aws.String("ACTIVE"),
				ServiceArn: aws.String("arn:aws:ecs:us-east-1:" + homelabAccount + ":service/bulk/" + name)}})
	}
	h := newHarness(t, estate)
	h.runOnce(t)

	if got := h.fake.Calls()["DescribeServices"]; got != 2 {
		t.Fatalf("DescribeServices calls = %d, want 2 (12 services in batches of 10)", got)
	}
	if got := h.count(t, `select count(*) from services`); got != 12 {
		t.Fatalf("services stored = %d, want 12", got)
	}
}

func TestLoop_SecondCycleWritesNothingWhenUnchanged(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)
	first := h.loop.writeTotals()
	if first.Inserted == 0 {
		t.Fatal("first cycle inserted nothing")
	}
	h.runOnce(t)
	second := h.loop.writeTotals()
	if second.Inserted != first.Inserted || second.Updated != first.Updated {
		t.Fatalf("second cycle wrote rows: totals %+v -> %+v", first, second)
	}
	if second.Unchanged <= first.Unchanged {
		t.Fatalf("second cycle did not compare any rows: %+v -> %+v", first, second)
	}
}

func TestLoop_TaskLeavingRunningIsCapturedAsStopped(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)

	var victim string
	for _, task := range h.estateTasks("RUNNING") {
		if aws.ToString(task.Task.Group) == "service:payments-api" {
			victim = aws.ToString(task.Task.TaskArn)
			break
		}
	}
	h.estate.StopTask(victim, "Task failed ELB health checks", 143)
	h.runOnce(t)

	var status, reason string
	err := h.store.Pool.QueryRow(context.Background(),
		`select last_status, coalesce(stopped_reason, '') from tasks where task_arn = $1`, victim).Scan(&status, &reason)
	if err != nil || status != "STOPPED" || reason != "Task failed ELB health checks" {
		t.Fatalf("victim = %q %q, %v; want STOPPED with the stop reason", status, reason, err)
	}
}

func TestLoop_TaskVanishingFromEstateIsMarkedMissing(t *testing.T) {
	h := mockupHarness(t)
	h.runOnce(t)

	h.estate.Mu.Lock()
	var victim string
	for i, task := range h.estate.Tasks {
		if aws.ToString(task.Task.LastStatus) == "RUNNING" && aws.ToString(task.Task.Group) == "service:web-frontend" {
			victim = aws.ToString(task.Task.TaskArn)
			h.estate.Tasks = append(h.estate.Tasks[:i:i], h.estate.Tasks[i+1:]...)
			break
		}
	}
	h.estate.Mu.Unlock()
	h.runOnce(t)

	var status, code string
	err := h.store.Pool.QueryRow(context.Background(),
		`select last_status, coalesce(stop_code, '') from tasks where task_arn = $1`, victim).Scan(&status, &code)
	if err != nil || status != "STOPPED" || code != "Unknown" {
		t.Fatalf("vanished task = %q %q, %v; want STOPPED/Unknown", status, code, err)
	}
}

func TestPollOnce_FillsStore(t *testing.T) {
	estate := fake.MockupEstate(func() time.Time { return loopNow })
	st := storetest.Open(t)
	if err := PollOnce(context.Background(), st, fake.New(estate), Config{MaxInFlight: 4}, loopTarget); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	keys, err := st.KnownTaskARNs(context.Background(), model.ClusterKey{AccountID: homelabAccount, Region: "us-east-1", Cluster: "core-services"}, "RUNNING")
	if err != nil || len(keys) == 0 {
		t.Fatalf("no running tasks after PollOnce: %v %v", keys, err)
	}
}

func TestLoop_UnknownAccountIsAnErrorNotAPanic(t *testing.T) {
	h := mockupHarness(t)
	h.loop.target = awsx.Target{AccountID: "000000000000", Region: "us-east-1"}
	if err := h.loop.runOnce(context.Background()); err == nil {
		t.Fatal("expected an error for an unknown account")
	}
}

func TestLoop_RunTicksEveryTierAndStopsOnCancel(t *testing.T) {
	h := mockupHarness(t)
	tick := 20 * time.Millisecond
	h.loop.p.cfg.Intervals = Intervals{Services: tick, RunningTasks: tick, FullTasks: tick, StoppedTasks: tick, Clusters: tick, ServiceList: tick}
	h.loop.p.cfg.Jitter = 5 * time.Millisecond
	for _, d := range h.loop.defs {
		d.tier.cur, d.tier.base = tick, tick
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.loop.Run(ctx); close(done) }()
	// Run's first pass (clusters, service list, services) is synchronous and database-bound: about 170 ms on an
	// idle machine, 350 ms or more under load. A fixed window would count that against the repeat cycles, so wait
	// until every tier has repeated (bounded) instead of for a fixed time.
	repeated := func() bool {
		calls := h.fake.Calls()
		for _, op := range []string{"ListClusters", "ListServices", "DescribeServices", "ListTasks", "DescribeTasks"} {
			if calls[op] < 2 {
				return false
			}
		}
		return h.count(t, `select count(*) from tasks where last_status = 'RUNNING'`) == len(h.estateTasks("RUNNING"))
	}
	for deadline := time.Now().Add(10 * time.Second); !repeated() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	calls := h.fake.Calls()
	for _, op := range []string{"ListClusters", "ListServices", "DescribeServices", "ListTasks", "DescribeTasks"} {
		if calls[op] < 2 {
			t.Errorf("%s called %d times, want a repeating tier (>= 2)", op, calls[op])
		}
	}
	if got := h.count(t, `select count(*) from tasks where last_status = 'RUNNING'`); got != len(h.estateTasks("RUNNING")) {
		t.Errorf("running tasks stored = %d, want %d", got, len(h.estateTasks("RUNNING")))
	}
}

// The scaling and target-health tiers list the stored services, so their first pass must run after
// the services tier has filled the table; otherwise every restart leaves them empty for a full interval.
func TestLoopRun_ObserveTiersSeeServicesOnFirstPass(t *testing.T) {
	h := mockupHarness(t)
	hour := time.Hour
	h.loop.p.cfg.Intervals = Intervals{Services: hour, RunningTasks: hour, FullTasks: hour, StoppedTasks: hour,
		Clusters: hour, ServiceList: hour, TargetHealth: hour, Scaling: hour, Alerts: hour}
	for _, d := range h.loop.defs {
		d.tier.cur, d.tier.base = hour, hour
	}

	var mu sync.Mutex
	servicesAtFirstRun := map[string]int{}
	for i, d := range h.loop.defs {
		name, realRun := d.tier.name, d.run
		switch name {
		case tierServices:
			// Real ECS calls take time; the observe tiers must not outrun them.
			h.loop.defs[i].run = func(ctx context.Context) error {
				time.Sleep(150 * time.Millisecond)
				return realRun(ctx)
			}
		case tierTargetHealth, tierScaling, tierAlerts:
			h.loop.defs[i].run = func(ctx context.Context) error {
				mu.Lock()
				if _, seen := servicesAtFirstRun[name]; !seen {
					servicesAtFirstRun[name] = h.count(t, `select count(*) from services`)
				}
				mu.Unlock()
				return realRun(ctx)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.loop.Run(ctx); close(done) }()
	time.Sleep(600 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{tierTargetHealth, tierScaling, tierAlerts} {
		count, ran := servicesAtFirstRun[name]
		if !ran || count == 0 {
			t.Errorf("%s first pass saw %d stored services (ran=%v), want the services tier to have filled them", name, count, ran)
		}
	}
}

// plan 06: tier timings, interval and snapshot-write counts reach the telemetry registry.
func TestLoop_ReportsTelemetry(t *testing.T) {
	reg := telemetry.New()
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	p := New(st, f, Config{MaxInFlight: 4, Telemetry: reg}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := newLoop(p, loopTarget).runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var out bytes.Buffer
	if err := reg.WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		`tier="services"`,
		`harbormaster_snapshot_rows_written_total{table="services"}`,
		`harbormaster_snapshot_rows_written_total{table="tasks"}`,
		`harbormaster_snapshot_rows_written_total{table="clusters"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics lack %s:\n%s", want, text)
		}
	}
	if len(reg.Snapshot().LoopInterval) == 0 {
		t.Error("no loop interval recorded")
	}
}

// plan 06: a fresh loop must not start the task tiers together (ECS cluster-resource-read bucket is 100 tokens).
func TestLoop_StartIsStaggeredAcrossTiers(t *testing.T) {
	h := mockupHarness(t)
	stagger := 80 * time.Millisecond
	h.loop.p.cfg.StartStagger = stagger
	hour := time.Hour
	h.loop.p.cfg.Intervals = Intervals{Services: hour, RunningTasks: hour, FullTasks: hour, StoppedTasks: hour, Clusters: hour, ServiceList: hour}
	for _, d := range h.loop.defs {
		d.tier.cur, d.tier.base = hour, hour
	}

	var mu sync.Mutex
	firstStart := map[string]time.Time{}
	for i, d := range h.loop.defs {
		switch d.tier.name {
		case tierRunningTasks, tierStoppedTasks, tierFullTasks:
			name := d.tier.name
			h.loop.defs[i].run = func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				if _, seen := firstStart[name]; !seen {
					firstStart[name] = time.Now()
				}
				return nil
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.loop.Run(ctx); close(done) }()
	// The synchronous first pass (database-bound, 170 ms idle, 350 ms+ under load) precedes the staggered
	// starts, so wait until the last tier has started (bounded) rather than for a fixed window.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		mu.Lock()
		_, lastStarted := firstStart[tierFullTasks]
		mu.Unlock()
		if lastStarted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	order := []string{tierRunningTasks, tierStoppedTasks, tierFullTasks}
	for _, name := range order {
		if firstStart[name].IsZero() {
			t.Fatalf("tier %s never ran its first cycle", name)
		}
	}
	for i := 1; i < len(order); i++ {
		if gap := firstStart[order[i]].Sub(firstStart[order[i-1]]); gap < stagger*3/4 {
			t.Errorf("%s started %v after %s, want >= %v (3/4 of the step, for scheduler jitter)", order[i], gap, order[i-1], stagger*3/4)
		}
	}
}

// plan 06: spec 5.3 bounds in-flight AWS calls per loop (not only ECS ones), so the observe, network and
// storage tiers must share the loop's semaphore.
func TestLoop_AllAWSCallsShareTheInFlightLimit(t *testing.T) {
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	f.CallLatency = 15 * time.Millisecond
	tick := 20 * time.Millisecond
	const limit = 2
	p := New(st, f, Config{
		MaxInFlight: limit,
		Intervals: Intervals{Services: tick, RunningTasks: tick, FullTasks: tick, StoppedTasks: tick, Clusters: tick,
			ServiceList: tick, TargetHealth: tick, Scaling: tick, Alerts: tick, Network: tick, Storage: tick},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	loop := newLoop(p, loopTarget)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.Run(ctx); close(done) }()
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	services := map[string]bool{}
	for key := range f.CallCounts() {
		services[strings.Split(key, "|")[2]] = true
	}
	for _, want := range []string{"ecs", "application-autoscaling", "elasticloadbalancing", "ec2", "efs"} {
		if !services[want] {
			t.Fatalf("no %s calls were made (%v); the test would not prove the shared limit", want, services)
		}
	}
	if got := f.MaxInFlight(loopTarget); got > limit {
		t.Fatalf("MaxInFlight = %d, want <= %d", got, limit)
	}
}

func TestServicesTier_StoresAWSTags(t *testing.T) {
	h := mockupHarness(t)
	staging := newLoop(h.loop.p, awsx.Target{AccountID: "731022849906", Region: "us-east-1"})
	if err := staging.runOnce(context.Background()); err != nil {
		t.Fatalf("staging runOnce: %v", err)
	}
	if got := h.count(t, `select count(*) from services
		where service_name = 'api' and cluster_name = 'staging-web'
		and raw->'Tags' @> '[{"Key":"env","Value":"staging"}]'`); got != 1 {
		t.Fatalf("staging-web api rows tagged env=staging = %d, want 1", got)
	}
}

// Legacy short-ARN services make tagging calls fail with InvalidParameterException; the services tier must still
// store the cluster's services (without tags) instead of failing the whole cluster.
func TestServicesTier_FallsBackToNoTagsOnInvalidParameter(t *testing.T) {
	st := storetest.Open(t)
	f := fake.New(fake.MockupEstate(func() time.Time { return loopNow }))
	var logs bytes.Buffer
	p := New(st, f, Config{MaxInFlight: 4}, slog.New(slog.NewTextHandler(&logs, nil)))
	loop := newLoop(p, loopTarget)
	f.FailNext("DescribeServices", &ecstypes.InvalidParameterException{Message: aws.String("short ARN")})

	if err := loop.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var n int
	if err := st.Pool.QueryRow(context.Background(), `select count(*) from services`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no services stored after the tags fallback")
	}
	if got := strings.Count(logs.String(), "without tags"); got != 1 {
		t.Fatalf("fallback warnings = %d, want 1 per cluster:\n%s", got, logs.String())
	}
}
