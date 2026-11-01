package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultMaxInFlight    = 8
	listPageSize          = 100
	describeClustersLimit = 100
	describeServicesLimit = 10
	describeTasksLimit    = 100
	// taskDefinitionRetryAfter is how long a failed task definition lookup is not retried.
	taskDefinitionRetryAfter = 10 * time.Minute
)

// serviceSignature is what, when it changes, makes a service's tasks worth re-describing.
type serviceSignature struct {
	desired, running, pending int
	primaryDeploymentID       string
}

// Loop polls one account x region: tiers of work at different cadences, each diffing AWS
// against the database so unchanged rows are never rewritten.
type Loop struct {
	p      *Poller
	target awsx.Target
	log    *slog.Logger
	// sem bounds concurrent ECS calls (cfg.MaxInFlight).
	sem chan struct{}
	// tiers is keyed by tier name; each tier's goroutine owns its entry.
	tiers map[string]*tier
	defs  []tierDef

	mu       sync.Mutex
	clusters []model.ClusterKey
	services map[string][]string // cluster name -> service names
	// signatures remembers each service's last-seen counts and PRIMARY deployment id.
	signatures map[model.ServiceKey]serviceSignature
	// dirty is cluster name -> services whose tasks must be re-described by the next runningTasks pass.
	dirty map[string]map[string]bool
	// taskDefinitions maps ARN -> zero time (stored) or the time a lookup failed.
	taskDefinitions map[string]time.Time
	// latest is the most recent services snapshot, sampled for the counts history.
	latest map[model.ServiceKey]model.Service
	// seenDeployments are deployment ids already offered to deployment_history by this loop.
	seenDeployments map[string]bool
	writes          store.WriteResult
}

// tierDef pairs a tier with its work function.
type tierDef struct {
	tier *tier
	run  func(context.Context) error
}

func newLoop(p *Poller, t awsx.Target) *Loop {
	inFlight := p.cfg.MaxInFlight
	if inFlight <= 0 {
		inFlight = defaultMaxInFlight
	}
	l := &Loop{
		p: p, target: t,
		log:             p.log.With("account", t.AccountID, "region", t.Region),
		sem:             make(chan struct{}, inFlight),
		tiers:           map[string]*tier{},
		services:        map[string][]string{},
		signatures:      map[model.ServiceKey]serviceSignature{},
		dirty:           map[string]map[string]bool{},
		taskDefinitions: map[string]time.Time{},
		latest:          map[model.ServiceKey]model.Service{},
		seenDeployments: map[string]bool{},
	}
	in := p.cfg.Intervals
	add := func(name string, base time.Duration, run func(context.Context) error) {
		tr := newTier(name, base)
		l.tiers[name] = tr
		l.defs = append(l.defs, tierDef{tier: tr, run: run})
	}
	// Order matters: runOnce executes the tiers in this order.
	add(tierClusters, in.Clusters, l.syncClusters)
	add(tierServiceList, in.ServiceList, l.syncServiceList)
	add(tierServices, in.Services, l.syncServices)
	add(tierRunningTasks, in.RunningTasks, l.syncRunningTasks)
	add(tierStoppedTasks, in.StoppedTasks, l.syncStoppedTasks)
	add(tierFullTasks, in.FullTasks, l.syncFullTasks)
	targetHealth := &TargetHealthLoop{Store: p.store, Clients: p.clients, Every: in.TargetHealth}
	add(tierTargetHealth, in.TargetHealth, func(ctx context.Context) error { return targetHealth.Run(ctx, l.target) })
	scaling := &ScalingLoop{Store: p.store, Clients: p.clients, Every: in.Scaling}
	add(tierScaling, in.Scaling, func(ctx context.Context) error { return scaling.Run(ctx, l.target) })
	alerts := &AlertsLoop{Store: p.store, Every: in.Alerts}
	add(tierAlerts, in.Alerts, func(ctx context.Context) error { return alerts.Run(ctx, l.target) })
	return l
}

