package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// RegionResult is the outcome of probing one region.
type RegionResult struct {
	Region   string
	OK       bool
	Clusters int
	Error    string
}

// Prober checks that a role can be assumed and ECS is reachable per region.
type Prober interface {
	Probe(ctx context.Context, roleARN, externalID string, regions []string) []RegionResult
}

// Probe assumes the role once (uncached) and counts ECS clusters per region.
func (r *Real) Probe(ctx context.Context, roleARN, externalID string, regions []string) []RegionResult {
	results := make([]RegionResult, len(regions))
	provider := stscreds.NewAssumeRoleProvider(r.newSTS(r.base), roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = roleSessionName
		o.Duration = roleDuration
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	})
	creds, err := aws.NewCredentialsCache(provider).Retrieve(ctx)
	if err != nil {
		for i, region := range regions {
			results[i] = RegionResult{Region: region, Error: fmt.Sprintf("assume role: %v", err)}
		}
		return results
	}
	static := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil })

	for i, region := range regions {
		cfg := r.base.Copy()
		cfg.Region = region
		cfg.Credentials = static
		cfg.Retryer = func() aws.Retryer { return retry.AddWithMaxAttempts(retry.NewStandard(), maxAttempts) }
		results[i] = probeRegion(ctx, ecs.NewFromConfig(cfg), region)
	}
	return results
}

func probeRegion(ctx context.Context, client ECSAPI, region string) RegionResult {
	res := RegionResult{Region: region}
	var token *string
	for {
		out, err := client.ListClusters(ctx, &ecs.ListClustersInput{NextToken: token})
		if err != nil {
			res.Error = err.Error()
			res.Clusters = 0
			return res
		}
		res.Clusters += len(out.ClusterArns)
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	res.OK = true
	return res
}
