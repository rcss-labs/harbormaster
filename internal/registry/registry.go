// Package registry reads image manifests from public container registries for the server: the digest a reference
// points at and the platforms an image index lists. It is the server's copy of the scanner's registry client (the
// scanner may import only internal/scan). Every connection goes through the dial-time address guard of
// internal/safedial, and every body it reads is bounded.
package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/safedial"
)

// Limits of what a registry may make the server read.
const (
	MaxDocumentBytes = 1 << 20
	MaxPlatforms     = 32
	maxTokenBytes    = 64 << 10
	maxRedirects     = 5
	maxRefLength     = 255
	dockerHubHost    = "registry-1.docker.io"
)

var (
	ErrNotFound    = errors.New("image not found")
	ErrDenied      = errors.New("registry refused access")
	ErrRateLimited = errors.New("registry rate limit")
	ErrTooLarge    = errors.New("registry document too large")
	ErrBlocked     = safedial.ErrBlocked
	errInvalidRef  = errors.New("the image reference is not valid")
)

var (
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	refHostPattern    = regexp.MustCompile(`^[a-zA-Z0-9]+(?:[.-][a-zA-Z0-9]+)*(?::[0-9]{1,5})?$`)
	refNamePattern    = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	refTagPattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	platformPattern   = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(/[a-z0-9]+)?$`)
	bearerParamRegexp = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^,\s]+))`)
)

const (
	mediaOCIIndex    = "application/vnd.oci.image.index.v1+json"
	mediaDockerList  = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerV2    = "application/vnd.docker.distribution.manifest.v2+json"
)

var manifestAccept = strings.Join([]string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerV2}, ", ")

// Ref is a normalised image reference; Reference is a tag or a digest.
type Ref struct{ Host, Name, Reference string }

// ParseRef normalises image the way Docker does (Docker Hub defaults, library/ prefix, latest tag) and validates it
// strictly, so nothing unchecked reaches a URL.
func ParseRef(image string) (Ref, error) {
	if image == "" || len(image) > maxRefLength {
		return Ref{}, errInvalidRef
	}
	rest, digest, hasDigest := strings.Cut(image, "@")
	if hasDigest && !digestPattern.MatchString(digest) {
		return Ref{}, errInvalidRef
	}
	tag := ""
	lastSlash := strings.LastIndex(rest, "/")
	if colon := strings.LastIndex(rest[lastSlash+1:], ":"); colon >= 0 {
		tag = rest[lastSlash+1+colon+1:]
		rest = rest[:lastSlash+1+colon]
		if !refTagPattern.MatchString(tag) {
			return Ref{}, errInvalidRef
		}
	}
	host, name := dockerHubHost, rest
	if first, remainder, found := strings.Cut(rest, "/"); found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if !refHostPattern.MatchString(first) {
			return Ref{}, errInvalidRef
		}
		host, name = strings.ToLower(first), remainder
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = dockerHubHost
	}
	if host == dockerHubHost && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if !refNamePattern.MatchString(name) {
		return Ref{}, errInvalidRef
	}
	reference := tag
	switch {
	case hasDigest:
		reference = digest
	case tag == "":
		reference = "latest"
	}
	return Ref{Host: host, Name: name, Reference: reference}, nil
}

// NewHTTPClient is an HTTP client for registries: https only, bounded redirects, no proxy, no credentials sent to
// another host than the one first asked, and connections only to addresses the policy allows.
func NewHTTPClient(policy safedial.Policy, tlsConfig *tls.Config) *http.Client {
	dialer := &safedial.Dialer{Policy: policy}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSClientConfig:     tlsConfig,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		// The host list stays empty: token realms live on other hosts (auth.docker.io), so the allow-list of registries
		// is the caller's, and the address guard decides what may be connected to.
		CheckRedirect: safedial.Policy{}.CheckRedirect(maxRedirects),
		Timeout:       30 * time.Second,
	}
}

// Client reads manifests over the Registry HTTP API. Every call fetches an anonymous token when asked for one.
type Client struct{ HTTP *http.Client }

// Digest is the digest the registry reports for ref, from a HEAD request. It is "" when the registry sends no
// valid Docker-Content-Digest header.
func (c *Client) Digest(ctx context.Context, ref Ref) (string, error) {
	_, header, err := c.get(ctx, http.MethodHead, ref, 0)
	if err != nil {
		return "", err
	}
	if digest := header.Get("Docker-Content-Digest"); digestPattern.MatchString(digest) {
		return digest, nil
	}
	return "", nil
}

