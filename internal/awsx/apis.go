package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ECSAPI lists only the ECS calls Harbormaster makes.
type ECSAPI interface {
	ListClusters(ctx context.Context, in *ecs.ListClustersInput, opts ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	ListServices(ctx context.Context, in *ecs.ListServicesInput, opts ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	// plan 04 (terminal)
	ExecuteCommand(ctx context.Context, in *ecs.ExecuteCommandInput, opts ...func(*ecs.Options)) (*ecs.ExecuteCommandOutput, error)
}

// SSMAPI lists only the SSM calls Harbormaster makes (plan 04).
type SSMAPI interface {
	TerminateSession(ctx context.Context, in *ssm.TerminateSessionInput, opts ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error)
}

// Placeholders: later plans add the methods they call (see conventions doc).
type (
	EC2API         interface{} // plan 05
	EFSAPI         interface{} // plan 05
	ELBAPI         interface{} // plan 03
	AutoScalingAPI interface{} // plan 03
	CloudWatchAPI  interface{} // plan 03
	LogsAPI        interface{} // plan 03
	ECRAPI         interface{} // plan 02
)
