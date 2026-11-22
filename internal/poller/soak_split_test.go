//go:build soak

// Soak tests for the split services at the design's estate (AWS call budget, "Estate assumptions"): 12 account x region
// slices, 300 clusters, the largest slice with 50, 15 services and 45 tasks per cluster. They add what the single-poller
// soak cannot show: three pollers sharing the slices, a graceful stop and a kill part-way, the cycle time of the largest
// slice with every write fenced by its lease, and the AWS call rates of the largest slice with 10 and 50 focused pages.
// Run them with `make soak` (needs HM_TEST_DATABASE_URL).
package poller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

const (
	splitSlices            = 12
	splitLargestClusters   = 50
	splitClustersTotal     = 300
	splitServicesPerClust  = 15
	splitTasksPerService   = 3
	splitPollerPool        = 8 // HM_DB_MAX_CONNS default for a poller task
	realisticCallLatency   = 25 * time.Millisecond
	servicesTierDefault    = 30 * time.Second // HM_POLL_SERVICES default
	focusBudgetPerCategory = 5.0              // HM_FOCUS_MAX_CALLS_PER_SEC default
	focusWindow            = time.Minute
)

// splitEstate builds the design's estate: 12 slices (3 accounts x 4 regions), 300 clusters, 50 of them in the first
// slice and the other 250 spread over the other 11 (23 in eight of them, 22 in three).
func splitEstate(t *testing.T) *fake.Estate {
	t.Helper()
	e := fake.Generate(1, fake.Scale{Accounts: 3, RegionsPerAccount: 4, ClustersPerRegion: splitLargestClusters,
		ServicesPerCluster: splitServicesPerClust, TasksPerService: splitTasksPerService})
	keep := map[string]int{}
	order := 0
	for _, a := range e.Accounts {
		for _, region := range a.Regions {
			switch {
			case order == 0:
				keep[a.ID+"|"+region] = splitLargestClusters
			case order <= 8:
				keep[a.ID+"|"+region] = 23
			default:
				keep[a.ID+"|"+region] = 22
			}
			order++
		}
	}
	clusterIndex := func(name string) int {
		n, err := strconv.Atoi(strings.TrimPrefix(name, "cluster-"))
		if err != nil {
			t.Fatalf("unexpected generated cluster name %q", name)
		}
		return n
	}
	serviceIndex := func(name string) int {
		n, err := strconv.Atoi(strings.TrimPrefix(name, "svc-"))
		if err != nil {
			t.Fatalf("unexpected generated service name %q", name)
		}
		return n
	}
	e.Clusters = slices.DeleteFunc(e.Clusters, func(c fake.Cluster) bool {
		return clusterIndex(aws.ToString(c.Cluster.ClusterName)) >= keep[c.AccountID+"|"+c.Region]
	})
	e.Services = slices.DeleteFunc(e.Services, func(s fake.Service) bool {
		return clusterIndex(s.ClusterName) >= keep[s.AccountID+"|"+s.Region]
	})
	e.Tasks = slices.DeleteFunc(e.Tasks, func(task fake.Task) bool {
		return clusterIndex(task.ClusterName) >= keep[task.AccountID+"|"+task.Region]
	})
	e.TaskDefinitions = slices.DeleteFunc(e.TaskDefinitions, func(td fake.TaskDefinition) bool {
		return serviceIndex(aws.ToString(td.Definition.Family)) >= keep[td.AccountID+"|"+td.Region]*splitServicesPerClust
	})
	perSlice := map[string]int{}
	for _, c := range e.Clusters {
		perSlice[c.AccountID+"|"+c.Region]++
	}
	total, largest := 0, 0
	for _, n := range perSlice {
		total += n
		largest = max(largest, n)
	}
	if len(perSlice) != splitSlices || total != splitClustersTotal || largest != splitLargestClusters {
		t.Fatalf("estate: %d slices, %d clusters, largest %d; want %d, %d, %d", len(perSlice), total, largest,
			splitSlices, splitClustersTotal, splitLargestClusters)
	}
	return e
}

