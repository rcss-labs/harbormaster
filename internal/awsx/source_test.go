package awsx

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/smithy-go/middleware"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

func TestSource_DefaultsToRequest(t *testing.T) {
	if got := SourceFrom(context.Background()); got != SourceRequest {
		t.Fatalf("SourceFrom(empty) = %q, want %q", got, SourceRequest)
	}
}

func TestSource_WithSourceRoundTrips(t *testing.T) {
	for _, s := range []Source{SourceTier, SourceFocus, SourceRequest, SourceJob} {
		if got := SourceFrom(WithSource(context.Background(), s)); got != s {
			t.Errorf("SourceFrom(WithSource(%q)) = %q", s, got)
		}
	}
}

func TestTelemetryMiddleware_ReportsSource(t *testing.T) {
	reg := telemetry.New()
	client := ecs.NewFromConfig(aws.Config{
		Region:           "us-east-1",
		Credentials:      aws.AnonymousCredentials{},
		RetryMaxAttempts: 1,
		HTTPClient:       &stubHTTP{responses: []stubResponse{{200, `{}`}, {200, `{}`}}},
		APIOptions:       []func(*middleware.Stack) error{TelemetryMiddleware("482109375512", "us-east-1", reg)},
	})
	_, _ = client.ListClusters(WithSource(context.Background(), SourceFocus), &ecs.ListClustersInput{})
	_, _ = client.ListClusters(context.Background(), &ecs.ListClustersInput{})
	calls := reg.Snapshot().AWSCalls
	for _, key := range []string{"482109375512|us-east-1|ECS|ListClusters|ok|focus", "482109375512|us-east-1|ECS|ListClusters|ok|request"} {
		if calls[key] != 1 {
			t.Errorf("%s = %d, want 1 (all: %v)", key, calls[key], calls)
		}
	}
}
