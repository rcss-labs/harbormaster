// Package google implements provider.Provider on the Gemini API (not Vertex AI).
package google

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"google.golang.org/genai"
)

const (
	// vendorBaseURL is the only host requests go to, unless a test overrides it.
	vendorBaseURL = "https://generativelanguage.googleapis.com/"
	apiVersion    = "v1beta"
)

// Config holds what the provider needs. BaseURL and HTTPClient exist for tests.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// retryAfterSink carries the Retry-After header of the last response out of the
// SDK, whose errors do not keep response headers.
type retryAfterSink struct{ value string }

type sinkKey struct{}

type headerRecorder struct{ next http.RoundTripper }

func (h headerRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := h.next.RoundTrip(r)
	if sink, ok := r.Context().Value(sinkKey{}).(*retryAfterSink); ok && resp != nil {
		sink.value = resp.Header.Get("Retry-After")
	}
	return resp, err
}

// newSDKClient is the one place the SDK client and its HTTP client are built.
// Everything the SDK would otherwise read from the environment (API key,
// backend, project, base URL, API version, proxy) is set explicitly here.
func newSDKClient(cfg Config) (*genai.Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("google provider: an API key is required")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = vendorBaseURL
	}

	httpClient := &http.Client{}
	if cfg.HTTPClient != nil {
		*httpClient = *cfg.HTTPClient
	}
	if httpClient.Transport == nil {
		httpClient.Transport = &http.Transport{
			Proxy:                 nil, // never the environment's proxy
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	httpClient.Transport = headerRecorder{next: httpClient.Transport}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:     cfg.APIKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: httpClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    baseURL,
			APIVersion: apiVersion,
			// A nil RetryOptions means the SDK does not retry; the loop owns retry.
		},
	})
	if err != nil {
		// The SDK's error text can include the client config, which holds the key.
		return nil, fmt.Errorf("google provider: could not build the client")
	}
	return client, nil
}