// schemaName returns the schema the soak store works in.
func (e *soakEnv) schemaName(t *testing.T) string {
	t.Helper()
	var schema string
	if err := e.st.Pool.QueryRow(context.Background(), `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

// openStore opens one more store on the soak schema, as another task would, with a pool of maxConns. A non-empty host
// replaces the database address in the URL (the address of a pgProxy).
func (e *soakEnv) openStore(t *testing.T, host string, maxConns int) *store.Store {
	t.Helper()
	raw := os.Getenv("HM_TEST_DATABASE_URL")
	if host != "" {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		u.Host = host
		raw = u.String()
	}
	st, err := store.OpenWithOptions(context.Background(), raw, store.OpenOptions{
		RuntimeParams: map[string]string{"search_path": e.schemaName(t)}, MaxConns: maxConns})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// pgProxy is a TCP proxy in front of Postgres that can be cut for good: a poller behind it is partitioned from the
// database and can release nothing, as a killed task cannot.
type pgProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startPGProxy(t *testing.T) *pgProxy {
	t.Helper()
	u, err := url.Parse(os.Getenv("HM_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &pgProxy{ln: ln}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			upstream, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, upstream)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
		}
	}()
	t.Cleanup(p.Cut)
	return p
}

func (p *pgProxy) Addr() string { return p.ln.Addr().String() }

// Cut closes the listener and every open connection; nothing connects through the proxy again.
func (p *pgProxy) Cut() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// ownership records which poller runs a loop for which slice, across every poller of a test.
type ownership struct {
	mu         sync.Mutex
	active     map[awsx.Target]map[string]int
	violations []string
}

func newOwnership() *ownership { return &ownership{active: map[awsx.Target]map[string]int{}} }

func (o *ownership) enter(poller string, t awsx.Target) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active[t] == nil {
		o.active[t] = map[string]int{}
	}
	o.active[t][poller]++
	if n := o.countLocked(t); n > 1 {
		o.violations = append(o.violations, fmt.Sprintf("%d loops for %v at %s: %v", n, t, time.Now().Format(time.TimeOnly), o.active[t]))
	}
}

func (o *ownership) leave(poller string, t awsx.Target) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active[t][poller]--
}

func (o *ownership) countLocked(t awsx.Target) int {
	n := 0
	for _, c := range o.active[t] {
		n += c
	}
	return n
}

// owners returns, per slice, the pollers that run a loop for it now.
func (o *ownership) owners() map[awsx.Target][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[awsx.Target][]string{}
	for t, byPoller := range o.active {
		for name, n := range byPoller {
			if n > 0 {
				out[t] = append(out[t], name)
			}
		}
	}
	return out
}

func (o *ownership) violated() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.violations)
}

// splitPoller is one poller task of a multi-poller test.
type splitPoller struct {
	name   string
	p      *Poller
	reg    *telemetry.Registry
	cancel context.CancelFunc
	done   chan error
}

// startSplitPoller runs a poller on its own store, with its own telemetry, sharing the legacy gate key.
func (e *soakEnv) startSplitPoller(t *testing.T, name string, st *store.Store, gateKey int64, rec *ownership) *splitPoller {
	t.Helper()
	cfg := e.cfg
	cfg.Telemetry = telemetry.New()
	p := New(st, e.fake, cfg, discardLogger())
	p.lockKey = gateKey
	p.retryInterval = 100 * time.Millisecond
	if rec != nil {
		p.loopFactory = func(ctx context.Context, target awsx.Target, w *store.SliceWriter) {
			rec.enter(name, target)
			defer rec.leave(name, target)
			p.defaultLoopFactory(ctx, target, w)
		}
	}
	sp := &splitPoller{name: name, p: p, reg: cfg.Telemetry}
	ctx, cancel := context.WithCancel(context.Background())
	sp.cancel = cancel
	sp.done = make(chan error, 1)
	go func() { sp.done <- p.Run(ctx); close(sp.done) }()
	t.Cleanup(func() { cancel(); <-sp.done })
	return sp
}

// metricValue sums the samples of a metric (all label sets) in the registry's Prometheus text.
func promSum(t *testing.T, reg *telemetry.Registry, name string) float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
			v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
			if err == nil {
				sum += v
			}
		}
	}
	return sum
}

func fencedRejected(t *testing.T, pollers ...*splitPoller) float64 {
	var n float64
	for _, p := range pollers {
		n += promSum(t, p.reg, "harbormaster_fenced_writes_rejected_total")
	}
	return n
}

func TestSoak_ThreePollersOneOwnerPerSlice(t *testing.T) {
	env := newSoakEnvFor(t, splitEstate(t))
	if len(env.targets) != splitSlices {
		t.Fatalf("%d targets, want %d", len(env.targets), splitSlices)
	}
	timing := lease.DefaultTiming()
	rec := newOwnership()
	gateKey := randomLockKey()
	proxy := startPGProxy(t)

	// Stores are closed after their poller has stopped (cleanups run last in, first out).
	stA, stB, stC := env.openStore(t, "", splitPollerPool), env.openStore(t, "", splitPollerPool), env.openStore(t, proxy.Addr(), splitPollerPool)
	t.Cleanup(stA.Close)
	t.Cleanup(stB.Close)
	t.Cleanup(stC.Close)
	a := env.startSplitPoller(t, "A", stA, gateKey, rec)
	b := env.startSplitPoller(t, "B", stB, gateKey, rec)
	c := env.startSplitPoller(t, "C", stC, gateKey, rec)
	all := []*splitPoller{a, b, c}

	ownedBy := func(names ...string) bool {
		owners := rec.owners()
		if len(owners) != splitSlices {
			return false
		}
		for _, who := range owners {
			if len(who) != 1 || !slices.Contains(names, who[0]) {
				return false
			}
		}
		return true
	}
	distribution := func() string {
		count := map[string]int{}
		for _, who := range rec.owners() {
			for _, name := range who {
				count[name]++
			}
		}
		return fmt.Sprint(count)
	}

	const (
		total       = 5 * time.Minute
		stopBAt     = 90 * time.Second
		killCAt     = 150 * time.Second
		sampleEvery = 500 * time.Millisecond
	)
	waitFor(t, 2*time.Minute, "every slice owned by one of the three pollers", func() bool { return ownedBy("A", "B", "C") })
	t.Logf("all %d slices owned: %s", splitSlices, distribution())
	start := time.Now()

	var stoppedB, killedC bool
	var stopBTook, killTook time.Duration
	var killedAt, stoppedAt time.Time
	var samples int
	for time.Since(start) < total {
		time.Sleep(sampleEvery)
		samples++
		if v := rec.violated(); len(v) > 0 {
			t.Fatalf("a slice had two loops at once (%d samples in): %v", samples, v)
		}
		for target, who := range rec.owners() {
			if len(who) > 1 {
				t.Fatalf("slice %v has loops of %v at once", target, who)
			}
		}
		elapsed := time.Since(start)

		if !stoppedB && elapsed >= stopBAt {
			if n := fencedRejected(t, all...); n != 0 {
				t.Errorf("harbormaster_fenced_writes_rejected_total = %v before any takeover, want 0", n)
			}
			stoppedAt = time.Now()
			stoppedB = true
			b.cancel()
			<-b.done
			t.Logf("poller B stopped gracefully after %v (run returned in %v)", elapsed.Round(time.Second), time.Since(stoppedAt).Round(time.Millisecond))
		}
		if stoppedB && stopBTook == 0 && ownedBy("A", "C") {
			stopBTook = time.Since(stoppedAt)
			t.Logf("every slice owned by A or C %v after B stopped: %s", stopBTook.Round(time.Millisecond), distribution())
		}
		if !killedC && stopBTook > 0 && elapsed >= killCAt {
			if n := fencedRejected(t, all...); n != 0 {
				t.Errorf("harbormaster_fenced_writes_rejected_total = %v after a graceful stop, want 0", n)
			}
			killedAt = time.Now()
			proxy.Cut()
			killedC = true
			t.Logf("poller C cut off from the database after %v", elapsed.Round(time.Second))
		}
		if killedC && killTook == 0 && ownedBy("A") {
			killTook = time.Since(killedAt)
			t.Logf("every slice owned by A %v after C was killed (lease TTL %v + tick %v)", killTook.Round(time.Millisecond), timing.TTL, timing.Tick)
		}
	}
	if !killedC || killTook == 0 {
		t.Fatalf("poller C was not killed or its slices were never taken over (killed %v, took %v)", killedC, killTook)
	}
	if limit := timing.TTL + timing.Tick + 5*time.Second; killTook > limit {
		t.Errorf("takeover after the kill took %v, want within TTL + tick (%v) plus 5 s of sampling and claiming", killTook, timing.TTL+timing.Tick)
	}
	if stopBTook > timing.Tick+10*time.Second {
		t.Errorf("takeover after a graceful stop took %v, want within about one tick (%v)", stopBTook, timing.Tick)
	}
	t.Logf("fenced writes rejected across the pollers over the whole run (a takeover happened): %v; samples %d", fencedRejected(t, all...), samples)
}

// durationBuckets returns the cumulative bucket counts (upper bound -> count) of the services tier histogram of one
// slice, and its sum and count.
func servicesTierHistogram(t *testing.T, reg *telemetry.Registry, target awsx.Target) (buckets map[float64]uint64, sum float64, count uint64) {
	t.Helper()
	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	buckets = map[float64]uint64{}
	account, region := `account="`+target.AccountID+`"`, `region="`+target.Region+`"`
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "harbormaster_poll_loop_duration_seconds") || !strings.Contains(line, account) ||
			!strings.Contains(line, region) || !strings.Contains(line, `tier="services"`) {
			continue
		}
		value, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
		if err != nil {
			t.Fatalf("metric line %q: %v", line, err)
		}
		switch name, _, _ := strings.Cut(line, "{"); name {
		case "harbormaster_poll_loop_duration_seconds_bucket":
			le := line[strings.Index(line, `le="`)+4:]
			le = le[:strings.IndexByte(le, '"')]
			bound := math.Inf(1)
			if le != "+Inf" {
				bound, _ = strconv.ParseFloat(le, 64)
			}
			buckets[bound] = uint64(value)
		case "harbormaster_poll_loop_duration_seconds_sum":
			sum = value
		case "harbormaster_poll_loop_duration_seconds_count":
			count = uint64(value)
		}
	}
	return buckets, sum, count
}

