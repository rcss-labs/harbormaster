package fake

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// Change-path calls: task definition registration and listing, tags, task
// stops and ECR image listing.

const (
	fakeHarbormasterRole = "harbormaster-access"
	stopEventTaskChars   = 12
)

func (c *ecsClient) RegisterTaskDefinition(_ context.Context, in *ecs.RegisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	if err := c.f.begin("RegisterTaskDefinition"); err != nil {
		return nil, err
	}
	family := aws.ToString(in.Family)
	if family == "" {
		return nil, invalidParameter("Family is required.")
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	latest := 0
	for _, td := range e.TaskDefinitions {
		if c.inScope(td.AccountID, td.Region) && aws.ToString(td.Definition.Family) == family {
			latest = max(latest, int(td.Definition.Revision))
		}
	}
	revision := latest + 1
	td := ecstypes.TaskDefinition{
		Family:                  aws.String(family),
		Revision:                int32(revision),
		TaskDefinitionArn:       aws.String(taskDefinitionARN(c.target.Region, c.target.AccountID, family, revision)),
		ContainerDefinitions:    deepCopy(in.ContainerDefinitions),
		Cpu:                     deepCopy(in.Cpu),
		Memory:                  deepCopy(in.Memory),
		ExecutionRoleArn:        deepCopy(in.ExecutionRoleArn),
		TaskRoleArn:             deepCopy(in.TaskRoleArn),
		NetworkMode:             in.NetworkMode,
		Volumes:                 deepCopy(in.Volumes),
		PlacementConstraints:    deepCopy(in.PlacementConstraints),
		RequiresCompatibilities: deepCopy(in.RequiresCompatibilities),
		EphemeralStorage:        deepCopy(in.EphemeralStorage),
		RuntimePlatform:         deepCopy(in.RuntimePlatform),
		IpcMode:                 in.IpcMode,
		PidMode:                 in.PidMode,
		ProxyConfiguration:      deepCopy(in.ProxyConfiguration),
		InferenceAccelerators:   deepCopy(in.InferenceAccelerators),
		Status:                  ecstypes.TaskDefinitionStatusActive,
		RegisteredAt:            aws.Time(e.now()),
		RegisteredBy:            aws.String("arn:aws:iam::" + c.target.AccountID + ":role/" + fakeHarbormasterRole),
	}
	e.TaskDefinitions = append(e.TaskDefinitions, TaskDefinition{
		AccountID: c.target.AccountID, Region: c.target.Region, Definition: td, Tags: deepCopy(in.Tags)})
	return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: ptrTo(deepCopy(td))}, nil
}

// ListTaskDefinitions matches AWS: family-prefix filter, ACTIVE unless a status
// is given, grouped by family then revision, descending when asked.
func (c *ecsClient) ListTaskDefinitions(_ context.Context, in *ecs.ListTaskDefinitionsInput, _ ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error) {
	if err := c.f.begin("ListTaskDefinitions"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	status := ecstypes.TaskDefinitionStatusActive
	if in.Status != "" {
		status = in.Status
	}
	prefix := aws.ToString(in.FamilyPrefix)
	var matched []TaskDefinition
	for _, td := range e.TaskDefinitions {
		if c.inScope(td.AccountID, td.Region) && td.Definition.Status == status &&
			strings.HasPrefix(aws.ToString(td.Definition.Family), prefix) {
			matched = append(matched, td)
		}
	}
	descending := in.Sort == ecstypes.SortOrderDesc
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i].Definition, matched[j].Definition
		if descending {
			a, b = b, a
		}
		if fa, fb := aws.ToString(a.Family), aws.ToString(b.Family); fa != fb {
			return fa < fb
		}
		return a.Revision < b.Revision
	})
	arns := make([]string, len(matched))
	for i, td := range matched {
		arns[i] = aws.ToString(td.Definition.TaskDefinitionArn)
	}
	page, next, err := paginate(arns, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &ecs.ListTaskDefinitionsOutput{TaskDefinitionArns: page, NextToken: next}, nil
}

