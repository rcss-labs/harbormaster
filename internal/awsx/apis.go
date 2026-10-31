package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ecr"
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

	// Plan 02 (change path).
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	ListTaskDefinitions(ctx context.Context, in *ecs.ListTaskDefinitionsInput, opts ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error)
	ListTagsForResource(ctx context.Context, in *ecs.ListTagsForResourceInput, opts ...func(*ecs.Options)) (*ecs.ListTagsForResourceOutput, error)
	StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	// plan 04 (terminal)
	ExecuteCommand(ctx context.Context, in *ecs.ExecuteCommandInput, opts ...func(*ecs.Options)) (*ecs.ExecuteCommandOutput, error)
}

// ECRAPI lists only the ECR calls Harbormaster makes.
type ECRAPI interface {
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
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
)