// quantileBound returns the upper bound of the bucket that holds the q quantile of the cycles between two readings.
func quantileBound(before, after map[float64]uint64, q float64) (bound float64, cycles uint64) {
	bounds := make([]float64, 0, len(after))
	for b := range after {
		bounds = append(bounds, b)
	}
	sort.Float64s(bounds)
	cycles = after[math.Inf(1)] - before[math.Inf(1)]
	need := uint64(math.Ceil(q * float64(cycles)))
	for _, b := range bounds {
		if after[b]-before[b] >= need {
			return b, cycles
		}
	}
	return math.Inf(1), cycles
}

func TestSoak_SliceCycleTimeWithFencedWrites(t *testing.T) {
	env := newSoakEnvFor(t, splitEstate(t))
	env.fake.CallLatency = realisticCallLatency // tens of milliseconds per regional ECS call, not the fake's 5 ms
	st := env.openStore(t, "", splitPollerPool)
	t.Cleanup(st.Close)
	largest := env.targets[0]
	if env.cfg.MaxInFlight != splitPollerPool {
		t.Fatalf("poll pool = %d, want the default %d", env.cfg.MaxInFlight, splitPollerPool)
	}
	sp := env.startSplitPoller(t, "A", st, randomLockKey(), nil)

	sleep(t, warmUp, "warm-up (first full cycles of every tier)")
	before, sumBefore, countBefore := servicesTierHistogram(t, sp.reg, largest)
	const window = 5 * time.Minute
	sleep(t, window, "measuring the largest slice's services tier")
	after, sumAfter, countAfter := servicesTierHistogram(t, sp.reg, largest)

	interval := env.cfg.Intervals.Services
	if interval != servicesTierDefault {
		t.Fatalf("services interval = %v, want the default %v", interval, servicesTierDefault)
	}
	p95, cycles := quantileBound(before, after, 0.95)
	mean := time.Duration((sumAfter - sumBefore) / float64(countAfter-countBefore) * float64(time.Second))
	t.Logf("largest slice %s/%s (%d clusters, %d services): %d services cycles in %v, mean %v, p95 within the %vs bucket, interval %v, fenced writes rejected %v, call latency %v",
		largest.AccountID, largest.Region, splitLargestClusters, splitLargestClusters*splitServicesPerClust, cycles, window,
		mean.Round(time.Millisecond), p95, interval, fencedRejected(t, sp), realisticCallLatency)
	if cycles < 8 {
		t.Fatalf("only %d cycles in the window, want at least 8", cycles)
	}
	if bound := interval.Seconds() / 2; p95 > bound {
		t.Errorf("95th percentile cycle time is in the %vs bucket, want under half the %v interval (%vs)", p95, interval, bound)
	}
	if n := fencedRejected(t, sp); n != 0 {
		t.Errorf("fenced writes rejected = %v with one poller, want 0", n)
	}
	// Every slice's loop is current: nothing was starved by the largest.
	for _, target := range env.targets {
		_, _, n := servicesTierHistogram(t, sp.reg, target)
		if n == 0 {
			t.Errorf("slice %v never completed a services cycle", target)
		}
	}
}

