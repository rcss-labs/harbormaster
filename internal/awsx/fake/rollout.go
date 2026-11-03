package fake

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Rollout simulation: UpdateService starts a deployment and Estate.Advance
// moves every rollout (and every desired-count mismatch) one step forward,
// mirroring the mockup's startDeploy/reconcile.

const (
	circuitBreakerFailures = 3
	oomStoppedReason       = "OutOfMemoryError: Container killed due to memory usage"
	oomExitCode            = 137
	// maxStoppedTasksPerService bounds the history an always-failing service
	// (image-resizer) accumulates while the fake ticker runs.
	maxStoppedTasksPerService = 60
	// failedDeploymentLingerSteps keeps a FAILED deployment visible after its
	// rollback completes, longer than one poll interval at the 2.4 s step.
	failedDeploymentLingerSteps = 6
)

// UpdateService supports TaskDefinition, ForceNewDeployment, DesiredCount,
// EnableExecuteCommand, DeploymentConfiguration and HealthCheckGracePeriodSeconds
// (the last only for services behind a load balancer). Settings changes alone
// start no deployment; tasks started later follow the new exec flag, running
// ones keep theirs until replaced. A task definition or force-new-deployment starts a deployment even while
// another is in progress, as ECS does.
func (c *ecsClient) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	done, err := c.f.begin(c.target, "ecs", "UpdateService")
	defer done()
	if err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	name := lastSegment(aws.ToString(in.Service))
	si := -1
	for i, s := range e.Services {
		if c.inScope(s.AccountID, s.Region) && s.ClusterName == cluster && aws.ToString(s.Service.ServiceName) == name {
			si = i
		}
	}
	if si < 0 {
		return nil, &ecstypes.ServiceNotFoundException{Message: aws.String("Service not found.")}
	}

	targetARN := ""
	if in.TaskDefinition != nil {
		td, ok := c.resolveTaskDefinition(aws.ToString(in.TaskDefinition))
		if !ok {
			return nil, invalidParameter("Unable to describe task definition.")
		}
		targetARN = aws.ToString(td.TaskDefinitionArn)
	}
	svc := &e.Services[si].Service
	if in.DesiredCount != nil && *in.DesiredCount < 0 {
		return nil, invalidParameter("desiredCount must be zero or greater.")
	}
	if in.HealthCheckGracePeriodSeconds != nil && len(svc.LoadBalancers) == 0 {
		return nil, invalidParameter("Health check grace period is only valid for services configured to use load balancers.")
	}
	var deploymentConfig *ecstypes.DeploymentConfiguration
	if in.DeploymentConfiguration != nil {
		merged, err := mergeDeploymentConfiguration(svc.DeploymentConfiguration, *in.DeploymentConfiguration)
		if err != nil {
			return nil, err
		}
		deploymentConfig = &merged
	}
	if in.DesiredCount != nil {
		e.setDesiredCount(si, *in.DesiredCount)
	}
	if in.EnableExecuteCommand != nil {
		svc.EnableExecuteCommand = *in.EnableExecuteCommand
	}
	if in.HealthCheckGracePeriodSeconds != nil {
		svc.HealthCheckGracePeriodSeconds = aws.Int32(*in.HealthCheckGracePeriodSeconds)
	}
	if deploymentConfig != nil {
		svc.DeploymentConfiguration = deploymentConfig
	}
	if targetARN != "" || in.ForceNewDeployment {
		if targetARN == "" {
			targetARN = aws.ToString(svc.TaskDefinition)
		}
		e.startDeployment(si, targetARN)
	}
	return &ecs.UpdateServiceOutput{Service: ptrTo(deepCopy(*svc))}, nil
}

// mergeDeploymentConfiguration overlays the fields the caller set onto the
// current configuration; the circuit breaker is replaced as a whole.
func mergeDeploymentConfiguration(current *ecstypes.DeploymentConfiguration, update ecstypes.DeploymentConfiguration) (ecstypes.DeploymentConfiguration, error) {
	merged := ecstypes.DeploymentConfiguration{}
	if current != nil {
		merged = deepCopy(*current)
	}
	if update.MinimumHealthyPercent != nil {
		merged.MinimumHealthyPercent = aws.Int32(*update.MinimumHealthyPercent)
	}
	if update.MaximumPercent != nil {
		merged.MaximumPercent = aws.Int32(*update.MaximumPercent)
	}
	if update.DeploymentCircuitBreaker != nil {
		breaker := *update.DeploymentCircuitBreaker
		if breaker.Rollback && !breaker.Enable {
			return merged, invalidParameter("Deployment circuit breaker rollback requires the circuit breaker to be enabled.")
		}
		merged.DeploymentCircuitBreaker = &breaker
	}
	return merged, nil
}

