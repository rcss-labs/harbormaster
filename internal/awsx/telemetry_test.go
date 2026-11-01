package awsx

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/smithy-go/middleware"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

type stubHTTP struct{ responses []stubResponse }

type stubResponse struct {
	status int
	body   string
}

func (s *stubHTTP) Do(*http.Request) (*http.Response, error) {
	r := s.responses[0]
	s.responses = s.responses[1:]
	return &http.Response{
		StatusCode: r.status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(bytes.NewBufferString(r.body)),
	}, nil
}

func TestTelemetryMiddleware_CountsOperationAndResult(t *testing.T) {
	reg := telemetry.New()
	client := ecs.NewFromConfig(aws.Config{
		Region:           "us-east-1",
		Credentials:      aws.AnonymousCredentials{},
		RetryMaxAttempts: 1,
		HTTPClient: &stubHTTP{responses: []stubResponse{
			{200, `{}`},
			{400, `{"__type":"ThrottlingException","message":"Rate exceeded"}`},
		}},
		APIOptions: []func(*middleware.Stack) error{TelemetryMiddleware("482109375512", "us-east-1", reg)},
	})
	for i := 0; i < 2; i++ {
		_, _ = client.ListClusters(t.Context(), &ecs.ListClustersInput{})
	}
	calls := reg.Snapshot().AWSCalls
	for _, key := range []string{"482109375512|us-east-1|ECS|ListClusters|ok", "482109375512|us-east-1|ECS|ListClusters|throttled"} {
		if calls[key] != 1 {
			t.Errorf("%s = %d, want 1 (all: %v)", key, calls[key], calls)
		}
	}
}
