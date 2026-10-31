package fake

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
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

	mu    sync.Mutex
	calls map[string]int
}

// New wraps an estate.
func New(e *Estate) *Fake { return &Fake{Estate: e, calls: map[string]int{}} }

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

// begin counts the call and applies the throttle knob.
func (f *Fake) begin(op string) error {
	f.mu.Lock()
	f.calls[op]++
	f.mu.Unlock()
	for {
		n := atomic.LoadInt32(&f.Throttle)
		if n <= 0 {
			return nil
		}
		if atomic.CompareAndSwapInt32(&f.Throttle, n, n-1) {
			return &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded", Fault: smithy.FaultClient}
		}
	}
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

func (f *Fake) EC2(context.Context, awsx.Target) (awsx.EC2API, error) { return nil, nil }
func (f *Fake) EFS(context.Context, awsx.Target) (awsx.EFSAPI, error) { return nil, nil }
func (f *Fake) ELB(context.Context, awsx.Target) (awsx.ELBAPI, error) { return nil, nil }
func (f *Fake) AutoScaling(context.Context, awsx.Target) (awsx.AutoScalingAPI, error) {
	return nil, nil
}
func (f *Fake) CloudWatch(context.Context, awsx.Target) (awsx.CloudWatchAPI, error) { return nil, nil }
func (f *Fake) Logs(context.Context, awsx.Target) (awsx.LogsAPI, error)             { return nil, nil }

// ECR returns a target-scoped DescribeImages client (see change.go).
func (f *Fake) ECR(_ context.Context, t awsx.Target) (awsx.ECRAPI, error) {
	if !f.knownAccount(t.AccountID) {
		return nil, awsx.ErrAccountUnknown
	}
	return &ecrClient{f: f, target: t}, nil
}

func (f *Fake) SSM(context.Context, awsx.Target) (awsx.SSMAPI, error) { return nil, nil }

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