// setDesiredCount sets the service's and its PRIMARY deployment's desired
// count. Caller holds Mu.
func (e *Estate) setDesiredCount(si int, desired int32) {
	svc := &e.Services[si].Service
	svc.DesiredCount = desired
	deployments := append([]ecstypes.Deployment(nil), svc.Deployments...)
	for i := range deployments {
		if aws.ToString(deployments[i].Status) == "PRIMARY" {
			deployments[i].DesiredCount = desired
		}
	}
	svc.Deployments = deployments
}

// clampToScalingRange moves every service's desired count into the range of
// its registered scalable target, as Application Auto Scaling does after a
// range change. Caller holds Mu.
func (e *Estate) clampToScalingRange() {
	for si, s := range e.Services {
		cfg := e.ScalingConfig[serviceKeyOf(s.AccountID, s.Region, s.ClusterName, aws.ToString(s.Service.ServiceName))]
		if cfg == nil {
			continue
		}
		desired := min(max(int(s.Service.DesiredCount), cfg.Min), cfg.Max)
		if int32(desired) != s.Service.DesiredCount {
			e.setDesiredCount(si, int32(desired))
		}
	}
}

// resolveTaskDefinition accepts an ARN, family:revision, or a bare family
// (latest ACTIVE revision). Caller holds Mu.
func (c *ecsClient) resolveTaskDefinition(ref string) (ecstypes.TaskDefinition, bool) {
	ref = lastSegment(ref)
	family, revision, hasRevision := strings.Cut(ref, ":")
	var best ecstypes.TaskDefinition
	found := false
	for _, td := range c.f.Estate.TaskDefinitions {
		d := td.Definition
		if !c.inScope(td.AccountID, td.Region) || aws.ToString(d.Family) != family || d.Status != ecstypes.TaskDefinitionStatusActive {
			continue
		}
		if hasRevision {
			if strconv.Itoa(int(d.Revision)) == revision {
				return d, true
			}
		} else if !found || d.Revision > best.Revision {
			best, found = d, true
		}
	}
	return best, found
}

// startDeployment makes a new IN_PROGRESS PRIMARY for targetARN; the previous
// PRIMARY becomes ACTIVE. Caller holds Mu.
func (e *Estate) startDeployment(si int, targetARN string) {
	svc := &e.Services[si].Service
	now := e.now()
	e.deploymentSeq++
	sd := newSeeder(e, hashSeed(aws.ToString(svc.ServiceArn), len(e.Tasks)+int(now.Unix())+e.deploymentSeq*7919))
	id := "ecs-svc/" + sd.digits(19)
	deployments := append([]ecstypes.Deployment(nil), svc.Deployments...)
	next := ecstypes.Deployment{
		Id: aws.String(id), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(targetARN),
		DesiredCount: svc.DesiredCount, RolloutState: ecstypes.DeploymentRolloutStateInProgress,
		RolloutStateReason: aws.String("ECS deployment " + id + " in progress."),
		CreatedAt:          aws.Time(now), UpdatedAt: aws.Time(now), LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: deepCopy(svc.NetworkConfiguration),
	}
	for i := range deployments {
		if aws.ToString(deployments[i].Status) == "PRIMARY" {
			deployments[i].Status = aws.String("ACTIVE")
			next.LaunchType = deployments[i].LaunchType
			next.NetworkConfiguration = deepCopy(deployments[i].NetworkConfiguration)
		}
	}
	svc.Deployments = append([]ecstypes.Deployment{next}, deployments...)
	svc.TaskDefinition = aws.String(targetARN)
	e.addEvent(si, fmt.Sprintf("(service %s) has started deployment %s for task definition %s.",
		aws.ToString(svc.ServiceName), id, shortDefinition(targetARN)))
}

// shortDefinition turns an ARN into family:revision.
func shortDefinition(arn string) string { return lastSegment(arn) }

