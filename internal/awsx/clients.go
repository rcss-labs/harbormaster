// Package awsx wraps the AWS SDK behind narrow interfaces so the rest of
// Harbormaster (and its tests) never touch the SDK directly.
package awsx

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
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