// ListTagsForResource answers for task definition ARNs in this target.
func (c *ecsClient) ListTagsForResource(_ context.Context, in *ecs.ListTagsForResourceInput, _ ...func(*ecs.Options)) (*ecs.ListTagsForResourceOutput, error) {
	if err := c.f.begin("ListTagsForResource"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	arn := aws.ToString(in.ResourceArn)
	for i := range e.TaskDefinitions {
		td := &e.TaskDefinitions[i]
		if c.inScope(td.AccountID, td.Region) && aws.ToString(td.Definition.TaskDefinitionArn) == arn {
			return &ecs.ListTagsForResourceOutput{Tags: deepCopy(td.Tags)}, nil
		}
	}
	return nil, &ecstypes.ResourceNotFoundException{Message: aws.String("The specified resource does not exist.")}
}

// StopTask marks a running task STOPPED and records a service event for it.
// A task that is already stopped is returned unchanged.
func (c *ecsClient) StopTask(_ context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	if err := c.f.begin("StopTask"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	id := lastSegment(aws.ToString(in.Task))
	for i := range e.Tasks {
		t := &e.Tasks[i]
		if !c.inScope(t.AccountID, t.Region) || t.ClusterName != cluster || lastSegment(aws.ToString(t.Task.TaskArn)) != id {
			continue
		}
		if aws.ToString(t.Task.LastStatus) != "STOPPED" {
			c.markStopped(t, aws.ToString(in.Reason))
		}
		return &ecs.StopTaskOutput{Task: ptrTo(deepCopy(t.Task))}, nil
	}
	return nil, invalidParameter("The referenced task was not found.")
}

// markStopped applies a user-initiated stop to t, records the service event
// and refreshes the owning service's counts. Caller holds Mu.
func (c *ecsClient) markStopped(t *Task, reason string) {
	e := c.f.Estate
	now := e.now()
	task := t.Task
	task.LastStatus = aws.String("STOPPED")
	task.DesiredStatus = aws.String("STOPPED")
	task.StoppedAt = aws.Time(now)
	task.StoppingAt = aws.Time(now)
	task.StoppedReason = aws.String(reason)
	task.StopCode = ecstypes.TaskStopCodeUserInitiated
	containers := make([]ecstypes.Container, len(task.Containers))
	for j, container := range task.Containers {
		container.LastStatus = aws.String("STOPPED")
		container.ExitCode = aws.Int32(0)
		containers[j] = container
	}
	task.Containers = containers
	t.Task = task

	for si := range e.Services {
		svc := e.Services[si]
		if svc.AccountID != t.AccountID || svc.Region != t.Region || svc.ClusterName != t.ClusterName ||
			"service:"+aws.ToString(svc.Service.ServiceName) != aws.ToString(task.Group) {
			continue
		}
		serviceName := aws.ToString(svc.Service.ServiceName)
		arn := aws.ToString(svc.Service.ServiceArn)
		message := fmt.Sprintf("(service %s) has stopped 1 running tasks: (task %s).", serviceName, id12(aws.ToString(task.TaskArn)))
		if e.Events == nil {
			e.Events = map[string][]ecstypes.ServiceEvent{}
		}
		event := ecstypes.ServiceEvent{Id: aws.String("stop-" + lastSegment(aws.ToString(task.TaskArn))), CreatedAt: aws.Time(now), Message: aws.String(message)}
		events := append([]ecstypes.ServiceEvent{event}, e.Events[arn]...)
		e.Events[arn] = events[:min(len(events), maxServiceEvents)]
		e.recountService(si)
		return
	}
}

// id12 is the short task id ECS uses in event messages.
func id12(taskARN string) string {
	id := lastSegment(taskARN)
	return id[:min(len(id), stopEventTaskChars)]
}

// ecrClient is the fake ECR API scoped to one account and region.
type ecrClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.ECRAPI = (*ecrClient)(nil)

// DescribeImages returns one image per seeded tag of the repository, newest
// first. A repository is one that a service in this target pulls from.
func (c *ecrClient) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	if err := c.f.begin("DescribeImages"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	repository := aws.ToString(in.RepositoryName)
	now := e.now()
	for _, svc := range e.Services {
		if !c.inScope(svc.AccountID, svc.Region) || c.repositoryOf(svc) != repository {
			continue
		}
		out := &ecr.DescribeImagesOutput{}
		for i, tag := range svc.ECRTags {
			out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{
				RegistryId:     aws.String(c.target.AccountID),
				RepositoryName: aws.String(repository),
				ImageDigest:    aws.String(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(repository+":"+tag)))),
				ImageTags:      []string{tag},
				ImagePushedAt:  aws.Time(now.Add(-time.Duration(i+1) * 24 * time.Hour)),
			})
		}
		return out, nil
	}
	return nil, &ecrtypes.RepositoryNotFoundException{Message: aws.String(
		"The repository with name '" + repository + "' does not exist in the registry with id '" + c.target.AccountID + "'")}
}

// repositoryOf is the ECR repository the service's primary task definition
// pulls its image from. Caller holds Mu.
func (c *ecrClient) repositoryOf(svc Service) string {
	primary := aws.ToString(svc.Service.TaskDefinition)
	for _, td := range c.f.Estate.TaskDefinitions {
		if aws.ToString(td.Definition.TaskDefinitionArn) != primary || len(td.Definition.ContainerDefinitions) == 0 {
			continue
		}
		image := aws.ToString(td.Definition.ContainerDefinitions[0].Image)
		image = image[strings.LastIndex(image, "/")+1:]
		if i := strings.Index(image, ":"); i >= 0 {
			image = image[:i]
		}
		return image
	}
	return ""
}

func (c *ecrClient) inScope(accountID, region string) bool {
	return accountID == c.target.AccountID && region == c.target.Region
}
