package anthropic

import (
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// newHTTPClient is the transport for the vendor's fixed host. It reads no proxy
// from the environment and never follows a redirect.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		CheckRedirect: noRedirect,
	}
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// newSDKClient is the one place the SDK client is built. The SDK's environment
// defaults (base URL, keys, profile, custom headers) are switched off so that
// only cfg decides where the key goes, and its own retries are off because the
// agent loop owns retry.
func newSDKClient(cfg Config) sdk.Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = newHTTPClient()
	} else {
		c := *httpClient
		c.CheckRedirect = noRedirect
		httpClient = &c
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	}
	if cfg.Endpoint != "" {
		opts = append(opts, option.WithBaseURL(cfg.Endpoint))
	}
	return sdk.NewClient(opts...)
}
