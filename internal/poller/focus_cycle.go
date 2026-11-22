package poller

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	tierFocus = "focus"
	// defaultFocusInterval and defaultFocusCallsPerSec apply when the poller configuration leaves them zero.
	defaultFocusInterval    = 5 * time.Second
	defaultFocusCallsPerSec = 5
)

// focusResult is what one focus cycle did: whether it wrote anything and how many AWS calls it made.
type focusResult struct {
	changed    bool
	calls      int
	byCategory map[focus.Category]int
}

// callCounter counts the AWS calls of one focus cycle, in total and by rate category.
type callCounter struct {
	mu sync.Mutex
	by map[focus.Category]int
}

func (c *callCounter) add(category focus.Category) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[focus.Category]int{}
	}
	c.by[category]++
}

func (c *callCounter) fill(res *focusResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res.byCategory = make(map[focus.Category]int, len(c.by))
	for category, n := range c.by {
		res.byCategory[category] = n
		res.calls += n
	}
}

// focusCluster reads one cluster from AWS: the cluster, its services (a fresh listing every sixth cycle) and its
// running tasks. It reuses the normal tiers' diffing, so an unchanged cluster writes nothing.
func (l *Loop) focusCluster(ctx context.Context, k model.ClusterKey, cycle int) (res focusResult, err error) {
	ctx, api, calls, err := l.focusAPI(ctx)
	if err != nil {
		return focusResult{}, err
	}
	defer func() { calls.fill(&res); l.finishFocusCycle(err) }()

	cluster, err := l.describeClustersNamed(ctx, api, []string{k.Cluster})
	if err != nil {
		return res, err
	}
	res.changed = cluster.Inserted+cluster.Updated > 0

	l.mu.Lock()
	haveServices := len(l.services[k.Cluster]) > 0
	l.mu.Unlock()
	if cycle%6 == 0 || !haveServices {
		if err := l.listServicesIn(ctx, api, k); err != nil {
			return res, err
		}
	}
	servicesChanged, err := l.syncServicesIn(ctx, api, k, nil)
	if err != nil {
		return res, err
	}
	tasksChanged, err := l.syncRunningTasksIn(ctx, api, k, "", false)
	if err != nil {
		return res, err
	}
	res.changed = res.changed || servicesChanged || tasksChanged
	return res, nil
}

// focusService reads one service from AWS: the service, its running tasks, its target groups' health, and every third
// cycle its recently stopped tasks.
func (l *Loop) focusService(ctx context.Context, k model.ServiceKey, cycle int) (res focusResult, err error) {
	ctx, api, calls, err := l.focusAPI(ctx)
	if err != nil {
		return focusResult{}, err
	}
	defer func() { calls.fill(&res); l.finishFocusCycle(err) }()

	servicesChanged, err := l.syncServicesIn(ctx, api, k.ClusterKey, []string{k.Service})
	if err != nil {
		return res, err
	}
	tasksChanged, err := l.syncRunningTasksIn(ctx, api, k.ClusterKey, k.Service, false)
	if err != nil {
		return res, err
	}
	health := &TargetHealthLoop{Store: l.p.reads, Writer: l.w, Clients: focusClients{Clients: l.p.clients, loop: l, calls: calls}}
	healthChanged, err := health.RunService(ctx, k)
	if err != nil {
		return res, err
	}
	res.changed = servicesChanged || tasksChanged || healthChanged
	if cycle%3 == 0 {
		stoppedChanged, err := l.syncStoppedTasksIn(ctx, api, k.ClusterKey, k.Service)
		if err != nil {
			return res, err
		}
		res.changed = res.changed || stoppedChanged
	}
	return res, nil
}

// finishFocusCycle applies the throttle rule to the focus interval: a throttled call doubles it, a clean cycle counts
// toward halving it.
func (l *Loop) finishFocusCycle(err error) {
	switch {
	case err == nil:
		l.focusTier.onSuccess()
	case errorsThrottled(err):
		l.focusTier.onThrottle()
	}
}

// onTierThrottle slows focus when any tier cycle of the slice was throttled: they share the slice's AWS budget.
func (l *Loop) onTierThrottle() { l.focusTier.onThrottle() }