// addEvent prepends a service event. Caller holds Mu.
func (e *Estate) addEvent(si int, message string) {
	arn := aws.ToString(e.Services[si].Service.ServiceArn)
	if e.Events == nil {
		e.Events = map[string][]ecstypes.ServiceEvent{}
	}
	sd := newSeeder(e, hashSeed(arn, len(e.Events[arn])+int(e.now().UnixNano())))
	event := ecstypes.ServiceEvent{Id: aws.String(sd.uuid()), CreatedAt: aws.Time(e.now()), Message: aws.String(message)}
	events := append([]ecstypes.ServiceEvent{event}, e.Events[arn]...)
	e.Events[arn] = events[:min(len(events), maxServiceEvents)]
}

// RunRollouts calls Advance every step until ctx is cancelled.
func (e *Estate) RunRollouts(ctx context.Context, step time.Duration) {
	ticker := time.NewTicker(step)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.Advance()
		}
	}
}

// Advance runs one simulation step for every service: an IN_PROGRESS rollout
// moves forward, otherwise running tasks are reconciled toward the desired count.
func (e *Estate) Advance() {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.expireFailedDeployments()
	e.clampToScalingRange()
	for si := range e.Services {
		if pi := primaryIndex(e.Services[si].Service); pi >= 0 &&
			e.Services[si].Service.Deployments[pi].RolloutState == ecstypes.DeploymentRolloutStateInProgress {
			e.advanceRollout(si, pi)
		} else {
			e.reconcile(si)
		}
		e.recountService(si)
	}
	e.trimStoppedTasks()
}

func primaryIndex(s ecstypes.Service) int {
	for i, d := range s.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			return i
		}
	}
	return -1
}

// failsWithOOM reports whether tasks of the definition are OOM-killed in this service.
func (e *Estate) failsWithOOM(si int, definitionARN string) bool {
	threshold := e.Services[si].OOMBelowMiB
	if threshold == 0 {
		return false
	}
	for _, td := range e.TaskDefinitions {
		if aws.ToString(td.Definition.TaskDefinitionArn) == definitionARN {
			memory, err := strconv.Atoi(aws.ToString(td.Definition.Memory))
			return err == nil && memory < threshold
		}
	}
	return false
}

// serviceTaskIndexes returns the indexes of the service's tasks with the given status.
func (e *Estate) serviceTaskIndexes(si int, status string) []int {
	s := e.Services[si]
	group := "service:" + aws.ToString(s.Service.ServiceName)
	var out []int
	for i, t := range e.Tasks {
		if t.AccountID == s.AccountID && t.Region == s.Region && t.ClusterName == s.ClusterName &&
			aws.ToString(t.Task.Group) == group && aws.ToString(t.Task.LastStatus) == status {
			out = append(out, i)
		}
	}
	return out
}

// newTask adds a RUNNING (or, with failure, OOM-STOPPED) task of the
// definition and returns its short id. Caller holds Mu.
func (e *Estate) newTask(si int, definitionARN, deploymentID string, failure bool) string {
	s := e.Services[si]
	name := aws.ToString(s.Service.ServiceName)
	sd := newSeeder(e, hashSeed(s.ClusterName+"/"+name, len(e.Tasks)))
	sd.indexUsedIPs()
	spec, ok := sd.specFor(s.ClusterName, name)
	if !ok {
		return ""
	}
	spec.exec = s.Service.EnableExecuteCommand // tasks follow the service's current setting
	now := e.now()
	options := taskOptions{taskDefinitionARN: definitionARN, slot: len(e.Tasks), startedAt: now, deploymentID: deploymentID}
	if failure {
		options.startedAt = now.Add(-40 * time.Second)
		options.stoppedAt = now
		options.reason = oomStoppedReason
		options.exitCode = oomExitCode
		options.stopCode = ecstypes.TaskStopCodeTaskFailedToStart
		options.health = ecstypes.HealthStatusUnknown
	}
	sd.addTask(spec, options)
	return id12(aws.ToString(e.Tasks[len(e.Tasks)-1].Task.TaskArn))
}

// stopRunningTask stops e.Tasks[ti] by the scheduler. Caller holds Mu.
func (e *Estate) stopRunningTask(ti int, reason string) string {
	now := e.now()
	task := e.Tasks[ti].Task
	task.LastStatus = aws.String("STOPPED")
	task.DesiredStatus = aws.String("STOPPED")
	task.StoppedAt = aws.Time(now)
	task.StoppingAt = aws.Time(now)
	task.StoppedReason = aws.String(reason)
	task.StopCode = ecstypes.TaskStopCodeServiceSchedulerInitiated
	containers := make([]ecstypes.Container, len(task.Containers))
	for i, c := range task.Containers {
		c.LastStatus = aws.String("STOPPED")
		c.ExitCode = aws.Int32(0)
		containers[i] = c
	}
	task.Containers = containers
	e.Tasks[ti].Task = task
	return id12(aws.ToString(task.TaskArn))
}

