package awsx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// RoleSource resolves the role to assume for a connected account. It must
// return an error wrapping ErrAccountUnknown when the account is not
// connected or is disabled.
type RoleSource interface {
	Role(ctx context.Context, accountID string) (roleARN, externalID string, err error)
}

const (
	roleSessionName = "harbormaster"
	roleDuration    = time.Hour
	expiryWindow    = 5 * time.Minute
	maxAttempts     = 2
)

// Real implements Clients against AWS using per-account assumed-role credentials.
type Real struct {
	base  aws.Config
	roles RoleSource

	// newSTS builds the AssumeRole client; overridden in tests.
	newSTS func(aws.Config) stscreds.AssumeRoleAPIClient

	mu    sync.Mutex
	creds map[string]*aws.CredentialsCache
	ecs   map[Target]ECSAPI
}

// NewReal returns a Clients backed by the AWS SDK. base supplies the
// harbormaster identity used to call sts:AssumeRole.
func NewReal(base aws.Config, roles RoleSource) *Real {
	return &Real{
		base:   base,
		roles:  roles,
		newSTS: func(c aws.Config) stscreds.AssumeRoleAPIClient { return sts.NewFromConfig(c) },
		creds:  map[string]*aws.CredentialsCache{},
		ecs:    map[Target]ECSAPI{},
	}
}

// Invalidate drops cached credentials and clients for an account.
func (r *Real) Invalidate(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.creds, accountID)
	for t := range r.ecs {
		if t.AccountID == accountID {
			delete(r.ecs, t)
		}
	}
}

func (r *Real) credsFor(ctx context.Context, accountID string) (*aws.CredentialsCache, error) {
	r.mu.Lock()
	c, ok := r.creds[accountID]
	r.mu.Unlock()
	if ok {
		return c, nil
	}
	roleARN, externalID, err := r.roles.Role(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountUnknown) {
			return nil, err
		}
		return nil, fmt.Errorf("look up role for account %s: %w", accountID, err)
	}
	provider := stscreds.NewAssumeRoleProvider(r.newSTS(r.base), roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = roleSessionName
		o.Duration = roleDuration
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	})
	fresh := aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = expiryWindow })

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.creds[accountID]; ok {
		return existing, nil // lost a race; keep the first
	}
	r.creds[accountID] = fresh
	return fresh, nil
}

// configFor is the single place that builds a per-target aws.Config. It
// retrieves credentials eagerly so role problems surface here.
func (r *Real) configFor(ctx context.Context, t Target) (aws.Config, error) {
	c, err := r.credsFor(ctx, t.AccountID)
	if err != nil {
		return aws.Config{}, err
	}
	if _, err := c.Retrieve(ctx); err != nil {
		return aws.Config{}, fmt.Errorf("assume role in account %s: %w", t.AccountID, err)
	}
	cfg := r.base.Copy()
	cfg.Region = t.Region
	cfg.Credentials = c
	cfg.Retryer = func() aws.Retryer { return retry.AddWithMaxAttempts(retry.NewStandard(), maxAttempts) }
	return cfg, nil
}

// Credentials returns the cached assumed-role credentials for the target's account.
func (r *Real) Credentials(ctx context.Context, t Target) (aws.CredentialsProvider, error) {
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	return cfg.Credentials, nil
}

func (r *Real) ECS(ctx context.Context, t Target) (ECSAPI, error) {
	r.mu.Lock()
	c, ok := r.ecs[t]
	r.mu.Unlock()
	if ok {
		// Still go through credentials so an Invalidate'd/expired account is refreshed.
		if _, err := r.configFor(ctx, t); err != nil {
			return nil, err
		}
		return c, nil
	}
	cfg, err := r.configFor(ctx, t)
	if err != nil {
		return nil, err
	}
	c = ecs.NewFromConfig(cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.ecs[t]; ok {
		return existing, nil
	}
	r.ecs[t] = c
	return c, nil
}

// The clients below are filled in by the plans named in the conventions doc.
func (r *Real) EC2(context.Context, Target) (EC2API, error)                 { return nil, nil } // plan 05
func (r *Real) EFS(context.Context, Target) (EFSAPI, error)                 { return nil, nil } // plan 05
func (r *Real) ELB(context.Context, Target) (ELBAPI, error)                 { return nil, nil } // plan 03
func (r *Real) AutoScaling(context.Context, Target) (AutoScalingAPI, error) { return nil, nil } // plan 03
func (r *Real) CloudWatch(context.Context, Target) (CloudWatchAPI, error)   { return nil, nil } // plan 03
func (r *Real) Logs(context.Context, Target) (LogsAPI, error)               { return nil, nil } // plan 03
func (r *Real) ECR(context.Context, Target) (ECRAPI, error)                 { return nil, nil } // plan 02
func (r *Real) SSM(context.Context, Target) (SSMAPI, error)                 { return nil, nil } // plan 04

var (
	_ Clients = (*Real)(nil)
	_ Prober  = (*Real)(nil)
)