// focusInterval is the interval one object is read at: the larger of the throttle back-off and the measured time a
// round over all focused objects takes, rounded up to whole seconds. throttled is true while AWS throttling has
// slowed it.
func (l *Loop) focusInterval() (effective time.Duration, throttled bool) {
	effective = l.focusIntervalExact()
	l.focusTier.mu.Lock()
	throttled = l.focusTier.cur > l.focusTier.base
	l.focusTier.mu.Unlock()
	if rem := effective % time.Second; rem != 0 {
		effective += time.Second - rem
	}
	return effective, throttled
}

// focusIntervalExact is focusInterval before rounding: what the scheduler waits between an object's cycles.
func (l *Loop) focusIntervalExact() time.Duration {
	l.focusTier.mu.Lock()
	cur := l.focusTier.cur
	l.focusTier.mu.Unlock()
	return max(cur, time.Duration(l.focusRound.Load()))
}

// setFocusRound records how long a round over this slice's focused objects takes, which stretches the interval when
// the call budget is the limit.
func (l *Loop) setFocusRound(d time.Duration) { l.focusRound.Store(int64(d)) }

// focusAPI returns the context and ECS client of one focus cycle: calls are labelled focus, take a limiter token of
// their category and then a slot of the loop's in-flight semaphore. calls counts them.
func (l *Loop) focusAPI(ctx context.Context) (context.Context, awsx.ECSAPI, *callCounter, error) {
	ctx = awsx.WithSource(ctx, awsx.SourceFocus)
	base, err := l.api(ctx)
	if err != nil {
		return ctx, nil, nil, err
	}
	calls := &callCounter{}
	return ctx, &focusECS{ECSAPI: base, limiter: l.limiter, calls: calls}, calls, nil
}

// focusECS makes every ECS call of a focus cycle wait for a token of its rate category.
type focusECS struct {
	awsx.ECSAPI
	limiter *focus.Limiter
	calls   *callCounter
}

func (c *focusECS) wait(ctx context.Context, category focus.Category) error {
	if err := c.limiter.Wait(ctx, category); err != nil {
		return err
	}
	c.calls.add(category)
	return nil
}

func (c *focusECS) DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	if err := c.wait(ctx, focus.ECSClusterRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.DescribeClusters(ctx, in, opts...)
}

func (c *focusECS) ListServices(ctx context.Context, in *ecs.ListServicesInput, opts ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	if err := c.wait(ctx, focus.ECSServiceRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.ListServices(ctx, in, opts...)
}

func (c *focusECS) DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if err := c.wait(ctx, focus.ECSServiceRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.DescribeServices(ctx, in, opts...)
}

func (c *focusECS) DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	if err := c.wait(ctx, focus.ECSServiceRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.DescribeTaskDefinition(ctx, in, opts...)
}

func (c *focusECS) ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if err := c.wait(ctx, focus.ECSClusterResourceRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.ListTasks(ctx, in, opts...)
}

func (c *focusECS) DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	if err := c.wait(ctx, focus.ECSClusterResourceRead); err != nil {
		return nil, err
	}
	return c.ECSAPI.DescribeTasks(ctx, in, opts...)
}

// focusClients hands the target health read an ELB client that takes limiter tokens and in-flight slots.
type focusClients struct {
	awsx.Clients
	loop  *Loop
	calls *callCounter
}

func (c focusClients) ELB(ctx context.Context, t awsx.Target) (awsx.ELBAPI, error) {
	inner, err := c.Clients.ELB(ctx, t)
	if err != nil {
		return nil, err
	}
	return &focusELB{ELBAPI: inner, loop: c.loop, calls: c.calls}, nil
}

type focusELB struct {
	awsx.ELBAPI
	loop  *Loop
	calls *callCounter
}

// gate waits for a limiter token and then a semaphore slot; the returned function frees the slot.
func (c *focusELB) gate(ctx context.Context) (func(), error) {
	if err := c.loop.limiter.Wait(ctx, focus.ELB); err != nil {
		return nil, err
	}
	select {
	case c.loop.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.calls.add(focus.ELB)
	return func() { <-c.loop.sem }, nil
}

func (c *focusELB) DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	release, err := c.gate(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ELBAPI.DescribeTargetGroups(ctx, in, opts...)
}

func (c *focusELB) DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	release, err := c.gate(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.ELBAPI.DescribeTargetHealth(ctx, in, opts...)
}
