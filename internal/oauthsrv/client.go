// Package oauthsrv is Harbormaster's OAuth 2.1 authorization server for MCP connectors: client identification by
// Client ID Metadata Documents, the authorization request and consent, and the token endpoint. It holds no state of
// its own beyond a small cache of client documents; grants, codes and tokens live in Postgres.
package oauthsrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rcss-labs/harbormaster/internal/safedial"
)

// Client is an OAuth client as its metadata document describes it.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
}

var (
	// ErrClientNotAllowed: the client_id is not an https URL on an allowlisted host, or it carries a fragment,
	// credentials or a query. Nothing was fetched.
	ErrClientNotAllowed = errors.New("client is not allowed")
	// ErrClientUnavailable: the metadata document could not be fetched or read (transport failure, a blocked address,
	// a redirect, a status other than 200, over 64 KiB, not JSON, or slower than 5 seconds).
	ErrClientUnavailable = errors.New("client metadata document is unavailable")
	// ErrClientInvalid: the document's client_id differs from its URL, or it lists no redirect URIs.
	ErrClientInvalid = errors.New("client metadata document is invalid")
)

const (
	clientFetchTimeout   = 5 * time.Second
	clientDocumentLimit  = 64 << 10
	clientCacheSuccess   = time.Hour
	clientCacheFailure   = time.Minute
	clientCacheMax       = 100
	clientNameMaxRunes   = 100
	maxRedirectURIsInDoc = 50
)

// ClientResolver turns a client_id URL into a Client by fetching its metadata document from an allowlisted host,
// and decides whether a redirect URI is acceptable for it. Hosts and RedirectPatterns come from configuration
// (HM_OAUTH_CLIENT_HOSTS, HM_OAUTH_REDIRECT_URIS).
type ClientResolver struct {
	Hosts            []string
	RedirectPatterns []string
	HTTP             *http.Client
	Now              func() time.Time
	// AllowExplicitPort admits a client_id with a port. Production leaves it false so an allowlisted host is only
	// ever fetched on the default https port; tests that serve the document on a random port set it.
	AllowExplicitPort bool

	mu    sync.Mutex
	cache map[string]cachedClient
}

type cachedClient struct {
	client  Client
	err     error
	expires time.Time
	added   time.Time
}

// NewClientResolver returns a resolver whose HTTP client dials only public addresses (safedial's zero policy: no
// private, loopback, link-local or metadata addresses, checked at dial time), uses no proxy and follows no redirect.
func NewClientResolver(hosts, redirectPatterns []string) *ClientResolver {
	dialer := &safedial.Dialer{}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    clientFetchTimeout,
			ResponseHeaderTimeout:  clientFetchTimeout,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 16 << 10,
		},
		Timeout:       clientFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &ClientResolver{Hosts: hosts, RedirectPatterns: redirectPatterns, HTTP: client, Now: time.Now}
}

func (r *ClientResolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// checkClientID applies the allowlist to a client_id before anything is fetched.
func (r *ClientResolver) checkClientID(clientID string) error {
	if clientID == "" || strings.ContainsAny(clientID, "#?\\ \t\r\n") {
		return ErrClientNotAllowed
	}
	parsed, err := url.Parse(clientID)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return ErrClientNotAllowed
	}
	if !r.AllowExplicitPort && (parsed.Port() != "" || strings.HasSuffix(parsed.Host, ":")) {
		return ErrClientNotAllowed
	}
	// A metadata document URL has a path, and no dot segments (the CIMD rules).
	if parsed.Path == "" || parsed.Path == "/" {
		return ErrClientNotAllowed
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return ErrClientNotAllowed
		}
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range r.Hosts {
		if host == strings.ToLower(allowed) {
			return nil
		}
	}
	return ErrClientNotAllowed
}

// Resolve returns the client described by the metadata document at clientID. Errors are ErrClientNotAllowed,
// ErrClientUnavailable or ErrClientInvalid, never carrying the fetch's own error text. Results are cached in memory
// for an hour, failures for a minute.
func (r *ClientResolver) Resolve(ctx context.Context, clientID string) (Client, error) {
	if err := r.checkClientID(clientID); err != nil {
		return Client{}, err
	}
	now := r.now()
	if cached, ok := r.cached(clientID, now); ok {
		return cached.client, cached.err
	}
	client, err := r.fetch(ctx, clientID)
	if err != nil && ctx.Err() != nil {
		return Client{}, ErrClientUnavailable // the caller went away; do not remember that as the client's failure
	}
	ttl := clientCacheSuccess
	if err != nil {
		ttl = clientCacheFailure
	}
	r.remember(clientID, cachedClient{client: client, err: err, expires: now.Add(ttl), added: now})
	return client, err
}

