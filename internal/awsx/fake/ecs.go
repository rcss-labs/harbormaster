package fake

import (
	"context"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const (
	describeServicesLimit = 10
	describeTasksLimit    = 100
	maxServiceEvents      = 100
	defaultPageSize       = 100
	pageTokenPrefix       = "fakepage-"
)

// ecsClient is the fake ECS API scoped to one account and region.
type ecsClient struct {
	f      *Fake
	target awsx.Target
}

var _ awsx.ECSAPI = (*ecsClient)(nil)

func invalidParameter(msg string) error {
	return &ecstypes.InvalidParameterException{Message: aws.String(msg)}
}

// paginate slices items by MaxResults/NextToken; the token is an opaque offset.
func paginate[T any](items []T, maxResults *int32, token *string) ([]T, *string, error) {
	size := defaultPageSize
	if maxResults != nil && *maxResults > 0 {
		size = int(*maxResults)
	}
	start := 0
	if token != nil && *token != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(*token, pageTokenPrefix))
		if err != nil || !strings.HasPrefix(*token, pageTokenPrefix) || n < 0 {
			return nil, nil, invalidParameter("Invalid token")
		}
		start = min(n, len(items))
	}
	end := min(start+size, len(items))
	var next *string
	if end < len(items) {
		next = aws.String(pageTokenPrefix + strconv.Itoa(end))
	}
	return append([]T(nil), items[start:end]...), next, nil
}

func clusterNameOf(in *string) string {
	if in == nil || *in == "" {
		return "default"
	}
	return lastSegment(*in)
}

// clusterExists reports whether the named cluster is in this target. Caller holds Mu.
func (c *ecsClient) clusterExists(name string) bool {
	for _, cl := range c.f.Estate.Clusters {
		if cl.AccountID == c.target.AccountID && cl.Region == c.target.Region && aws.ToString(cl.Cluster.ClusterName) == name {
			return true
		}
	}
	return false
}

func (c *ecsClient) requireCluster(name string) error {
	if !c.clusterExists(name) {
		return &ecstypes.ClusterNotFoundException{Message: aws.String("Cluster not found.")}
	}
	return nil
}

func (c *ecsClient) inScope(accountID, region string) bool {
	return accountID == c.target.AccountID && region == c.target.Region
}

func (c *ecsClient) ListClusters(_ context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	if err := c.f.begin("ListClusters"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var arns []string
	for _, cl := range e.Clusters {
		if c.inScope(cl.AccountID, cl.Region) {
			arns = append(arns, aws.ToString(cl.Cluster.ClusterArn))
		}
	}
	page, next, err := paginate(arns, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &ecs.ListClustersOutput{ClusterArns: page, NextToken: next}, nil
}

// DescribeClusters always returns statistics and settings, whatever Include says.
func (c *ecsClient) DescribeClusters(_ context.Context, in *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	if err := c.f.begin("DescribeClusters"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := &ecs.DescribeClustersOutput{}
	for _, ref := range in.Clusters {
		name := lastSegment(ref)
		found := false
		for _, cl := range e.Clusters {
			if !c.inScope(cl.AccountID, cl.Region) || aws.ToString(cl.Cluster.ClusterName) != name {
				continue
			}
			found = true
			out.Clusters = append(out.Clusters, c.withStatistics(cl))
		}
		if !found {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(clusterARN(c.target.Region, c.target.AccountID, name)), Reason: aws.String("MISSING")})
		}
	}
	return out, nil
}

// withStatistics fills the live task/service counts. Caller holds Mu.
func (c *ecsClient) withStatistics(cl Cluster) ecstypes.Cluster {
	e := c.f.Estate
	out := cl.Cluster
	var running, pending, services int32
	name := aws.ToString(out.ClusterName)
	for _, t := range e.Tasks {
		if t.AccountID != cl.AccountID || t.Region != cl.Region || t.ClusterName != name {
			continue
		}
		switch aws.ToString(t.Task.LastStatus) {
		case "RUNNING":
			running++
		case "PENDING", "PROVISIONING", "ACTIVATING":
			pending++
		}
	}
	for _, s := range e.Services {
		if s.AccountID == cl.AccountID && s.Region == cl.Region && s.ClusterName == name {
			services++
		}
	}
	out.RunningTasksCount, out.PendingTasksCount, out.ActiveServicesCount = running, pending, services
	stat := func(k string, v int32) ecstypes.KeyValuePair {
		return ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(strconv.Itoa(int(v)))}
	}
	out.Statistics = []ecstypes.KeyValuePair{
		stat("runningEC2TasksCount", 0), stat("runningFargateTasksCount", running),
		stat("pendingEC2TasksCount", 0), stat("pendingFargateTasksCount", pending),
		stat("activeEC2ServiceCount", 0), stat("activeFargateServiceCount", services),
		stat("drainingEC2ServiceCount", 0), stat("drainingFargateServiceCount", 0),
	}
	return out
}

