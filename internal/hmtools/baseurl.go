package hmtools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseBaseURL validates a required base URL setting: https, or http only on a
// loopback host; a host; no path, query, fragment or user info. It returns the
// URL as scheme://host[:port] with no trailing slash. Errors name the setting
// and never repeat the value, which may carry a credential.
func ParseBaseURL(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL", name)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("%s must be an absolute URL with a host", name)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !IsLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("%s must use https:// (http:// is allowed only for a loopback host)", name)
		}
	default:
		return "", fmt.Errorf("%s must use https://", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not contain user info, a query or a fragment", name)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("%s must not have a path", name)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// IsLoopbackHost reports whether host is exactly localhost or a loopback IP address.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
