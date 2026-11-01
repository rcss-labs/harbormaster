//go:build soak

// Soak tests at the target estate scale (spec 5.3): 20 account x region loops, 400 clusters, 6 000 services,
// 18 000 tasks, production default intervals, real wall-clock time. They take about 15 minutes in total; run them
// with `make soak` (needs HM_TEST_DATABASE_URL). The logged numbers are recorded in docs/ops/ecs-api-budget.md.
package poller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	// maxBucketCallsPerSecond is 50 % of the documented 20/s refill of every ECS throttle bucket
	// (docs/ops/ecs-api-budget.md, "Documented limits").
	maxBucketCallsPerSecond = 10.0
	// maxInFlightPerTarget is the ECS semaphore (HM_POLL_MAX_INFLIGHT, default 8) plus one in-flight call for each
	// tier that bypasses it: scaling (application-autoscaling) and target health (elasticloadbalancing). The fake's
	// MaxInFlight counts every AWS service of a target. With scaling disabled the soak measured exactly 8.
	maxInFlightPerTarget = 8 + 2
	// snapshotRowsAtTargetScale is 400 clusters + 6 000 services + 18 000 tasks.
	snapshotRowsAtTargetScale = 24_400
	warmUp                    = 60 * time.Second // the longest default tier interval
	cycle                     = 5 * time.Second  // the fastest tier interval
	// pgStatSettle exceeds Postgres' 10 s idle flush of per-backend table statistics.
	pgStatSettle = 12 * time.Second
	fakeLatency  = 5 * time.Millisecond
)

// ECS throttle buckets (docs/ops/ecs-api-budget.md); operations within one bucket share a token pool.
var ecsBuckets = map[string][]string{
	"service read":          {"ListServices", "DescribeServices"},
	"cluster resource read": {"ListTasks", "DescribeTasks"},
	"cluster read":          {"ListClusters", "DescribeClusters"},
}

// snapshotTables are the tables the diff writer keeps current; their row total is snapshotRowsAtTargetScale.
var snapshotTables = []string{"clusters", "services", "service_events", "tasks", "task_definitions"}

type soakEnv struct {
	st      *store.Store
	fake    *fake.Fake
	estate  *fake.Estate
	targets []awsx.Target
	cfg     Config
}

