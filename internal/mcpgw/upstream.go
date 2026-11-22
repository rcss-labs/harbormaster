package mcpgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Version is the gateway's build version, sent in the upstream User-Agent and
// reported as the MCP server version. The command sets it from -ldflags.
var Version = "dev"

// maxUpstreamBodyBytes is the default bound on how much of an upstream answer
// is read. NewHandler lowers it to upstreamReadCap(MaxResultBytes).
const maxUpstreamBodyBytes = 8 << 20

// minUpstreamReadCap is the floor of the per-answer read cap.
const minUpstreamReadCap = 64 << 10

// upstreamReadCap is how much of one upstream answer is read: twice the result
// cap, so an oversized answer is still seen as oversized but many concurrent
// calls cannot each hold megabytes in memory.
func upstreamReadCap(maxResultBytes int) int {
	return min(max(2*maxResultBytes, minUpstreamReadCap), maxUpstreamBodyBytes)
}

// Route is one Harbormaster API route, written exactly as its mux pattern.
type Route struct {
	Method  string
	Pattern string
}

// String renders the route as "GET /api/services/{serviceId}", byte-identical
// to the pattern registered in internal/api.
func (r Route) String() string { return r.Method + " " + r.Pattern }

// Call is one upstream request: a declared route, the values for its path
// placeholders, a query string and an optional JSON body.
type Call struct {
	Route   Route
	Path    map[string]string
	Query   url.Values
	Body    any
	Timeout time.Duration
}

// Response is an upstream answer. On a non-2xx status the error fields hold
// Harbormaster's {"error":{"code","message"}} envelope and Retry-After header.
type Response struct {
	Status       int
	Body         []byte
	ErrorCode    string
	ErrorMessage string
	RetryAfter   string
}

// Upstream is the gateway's only HTTP client. It reaches exactly one base
// URL, only on paths built from a declared route pattern, and never follows a
// redirect.
type Upstream struct {
	base           *url.URL
	defaultTimeout time.Duration
	client         *http.Client
	maxBodyBytes   int64
}

// NewUpstream returns the client for baseURL (scheme and host only). client
// may be nil; a given client is copied and its redirect policy and cookie jar
// are overridden.
func NewUpstream(baseURL string, defaultTimeout time.Duration, client *http.Client) (*Upstream, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("upstream base URL: %w", err)
	}
	if (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("upstream base URL must be an absolute http(s) URL, got %q", baseURL)
	}
	if (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, fmt.Errorf("upstream base URL must be scheme://host[:port] only, got %q", baseURL)
	}
	if defaultTimeout <= 0 {
		return nil, errors.New("upstream default timeout must be positive")
	}

	var httpClient http.Client
	if client != nil {
		httpClient = *client
	} else {
		// No proxy from the environment: the request goes straight to the configured host.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		httpClient = http.Client{Transport: transport}
	}
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Upstream{
		base:           &url.URL{Scheme: base.Scheme, Host: base.Host},
		defaultTimeout: defaultTimeout,
		client:         &httpClient,
		maxBodyBytes:   maxUpstreamBodyBytes,
	}, nil
}

// Do sends c to Harbormaster with the caller's bearer token. The returned
// error is only for a refused call, a transport failure or a timeout; any
// HTTP answer, including a 3xx, is a Response.
func (u *Upstream) Do(ctx context.Context, bearer, forwardedFor string, c Call) (Response, error) {
	if c.Route.Method != http.MethodGet && c.Route.Method != http.MethodPost {
		return Response{}, fmt.Errorf("method %q is not allowed upstream", c.Route.Method)
	}
	rawPath, escapedPath, err := buildPath(c.Route.Pattern, c.Path)
	if err != nil {
		return Response{}, err
	}
	target := &url.URL{
		Scheme:   u.base.Scheme,
		Host:     u.base.Host,
		Path:     rawPath,
		RawPath:  escapedPath,
		RawQuery: c.Query.Encode(),
	}

	var body io.Reader
	if c.Body != nil {
		encoded, err := json.Marshal(c.Body)
		if err != nil {
			return Response{}, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = u.defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, c.Route.Method, target.String(), body)
	if err != nil {
		return Response{}, fmt.Errorf("build upstream request: %w", err)
	}
	// Defence in depth: the request must address the configured host and nothing else.
	if req.URL.Scheme != u.base.Scheme || req.URL.Host != u.base.Host {
		return Response{}, errors.New("upstream request does not address the configured host")
	}
	req.Header = http.Header{}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "harbormaster-mcp/"+Version)
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("upstream %s: %w", c.Route, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, u.maxBodyBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read upstream %s: %w", c.Route, err)
	}

	out := Response{Status: resp.StatusCode, Body: respBody}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		out.RetryAfter = resp.Header.Get("Retry-After")
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &envelope) == nil {
			out.ErrorCode = envelope.Error.Code
			out.ErrorMessage = envelope.Error.Message
		}
	}
	return out, nil
}

// buildPath substitutes each {name} in pattern with its value, returning the
// unescaped path and the escaped one sent on the wire. Each value becomes one
// path segment: it is escaped with url.PathEscape, so "/", "?" and "%" cannot
// change the route, and "." and ".." are refused outright.
func buildPath(pattern string, values map[string]string) (rawPath, escapedPath string, err error) {
	if !strings.HasPrefix(pattern, "/api/") {
		return "", "", fmt.Errorf("route pattern %q is not under /api/", pattern)
	}
	var raw, escaped strings.Builder
	rest := pattern
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			raw.WriteString(rest)
			escaped.WriteString(rest)
			break
		}
		closing := strings.IndexByte(rest[open:], '}')
		if closing < 0 {
			return "", "", fmt.Errorf("route pattern %q has an unclosed placeholder", pattern)
		}
		closing += open
		name := rest[open+1 : closing]
		value, ok := values[name]
		if !ok || value == "" {
			return "", "", fmt.Errorf("path parameter %q is missing", name)
		}
		if value == "." || value == ".." {
			return "", "", fmt.Errorf("path parameter %q is not a valid id", name)
		}
		raw.WriteString(rest[:open])
		raw.WriteString(value)
		escaped.WriteString(rest[:open])
		escaped.WriteString(url.PathEscape(value))
		rest = rest[closing+1:]
	}
	return raw.String(), escaped.String(), nil
}

// relayTokenPath is the one Harbormaster path outside the tool table.
const relayTokenPath = "/api/oauth/token"

const (
	relayTokenTimeout      = 10 * time.Second
	maxRelayTokenBodyBytes = 64 << 10
)

// RelayToken forwards an OAuth token request (a form body, already size and
// type checked) to Harbormaster's token endpoint and returns its status and
// body unchanged. It sends no Authorization header: the endpoint is public and
// the body carries the code or refresh token. It is the only upstream call
// that is not a tool call, and it neither logs nor returns the form.
func (u *Upstream) RelayToken(ctx context.Context, form []byte, forwardedFor string) (status int, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, relayTokenTimeout)
	defer cancel()

	target := &url.URL{Scheme: u.base.Scheme, Host: u.base.Host, Path: relayTokenPath}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(form))
	if err != nil {
		return 0, nil, fmt.Errorf("build upstream token request: %w", err)
	}
	if req.URL.Scheme != u.base.Scheme || req.URL.Host != u.base.Host {
		return 0, nil, errors.New("upstream request does not address the configured host")
	}
	req.Header = http.Header{}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "harbormaster-mcp/"+Version)
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("upstream token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxRelayTokenBodyBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("read upstream token endpoint: %w", err)
	}
	return resp.StatusCode, body, nil
}