func (r *ClientResolver) cached(clientID string, now time.Time) (cachedClient, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[clientID]
	if !ok || !now.Before(entry.expires) {
		return cachedClient{}, false
	}
	return entry, true
}

// remember stores an entry, keeping at most clientCacheMax: expired entries go first, then the oldest.
func (r *ClientResolver) remember(clientID string, entry cachedClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]cachedClient{}
	}
	if _, exists := r.cache[clientID]; !exists && len(r.cache) >= clientCacheMax {
		for id, old := range r.cache {
			if !entry.added.Before(old.expires) {
				delete(r.cache, id)
			}
		}
		for len(r.cache) >= clientCacheMax {
			oldestID, oldest := "", time.Time{}
			for id, old := range r.cache {
				if oldestID == "" || old.added.Before(oldest) {
					oldestID, oldest = id, old.added
				}
			}
			delete(r.cache, oldestID)
		}
	}
	r.cache[clientID] = entry
}

// clientDocument is the part of a Client ID Metadata Document Harbormaster reads.
type clientDocument struct {
	ClientID     string          `json:"client_id"`
	ClientName   string          `json:"client_name"`
	RedirectURIs json.RawMessage `json:"redirect_uris"`
}

func (r *ClientResolver) fetch(ctx context.Context, clientID string) (Client, error) {
	httpClient := r.HTTP
	if httpClient == nil {
		return Client{}, ErrClientUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, clientFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return Client{}, ErrClientUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Harbormaster-OAuth")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Client{}, ErrClientUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Client{}, ErrClientUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, clientDocumentLimit+1))
	if err != nil || len(body) > clientDocumentLimit {
		return Client{}, ErrClientUnavailable
	}
	var doc clientDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return Client{}, ErrClientUnavailable
	}
	if doc.ClientID != clientID {
		return Client{}, ErrClientInvalid
	}
	var rawRedirects []any
	if len(doc.RedirectURIs) > 0 {
		if err := json.Unmarshal(doc.RedirectURIs, &rawRedirects); err != nil {
			return Client{}, ErrClientInvalid
		}
	}
	redirects := make([]string, 0, len(rawRedirects))
	for _, item := range rawRedirects {
		if s, ok := item.(string); ok && s != "" && len(redirects) < maxRedirectURIsInDoc {
			redirects = append(redirects, s)
		}
	}
	if len(redirects) == 0 {
		return Client{}, ErrClientInvalid
	}
	return Client{ID: clientID, Name: cleanClientName(doc.ClientName, clientID), RedirectURIs: redirects}, nil
}

// cleanClientName keeps a printable, bounded name; without one the consent page shows the document's host.
func cleanClientName(name, clientID string) string {
	var b strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(name) {
		if count >= clientNameMaxRunes {
			break
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
		count++
	}
	if cleaned := strings.TrimSpace(b.String()); cleaned != "" {
		return cleaned
	}
	if parsed, err := url.Parse(clientID); err == nil {
		return parsed.Hostname()
	}
	return clientID
}

// RedirectAllowed reports whether the client may use redirectURI: the document lists it (exactly, or for an http
// loopback URI the same scheme, host and path on any port) and it matches a configured pattern (exactly for https;
// for http://localhost/<path> and http://127.0.0.1/<path> the same path on any port). A trailing slash, an added
// query, a fragment or credentials never match.
func (r *ClientResolver) RedirectAllowed(c Client, redirectURI string) bool {
	target, ok := parseRedirect(redirectURI)
	if !ok {
		return false
	}
	listed := false
	for _, entry := range c.RedirectURIs {
		if entry == redirectURI {
			listed = true
			break
		}
		if loopbackMatch(entry, target) {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	for _, pattern := range r.RedirectPatterns {
		if pattern == redirectURI {
			return true
		}
		if loopbackMatch(pattern, target) {
			return true
		}
	}
	return false
}

func parseRedirect(raw string) (*url.URL, bool) {
	if raw == "" || strings.ContainsAny(raw, "#\\ \t\r\n") {
		return nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return nil, false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, false
	}
	return parsed, true
}

// loopbackMatch reports whether target is an http loopback redirect equal to reference except, possibly, its port.
func loopbackMatch(reference string, target *url.URL) bool {
	if target.Scheme != "http" || !isLoopbackHost(target.Hostname()) || target.RawQuery != "" || target.ForceQuery {
		return false
	}
	ref, ok := parseRedirect(reference)
	if !ok || ref.Scheme != "http" || ref.RawQuery != "" || ref.ForceQuery {
		return false
	}
	return ref.Hostname() == target.Hostname() && ref.EscapedPath() == target.EscapedPath() && ref.Path != ""
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}