func (e *Estate) advanceRollout(si, pi int) {
	svc := &e.Services[si].Service
	name := aws.ToString(svc.ServiceName)
	primary := svc.Deployments[pi]
	id, target := aws.ToString(primary.Id), aws.ToString(primary.TaskDefinition)
	rollback := e.rollbackDeployments[id]
	// A revision that already completed on this service (a manual rollback to it) ran fine: only one that never did
	// can be OOM-killed.
	knownGood := hasCompletedDeployment(svc.Deployments, target)
	now := e.now()

	setPrimary := func(change func(*ecstypes.Deployment)) {
		deployments := append([]ecstypes.Deployment(nil), svc.Deployments...)
		change(&deployments[pi])
		deployments[pi].UpdatedAt = aws.Time(now)
		svc.Deployments = deployments
	}

	if !rollback && !knownGood && e.failsWithOOM(si, target) {
		taskID := e.newTask(si, target, id, true)
		setPrimary(func(d *ecstypes.Deployment) { d.FailedTasks++ })
		e.addEvent(si, fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s). Reason: OutOfMemoryError.", name, taskID))
		if svc.Deployments[pi].FailedTasks >= circuitBreakerFailures {
			e.failDeployment(si, pi)
		}
		return
	}

	isNew := func(t Task) bool {
		return aws.ToString(t.Task.StartedBy) == id || (rollback && aws.ToString(t.Task.TaskDefinitionArn) == target)
	}
	counts := func() (fresh, old []int) {
		for _, ti := range e.serviceTaskIndexes(si, "RUNNING") {
			if isNew(e.Tasks[ti]) {
				fresh = append(fresh, ti)
			} else {
				old = append(old, ti)
			}
		}
		return fresh, old
	}
	fresh, old := counts()
	if int32(len(fresh)) > primary.DesiredCount {
		// Desired count was lowered mid-rollout: shed one surplus new task per step.
		taskID := e.stopRunningTask(fresh[0], "Scaling activity initiated by (desired count change)")
		e.addEvent(si, fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s).", name, taskID))
	} else if int32(len(fresh)) < primary.DesiredCount {
		taskID := e.newTask(si, target, id, false)
		e.addEvent(si, fmt.Sprintf("(service %s) has started 1 tasks: (task %s).", name, taskID))
	}
	if len(old) > 0 {
		taskID := e.stopRunningTask(old[0], "Scaling activity initiated by (deployment "+id+")")
		e.addEvent(si, fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s).", name, taskID))
	}
	fresh, old = counts()
	if int32(len(fresh)) == primary.DesiredCount && len(old) == 0 {
		setPrimary(func(d *ecstypes.Deployment) {
			d.RolloutState = ecstypes.DeploymentRolloutStateCompleted
			d.RolloutStateReason = aws.String("ECS deployment " + id + " completed.")
		})
		kept := []ecstypes.Deployment{svc.Deployments[pi]}
		for i, d := range svc.Deployments {
			if i != pi && d.RolloutState == ecstypes.DeploymentRolloutStateFailed {
				kept = append(kept, d)
				if e.failedLinger == nil {
					e.failedLinger = map[string]int{}
				}
				if _, ok := e.failedLinger[aws.ToString(d.Id)]; !ok {
					e.failedLinger[aws.ToString(d.Id)] = failedDeploymentLingerSteps
				}
			}
		}
		svc.Deployments = kept
		delete(e.rollbackDeployments, id)
		e.addEvent(si, fmt.Sprintf("(service %s) has reached a steady state.", name))
	}
}

// hasCompletedDeployment reports whether the service still lists a COMPLETED deployment of the task definition.
func hasCompletedDeployment(deployments []ecstypes.Deployment, targetARN string) bool {
	for _, d := range deployments {
		if aws.ToString(d.TaskDefinition) == targetARN && d.RolloutState == ecstypes.DeploymentRolloutStateCompleted {
			return true
		}
	}
	return false
}