// callRecorder counts the fake's calls of one slice by source and throttle bucket.
type callRecorder struct {
	target awsx.Target
	mu     sync.Mutex
	counts map[string]uint64 // "<source>|<bucket>"
}

func (r *callRecorder) onCall(ctx context.Context, t awsx.Target, service, op, _ string) {
	if t != r.target {
		return
	}
	bucket := "elb"
	if service == "ecs" {
		bucket = "other ecs"
		for name, ops := range ecsBuckets {
			if slices.Contains(ops, op) {
				bucket = name
			}
		}
	} else if service != "elasticloadbalancing" {
		return
	}
	r.mu.Lock()
	r.counts[string(awsx.SourceFrom(ctx))+"|"+bucket]++
	r.mu.Unlock()
}

func (r *callRecorder) snapshot() map[string]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.counts)
}

func TestSoak_FocusCallRatesWithinBudget(t *testing.T) {
	env := newSoakEnvFor(t, splitEstate(t))
	env.fake.CallLatency = realisticCallLatency
	largest := env.targets[0]
	recorder := &callRecorder{target: largest, counts: map[string]uint64{}}
	env.fake.OnCall = recorder.onCall

	registry := focus.NewMemoryRegistry(time.Now)
	cfg := env.cfg
	cfg.Registry, cfg.Bus = registry, signal.NewMemoryBus()
	cfg.Telemetry = telemetry.New()
	st := env.openStore(t, "", splitPollerPool)
	t.Cleanup(st.Close)
	p := New(st, env.fake, cfg, discardLogger())
	p.lockKey = randomLockKey()
	p.retryInterval = 100 * time.Millisecond
	startRun(t, p)

	// Half service pages, half cluster pages, all in the largest slice. Users are spread so the per-user cap does not
	// decide the count: five objects each.
	objects := func(n int) []focus.Object {
		var out []focus.Object
		for i := 0; i < n; i++ {
			cluster := model.ClusterKey{AccountID: largest.AccountID, Region: largest.Region, Cluster: fmt.Sprintf("cluster-%02d", i)}
			if i%2 == 0 {
				// the first service of cluster i is svc-(i*15)
				out = append(out, focus.Object{Kind: focus.KindService, Cluster: cluster, Service: fmt.Sprintf("svc-%03d", i*splitServicesPerClust)})
			} else {
				out = append(out, focus.Object{Kind: focus.KindCluster, Cluster: cluster})
			}
		}
		return out
	}
	heartbeat := func(ctx context.Context, objs []focus.Object) {
		lim := focus.Limits{TTL: cfg.Focus.TTL, MaxObjects: cfg.Focus.MaxObjects, MaxPerUser: cfg.Focus.MaxPerUser}
		for i, o := range objs {
			if admission, err := registry.Heartbeat(ctx, o, int64(i/cfg.Focus.MaxPerUser+1), lim); err != nil || (admission != focus.Admitted && admission != focus.Refreshed) {
				t.Errorf("heartbeat for %v: %q, %v", o.Member(), admission, err)
			}
		}
	}

	sleep(t, warmUp, "warm-up with nobody on a page")
	idleBefore := recorder.snapshot()
	sleep(t, focusWindow, "no focus: the normal pace of the largest slice")
	idle := recorder.snapshot()
	for _, bucket := range bucketNames() {
		t.Logf("no focus   %-24s tier %5.2f/s", bucket, rate(idle, idleBefore, "tier", bucket, focusWindow))
	}

	for _, n := range []int{10, 50} {
		objs := objects(n)
		ctx, stop := context.WithCancel(context.Background())
		var beats sync.WaitGroup
		beats.Add(1)
		go func() {
			defer beats.Done()
			heartbeat(ctx, objs)
			for tick := time.NewTicker(10 * time.Second); ; {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					heartbeat(ctx, objs)
				}
			}
		}()
		sleep(t, 40*time.Second, fmt.Sprintf("%d focused objects settling", n))
		before := recorder.snapshot()
		sleep(t, focusWindow, fmt.Sprintf("measuring with %d focused objects", n))
		after := recorder.snapshot()

		for _, bucket := range bucketNames() {
			tier, focused := rate(after, before, "tier", bucket, focusWindow), rate(after, before, "focus", bucket, focusWindow)
			t.Logf("%2d objects %-24s tier %5.2f/s focus %5.2f/s total %5.2f/s", n, bucket, tier, focused, tier+focused)
			if bucket == "elb" {
				continue // no published limit; logged only
			}
			if focused > focusBudgetPerCategory {
				t.Errorf("%d objects: %s focus rate %.2f/s exceeds the %v/s ceiling", n, bucket, focused, focusBudgetPerCategory)
			}
			if tier+focused >= maxBucketCallsPerSecond {
				t.Errorf("%d objects: %s tier plus focus %.2f/s is not under half of ECS's 20/s (%v/s)", n, bucket, tier+focused, maxBucketCallsPerSecond)
			}
		}

		// Every focused object reports how fresh it is and at which interval.
		var minInterval, maxInterval time.Duration
		for _, o := range objs {
			fresh, ok, err := registry.Fresh(context.Background(), o)
			switch {
			case err != nil:
				t.Errorf("%d objects: fresh for %v: %v", n, o.Member(), err)
			case !ok:
				t.Errorf("%d objects: no fresh entry for %v", n, o.Member())
			case fresh.Interval <= 0:
				t.Errorf("%d objects: %v reports interval %v", n, o.Member(), fresh.Interval)
			default:
				if minInterval == 0 || fresh.Interval < minInterval {
					minInterval = fresh.Interval
				}
				maxInterval = max(maxInterval, fresh.Interval)
			}
		}
		t.Logf("%2d objects: effective intervals reported in fresh: %v to %v", n, minInterval, maxInterval)
		stop()
		beats.Wait()
	}
}

func bucketNames() []string {
	names := make([]string, 0, len(ecsBuckets)+1)
	for name := range ecsBuckets {
		names = append(names, name)
	}
	sort.Strings(names)
	return append(names, "elb")
}

func rate(after, before map[string]uint64, source, bucket string, window time.Duration) float64 {
	key := source + "|" + bucket
	return float64(after[key]-before[key]) / window.Seconds()
}
