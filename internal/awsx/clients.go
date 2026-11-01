// Package awsx wraps the AWS SDK behind narrow interfaces so the rest of
// Harbormaster (and its tests) never touch the SDK directly.
package awsx

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ErrAccountUnknown is returned when an account is not connected or is disabled.
var ErrAccountUnknown = errors.New("account not connected")

// Target identifies one account and region.
type Target struct{ AccountID, Region string }

// Clients hands out per-target AWS API clients.
type Clients interface {
	ECS(ctx context.Context, t Target) (ECSAPI, error)
	EC2(ctx context.Context, t Target) (EC2API, error)                          // 05
	EFS(ctx context.Context, t Target) (EFSAPI, error)                          // 05
	ELB(ctx context.Context, t Target) (ELBAPI, error)                          // 03
	AutoScaling(ctx context.Context, t Target) (AutoScalingAPI, error)          // 03
	CloudWatch(ctx context.Context, t Target) (CloudWatchAPI, error)            // 03
	Logs(ctx context.Context, t Target) (LogsAPI, error)                        // 03
	ECR(ctx context.Context, t Target) (ECRAPI, error)                          // 02
	SSM(ctx context.Context, t Target) (SSMAPI, error)                          // 04
	Credentials(ctx context.Context, t Target) (aws.CredentialsProvider, error) // 04 SigV4 on ssmmessages; 03 if needed
}

// SSM returns an SSM client for the target, built from the cached assumed-role config (plan 04).
func (r *Real) SSM(ctx context.Context, t Target) (SSMAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return ssm.NewFromConfig(cfg), nil
}

// The plan 03 clients are cheap to build (credentials are cached per account
// inside configFor), so they are constructed per call.

func (r *Real) CloudWatch(ctx context.Context, t Target) (CloudWatchAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return cloudwatch.NewFromConfig(cfg), nil
}

// plan 05: EC2 and EFS clients, built like the plan 03 ones.

func (r *Real) EC2(ctx context.Context, t Target) (EC2API, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return ec2.NewFromConfig(cfg), nil
}

func (r *Real) EFS(ctx context.Context, t Target) (EFSAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return efs.NewFromConfig(cfg), nil
}

func (r *Real) ELB(ctx context.Context, t Target) (ELBAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return elasticloadbalancingv2.NewFromConfig(cfg), nil
}

func (r *Real) AutoScaling(ctx context.Context, t Target) (AutoScalingAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return applicationautoscaling.NewFromConfig(cfg), nil
}

func (r *Real) Logs(ctx context.Context, t Target) (LogsAPI, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return &realLogs{Client: cloudwatchlogs.NewFromConfig(cfg)}, nil
}

// realLogs adds the LiveTail abstraction to the SDK client; FilterLogEvents
// comes from the embedded client.
type realLogs struct{ *cloudwatchlogs.Client }

func (l *realLogs) LiveTail(ctx context.Context, in *cloudwatchlogs.StartLiveTailInput) (LiveTailStream, error) {
	out, err := l.Client.StartLiveTail(ctx, in)
	if err != nil {
		return nil, err
	}
	return sdkLiveTail{out.GetStream()}, nil
}

// sdkLiveTail adapts the SDK's concrete event stream to LiveTailStream.
type sdkLiveTail struct {
	s *cloudwatchlogs.StartLiveTailEventStream
}

func (s sdkLiveTail) Events() <-chan logstypes.StartLiveTailResponseStream { return s.s.Events() }
func (s sdkLiveTail) Err() error                                           { return s.s.Err() }
func (s sdkLiveTail) Close() error                                         { return s.s.Close() }