// failDeployment trips the circuit breaker: the PRIMARY at pi becomes an
// ACTIVE FAILED deployment and a rollback PRIMARY starts for the previous
// definition. Caller holds Mu.
func (e *Estate) failDeployment(si, pi int) {
	svc := &e.Services[si].Service
	name := aws.ToString(svc.ServiceName)
	deployments := append([]ecstypes.Deployment(nil), svc.Deployments...)
	failed := &deployments[pi]
	failed.Status = aws.String("ACTIVE")
	failed.RolloutState = ecstypes.DeploymentRolloutStateFailed
	failed.RolloutStateReason = aws.String("ECS deployment circuit breaker: tasks failed to start.")
	failed.UpdatedAt = aws.Time(e.now())

	rollbackTo := ""
	var kept []ecstypes.Deployment
	for i, d := range deployments {
		switch {
		case i == pi:
			kept = append(kept, d)
		case rollbackTo == "" && d.RolloutState != ecstypes.DeploymentRolloutStateFailed:
			rollbackTo = aws.ToString(d.TaskDefinition)
			// superseded by the rollback PRIMARY created below
		default:
			kept = append(kept, d)
		}
	}
	svc.Deployments = kept
	if rollbackTo == "" {
		e.addEvent(si, fmt.Sprintf("(service %s) deployment failed: tasks failed to start.", name))
		return
	}
	e.startDeployment(si, rollbackTo)
	e.addEvent(si, fmt.Sprintf("(service %s) deployment failed: tasks failed to start. Circuit breaker rolled back to %s.",
		name, shortDefinition(rollbackTo)))
	if e.rollbackDeployments == nil {
		e.rollbackDeployments = map[string]bool{}
	}
	e.rollbackDeployments[deploymentIDOf(*svc)] = true
}

// reconcile moves the running task count one step toward the desired count.
func (e *Estate) reconcile(si int) {
	svc := e.Services[si].Service
	name := aws.ToString(svc.ServiceName)
	pi := primaryIndex(svc)
	if pi < 0 {
		return
	}
	running := e.serviceTaskIndexes(si, "RUNNING")
	desired := int(svc.DesiredCount)
	target, id := aws.ToString(svc.Deployments[pi].TaskDefinition), aws.ToString(svc.Deployments[pi].Id)
	switch {
	case len(running) < desired:
		if e.failsWithOOM(si, target) {
			taskID := e.newTask(si, target, id, true)
			e.addEvent(si, fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s). Reason: OutOfMemoryError.", name, taskID))
			return
		}
		taskID := e.newTask(si, target, id, false)
		e.addEvent(si, fmt.Sprintf("(service %s) has started 1 tasks: (task %s).", name, taskID))
		if len(running)+1 == desired {
			e.addEvent(si, fmt.Sprintf("(service %s) has reached a steady state.", name))
		}
	case len(running) > desired:
		taskID := e.stopRunningTask(running[0], "Scaling activity initiated by (desired count change)")
		e.addEvent(si, fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s).", name, taskID))
		if len(running)-1 == desired {
			e.addEvent(si, fmt.Sprintf("(service %s) has reached a steady state.", name))
		}
	}
}

// trimStoppedTasks drops the oldest STOPPED tasks of any service beyond the cap.
func (e *Estate) trimStoppedTasks() {
	var drop map[int]bool
	for si := range e.Services {
		stopped := e.serviceTaskIndexes(si, "STOPPED")
		for _, ti := range stopped[:max(0, len(stopped)-maxStoppedTasksPerService)] {
			if drop == nil {
				drop = map[int]bool{}
			}
			drop[ti] = true
		}
	}
	if drop == nil {
		return
	}
	kept := e.Tasks[:0:0]
	for i, t := range e.Tasks {
		if !drop[i] {
			kept = append(kept, t)
		}
	}
	e.Tasks = kept
}

// expireFailedDeployments ages FAILED deployments that outlived their rollback
// and drops them once their linger steps are used up. Caller holds Mu.
func (e *Estate) expireFailedDeployments() {
	if len(e.failedLinger) == 0 {
		return
	}
	for id, left := range e.failedLinger {
		if left > 1 {
			e.failedLinger[id] = left - 1
			continue
		}
		delete(e.failedLinger, id)
		for si := range e.Services {
			svc := &e.Services[si].Service
			var kept []ecstypes.Deployment
			for _, d := range svc.Deployments {
				if aws.ToString(d.Id) != id {
					kept = append(kept, d)
				}
			}
			svc.Deployments = kept
		}
	}
}