// PollOnce runs every tier once, in order, for one target. API tests use it to fill the store from a fake estate.
func PollOnce(ctx context.Context, st *store.Store, clients awsx.Clients, cfg Config, t awsx.Target) error {
	return newLoop(New(st, clients, cfg, slog.Default()), t).runOnce(ctx)
}

// runOnce runs every tier once, synchronously: clusters, serviceList, services,
// runningTasks, stoppedTasks, fullTasks. It returns the joined tier errors.
func (l *Loop) runOnce(ctx context.Context) error {
	var errs []error
	for _, d := range l.defs {
		if err := l.runTier(ctx, d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run polls until ctx is cancelled. The clusters and serviceList tiers run first,
// synchronously, because every other tier needs their results; then each tier ticks
// in its own goroutine.
func (l *Loop) Run(ctx context.Context) {
	for _, d := range l.defs[:2] {
		_ = l.runTier(ctx, d)
	}
	var wg sync.WaitGroup
	if every := l.p.cfg.CountsHistory; every > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.countsLoop(ctx, every)
		}()
	}
	// One shared reference keeps the gaps between tiers at the step, whatever the goroutine scheduling.
	startedAt := time.Now()
	stagger := l.startDelays()
	for i, d := range l.defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if wait := time.Until(startedAt.Add(stagger[d.tier.name])); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			l.tickTier(ctx, d, i >= 2)
		}()
	}
	wg.Wait()
}

// startDelays staggers the first cycles of the task tiers, which all hit ECS's cluster-resource-read
// bucket (100 tokens, 20/s refill): runningTasks starts at once, stoppedTasks one step later, fullTasks two.
func (l *Loop) startDelays() map[string]time.Duration {
	step := l.p.cfg.StartStagger
	return map[string]time.Duration{tierStoppedTasks: step, tierFullTasks: 2 * step}
}

// countsLoop samples service counts into the history every interval until ctx ends.
func (l *Loop) countsLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.recordCounts(ctx)
		}
	}
}

// tickTier repeats a tier until ctx ends, sleeping its jittered interval between runs.
func (l *Loop) tickTier(ctx context.Context, d tierDef, runImmediately bool) {
	if runImmediately {
		_ = l.runTier(ctx, d)
	}
	for {
		timer := time.NewTimer(d.tier.nextDelay(l.p.cfg.Jitter))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		_ = l.runTier(ctx, d)
	}
}

// runTier runs one tier, logs its failure and records the cycle in poll_loops.
func (l *Loop) runTier(ctx context.Context, d tierDef) error {
	started := time.Now()
	err := d.run(ctx)
	if err != nil && ctx.Err() == nil {
		if errors.Is(err, awsx.ErrAccountUnknown) {
			l.log.Info("poller: account not connected, retrying next tick", "tier", d.tier.name)
		} else {
			l.log.Warn("poller: tier failed", "tier", d.tier.name, "err", err)
		}
	}
	if ctx.Err() == nil {
		l.recordCycle(ctx, d.tier, err)
		// plan 06: recordCycle has applied backoff, so cur is the interval the tier will run at next.
		d.tier.mu.Lock()
		interval := d.tier.cur
		d.tier.mu.Unlock()
		l.p.cfg.Telemetry.ObserveLoop(l.target.AccountID, l.target.Region, d.tier.name, time.Since(started), err == nil)
		l.p.cfg.Telemetry.SetLoopInterval(l.target.AccountID, l.target.Region, d.tier.name, interval)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", d.tier.name, err)
	}
	return nil
}

// writeTotals returns the cumulative result of every database upsert this loop made.
func (l *Loop) writeTotals() store.WriteResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writes
}

