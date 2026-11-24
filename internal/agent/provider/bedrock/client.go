package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
)

// Config configures the Bedrock provider. Credentials come from the task role
// through the standard AWS credential chain.
type Config struct {
	// Region is the AWS region whose bedrock-runtime endpoint is used.
	Region string

	// The fields below are used only by tests.
	HTTPClient  *http.Client
	Endpoint    string
	Credentials aws.CredentialsProvider
}

// fixedEndpoint always resolves to one URL, whatever the environment or shared
// config says (AWS_ENDPOINT_URL*, FIPS or dual-stack switches).
type fixedEndpoint struct{ uri url.URL }

func (f fixedEndpoint) ResolveEndpoint(context.Context, bedrockruntime.EndpointParameters) (smithyendpoints.Endpoint, error) {
	return smithyendpoints.Endpoint{URI: f.uri}, nil
}

func refuseRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// newHTTPClient builds the only HTTP client used: no proxy from the
// environment, no redirects.
func newHTTPClient(override *http.Client) aws.HTTPClient {
	if override != nil {
		c := *override
		c.CheckRedirect = refuseRedirects
		return &c
	}
	buildable := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
		t.Proxy = nil
	})
	return &http.Client{Transport: buildable.GetTransport(), CheckRedirect: refuseRedirects}
}

func newClient(cfg Config) (*bedrockruntime.Client, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("bedrock: region is required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://bedrock-runtime." + cfg.Region + ".amazonaws.com"
	}
	uri, err := url.Parse(endpoint)
	if err != nil || uri.Host == "" {
		return nil, fmt.Errorf("bedrock: invalid endpoint")
	}
	if cfg.Endpoint == "" && !validRegion(cfg.Region) {
		return nil, fmt.Errorf("bedrock: invalid region")
	}
	loadOpts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
		config.WithHTTPClient(newHTTPClient(cfg.HTTPClient)),
		// The loop owns retries.
		config.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
	}
	if cfg.Credentials != nil {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(cfg.Credentials))
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: load aws config: %w", err)
	}
	return bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = nil
		o.EndpointResolverV2 = fixedEndpoint{uri: *uri}
	}), nil
}

func validRegion(r string) bool {
	if r == "" {
		return false
	}
	for _, c := range r {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