func (c *ecsClient) ListServices(_ context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	if err := c.f.begin("ListServices"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	var arns []string
	for _, s := range e.Services {
		if c.inScope(s.AccountID, s.Region) && s.ClusterName == cluster {
			arns = append(arns, aws.ToString(s.Service.ServiceArn))
		}
	}
	page, next, err := paginate(arns, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &ecs.ListServicesOutput{ServiceArns: page, NextToken: next}, nil
}

func (c *ecsClient) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if err := c.f.begin("DescribeServices"); err != nil {
		return nil, err
	}
	if len(in.Services) > describeServicesLimit {
		return nil, invalidParameter("services can have at most 10 items.")
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	out := &ecs.DescribeServicesOutput{}
	for _, ref := range in.Services {
		name := lastSegment(ref)
		found := false
		for _, s := range e.Services {
			if !c.inScope(s.AccountID, s.Region) || s.ClusterName != cluster || aws.ToString(s.Service.ServiceName) != name {
				continue
			}
			found = true
			svc := s.Service
			events := e.Events[aws.ToString(svc.ServiceArn)]
			svc.Events = append([]ecstypes.ServiceEvent(nil), events[:min(len(events), maxServiceEvents)]...)
			out.Services = append(out.Services, svc)
		}
		if !found {
			arn := ref
			if !strings.HasPrefix(ref, "arn:") {
				arn = serviceARN(c.target.Region, c.target.AccountID, cluster, ref)
			}
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
		}
	}
	return out, nil
}

// ListTasks filters by DesiredStatus (default RUNNING), ServiceName and Family.
func (c *ecsClient) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if err := c.f.begin("ListTasks"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	desired := "RUNNING"
	if in.DesiredStatus != "" {
		desired = string(in.DesiredStatus)
	}
	var arns []string
	for _, t := range e.Tasks {
		if !c.inScope(t.AccountID, t.Region) || t.ClusterName != cluster || aws.ToString(t.Task.DesiredStatus) != desired {
			continue
		}
		if in.ServiceName != nil && aws.ToString(t.Task.Group) != "service:"+*in.ServiceName {
			continue
		}
		if in.Family != nil && familyOf(aws.ToString(t.Task.TaskDefinitionArn)) != *in.Family {
			continue
		}
		arns = append(arns, aws.ToString(t.Task.TaskArn))
	}
	page, next, err := paginate(arns, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &ecs.ListTasksOutput{TaskArns: page, NextToken: next}, nil
}

// familyOf extracts "family" from ".../task-definition/family:rev".
func familyOf(taskDefinitionARN string) string {
	seg := lastSegment(taskDefinitionARN)
	if i := strings.LastIndex(seg, ":"); i >= 0 {
		return seg[:i]
	}
	return seg
}

func (c *ecsClient) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	if err := c.f.begin("DescribeTasks"); err != nil {
		return nil, err
	}
	if len(in.Tasks) > describeTasksLimit {
		return nil, invalidParameter("tasks can have at most 100 items.")
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		return nil, err
	}
	out := &ecs.DescribeTasksOutput{}
	for _, ref := range in.Tasks {
		id := lastSegment(ref)
		found := false
		for _, t := range e.Tasks {
			if c.inScope(t.AccountID, t.Region) && t.ClusterName == cluster && lastSegment(aws.ToString(t.Task.TaskArn)) == id {
				found = true
				out.Tasks = append(out.Tasks, t.Task)
				break
			}
		}
		if !found {
			arn := ref
			if !strings.HasPrefix(ref, "arn:") {
				arn = taskARN(c.target.Region, c.target.AccountID, cluster, ref)
			}
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
		}
	}
	return out, nil
}

// DescribeTaskDefinition accepts an ARN, "family:revision" or a bare family (latest).
func (c *ecsClient) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	if err := c.f.begin("DescribeTaskDefinition"); err != nil {
		return nil, err
	}
	e := c.f.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	ref := lastSegment(aws.ToString(in.TaskDefinition))
	family, revision := ref, 0
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		family = ref[:i]
		revision, _ = strconv.Atoi(ref[i+1:])
	}
	var best *ecstypes.TaskDefinition
	for i := range e.TaskDefinitions {
		td := &e.TaskDefinitions[i]
		if !c.inScope(td.AccountID, td.Region) || aws.ToString(td.Definition.Family) != family {
			continue
		}
		if revision > 0 && int(td.Definition.Revision) == revision {
			best = &td.Definition
			break
		}
		if revision == 0 && (best == nil || td.Definition.Revision > best.Revision) {
			best = &td.Definition
		}
	}
	if best == nil {
		return nil, &ecstypes.ClientException{Message: aws.String("Unable to describe task definition.")}
	}
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: copyTD(best)}, nil
}

func copyTD(td *ecstypes.TaskDefinition) *ecstypes.TaskDefinition {
	cp := *td
	return &cp
}