func (l *Loop) recordWrite(table string, res store.WriteResult) {
	l.p.cfg.Telemetry.CountSnapshotWrites(table, res.Inserted+res.Updated)
	l.mu.Lock()
	l.writes.Inserted += res.Inserted
	l.writes.Updated += res.Updated
	l.writes.Unchanged += res.Unchanged
	l.mu.Unlock()
}

// api returns the target's ECS client with every call bounded by the loop's semaphore.
func (l *Loop) api(ctx context.Context) (awsx.ECSAPI, error) {
	client, err := l.p.clients.ECS(ctx, l.target)
	if err != nil {
		return nil, err
	}
	return &limitedECS{ECSAPI: client, sem: l.sem}, nil
}

// limitedECS gates each call on the semaphore.
type limitedECS struct {
	awsx.ECSAPI
	sem chan struct{}
}

func (c *limitedECS) acquire(ctx context.Context) (release func(), err error) {
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *limitedECS) ListClusters(ctx context.Context, in *ecs.ListClustersInput, opts ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.ListClusters(ctx, in, opts...)
}

func (c *limitedECS) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.DescribeClusters(ctx, in, opts...)
}

func (c *limitedECS) ListServices(ctx context.Context, in *ecs.ListServicesInput, opts ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.ListServices(ctx, in, opts...)
}

func (c *limitedECS) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.DescribeServices(ctx, in, opts...)
}

func (c *limitedECS) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.ListTasks(ctx, in, opts...)
}

func (c *limitedECS) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.DescribeTasks(ctx, in, opts...)
}

func (c *limitedECS) DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ECSAPI.DescribeTaskDefinition(ctx, in, opts...)
}

