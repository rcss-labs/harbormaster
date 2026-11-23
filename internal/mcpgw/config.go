package mcpgw

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults and bounds for the gateway settings (design, "Part 2: the gateway").
const (
	defaultListen          = ":8481"
	defaultMaxResultBytes  = 100000
	minMaxResultBytes      = 10000
	maxMaxResultBytes      = 140000
	defaultUpstreamTimeout = 30 * time.Second
	minUpstreamTimeout     = time.Second
	maxUpstreamTimeout     = 120 * time.Second
)

// Config is the gateway's whole configuration. It holds no secret.
type Config struct {
	Listen      string
	UpstreamURL string
	PublicURL   string
	// AuthorizeURL is Harbormaster's private base URL that hosts /oauth/authorize. Empty turns OAuth off.
	AuthorizeURL string
	// AuthorizeViaGateway publishes the gateway's own /oauth/authorize (a bare redirect to AuthorizeURL) as the
	// authorization endpoint, for a client that refuses an endpoint on another host than the issuer.
	AuthorizeViaGateway bool
	AllowedOrigins      []string
	TrustedProxy        bool
	MaxResultBytes      int
	UpstreamTimeout     time.Duration
}

// LoadConfig reads the HM_MCP_* settings through getenv and validates them.
// Every error names the variable at fault.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:          defaultListen,
		MaxResultBytes:  defaultMaxResultBytes,
		UpstreamTimeout: defaultUpstreamTimeout,
	}
	if listen := strings.TrimSpace(getenv("HM_MCP_LISTEN")); listen != "" {
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return Config{}, fmt.Errorf("HM_MCP_LISTEN must be host:port: %w", err)
		}
		cfg.Listen = listen
	}

	upstream, err := parseBaseURL("HM_MCP_UPSTREAM_URL", getenv("HM_MCP_UPSTREAM_URL"))
	if err != nil {
		return Config{}, err
	}
	cfg.UpstreamURL = upstream

	public, err := parseBaseURL("HM_MCP_PUBLIC_URL", getenv("HM_MCP_PUBLIC_URL"))
	if err != nil {
		return Config{}, err
	}
	cfg.PublicURL = public

	if raw := strings.TrimSpace(getenv("HM_MCP_AUTHORIZE_URL")); raw != "" {
		authorize, err := parseBaseURL("HM_MCP_AUTHORIZE_URL", raw)
		if err != nil {
			return Config{}, err
		}
		cfg.AuthorizeURL = authorize
	}

	if raw := strings.TrimSpace(getenv("HM_MCP_AUTHORIZE_VIA_GATEWAY")); raw != "" {
		via, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HM_MCP_AUTHORIZE_VIA_GATEWAY must be true or false, got %q", raw)
		}
		if via && cfg.AuthorizeURL == "" {
			return Config{}, fmt.Errorf("HM_MCP_AUTHORIZE_VIA_GATEWAY=true needs HM_MCP_AUTHORIZE_URL: the gateway's authorize redirect has no target")
		}
		cfg.AuthorizeViaGateway = via
	}

	for _, origin := range strings.Split(getenv("HM_MCP_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
		}
	}

	if raw := strings.TrimSpace(getenv("HM_MCP_TRUSTED_PROXY")); raw != "" {
		trusted, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HM_MCP_TRUSTED_PROXY must be true or false, got %q", raw)
		}
		cfg.TrustedProxy = trusted
	}

	if raw := strings.TrimSpace(getenv("HM_MCP_MAX_RESULT_BYTES")); raw != "" {
		maxBytes, err := strconv.Atoi(raw)
		if err != nil || maxBytes < minMaxResultBytes || maxBytes > maxMaxResultBytes {
			return Config{}, fmt.Errorf("HM_MCP_MAX_RESULT_BYTES must be a number between %d and %d, got %q",
				minMaxResultBytes, maxMaxResultBytes, raw)
		}
		cfg.MaxResultBytes = maxBytes
	}

	if raw := strings.TrimSpace(getenv("HM_MCP_UPSTREAM_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout < minUpstreamTimeout || timeout > maxUpstreamTimeout {
			return Config{}, fmt.Errorf("HM_MCP_UPSTREAM_TIMEOUT must be a duration between %s and %s, got %q",
				minUpstreamTimeout, maxUpstreamTimeout, raw)
		}
		cfg.UpstreamTimeout = timeout
	}
	return cfg, nil
}

// MCPURL is the MCP endpoint clients connect to.
func (c Config) MCPURL() string { return c.PublicURL + "/mcp" }

// OAuthEnabled reports whether the gateway publishes OAuth metadata and relays the token endpoint.
func (c Config) OAuthEnabled() bool { return c.AuthorizeURL != "" }

// parseBaseURL validates a required base URL: https, or http only on a
// loopback host; a host; no path, query, fragment or user info. It returns the
// URL as scheme://host[:port] with no trailing slash.
func parseBaseURL(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL: %w", name, err)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%s must be an absolute URL with a host, got %q", name, raw)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("%s must use https:// (http:// is allowed only for a loopback host), got %q", name, raw)
		}
	default:
		return "", fmt.Errorf("%s must use https://, got %q", name, raw)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not contain user info, a query or a fragment, got %q", name, raw)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%s must not have a path, got %q", name, raw)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
