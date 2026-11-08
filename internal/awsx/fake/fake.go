package fake

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

var (
	_ awsx.Clients = (*Fake)(nil)
	_ awsx.Prober  = (*Fake)(nil)
)

// Fake implements awsx.Clients and awsx.Prober on top of an Estate.
type Fake struct {
	Estate *Estate
	// Throttle makes the next N ECS calls fail with ThrottlingException.
	// Access it atomically once calls are in flight; setting it before is fine.
	Throttle int32
	// ECRDelay makes every ECR DescribeImages call wait this long, or until its context ends.
	ECRDelay time.Duration
	// ExecServer backs ECS ExecuteCommand and SSM TerminateSession (plan 04); nil means no exec support.
	ExecServer *SSMServer

	// Plan 03 (observe) knobs. The Throttle* counters work like Throttle but
	// for their own service; set them before calls are in flight or access
	// them atomically.
	ThrottleCloudWatch  int32
	ThrottleELB         int32
	ThrottleAutoScaling int32
	ThrottleLogs        int32
	ThrottleEC2         int32         // plan 05
	ThrottleEFS         int32         // plan 05
	CloudWatchPageSize  int           // datapoints per GetMetricData page; 0 means 100,800 (the AWS limit)
	LogsPageSize        int           // events per FilterLogEvents page; 0 means 100
	LiveTailInterval    time.Duration // gap between live tail updates; 0 means 2 s
	LiveTailError       error         // ends every live tail stream with this error (see LiveTailErrorAfter)
	LiveTailErrorAfter  int           // number of SessionUpdates sent before LiveTailError ends the stream
	liveTailMu          sync.Mutex
	lastLiveTailInput   *cloudwatchlogs.StartLiveTailInput

	// CallLatency delays every API call (plan 06), so concurrent callers overlap and MaxInFlight is meaningful.
	CallLatency time.Duration

	mu         sync.Mutex
	calls      map[string]int
	callCounts map[string]uint64       // "account|region|service|operation"; see CallCounts
	inFlight   map[awsx.Target]*flight // plan 06
	limits     map[awsx.Target]*tokenBucket

	failures     failureQueue    // see failnext.go
	deniedWrites map[string]bool // accounts whose Application Auto Scaling writes fail; see DenyWrites
}

// New wraps an estate.
func New(e *Estate) *Fake {
	f := &Fake{Estate: e, calls: map[string]int{}, callCounts: map[string]uint64{},
		inFlight: map[awsx.Target]*flight{}, limits: map[awsx.Target]*tokenBucket{}}
	f.failFromEnv()
	return f
}

// Calls returns a copy of the per-operation call counters (throttled calls count).
func (f *Fake) Calls() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.calls))
	for k, v := range f.calls {
		out[k] = v
	}
	return out
}

// plan 06: begin and beginThrottled are the single dispatch point of every
// fake API call. Each call is counted (Calls, CallCounts), tracked as in
// flight for its target (MaxInFlight), delayed by CallLatency, then checked
// against the per-target rate limit (SetRateLimit), FailNext and the
// Throttle knobs. The returned function ends the in-flight tracking; callers
// run it with defer, also on error.
//
// Service names used in CallCounts keys (stable, lowercase): "ecs", "ecr",
// "ec2", "efs", "elasticloadbalancing", "application-autoscaling",
// "cloudwatch", "logs", "ssm". The fake SSM client has no target, so its
// calls are counted under the zero Target (empty account and region).

// begin counts the call and applies the ECS throttle knob.
func (f *Fake) begin(t awsx.Target, service, op string) (func(), error) {
	done := f.enter(t, service, op)
	if err := f.takeFailure(op); err != nil {
		return done, err
	}
	if err := f.takeRateToken(t); err != nil {
		return done, err
	}
	return done, takeThrottle(&f.Throttle)
}

// beginThrottled counts the call and applies a per-service throttle knob.
func (f *Fake) beginThrottled(t awsx.Target, service, op string, knob *int32) (func(), error) {
	done := f.enter(t, service, op)
	if err := f.takeFailure(op); err != nil {
		return done, err
	}
	if err := f.takeRateToken(t); err != nil {
		return done, err
	}
	return done, takeThrottle(knob)
}

// takeThrottle consumes one unit of a Throttle knob, if any is left.
func takeThrottle(knob *int32) error {
	for {
		n := atomic.LoadInt32(knob)
		if n <= 0 {
			return nil
		}
		if atomic.CompareAndSwapInt32(knob, n, n-1) {
			return throttlingError()
		}
	}
}

func throttlingError() error {
	return &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded", Fault: smithy.FaultClient}
}

func (f *Fake) knownAccount(id string) bool {
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	for _, a := range f.Estate.Accounts {
		if a.ID == id {
			return true
		}
	}
	return false
}

func (f *Fake) ECS(_ context.Context, t awsx.Target) (awsx.ECSAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &ecsClient{f: f, target: t}, nil
}

// plan 05
func (f *Fake) EC2(_ context.Context, t awsx.Target) (awsx.EC2API, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &EC2{f: f, target: t}, nil
}

func (f *Fake) EFS(_ context.Context, t awsx.Target) (awsx.EFSAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &EFS{f: f, target: t}, nil
}

func (f *Fake) ELB(_ context.Context, t awsx.Target) (awsx.ELBAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &elbClient{f: f, target: t}, nil
}

func (f *Fake) AutoScaling(_ context.Context, t awsx.Target) (awsx.AutoScalingAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &autoScalingClient{f: f, target: t}, nil
}

func (f *Fake) CloudWatch(_ context.Context, t awsx.Target) (awsx.CloudWatchAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &cloudWatchClient{f: f, target: t}, nil
}

func (f *Fake) Logs(_ context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &logsClient{f: f, target: t}, nil
}

func (f *Fake) SSM(_ context.Context, t awsx.Target) (awsx.SSMAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &ssmClient{f: f}, nil
}

func (f *Fake) Credentials(_ context.Context, t awsx.Target) (aws.CredentialsProvider, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return credentials.NewStaticCredentialsProvider("FAKEKEY", "FAKESECRET", ""), nil
}

// Probe succeeds when the role's account is in the estate and an external ID is given.
func (f *Fake) Probe(_ context.Context, roleARN, externalID string, regions []string) []awsx.RegionResult {
	account := ""
	if parts := strings.Split(roleARN, ":"); len(parts) >= 5 {
		account = parts[4]
	}
	results := make([]awsx.RegionResult, len(regions))
	authorised := externalID != "" && f.knownAccount(account)
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	for i, region := range regions {
		if !authorised {
			results[i] = awsx.RegionResult{Region: region, Error: "AccessDenied: not authorized to perform sts:AssumeRole"}
			continue
		}
		n := 0
		for _, c := range f.Estate.Clusters {
			if c.AccountID == account && c.Region == region {
				n++
			}
		}
		results[i] = awsx.RegionResult{Region: region, OK: true, Clusters: n}
	}
	return results
}

// DenyWrites makes every Application Auto Scaling write in the account fail with
// AccessDeniedException, as an IAM policy without those actions would. Reads still work.
func (f *Fake) DenyWrites(accountID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deniedWrites == nil {
		f.deniedWrites = map[string]bool{}
	}
	f.deniedWrites[accountID] = true
}

// writeDenied is the AccessDeniedException for a write to the target's account, or nil.
func (f *Fake) writeDenied(t awsx.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deniedWrites[t.AccountID] {
		return nil
	}
	return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "User is not authorized to perform this application-autoscaling write", Fault: smithy.FaultClient}
}