// forEachCluster runs fn for every known cluster in parallel (the semaphore bounds the AWS
// calls) and joins the errors, so one failing cluster never aborts the others.
func (l *Loop) forEachCluster(ctx context.Context, fn func(ctx context.Context, k model.ClusterKey) error) error {
	l.mu.Lock()
	clusters := append([]model.ClusterKey(nil), l.clusters...)
	l.mu.Unlock()

	errs := make([]error, len(clusters))
	var wg sync.WaitGroup
	for i, k := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx, k); err != nil {
				errs[i] = fmt.Errorf("cluster %s: %w", k.Cluster, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// syncClusters lists and describes clusters, upserts them and deletes the ones that vanished.
func (l *Loop) syncClusters(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	var arns []string
	in := &ecs.ListClustersInput{MaxResults: aws.Int32(listPageSize)}
	for {
		out, err := api.ListClusters(ctx, in)
		if err != nil {
			return fmt.Errorf("list clusters: %w", err)
		}
		arns = append(arns, out.ClusterArns...)
		if out.NextToken == nil {
			break
		}
		in.NextToken = out.NextToken
	}

	now := time.Now().UTC()
	var rows []model.Cluster
	for _, batch := range chunk(arns, describeClustersLimit) {
		out, err := api.DescribeClusters(ctx, &ecs.DescribeClustersInput{
			Clusters: batch,
			Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldStatistics, ecstypes.ClusterFieldSettings},
		})
		if err != nil {
			return fmt.Errorf("describe clusters: %w", err)
		}
		for _, failure := range out.Failures {
			l.log.Warn("poller: describe cluster failed", "arn", aws.ToString(failure.Arn), "reason", aws.ToString(failure.Reason))
		}
		for _, c := range out.Clusters {
			rows = append(rows, normalizeCluster(l.target, c, now))
		}
	}

	res, err := l.p.store.UpsertClusters(ctx, rows)
	if err != nil {
		return err
	}
	l.recordWrite("clusters", res)

	// Keep every listed cluster, even one whose describe failed, so a hiccup never deletes data.
	keys := make([]model.ClusterKey, 0, len(arns))
	for _, arn := range arns {
		keys = append(keys, model.ClusterKey{AccountID: l.target.AccountID, Region: l.target.Region, Cluster: clusterNameFromARN(arn)})
	}
	if _, err := l.p.store.DeleteClustersNotIn(ctx, l.target, keys); err != nil {
		return err
	}

	l.mu.Lock()
	l.clusters = keys
	present := map[string]bool{}
	for _, k := range keys {
		present[k.Cluster] = true
	}
	for name := range l.services {
		if !present[name] {
			delete(l.services, name)
			delete(l.dirty, name)
		}
	}
	l.mu.Unlock()
	return nil
}

// syncServiceList records each cluster's service names and deletes services that vanished.
func (l *Loop) syncServiceList(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	return l.forEachCluster(ctx, func(ctx context.Context, k model.ClusterKey) error {
		names := []string{}
		in := &ecs.ListServicesInput{Cluster: aws.String(k.Cluster), MaxResults: aws.Int32(listPageSize)}
		for {
			out, err := api.ListServices(ctx, in)
			if err != nil {
				return fmt.Errorf("list services: %w", err)
			}
			for _, arn := range out.ServiceArns {
				names = append(names, clusterNameFromARN(arn)) // last path segment
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if _, err := l.p.store.DeleteServicesNotIn(ctx, k, names); err != nil {
			return err
		}
		l.mu.Lock()
		l.services[k.Cluster] = names
		l.mu.Unlock()
		return nil
	})
}

// syncServices describes services in batches of ten, stores services and new events, and
// flags services whose counts or PRIMARY deployment changed so their tasks get re-described.
func (l *Loop) syncServices(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	return l.forEachCluster(ctx, func(ctx context.Context, k model.ClusterKey) error {
		l.mu.Lock()
		names := append([]string(nil), l.services[k.Cluster]...)
		l.mu.Unlock()

		now := time.Now().UTC()
		var services []model.Service
		var events []model.ServiceEvent
		for _, batch := range chunk(names, describeServicesLimit) {
			out, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(k.Cluster), Services: batch})
			if err != nil {
				return fmt.Errorf("describe services: %w", err)
			}
			for _, failure := range out.Failures {
				l.log.Warn("poller: describe service failed", "arn", aws.ToString(failure.Arn), "reason", aws.ToString(failure.Reason))
			}
			for _, s := range out.Services {
				service, serviceEvents := normalizeService(l.target, k.Cluster, s, now)
				services = append(services, service)
				events = append(events, serviceEvents...)
			}
		}

		res, err := l.p.store.UpsertServices(ctx, services)
		if err != nil {
			return err
		}
		l.recordWrite("services", res)
		if _, err := l.p.store.InsertServiceEvents(ctx, events); err != nil {
			return err
		}
		l.markChangedServicesDirty(services)
		l.rememberServices(k.Cluster, services)
		l.recordDeployments(ctx, services)

		var definitionARNs []string
		for _, s := range services {
			for _, d := range s.Deployments {
				definitionARNs = append(definitionARNs, d.TaskDefinitionARN)
			}
		}
		l.ensureTaskDefinitions(ctx, api, definitionARNs)
		return nil
	})
}

// rememberServices replaces the cluster's entries in the latest snapshot.
func (l *Loop) rememberServices(cluster string, services []model.Service) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key := range l.latest {
		if key.Cluster == cluster {
			delete(l.latest, key)
		}
	}
	for _, s := range services {
		l.latest[s.Key] = s
	}
}

func (l *Loop) markChangedServicesDirty(services []model.Service) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range services {
		sig := serviceSignature{desired: s.Desired, running: s.Running, pending: s.Pending}
		for _, d := range s.Deployments {
			if d.Status == "PRIMARY" {
				sig.primaryDeploymentID = d.ID
			}
		}
		previous, seen := l.signatures[s.Key]
		l.signatures[s.Key] = sig
		if seen && previous != sig {
			if l.dirty[s.Key.Cluster] == nil {
				l.dirty[s.Key.Cluster] = map[string]bool{}
			}
			l.dirty[s.Key.Cluster][s.Key.Service] = true
		}
	}
}

