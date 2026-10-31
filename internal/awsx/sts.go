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
	gen   map[string]uint64 // per-account generation, bumped by Invalidate
	creds map[string]*aws.CredentialsCache
	ecs   map[Target]ecsEntry
}

// ecsEntry remembers the account generation a client was built under so a
// client built before an Invalidate is never served afterwards.
type ecsEntry struct {
	client ECSAPI
	gen    uint64
}

// maxBuildAttempts bounds retries when Invalidate keeps racing a build.
const maxBuildAttempts = 3

var errInvalidatedDuringBuild = errors.New("account was modified while building clients")

// NewReal returns a Clients backed by the AWS SDK. base supplies the
// harbormaster identity used to call sts:AssumeRole.
func NewReal(base aws.Config, roles RoleSource) *Real {
	return &Real{
		base:   base,
		roles:  roles,
		newSTS: func(c aws.Config) stscreds.AssumeRoleAPIClient { return sts.NewFromConfig(c) },
		gen:    map[string]uint64{},
		creds:  map[string]*aws.CredentialsCache{},
		ecs:    map[Target]ecsEntry{},
	}
}

// Invalidate drops cached credentials and clients for an account. Builds that
// are in flight notice the generation change and discard their result.
func (r *Real) Invalidate(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen[accountID]++
	delete(r.creds, accountID)
	for t := range r.ecs {
		if t.AccountID == accountID {
			delete(r.ecs, t)
		}
	}
}

// credsFor returns the account's credential cache and the generation it
// belongs to. A result built under a stale generation is never stored.
func (r *Real) credsFor(ctx context.Context, accountID string) (*aws.CredentialsCache, uint64, error) {
	r.mu.Lock()
	c, ok := r.creds[accountID]
	startGen := r.gen[accountID]
	r.mu.Unlock()
	if ok {
		return c, startGen, nil
	}
	roleARN, externalID, err := r.roles.Role(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountUnknown) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("look up role for account %s: %w", accountID, err)
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
	if r.gen[accountID] != startGen {
		return nil, 0, errInvalidatedDuringBuild
	}
	if existing, ok := r.creds[accountID]; ok {
		return existing, startGen, nil // lost a race; keep the first
	}
	r.creds[accountID] = fresh
	return fresh, startGen, nil
}

// buildConfig builds a per-target aws.Config and the generation it was built
// under. Credentials are retrieved eagerly so role problems surface here.
// errInvalidatedDuringBuild means Invalidate ran meanwhile; callers retry.
func (r *Real) buildConfig(ctx context.Context, t Target) (aws.Config, uint64, error) {
	c, gen, err := r.credsFor(ctx, t.AccountID)
	if err != nil {
		return aws.Config{}, 0, err
	}
	if _, err := c.Retrieve(ctx); err != nil {
		return aws.Config{}, 0, fmt.Errorf("assume role in account %s: %w", t.AccountID, err)
	}
	r.mu.Lock()
	stale := r.gen[t.AccountID] != gen
	r.mu.Unlock()
	if stale {
		return aws.Config{}, 0, errInvalidatedDuringBuild
	}
	cfg := r.base.Copy()
	cfg.Region = t.Region
	cfg.Credentials = c
	cfg.Retryer = func() aws.Retryer { return retry.AddWithMaxAttempts(retry.NewStandard(), maxAttempts) }
	return cfg, gen, nil
}

// configFor is the single place that builds a per-target aws.Config.
func (r *Real) configFor(ctx context.Context, t Target) (aws.Config, error) {
	for i := 0; i < maxBuildAttempts; i++ {
		cfg, _, err := r.buildConfig(ctx, t)
		if !errors.Is(err, errInvalidatedDuringBuild) {
			return cfg, err
		}
	}
	return aws.Config{}, errInvalidatedDuringBuild
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
	for i := 0; i < maxBuildAttempts; i++ {
		r.mu.Lock()
		e, ok := r.ecs[t]
		current := r.gen[t.AccountID]
		r.mu.Unlock()
		if ok && e.gen == current {
			return e.client, nil
		}
		cfg, gen, err := r.buildConfig(ctx, t)
		if errors.Is(err, errInvalidatedDuringBuild) {
			continue
		}
		if err != nil {
			return nil, err
		}
		client := ecs.NewFromConfig(cfg)
		r.mu.Lock()
		if r.gen[t.AccountID] != gen {
			r.mu.Unlock()
			continue
		}
		if existing, ok := r.ecs[t]; ok && existing.gen == gen {
			r.mu.Unlock()
			return existing.client, nil
		}
		r.ecs[t] = ecsEntry{client: client, gen: gen}
		r.mu.Unlock()
		return client, nil
	}
	return nil, errInvalidatedDuringBuild
}

// The clients below are filled in by the plans named in the conventions doc.
func (r *Real) EC2(context.Context, Target) (EC2API, error)                 { return nil, nil } // plan 05
func (r *Real) EFS(context.Context, Target) (EFSAPI, error)                 { return nil, nil } // plan 05
func (r *Real) ELB(context.Context, Target) (ELBAPI, error)                 { return nil, nil } // plan 03
func (r *Real) AutoScaling(context.Context, Target) (AutoScalingAPI, error) { return nil, nil } // plan 03
func (r *Real) CloudWatch(context.Context, Target) (CloudWatchAPI, error)   { return nil, nil } // plan 03
func (r *Real) Logs(context.Context, Target) (LogsAPI, error)               { return nil, nil } // plan 03
func (r *Real) ECR(context.Context, Target) (ECRAPI, error)                 { return nil, nil } // plan 02

var (
	_ Clients = (*Real)(nil)
	_ Prober  = (*Real)(nil)
)