// Manifest fetches the manifest or index of ref and reports its media type, from the body when the Content-Type
// is not one of the four known types.
func (c *Client) Manifest(ctx context.Context, ref Ref) ([]byte, string, error) {
	body, header, err := c.get(ctx, http.MethodGet, ref, MaxDocumentBytes)
	if err != nil {
		return nil, "", err
	}
	mediaType, _, _ := strings.Cut(header.Get("Content-Type"), ";")
	mediaType = strings.TrimSpace(mediaType)
	switch mediaType {
	case mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerV2:
	default:
		var doc struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(body, &doc)
		mediaType = doc.MediaType
	}
	return body, mediaType, nil
}

// get requests the manifest. On a Bearer challenge it fetches an anonymous token once and retries; a second 401
// means access is refused.
func (c *Client) get(ctx context.Context, method string, ref Ref, limit int64) ([]byte, http.Header, error) {
	target := "https://" + ref.Host + "/v2/" + ref.Name + "/manifests/" + ref.Reference
	authorization, tokenFetched := "", false
	for {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("registry request: %w", err)
		}
		req.Header.Set("Accept", manifestAccept)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("registry request: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && authorization == "" && !tokenFetched {
			challenge := resp.Header.Get("WWW-Authenticate")
			_ = resp.Body.Close()
			tokenFetched = true
			if authorization, err = c.token(ctx, challenge, ref); err != nil {
				return nil, nil, err
			}
			continue
		}
		var body []byte
		var readErr error
		if method != http.MethodHead {
			body, readErr = readBounded(resp.Body, limit)
		}
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil, ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, nil, ErrDenied
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, nil, ErrRateLimited
		case resp.StatusCode != http.StatusOK:
			return nil, nil, fmt.Errorf("registry answered HTTP %d", resp.StatusCode)
		case errors.Is(readErr, ErrTooLarge):
			return nil, nil, ErrTooLarge
		case readErr != nil:
			return nil, nil, fmt.Errorf("registry read: %w", readErr)
		}
		return body, resp.Header, nil
	}
}

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// token answers a Bearer challenge with an anonymous pull token and returns the Authorization value.
func (c *Client) token(ctx context.Context, challenge string, ref Ref) (string, error) {
	scheme, params, _ := strings.Cut(strings.TrimSpace(challenge), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", ErrDenied
	}
	values := map[string]string{}
	for _, match := range bearerParamRegexp.FindAllStringSubmatch(params, -1) {
		values[strings.ToLower(match[1])] = match[2] + match[3]
	}
	realm, err := url.Parse(values["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" || realm.User != nil {
		return "", ErrDenied
	}
	scope := values["scope"]
	if scope == "" {
		scope = "repository:" + ref.Name + ":pull"
	}
	query := realm.Query()
	if service := values["service"]; service != "" {
		query.Set("service", service)
	}
	query.Set("scope", scope)
	realm.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", ErrDenied
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token request: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := readBounded(resp.Body, maxTokenBytes)
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK || readErr != nil {
		return "", ErrDenied
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &token) != nil {
		return "", ErrDenied
	}
	value := token.Token
	if value == "" {
		value = token.AccessToken
	}
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", ErrDenied
	}
	return "Bearer " + value, nil
}

// Platforms lists the platforms of an OCI index or Docker manifest list as "linux/arm64" or "linux/arm/v7". A single
// manifest is not an index and has none. Attestation entries (unknown/unknown) and anything that is not
// os/architecture[/variant] are skipped, and at most MaxPlatforms are kept, so nothing a registry sends can reach
// storage or the screen unchecked.
func Platforms(manifest []byte, mediaType string) (platforms []string, isIndex bool, err error) {
	if mediaType != mediaOCIIndex && mediaType != mediaDockerList {
		return nil, false, nil
	}
	if len(manifest) > MaxDocumentBytes {
		return nil, true, ErrTooLarge
	}
	var index struct {
		Manifests []struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(manifest, &index); err != nil {
		return nil, true, errors.New("image index is not valid JSON")
	}
	seen := map[string]bool{}
	for _, entry := range index.Manifests {
		p := entry.Platform
		if p.OS == "unknown" || p.Architecture == "unknown" || p.OS == "" || p.Architecture == "" {
			continue
		}
		platform := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			platform += "/" + p.Variant
		}
		if !platformPattern.MatchString(platform) || seen[platform] || len(platforms) >= MaxPlatforms {
			continue
		}
		seen[platform] = true
		platforms = append(platforms, platform)
	}
	return platforms, true, nil
}