// takeDirty removes and returns the cluster's dirty service names.
func (l *Loop) takeDirty(cluster string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var names []string
	for name := range l.dirty[cluster] {
		names = append(names, name)
	}
	delete(l.dirty, cluster)
	return names
}

func (l *Loop) restoreDirty(cluster string, names []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dirty[cluster] == nil {
		l.dirty[cluster] = map[string]bool{}
	}
	for _, name := range names {
		l.dirty[cluster][name] = true
	}
}

// syncRunningTasks keeps non-STOPPED tasks current. Each cycle it compares ECS's RUNNING list with the
// database's non-STOPPED rows (not in-memory state, so restarts and failovers need no special case):
// new ARNs and the tasks of dirty services are described; ARNs that left the list are described too
// (normally they are now STOPPED, with their reason), and any ECS no longer knows are marked missing.
func (l *Loop) syncRunningTasks(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	return l.forEachCluster(ctx, func(ctx context.Context, k model.ClusterKey) (err error) {
		dirty := l.takeDirty(k.Cluster)
		defer func() {
			if err != nil {
				l.restoreDirty(k.Cluster, dirty)
			}
		}()

		listed, err := l.listTaskARNs(ctx, api, k, ecstypes.DesiredStatusRunning, "")
		if err != nil {
			return err
		}
		known, err := l.p.store.KnownTaskARNs(ctx, k, "")
		if err != nil {
			return err
		}
		knownSet := toSet(known)
		listedSet := toSet(listed)

		wanted := map[string]bool{}
		for _, arn := range listed {
			if !knownSet[arn] {
				wanted[arn] = true
			}
		}
		for _, arn := range known {
			if !listedSet[arn] {
				wanted[arn] = true
			}
		}
		for _, service := range dirty {
			arns, err := l.listTaskARNs(ctx, api, k, ecstypes.DesiredStatusRunning, service)
			if err != nil {
				return err
			}
			for _, arn := range arns {
				wanted[arn] = true
			}
		}
		return l.describeAndStoreTasks(ctx, api, k, keysOf(wanted), true)
	})
}

// syncFullTasks re-describes every non-STOPPED task so slow-changing fields (health, agents) stay fresh.
func (l *Loop) syncFullTasks(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	return l.forEachCluster(ctx, func(ctx context.Context, k model.ClusterKey) error {
		known, err := l.p.store.KnownTaskARNs(ctx, k, "")
		if err != nil {
			return err
		}
		return l.describeAndStoreTasks(ctx, api, k, known, true)
	})
}

// syncStoppedTasks stores recently stopped tasks not yet recorded as STOPPED.
func (l *Loop) syncStoppedTasks(ctx context.Context) error {
	api, err := l.api(ctx)
	if err != nil {
		return err
	}
	return l.forEachCluster(ctx, func(ctx context.Context, k model.ClusterKey) error {
		listed, err := l.listTaskARNs(ctx, api, k, ecstypes.DesiredStatusStopped, "")
		if err != nil {
			return err
		}
		stored, err := l.p.store.KnownTaskARNs(ctx, k, "STOPPED")
		if err != nil {
			return err
		}
		storedSet := toSet(stored)
		var fresh []string
		for _, arn := range listed {
			if !storedSet[arn] {
				fresh = append(fresh, arn)
			}
		}
		return l.describeAndStoreTasks(ctx, api, k, fresh, false)
	})
}

// listTaskARNs pages through ListTasks for a desired status, optionally for one service.
func (l *Loop) listTaskARNs(ctx context.Context, api awsx.ECSAPI, k model.ClusterKey, desired ecstypes.DesiredStatus, service string) ([]string, error) {
	in := &ecs.ListTasksInput{Cluster: aws.String(k.Cluster), DesiredStatus: desired, MaxResults: aws.Int32(listPageSize)}
	if service != "" {
		in.ServiceName = aws.String(service)
	}
	var arns []string
	for {
		out, err := api.ListTasks(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("list %s tasks: %w", desired, err)
		}
		arns = append(arns, out.TaskArns...)
		if out.NextToken == nil {
			return arns, nil
		}
		in.NextToken = out.NextToken
	}
}

