package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

// baseTimeout bounds everything done for one base image; a variable so tests can shorten it.
var baseTimeout = 30 * time.Second

const (
	maxBaseDocBytes = 1 << 20
	maxTokenBytes   = 64 << 10
	maxRedirects    = 5
	dockerHubHost   = "registry-1.docker.io"
	maxRefLength    = 255
)

var (
	ecrHostPattern    = regexp.MustCompile(`^(\d{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$`)
	refHostPattern    = regexp.MustCompile(`^[a-zA-Z0-9]+(?:[.-][a-zA-Z0-9]+)*(?::[0-9]{1,5})?$`)
	refNamePattern    = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	refTagPattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	bearerParamRegexp = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^,\s]+))`)
)

const (
	errInvalidRef = "The base image reference is not valid."
	errNotFound   = "The base image was not found."
	errDenied     = "The registry refused access to the base image."
)

func baseReadFailure(code string) error {
	return errors.New("The base image could not be read: " + code)
}

// baseRef is a normalised base image reference; reference is a tag or a digest.
type baseRef struct{ host, name, reference string }

// parseBaseRef normalises ref the way Docker does (Docker Hub defaults, library/ prefix, latest tag) and
// validates it strictly, so nothing unchecked reaches a URL.
func parseBaseRef(ref string) (baseRef, error) {
	invalid := errors.New(errInvalidRef)
	if ref == "" || len(ref) > maxRefLength {
		return baseRef{}, invalid
	}
	rest, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && !sbomDigestPattern.MatchString(digest) {
		return baseRef{}, invalid
	}
	tag := ""
	lastSlash := strings.LastIndex(rest, "/")
	if colon := strings.LastIndex(rest[lastSlash+1:], ":"); colon >= 0 {
		tag = rest[lastSlash+1+colon+1:]
		rest = rest[:lastSlash+1+colon]
		if !refTagPattern.MatchString(tag) {
			return baseRef{}, invalid
		}
	}
	host, name := dockerHubHost, rest
	if first, remainder, found := strings.Cut(rest, "/"); found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		if !refHostPattern.MatchString(first) {
			return baseRef{}, invalid
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
		return baseRef{}, invalid
	}
	reference := tag
	switch {
	case hasDigest:
		reference = digest
	case tag == "":
		reference = "latest"
	}
	return baseRef{host: host, name: name, reference: reference}, nil
}

// baseLayers resolves the diff IDs of the base image ref for platform over the Registry HTTP API. Every
// (ref, platform) is resolved once per run. A failure is returned in the result, never as an error, and
// ends at baseTimeout at the latest.
func (r *Runner) baseLayers(ctx context.Context, registries map[string]scan.JobRegistry, ref, platform string) scan.BaseLayers {
	if platform == "" {
		platform = defaultPlatform
	}
	key := ref + "|" + platform
	if cached, ok := r.bases[key]; ok {
		return cached
	}
	if r.bases == nil {
		r.bases, r.ecrClients, r.auths = map[string]scan.BaseLayers{}, map[string]ECRClient{}, map[string]registryAuth{}
	}
	ctx, cancel := context.WithTimeout(ctx, baseTimeout)
	defer cancel()
	result := scan.BaseLayers{Ref: scan.SanitizeText(ref), Layers: []string{}}
	digest, layers, err := r.readBase(ctx, registries, ref, platform)
	if err != nil {
		result.Error = scan.SanitizeText(err.Error())
	} else {
		result.Digest, result.Layers = digest, layers
	}
	r.bases[key] = result
	return result
}

func (r *Runner) readBase(ctx context.Context, registries map[string]scan.JobRegistry, ref, platform string) (string, []string, error) {
	parsed, err := parseBaseRef(ref)
	if err != nil {
		return "", nil, err
	}
	session := &baseSession{client: r.baseClient(), baseRef: parsed}
	if match := ecrHostPattern.FindStringSubmatch(parsed.host); match != nil {
		account, region := match[1], match[2]
		reg, ok := registries[account]
		if !ok {
			return "", nil, fmt.Errorf("Connect AWS account %s to read this base image.", account)
		}
		client, err := r.ecrClient(ctx, reg, region)
		if err == nil {
			var auth registryAuth
			if auth, err = r.registryAuth(ctx, parsed.host, client); err == nil {
				session.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(auth.username+":"+auth.password))
			}
		}
		if err != nil {
			if _, denied := awsFailureCode(err); denied {
				return "", nil, errors.New(errDenied)
			}
			return "", nil, baseReadFailure(contextCode(ctx, "credentials"))
		}
	}
	return session.resolve(ctx, platform)
}

// baseClient is the HTTP client for registries: https only, bounded redirects, and no credentials sent
// to a host other than the one first asked.
func (r *Runner) baseClient() *http.Client {
	return &http.Client{
		Transport: r.BaseTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect is not https")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

type baseSession struct {
	client *http.Client
	baseRef
	authorization string // Basic for ECR, Bearer once a token was fetched
	tokenFetched  bool
}

// contextCode is "timeout" when ctx ran out, else code.
func contextCode(ctx context.Context, code string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return code
}

func (s *baseSession) resolve(ctx context.Context, platform string) (string, []string, error) {
	body, mediaType, err := s.getManifest(ctx, s.reference)
	if err != nil {
		return "", nil, err
	}
	digest := sha256Digest(body)
	if sbomDigestPattern.MatchString(s.reference) && digest != s.reference {
		return "", nil, baseReadFailure("digest mismatch")
	}
	if mediaType == mediaOCIIndex || mediaType == mediaDockerList {
		platformDigest, _, err := ResolvePlatformDigest(body, mediaType, platform)
		if err != nil {
			return "", nil, baseReadFailure("no matching platform")
		}
		if body, mediaType, err = s.getManifest(ctx, platformDigest); err != nil {
			return "", nil, err
		}
		if digest = sha256Digest(body); digest != platformDigest {
			return "", nil, baseReadFailure("digest mismatch")
		}
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if mediaType == mediaOCIIndex || mediaType == mediaDockerList || json.Unmarshal(body, &manifest) != nil ||
		!sbomDigestPattern.MatchString(manifest.Config.Digest) {
		return "", nil, baseReadFailure("unsupported manifest")
	}

	config, _, err := s.get(ctx, "/blobs/"+manifest.Config.Digest, "", maxBaseDocBytes)
	if err != nil {
		return "", nil, err
	}
	if sha256Digest(config) != manifest.Config.Digest {
		return "", nil, baseReadFailure("digest mismatch")
	}
	var image struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if json.Unmarshal(config, &image) != nil {
		return "", nil, baseReadFailure("invalid config")
	}
	layers := image.RootFS.DiffIDs
	if len(layers) == 0 {
		return "", nil, baseReadFailure("no layers")
	}
	if len(layers) > scan.MaxLayers {
		layers = layers[:scan.MaxLayers]
	}
	for _, layer := range layers {
		if !sbomDigestPattern.MatchString(layer) {
			return "", nil, baseReadFailure("invalid layer")
		}
	}
	return digest, layers, nil
}

func sha256Digest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

var manifestAccept = strings.Join([]string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerV2}, ", ")

// getManifest fetches a manifest or index and reports its media type, from the body when the
// Content-Type is not one of the four known types.
func (s *baseSession) getManifest(ctx context.Context, reference string) ([]byte, string, error) {
	body, header, err := s.get(ctx, "/manifests/"+reference, manifestAccept, maxBaseDocBytes)
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

// get fetches path under the repository's /v2 prefix. On a Bearer challenge it fetches an anonymous token
// once and retries; a second 401 means access is refused.
func (s *baseSession) get(ctx context.Context, path, accept string, limit int64) ([]byte, http.Header, error) {
	target := "https://" + s.host + "/v2/" + s.name + path
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, nil, baseReadFailure("request")
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if s.authorization != "" {
			req.Header.Set("Authorization", s.authorization)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, nil, baseReadFailure(contextCode(ctx, "network"))
		}
		if resp.StatusCode == http.StatusUnauthorized && s.authorization == "" && !s.tokenFetched {
			challenge := resp.Header.Get("WWW-Authenticate")
			_ = resp.Body.Close()
			if err := s.fetchToken(ctx, challenge); err != nil {
				return nil, nil, err
			}
			continue
		}
		body, err := readBounded(resp.Body, limit)
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil, errors.New(errNotFound)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, nil, errors.New(errDenied)
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, nil, baseReadFailure("rate limited")
		case resp.StatusCode != http.StatusOK:
			return nil, nil, baseReadFailure(fmt.Sprintf("http %d", resp.StatusCode))
		case errors.Is(err, errTooLarge):
			return nil, nil, baseReadFailure("too large")
		case err != nil:
			return nil, nil, baseReadFailure(contextCode(ctx, "network"))
		}
		return body, resp.Header, nil
	}
}

// readBounded reads at most limit bytes and errTooLarge when the body is longer.
func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}

// fetchToken answers a Bearer challenge with an anonymous token for pulling this repository.
func (s *baseSession) fetchToken(ctx context.Context, challenge string) error {
	s.tokenFetched = true
	scheme, params, _ := strings.Cut(strings.TrimSpace(challenge), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return errors.New(errDenied)
	}
	values := map[string]string{}
	for _, match := range bearerParamRegexp.FindAllStringSubmatch(params, -1) {
		values[strings.ToLower(match[1])] = match[2] + match[3]
	}
	realm, err := url.Parse(values["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" || realm.User != nil {
		return errors.New(errDenied)
	}
	scope := values["scope"]
	if scope == "" {
		scope = "repository:" + s.name + ":pull"
	}
	query := realm.Query()
	if service := values["service"]; service != "" {
		query.Set("service", service)
	}
	query.Set("scope", scope)
	realm.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return errors.New(errDenied)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return baseReadFailure(contextCode(ctx, "network"))
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, maxTokenBytes)
	if resp.StatusCode != http.StatusOK || err != nil {
		return errors.New(errDenied)
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &token) != nil {
		return errors.New(errDenied)
	}
	value := token.Token
	if value == "" {
		value = token.AccessToken
	}
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return errors.New(errDenied)
	}
	s.authorization = "Bearer " + value
	return nil
}
