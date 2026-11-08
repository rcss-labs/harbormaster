package model

import (
	"encoding/json"
	"strings"
	"time"
)

type Status string

const (
	StatusSteady        Status = "steady"
	StatusDeploying     Status = "deploying"
	StatusDegraded      Status = "degraded"
	StatusFailing       Status = "failing"
	StatusUnhealthyTask Status = "unhealthy_task"
	StatusScaledToZero  Status = "scaled_to_zero"
)

type ClusterStatus string

const (
	ClusterHealthy        ClusterStatus = "healthy"
	ClusterDegraded       ClusterStatus = "degraded"
	ClusterDeploying      ClusterStatus = "deploying"
	ClusterNeedsAttention ClusterStatus = "needs_attention"
)

type Cluster struct {
	Key               ClusterKey
	ARN               string
	Status            string
	RunningTasks      int
	PendingTasks      int
	ActiveServices    int
	ContainerInsights bool // settings containerInsights is enabled or enhanced
	CapacityProviders []string
	Raw               json.RawMessage
	PolledAt          time.Time
}

type Service struct {
	Key                    ServiceKey
	ARN                    string
	Status                 string
	Desired                int
	Running                int
	Pending                int
	TaskDefinitionARN      string
	Family                 string
	Revision               int // of the PRIMARY deployment
	LaunchType             string
	PlatformVersion        string
	SchedulingStrategy     string
	DeploymentController   string // ECS, CODE_DEPLOY or EXTERNAL
	MinHealthyPercent      int
	MaxPercent             int
	CircuitBreakerEnable   bool
	CircuitBreakerRollback bool
	EnableExecuteCommand   bool
	Subnets                []string
	SecurityGroups         []string
	AssignPublicIP         bool
	LoadBalancers          []ServiceLoadBalancer
	Deployments            []Deployment
	// Tags are the service's AWS tags by key. Raw holds them too, but Raw is excluded from the
	// content hash, so this normalized copy is what makes a tag-only change count as changed.
	// JSON encoding sorts map keys, so tag order never affects the hash;
	// omitempty keeps untagged services' existing hashes unchanged.
	Tags      map[string]string `json:",omitempty"`
	CreatedAt time.Time
	Raw       json.RawMessage
	PolledAt  time.Time
}

type ServiceLoadBalancer struct {
	TargetGroupARN string
	ContainerName  string
	ContainerPort  int
}

type Deployment struct {
	ID                 string
	Status             string // PRIMARY, ACTIVE or INACTIVE
	TaskDefinitionARN  string
	Revision           int
	Desired            int
	Running            int
	Pending            int
	Failed             int
	RolloutState       string // COMPLETED, IN_PROGRESS or FAILED
	RolloutStateReason string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ServiceEvent struct {
	Key       ServiceKey
	ID        string
	CreatedAt time.Time
	Message   string
}

type Task struct {
	Cluster           ClusterKey
	ARN               string
	ServiceName       string // "" means standalone
	Group             string
	StartedBy         string
	TaskDefinitionARN string
	Family            string
	Revision          int
	LastStatus        string
	DesiredStatus     string
	HealthStatus      string
	LaunchType        string // FARGATE, FARGATE_SPOT or ""
	CapacityProvider  string
	CPU               string
	Memory            string
	AvailabilityZone  string
	SubnetID          string
	PrivateIP         string
	PublicIP          string
	ENIID             string
	CreatedAt         time.Time
	StartedAt         *time.Time
	StoppingAt        *time.Time
	StoppedAt         *time.Time
	StopCode          string
	StoppedReason     string
	Containers        []Container
	// EnableExecuteCommand is the task's own DescribeTasks flag: whether RunTask/StartTask turned ECS Exec on.
	EnableExecuteCommand bool
	Raw                  json.RawMessage
	PolledAt             time.Time
}

type Container struct {
	Name            string
	Image           string
	ImageDigest     string
	LastStatus      string
	HealthStatus    string
	ExitCode        *int
	Reason          string
	RuntimeID       string
	ExecAgentStatus string // ManagedAgents ExecuteCommandAgent lastStatus
}

type TaskDefinitionRef struct {
	AccountID               string
	Region                  string
	ARN                     string
	Family                  string
	Revision                int
	CPU                     string
	Memory                  string
	NetworkMode             string
	TaskRoleARN             string
	ExecutionRoleARN        string
	RequiresCompatibilities []string
	Containers              []ContainerDef
	EFSVolumes              []EFSVolume
	RegisteredAt            time.Time
	Raw                     json.RawMessage
	PolledAt                time.Time
}

type ContainerDef struct {
	Name            string
	Image           string
	Essential       bool
	LogDriver       string
	LogGroup        string
	LogRegion       string
	LogStreamPrefix string
	MountPoints     []MountPoint
}

type MountPoint struct {
	SourceVolume  string
	ContainerPath string
	ReadOnly      bool
}

type EFSVolume struct {
	Name          string
	FileSystemID  string
	AccessPointID string
}

// IsRunning is the single definition of a live task: anything ECS has not stopped, so tasks that
// are draining (DEPROVISIONING) still count. store's runningSQL mirrors it; keep the two in step.
func (t Task) IsRunning() bool { return t.LastStatus != "STOPPED" }

// ShortID returns the full last path segment of the task ARN. The UI truncates it.
func (t Task) ShortID() string {
	if i := strings.LastIndex(t.ARN, "/"); i >= 0 {
		return t.ARN[i+1:]
	}
	return t.ARN
}

// ExitCode returns the highest non-zero container exit code. If every code is zero,
// it returns the first non-nil code. It returns nil when no container has an exit code.
func (t Task) ExitCode() *int {
	var highestNonZero *int
	var firstSet *int
	for _, container := range t.Containers {
		if container.ExitCode == nil {
			continue
		}
		if firstSet == nil {
			firstSet = container.ExitCode
		}
		if *container.ExitCode != 0 && (highestNonZero == nil || *container.ExitCode > *highestNonZero) {
			highestNonZero = container.ExitCode
		}
	}
	if highestNonZero != nil {
		return highestNonZero
	}
	return firstSet
}

// MainContainer returns the container named serviceName, else the first essential
// container, else the first container. It returns the zero value when there are none.
func (d TaskDefinitionRef) MainContainer(serviceName string) ContainerDef {
	if len(d.Containers) == 0 {
		return ContainerDef{}
	}
	for _, container := range d.Containers {
		if container.Name == serviceName {
			return container
		}
	}
	for _, container := range d.Containers {
		if container.Essential {
			return container
		}
	}
	return d.Containers[0]
}

// ImageTag returns the text after the last ':' of the main container image. A ':' that
// belongs to a registry host port (before the last '/') does not start a tag, so it returns "".
func (d TaskDefinitionRef) ImageTag(serviceName string) string {
	image := d.MainContainer(serviceName).Image
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < strings.LastIndex(image, "/") {
		return ""
	}
	return image[colon+1:]
}