// describeAndStoreTasks describes ARNs in batches of 100 and upserts the tasks, ensuring their task
// definitions are cached. With markMissing, ARNs ECS reports as MISSING are marked STOPPED/Unknown.
func (l *Loop) describeAndStoreTasks(ctx context.Context, api awsx.ECSAPI, k model.ClusterKey, arns []string, markMissing bool) error {
	now := time.Now().UTC()
	var rows []model.Task
	var missing []string
	for _, batch := range chunk(arns, describeTasksLimit) {
		out, err := api.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(k.Cluster), Tasks: batch})
		if err != nil {
			return fmt.Errorf("describe tasks: %w", err)
		}
		for _, task := range out.Tasks {
			rows = append(rows, normalizeTask(l.target, k.Cluster, task, now))
		}
		for _, failure := range out.Failures {
			if aws.ToString(failure.Reason) == "MISSING" {
				missing = append(missing, aws.ToString(failure.Arn))
			}
		}
	}
	res, err := l.p.store.UpsertTasks(ctx, rows)
	if err != nil {
		return err
	}
	l.recordWrite("tasks", res)
	if markMissing {
		if err := l.p.store.MarkTasksMissing(ctx, k, missing, now); err != nil {
			return err
		}
	}
	definitionARNs := make([]string, 0, len(rows))
	for _, row := range rows {
		definitionARNs = append(definitionARNs, row.TaskDefinitionARN)
	}
	l.ensureTaskDefinitions(ctx, api, definitionARNs)
	return nil
}

// ensureTaskDefinitions stores any task definition not yet in the database. Task definitions are
// immutable, so a stored one is never fetched again; failed lookups are not retried for a while.
// Failures are logged, not returned: a missing definition must not fail the tier that found it.
func (l *Loop) ensureTaskDefinitions(ctx context.Context, api awsx.ECSAPI, arns []string) {
	seen := map[string]bool{}
	for _, arn := range arns {
		if arn == "" || seen[arn] {
			continue
		}
		seen[arn] = true

		l.mu.Lock()
		failedAt, cached := l.taskDefinitions[arn]
		l.mu.Unlock()
		if cached && (failedAt.IsZero() || time.Since(failedAt) < taskDefinitionRetryAfter) {
			continue
		}
		if err := l.cacheTaskDefinition(ctx, api, arn); err != nil {
			if ctx.Err() != nil {
				return
			}
			l.log.Warn("poller: cache task definition", "arn", arn, "err", err)
			l.mu.Lock()
			l.taskDefinitions[arn] = time.Now()
			l.mu.Unlock()
			continue
		}
		l.mu.Lock()
		l.taskDefinitions[arn] = time.Time{}
		l.mu.Unlock()
	}
}

func (l *Loop) cacheTaskDefinition(ctx context.Context, api awsx.ECSAPI, arn string) error {
	stored, err := l.p.store.HasTaskDefinition(ctx, l.target.AccountID, l.target.Region, arn)
	if err != nil || stored {
		return err
	}
	out, err := api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(arn)})
	if err != nil {
		return fmt.Errorf("describe task definition: %w", err)
	}
	if out.TaskDefinition == nil {
		return errors.New("describe task definition: empty response")
	}
	return l.p.store.UpsertTaskDefinition(ctx, normalizeTaskDefinition(l.target, *out.TaskDefinition, time.Now().UTC()))
}

func toSet(xs []string) map[string]bool {
	set := make(map[string]bool, len(xs))
	for _, x := range xs {
		set[x] = true
	}
	return set
}

func keysOf(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys
}
