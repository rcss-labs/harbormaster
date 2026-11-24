package openai

import (
	"net/http"
	"net/url"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// fixedBaseURL is the only host requests go to outside tests.
const fixedBaseURL = "https://api.openai.com/v1/"

// newResponses builds the SDK's Responses service. The service is built
// directly rather than through openai.NewClient, which reads OPENAI_* and
// proxy-related environment variables; here every option is explicit.
func newResponses(apiKey, baseURL string) (responses.ResponseService, error) {
	if baseURL == "" {
		baseURL = fixedBaseURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return responses.ResponseService{}, err
	}
	return responses.NewResponseService(
		option.WithHTTPClient(newHTTPClient()),
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
	), nil
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