// newSoakEnv builds the target-scale estate, a fresh schema with one enabled account row per estate account and
// the production poller configuration (config defaults through ConfigFrom).
func newSoakEnv(t *testing.T, scale fake.Scale) *soakEnv {
	t.Helper()
	st := storetest.Open(t)
	estate := fake.Generate(1, scale)
	f := fake.New(estate)
	f.CallLatency = fakeLatency // before any poller starts; makes MaxInFlight meaningful

	appCfg, err := config.Load(func(name string) string {
		if name == "HM_DATABASE_URL" {
			return "postgres://soak" // Load requires a database; the soak uses storetest's store
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	env := &soakEnv{st: st, fake: f, estate: estate, cfg: ConfigFrom(appCfg)}
	env.cfg.Telemetry = telemetry.New()
	for _, a := range estate.Accounts {
		if _, err := st.CreateAccount(context.Background(), store.AWSAccount{
			Name: a.Name, AccountID: a.ID, RoleARN: "arn:aws:iam::" + a.ID + ":role/soak", ExternalID: "soak",
			Regions: a.Regions, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		for _, region := range a.Regions {
			env.targets = append(env.targets, awsx.Target{AccountID: a.ID, Region: region})
		}
	}
	return env
}

func (e *soakEnv) newPoller(cfg Config) *Poller {
	p := New(e.st, e.fake, cfg, discardLogger())
	p.lockKey = randomLockKey()
	p.retryInterval = 100 * time.Millisecond
	return p
}

// startPoller runs a poller on the environment and returns a stop function that waits for it to exit.
func (e *soakEnv) startPoller(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p := e.newPoller(e.cfg)
	go func() { _ = p.Run(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

func sleep(t *testing.T, d time.Duration, why string) {
	t.Helper()
	t.Logf("%s: waiting %v", why, d)
	time.Sleep(d)
}

// tableWrites returns n_tup_ins + n_tup_upd per table of the test schema. The reader is a dedicated connection.
func (e *soakEnv) tableWrites(t *testing.T) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	var schema string
	if err := e.st.Pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Pool.Query(ctx,
		`select relname, n_tup_ins + n_tup_upd from pg_stat_user_tables where schemaname = $1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (kv struct {
		k string
		v int64
	}, err error) {
		err = r.Scan(&kv.k, &kv.v)
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int64{}
	for _, kv := range out {
		m[kv.k] = kv.v
	}
	return m
}

// snapshotWriteDelta sums the snapshot tables' write growth between two readings and logs every table's delta.
func snapshotWriteDelta(t *testing.T, before, after map[string]int64) int64 {
	t.Helper()
	var total int64
	for table, n := range after {
		t.Logf("writes (ins+upd) %-24s %d", table, n-before[table])
	}
	for _, table := range snapshotTables {
		total += after[table] - before[table]
	}
	t.Logf("snapshot-table writes total: %d", total)
	return total
}

func TestSoak_SteadyStateCallBudget(t *testing.T) {
	env := newSoakEnv(t, fake.TargetScale)
	env.startPoller(t)
	sleep(t, warmUp, "warm-up")

	env.fake.ResetCallCounts()
	const window = 60 * time.Second
	sleep(t, window, "measuring")
	counts := env.fake.CallCounts()
	seconds := window.Seconds()

	var sumRate float64
	var nonECS uint64
	for _, target := range env.targets {
		prefix := target.AccountID + "|" + target.Region + "|"
		perOp := map[string]uint64{}
		var ecsTotal uint64
		for key, n := range counts {
			rest, ok := strings.CutPrefix(key, prefix)
			if !ok {
				continue
			}
			service, op, _ := strings.Cut(rest, "|")
			if service == "ecs" {
				perOp[op] += n
				ecsTotal += n
			} else {
				nonECS += n
			}
		}
		for bucket, ops := range ecsBuckets {
			var n uint64
			for _, op := range ops {
				n += perOp[op]
			}
			rate := float64(n) / seconds
			t.Logf("%s/%s ecs bucket %-22s %6.2f calls/s", target.AccountID, target.Region, bucket, rate)
			if rate > maxBucketCallsPerSecond {
				t.Errorf("%v: %s bucket %.2f calls/s exceeds %.1f", target, bucket, rate, maxBucketCallsPerSecond)
			}
		}
		t.Logf("%s/%s ecs total %.2f calls/s per loop, MaxInFlight %d", target.AccountID, target.Region,
			float64(ecsTotal)/seconds, env.fake.MaxInFlight(target))
		sumRate += float64(ecsTotal) / seconds
		if got := env.fake.MaxInFlight(target); got > maxInFlightPerTarget {
			t.Errorf("%v: MaxInFlight %d exceeds %d", target, got, maxInFlightPerTarget)
		}
	}
	t.Logf("sum of ECS calls/s across %d targets: %.2f (mean %.2f per loop)", len(env.targets), sumRate, sumRate/float64(len(env.targets)))
	t.Logf("non-ECS calls across all targets in window: %d (%.2f/s total, %.2f/s per loop)", nonECS,
		float64(nonECS)/seconds, float64(nonECS)/seconds/float64(len(env.targets)))
	perService := map[string]uint64{}
	for key, n := range counts {
		parts := strings.Split(key, "|")
		perService[parts[2]] += n
	}
	t.Logf("calls in window by AWS service: %v", perService)
	if limit := maxBucketCallsPerSecond * float64(len(env.targets)) * float64(len(ecsBuckets)); sumRate > limit {
		t.Errorf("sum of ECS calls/s %.2f exceeds %.1f", sumRate, limit)
	}
}

func TestSoak_DiffWritesNearZeroWhenStatic(t *testing.T) {
	env := newSoakEnv(t, fake.TargetScale)
	stop := env.startPoller(t)
	sleep(t, warmUp+pgStatSettle, "warm-up and stats flush")
	before := env.tableWrites(t)

	sleep(t, 10*cycle, "static window (10 cycles)")
	stop()
	sleep(t, pgStatSettle, "stats flush")
	total := snapshotWriteDelta(t, before, env.tableWrites(t))
	if limit := int64(snapshotRowsAtTargetScale / 100); total > limit {
		t.Errorf("static window wrote %d snapshot rows, limit %d (1 %% of %d)", total, limit, snapshotRowsAtTargetScale)
	}
}

func TestSoak_WritesTrackMutations(t *testing.T) {
	env := newSoakEnv(t, fake.TargetScale)
	stop := env.startPoller(t)
	sleep(t, warmUp+pgStatSettle, "warm-up and stats flush")
	before := env.tableWrites(t)

	// 180 changed tasks per cycle across the estate: 9 per target.
	perTarget := 180 / len(env.targets)
	const rounds = 10
	for i := 0; i < rounds; i++ {
		for _, target := range env.targets {
			env.estate.MutateTasks(target, perTarget)
		}
		time.Sleep(cycle)
	}
	// fullTasks (30 s) is the tier that re-describes every task; let it pick up the last mutations.
	sleep(t, 35*time.Second, "drain so the last mutations are polled")
	stop()
	sleep(t, pgStatSettle, "stats flush")
	total := snapshotWriteDelta(t, before, env.tableWrites(t))
	mutated := int64(rounds * perTarget * len(env.targets))
	t.Logf("mutations applied: %d (draws; repeats can coalesce or flip back)", mutated)
	if limit := 3 * mutated; total > limit {
		t.Errorf("wrote %d snapshot rows for %d mutations, limit %d", total, mutated, limit)
	}
	// MutateTasks draws at random, so about mutated^2 / (2 x 18 000) draws hit a task twice and coalesce
	// (about 90 of 1 800, more when a second flip restores the original state); allow 10 %.
	if floor := mutated * 9 / 10; total < floor {
		t.Errorf("wrote %d snapshot rows for %d mutations, want at least %d; changes were not stored", total, mutated, floor)
	}
}

func TestSoak_BackoffUnderThrottle(t *testing.T) {
	env := newSoakEnv(t, fake.TargetScale)
	registry := env.cfg.Telemetry
	slow := env.targets[0]
	key := func(target awsx.Target) string {
		return target.AccountID + "|" + target.Region + "|" + tierServices
	}
	base := env.cfg.Intervals.Services

	env.fake.SetRateLimit(slow, 3, 3)
	env.startPoller(t)

	// First throttled cycle doubles the interval; lift the limit as soon as it has backed off so recovery
	// (five clean cycles per halving) fits in the test.
	var peak time.Duration
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		peak = registry.Snapshot().LoopInterval[key(slow)]
		if peak >= 10*time.Second {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("%s services interval after throttling: %v", slow.AccountID+"/"+slow.Region, peak)
	if peak < 10*time.Second || peak > 60*time.Second {
		t.Fatalf("throttled services interval %v, want within [10s, 60s]", peak)
	}
	snap := registry.Snapshot()
	for _, target := range env.targets[1:] {
		if got := snap.LoopInterval[key(target)]; got != 0 && got != base {
			t.Errorf("%v: services interval %v, want default %v", target, got, base)
		}
	}
	env.fake.SetRateLimit(slow, 0, 0)
	recoveredBy := time.Now().Add(5 * time.Minute)
	for time.Now().Before(recoveredBy) {
		if registry.Snapshot().LoopInterval[key(slow)] == base {
			t.Logf("services interval back to %v after %v", base, 5*time.Minute-time.Until(recoveredBy))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("services interval %v did not return to %v", registry.Snapshot().LoopInterval[key(slow)], base)
}

func TestSoak_SingleLeaderAcrossTwoPollers(t *testing.T) {
	// One loop is enough to see which poller polls; the target scale is not the point here.
	scale := fake.TargetScale
	scale.Accounts, scale.RegionsPerAccount = 1, 1
	env := newSoakEnv(t, scale)
	key := env.newPoller(env.cfg).lockKey

	type instance struct {
		name   string
		reg    *telemetry.Registry
		cancel context.CancelFunc
		done   chan error
	}
	var instances []*instance
	for _, name := range []string{"A", "B"} {
		cfg := env.cfg
		cfg.Telemetry = telemetry.New()
		p := env.newPoller(cfg)
		p.lockKey = key
		cancel, done := startRun(t, p)
		instances = append(instances, &instance{name: name, reg: cfg.Telemetry, cancel: cancel, done: done})
	}

	leaderOf := func() (leader, follower *instance) {
		for i, in := range instances {
			if in.reg.Snapshot().Leader {
				if leader != nil {
					return nil, nil // two leaders: caller treats as not settled and the final check fails
				}
				leader, follower = in, instances[1-i]
			}
		}
		return leader, follower
	}
	waitFor(t, 30*time.Second, "one leader polling", func() bool {
		leader, _ := leaderOf()
		return leader != nil && len(leader.reg.Snapshot().LoopInterval) > 0
	})
	leader, follower := leaderOf()
	if leader == nil {
		t.Fatal("want exactly one leader")
	}
	t.Logf("leader %s (loop entries %d), follower %s (loop entries %d)", leader.name,
		len(leader.reg.Snapshot().LoopInterval), follower.name, len(follower.reg.Snapshot().LoopInterval))
	if got := follower.reg.Snapshot().LoopInterval; len(got) != 0 {
		t.Errorf("follower recorded loop runs: %v", got)
	}

	start := time.Now()
	leader.cancel()
	<-leader.done
	waitFor(t, 10*time.Second, "the follower to become leader", func() bool { return follower.reg.Snapshot().Leader })
	t.Logf("failover took %v", time.Since(start))
	waitFor(t, 30*time.Second, "the new leader polling", func() bool { return len(follower.reg.Snapshot().LoopInterval) > 0 })
}
