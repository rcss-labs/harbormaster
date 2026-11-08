package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
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

// plan 05 (networks and storage)

// EC2API lists the EC2 calls Harbormaster makes.
type EC2API interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeInternetGateways(ctx context.Context, in *ec2.DescribeInternetGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error)
	DescribeVpcEndpoints(ctx context.Context, in *ec2.DescribeVpcEndpointsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// EFSAPI lists the EFS calls Harbormaster makes.
type EFSAPI interface {
	DescribeFileSystems(ctx context.Context, in *efs.DescribeFileSystemsInput, opts ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error)
	DescribeAccessPoints(ctx context.Context, in *efs.DescribeAccessPointsInput, opts ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error)
	DescribeMountTargets(ctx context.Context, in *efs.DescribeMountTargetsInput, opts ...func(*efs.Options)) (*efs.DescribeMountTargetsOutput, error)
}

// CloudWatchAPI lists the CloudWatch calls Harbormaster makes (plan 03; the writes are the Deploy tab's scaling edits).
type CloudWatchAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// ELBAPI lists the ELBv2 calls Harbormaster makes (plan 03; the writes are the Deploy tab's scaling edits).
type ELBAPI interface {
	DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

// AutoScalingAPI lists the Application Auto Scaling calls Harbormaster makes (plan 03; the writes are the Deploy tab's scaling edits).
type AutoScalingAPI interface {
	DescribeScalableTargets(ctx context.Context, in *applicationautoscaling.DescribeScalableTargetsInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalableTargetsOutput, error)
	DescribeScalingPolicies(ctx context.Context, in *applicationautoscaling.DescribeScalingPoliciesInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingPoliciesOutput, error)
	DescribeScheduledActions(ctx context.Context, in *applicationautoscaling.DescribeScheduledActionsInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScheduledActionsOutput, error)
	DescribeScalingActivities(ctx context.Context, in *applicationautoscaling.DescribeScalingActivitiesInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingActivitiesOutput, error)
	RegisterScalableTarget(ctx context.Context, in *applicationautoscaling.RegisterScalableTargetInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error)
	DeregisterScalableTarget(ctx context.Context, in *applicationautoscaling.DeregisterScalableTargetInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeregisterScalableTargetOutput, error)
	PutScalingPolicy(ctx context.Context, in *applicationautoscaling.PutScalingPolicyInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScalingPolicyOutput, error)
	DeleteScalingPolicy(ctx context.Context, in *applicationautoscaling.DeleteScalingPolicyInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScalingPolicyOutput, error)
	PutScheduledAction(ctx context.Context, in *applicationautoscaling.PutScheduledActionInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScheduledActionOutput, error)
	DeleteScheduledAction(ctx context.Context, in *applicationautoscaling.DeleteScheduledActionInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScheduledActionOutput, error)
}

// LogsAPI lists the CloudWatch Logs calls Harbormaster makes (plan 03; the writes are the Deploy tab's scaling edits).
type LogsAPI interface {
	FilterLogEvents(ctx context.Context, in *cloudwatchlogs.FilterLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
	// LiveTail departs from the SDK signature: the SDK's
	// *StartLiveTailEventStream is a concrete struct that cannot be faked, so
	// the real client wraps it in a LiveTailStream.
	LiveTail(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput) (LiveTailStream, error)

	// Logs Insights (log search); these keep the SDK signatures.
	StartQuery(ctx context.Context, in *cloudwatchlogs.StartQueryInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(ctx context.Context, in *cloudwatchlogs.GetQueryResultsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	StopQuery(ctx context.Context, in *cloudwatchlogs.StopQueryInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error)
}

// LiveTailStream is a running Logs Live Tail session.
type LiveTailStream interface {
	// Events is closed when the stream ends.
	Events() <-chan logstypes.StartLiveTailResponseStream
	// Err reports why the stream ended; read it after Events() closes.
	Err() error
	Close() error
}
