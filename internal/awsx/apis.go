package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
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
}

// Placeholders: later plans add the methods they call (see conventions doc).
type (
	EC2API interface{} // plan 05
	EFSAPI interface{} // plan 05
	ECRAPI interface{} // plan 02
	SSMAPI interface{} // plan 04
)

// CloudWatchAPI lists the CloudWatch calls Harbormaster makes (plan 03).
type CloudWatchAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// ELBAPI lists the ELBv2 calls Harbormaster makes (plan 03).
type ELBAPI interface {
	DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

// AutoScalingAPI lists the Application Auto Scaling calls Harbormaster makes (plan 03).
type AutoScalingAPI interface {
	DescribeScalableTargets(ctx context.Context, in *applicationautoscaling.DescribeScalableTargetsInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalableTargetsOutput, error)
	DescribeScalingPolicies(ctx context.Context, in *applicationautoscaling.DescribeScalingPoliciesInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingPoliciesOutput, error)
	DescribeScheduledActions(ctx context.Context, in *applicationautoscaling.DescribeScheduledActionsInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScheduledActionsOutput, error)
	DescribeScalingActivities(ctx context.Context, in *applicationautoscaling.DescribeScalingActivitiesInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalingActivitiesOutput, error)
}

// LogsAPI lists the CloudWatch Logs calls Harbormaster makes (plan 03).
type LogsAPI interface {
	FilterLogEvents(ctx context.Context, in *cloudwatchlogs.FilterLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
	// LiveTail departs from the SDK signature: the SDK's
	// *StartLiveTailEventStream is a concrete struct that cannot be faked, so
	// the real client wraps it in a LiveTailStream.
	LiveTail(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput) (LiveTailStream, error)
}

// LiveTailStream is a running Logs Live Tail session.
type LiveTailStream interface {
	// Events is closed when the stream ends.
	Events() <-chan logstypes.StartLiveTailResponseStream
	// Err reports why the stream ended; read it after Events() closes.
	Err() error
	Close() error
}
