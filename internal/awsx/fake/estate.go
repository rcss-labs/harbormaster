// Package fake is an in-memory AWS estate (seeded from the design mockup)
// that implements awsx.Clients and awsx.Prober for tests and `serve --fake-aws`.
package fake

import (
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Account is a connected AWS account.
type Account struct {
	ID, Name string
	Regions  []string
}

// Cluster, Service, Task and TaskDefinition wrap the SDK types with the
// account/region/cluster they live in so scoping a request is a field compare.
type Cluster struct {
	AccountID, Region string
	Cluster           ecstypes.Cluster
}

type Service struct {
	AccountID, Region, ClusterName string
	Service                        ecstypes.Service
	// OOMBelowMiB makes tasks of a task definition with less memory than this
	// fail with OutOfMemoryError (0: never). Mirrors the mockup's oomBelow.
	OOMBelowMiB int
	// TaskDefinitionTags are the tags seeded on every revision of the family.
	TaskDefinitionTags []ecstypes.Tag
}

type Task struct {
	AccountID, Region, ClusterName string
	Task                           ecstypes.Task
}

type TaskDefinition struct {
	AccountID, Region string
	Definition        ecstypes.TaskDefinition
	// Tags are this revision's tags, as given to RegisterTaskDefinition.
	Tags []ecstypes.Tag
}

// Estate is the whole fake AWS world. Mu guards every field: tests and later
// plans mutate the estate under Mu (e.g. flip a rollout state, drop a task).
// The helper methods below take Mu themselves, so never call them while
// holding it.
type Estate struct {
	Mu              sync.Mutex
	Now             func() time.Time // injectable clock
	Accounts        []Account
	Clusters        []Cluster
	Services        []Service
	Tasks           []Task
	TaskDefinitions []TaskDefinition
	// Events holds service events newest first, keyed by service ARN.
	Events map[string][]ecstypes.ServiceEvent
	// Repositories holds each ECR repository's tags, newest first, keyed by
	// repository name. A repository is visible to a target that has a task
	// definition with a container image from it (see ecrClient.DescribeImages).
	Repositories map[string][]string
	// RunningTags is the tag each repository's service ran when the estate was
	// seeded; the fake scanner measures how far a tag is ahead of it.
	RunningTags map[string]string

	// Plan 03 (observe) state; see seed_observe.go. All guarded by Mu.
	MetricProfiles    map[model.ServiceKey]MetricProfile
	TargetGroups      map[model.ServiceKey]TargetGroup   // each service's primary target group
	ExtraTargetGroups map[model.ServiceKey][]TargetGroup // further groups (AddTargetGroup)
	ScalingConfig     map[model.ServiceKey]*ScalingSeed
	LogPools          map[string][][2]string // service name (or "default") -> [level, message]
	ContainerInsights map[model.ClusterKey]string
	LogLines          map[string][]LogLine // log group -> explicit lines (SetLogLines)

	// Plan 05 (networks and storage) state; see estate_network.go. Guarded by Mu.
	Network NetworkEstate

	// movedTags and tagMoves implement MoveTag (see change.go), keyed by "repository:tag".
	movedTags map[string]string
	tagMoves  map[string]int

	// rollbackDeployments holds the ids of circuit-breaker rollback
	// deployments, which are exempt from the OOM failure path (see rollout.go).
	rollbackDeployments map[string]bool
	// failedLinger counts the Advance steps a FAILED deployment stays visible
	// after its rollback completed (see failedDeploymentLingerSteps).
	failedLinger map[string]int
	// deploymentSeq makes deployment ids unique even within one clock instant.
	deploymentSeq int
}

func (e *Estate) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func clusterARN(region, account, name string) string {
	return "arn:aws:ecs:" + region + ":" + account + ":cluster/" + name
}

func serviceARN(region, account, cluster, name string) string {
	return "arn:aws:ecs:" + region + ":" + account + ":service/" + cluster + "/" + name
}

func taskARN(region, account, cluster, id string) string {
	return "arn:aws:ecs:" + region + ":" + account + ":task/" + cluster + "/" + id
}

func taskDefinitionARN(region, account, family string, revision int) string {
	return "arn:aws:ecs:" + region + ":" + account + ":task-definition/" + family + ":" + itoa(revision)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// lastSegment returns the part after the final "/" (or the whole string).
func lastSegment(s string) string {
	return s[strings.LastIndex(s, "/")+1:]
}

// recountService recomputes running counts for a service and its deployments
// from the task list. Caller holds Mu.
func (e *Estate) recountService(idx int) {
	svc := &e.Services[idx]
	group := "service:" + aws.ToString(svc.Service.ServiceName)
	perTaskDef := map[string]int32{}
	var total int32
	for _, t := range e.Tasks {
		if t.AccountID != svc.AccountID || t.Region != svc.Region || t.ClusterName != svc.ClusterName ||
			aws.ToString(t.Task.Group) != group || aws.ToString(t.Task.LastStatus) != "RUNNING" {
			continue
		}
		total++
		perTaskDef[aws.ToString(t.Task.TaskDefinitionArn)]++
	}
	deployments := append([]ecstypes.Deployment(nil), svc.Service.Deployments...)
	for i := range deployments {
		deployments[i].RunningCount = perTaskDef[aws.ToString(deployments[i].TaskDefinition)]
	}
	svc.Service.Deployments = deployments
	svc.Service.RunningCount = total
}

func (e *Estate) recountAll() {
	for i := range e.Services {
		e.recountService(i)
	}
}

// StopTask moves a task to STOPPED (stoppedAt = Now(), stopCode
// EssentialContainerExited) and gives its containers the exit code.
func (e *Estate) StopTask(arn, reason string, exitCode int) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	now := e.now()
	for i := range e.Tasks {
		t := &e.Tasks[i]
		if aws.ToString(t.Task.TaskArn) != arn {
			continue
		}
		task := t.Task
		task.LastStatus = aws.String("STOPPED")
		task.DesiredStatus = aws.String("STOPPED")
		task.StoppedAt = aws.Time(now)
		task.StoppingAt = aws.Time(now)
		task.StoppedReason = aws.String(reason)
		task.StopCode = ecstypes.TaskStopCodeEssentialContainerExited
		containers := make([]ecstypes.Container, len(task.Containers))
		for j, c := range task.Containers {
			c.LastStatus = aws.String("STOPPED")
			c.ExitCode = aws.Int32(int32(exitCode))
			containers[j] = c
		}
		task.Containers = containers
		t.Task = task
		for si := range e.Services {
			s := e.Services[si]
			if s.AccountID == t.AccountID && s.Region == t.Region && s.ClusterName == t.ClusterName &&
				"service:"+aws.ToString(s.Service.ServiceName) == aws.ToString(task.Group) {
				e.recountService(si)
			}
		}
		return
	}
}

// AddRunningTasks clones n RUNNING tasks of the service's PRIMARY revision.
func (e *Estate) AddRunningTasks(cluster, service string, n int) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for si := range e.Services {
		s := e.Services[si]
		if s.ClusterName != cluster || aws.ToString(s.Service.ServiceName) != service {
			continue
		}
		sd := newSeeder(e, hashSeed(cluster+"/"+service, len(e.Tasks)))
		sd.indexUsedIPs()
		primaryTD := aws.ToString(s.Service.TaskDefinition)
		for _, d := range s.Service.Deployments {
			if aws.ToString(d.Status) == "PRIMARY" {
				primaryTD = aws.ToString(d.TaskDefinition)
			}
		}
		spec, ok := sd.specFor(cluster, service)
		if !ok {
			return
		}
		for i := 0; i < n; i++ {
			sd.addTask(spec, taskOptions{
				taskDefinitionARN: primaryTD,
				slot:              len(e.Tasks) + i,
				startedAt:         e.now().Add(-time.Duration(1+i%60) * time.Minute),
				deploymentID:      deploymentIDOf(s.Service),
			})
		}
		e.recountService(si)
		return
	}
}

// SetRolloutState sets the PRIMARY deployment's rollout state (IN_PROGRESS,
// COMPLETED or FAILED) for every service with that name.
func (e *Estate) SetRolloutState(serviceName, state string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	now := e.now()
	for i := range e.Services {
		svc := &e.Services[i]
		if aws.ToString(svc.Service.ServiceName) != serviceName {
			continue
		}
		deployments := append([]ecstypes.Deployment(nil), svc.Service.Deployments...)
		for j := range deployments {
			if aws.ToString(deployments[j].Status) == "PRIMARY" {
				deployments[j].RolloutState = ecstypes.DeploymentRolloutState(state)
				deployments[j].UpdatedAt = aws.Time(now)
			}
		}
		svc.Service.Deployments = deployments
	}
}

func deploymentIDOf(s ecstypes.Service) string {
	for _, d := range s.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			return aws.ToString(d.Id)
		}
	}
	return ""
}
