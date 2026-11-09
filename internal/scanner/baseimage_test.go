package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

func diffID(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func sha(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// fakeRegistry serves one repository: a manifest list at its tag, platform manifests by digest and a config blob.
type fakeRegistry struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []string          // "METHOD /path"
	authSeen map[string]string // path -> Authorization header

	repo         string
	tag          string
	needToken    bool   // anonymous clients must fetch a token first
	denyAlways   bool   // 401 even with a token
	wantBasic    string // when set, every /v2 request needs this Authorization header
	tokenQuery   url.Values
	manifestCode int    // when set, the status of every manifest GET
	configBody   []byte // overrides the config blob
	blobRedirect string // when set, blob GETs redirect here
	accept       string

	amd64Layers, arm64Layers []string
	list, amd64, arm64       []byte
	amd64Digest, arm64Digest string
	config                   map[string][]byte // by platform manifest digest
}

func configJSON(layers []string) []byte {
	data, _ := json.Marshal(map[string]any{"rootfs": map[string]any{"type": "layers", "diff_ids": layers}})
	return data
}

func newFakeRegistry(t *testing.T, repo, tag string) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{t: t, repo: repo, tag: tag, authSeen: map[string]string{}, config: map[string][]byte{},
		amd64Layers: []string{diffID("1"), diffID("2")}, arm64Layers: []string{diffID("1"), diffID("3"), diffID("4")}}
	f.rebuild()
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// rebuild derives manifests and digests from the layer lists.
func (f *fakeRegistry) rebuild() {
	build := func(layers []string) (manifest []byte, digest string) {
		cfg := configJSON(layers)
		if f.configBody != nil {
			cfg = f.configBody
		}
		manifest, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaOCIManifest,
			"config": map[string]any{"digest": sha(cfg), "size": len(cfg)}, "layers": []any{}})
		digest = sha(manifest)
		f.config[sha(cfg)] = cfg
		return manifest, digest
	}
	f.amd64, f.amd64Digest = build(f.amd64Layers)
	f.arm64, f.arm64Digest = build(f.arm64Layers)
	f.list, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaOCIIndex, "manifests": []any{
		map[string]any{"digest": f.amd64Digest, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
		map[string]any{"digest": f.arm64Digest, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
	}})
}

func (f *fakeRegistry) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.authSeen[r.URL.Path] = r.Header.Get("Authorization")
	f.mu.Unlock()

	if r.URL.Path == "/token" {
		f.tokenQuery = r.URL.Query()
		if r.Header.Get("Authorization") != "" {
			f.t.Errorf("the token request carried Authorization")
		}
		_, _ = io.WriteString(w, `{"token":"tok-1"}`)
		return
	}
	if f.wantBasic != "" && r.Header.Get("Authorization") != f.wantBasic {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.needToken && r.Header.Get("Authorization") != "Bearer tok-1" || f.denyAlways {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="https://%s/token",service="registry.test",scope="repository:%s:pull"`, r.Host, f.repo))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	prefix := "/v2/" + f.repo + "/"
	switch {
	case strings.HasPrefix(r.URL.Path, prefix+"manifests/"):
		f.accept = r.Header.Get("Accept")
		if f.manifestCode != 0 {
			w.WriteHeader(f.manifestCode)
			_, _ = io.WriteString(w, "secret internal detail")
			return
		}
		switch strings.TrimPrefix(r.URL.Path, prefix+"manifests/") {
		case f.tag:
			w.Header().Set("Content-Type", mediaOCIIndex)
			_, _ = w.Write(f.list)
		case f.amd64Digest:
			w.Header().Set("Content-Type", mediaOCIManifest)
			w.Header().Set("Docker-Content-Digest", f.amd64Digest)
			_, _ = w.Write(f.amd64)
		case f.arm64Digest:
			w.Header().Set("Content-Type", mediaOCIManifest)
			w.Header().Set("Docker-Content-Digest", f.arm64Digest)
			_, _ = w.Write(f.arm64)
		default:
			http.NotFound(w, r)
		}
	case strings.HasPrefix(r.URL.Path, prefix+"blobs/"):
		if f.blobRedirect != "" {
			http.Redirect(w, r, f.blobRedirect, http.StatusTemporaryRedirect)
			return
		}
		digest := strings.TrimPrefix(r.URL.Path, prefix+"blobs/")
		body, ok := f.config[digest]
		if f.configBody != nil {
			body, ok = f.configBody, true
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRegistry) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// address is host:port of the test server, usable as a registry host in a reference.
func (f *fakeRegistry) address() string { return f.srv.Listener.Addr().String() }

// transportFor trusts the test server's certificate and sends the named hosts to it.
func transportFor(srv *httptest.Server, hosts ...string) http.RoundTripper {
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com" // the test certificate's name
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(address); err == nil && slices.Contains(hosts, host) {
			address = srv.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return transport
}

func newBaseRunner(transport http.RoundTripper) *Runner {
	return &Runner{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseTransport: transport,
		ecrClients: map[string]ECRClient{}, auths: map[string]registryAuth{}}
}

func TestBaseLayers_AnonymousBearerFlowOnDockerHub(t *testing.T) {
	reg := newFakeRegistry(t, "library/node", "20-bookworm-slim")
	reg.needToken = true
	r := newBaseRunner(transportFor(reg.srv, "registry-1.docker.io"))

	got := r.baseLayers(context.Background(), nil, "node:20-bookworm-slim", "linux/arm64")

	if got.Error != "" {
		t.Fatalf("error = %q", got.Error)
	}
	if got.Ref != "node:20-bookworm-slim" || got.Digest != reg.arm64Digest || !slices.Equal(got.Layers, reg.arm64Layers) {
		t.Errorf("base = %+v, want the arm64 manifest %s with layers %v", got, reg.arm64Digest, reg.arm64Layers)
	}
	if scope := reg.tokenQuery.Get("scope"); scope != "repository:library/node:pull" || reg.tokenQuery.Get("service") != "registry.test" {
		t.Errorf("token query = %v", reg.tokenQuery)
	}
	for _, media := range []string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerV2} {
		if !strings.Contains(reg.accept, media) {
			t.Errorf("Accept %q lacks %s", reg.accept, media)
		}
	}
}

func TestBaseLayers_DefaultsToTheAMD64PlatformAndCachesPerRun(t *testing.T) {
	reg := newFakeRegistry(t, "team/base", "1")
	r := newBaseRunner(reg.srv.Client().Transport)
	ref := reg.address() + "/team/base:1"

	first := r.baseLayers(context.Background(), nil, ref, "")
	requests := reg.requestCount()
	second := r.baseLayers(context.Background(), nil, ref, "linux/amd64")

	if first.Error != "" || !slices.Equal(first.Layers, reg.amd64Layers) || first.Digest != reg.amd64Digest {
		t.Errorf("first = %+v", first)
	}
	if reg.requestCount() != requests || second.Digest != first.Digest {
		t.Errorf("the same (ref, platform) was resolved twice: %d requests after the first, %d in total", requests, reg.requestCount())
	}
	r.baseLayers(context.Background(), nil, ref, "linux/arm64")
	if reg.requestCount() == requests {
		t.Error("another platform must be resolved separately")
	}
}

func TestBaseLayers_DigestReference(t *testing.T) {
	reg := newFakeRegistry(t, "team/base", "1")
	r := newBaseRunner(reg.srv.Client().Transport)

	got := r.baseLayers(context.Background(), nil, reg.address()+"/team/base@"+reg.amd64Digest, "linux/amd64")

	if got.Error != "" || got.Digest != reg.amd64Digest || !slices.Equal(got.Layers, reg.amd64Layers) {
		t.Errorf("base = %+v", got)
	}
}

func TestBaseLayers_FailuresUseFixedPhrasesAndNeverEchoBodies(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeRegistry)
		want  string
	}{
		{"not found", func(f *fakeRegistry) { f.manifestCode = http.StatusNotFound }, "The base image was not found."},
		{"denied after the token", func(f *fakeRegistry) { f.denyAlways = true }, "The registry refused access to the base image."},
		{"forbidden", func(f *fakeRegistry) { f.manifestCode = http.StatusForbidden }, "The registry refused access to the base image."},
		{"server error", func(f *fakeRegistry) { f.manifestCode = http.StatusInternalServerError }, "The base image could not be read: http 500"},
		{"oversized config", func(f *fakeRegistry) { f.configBody = []byte(strings.Repeat("x", 2<<20)) }, "The base image could not be read: too large"},
		{"malformed diff id", func(f *fakeRegistry) {
			f.configBody = configJSON([]string{diffID("1"), "sha256:nothex"})
		}, "The base image could not be read: invalid layer"},
		{"no layers", func(f *fakeRegistry) { f.configBody = configJSON(nil) }, "The base image could not be read: no layers"},
		{"config is not json", func(f *fakeRegistry) { f.configBody = []byte("<html>") }, "The base image could not be read: invalid config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := newFakeRegistry(t, "team/base", "1")
			tc.setup(reg)
			reg.rebuild() // the manifests name the overridden config
			r := newBaseRunner(reg.srv.Client().Transport)

			got := r.baseLayers(context.Background(), nil, reg.address()+"/team/base:1", "linux/amd64")

			if got.Error != tc.want || len(got.Layers) != 0 || got.Ref == "" {
				t.Errorf("base = %+v, want error %q and no layers", got, tc.want)
			}
			if strings.Contains(got.Error, "secret") {
				t.Errorf("error echoes a response body: %q", got.Error)
			}
		})
	}
}

func TestBaseLayers_ConfigThatDoesNotMatchItsDigestIsRejected(t *testing.T) {
	reg := newFakeRegistry(t, "team/base", "1")
	reg.configBody = configJSON([]string{diffID("9")}) // valid, but not the bytes the manifests name
	r := newBaseRunner(reg.srv.Client().Transport)

	got := r.baseLayers(context.Background(), nil, reg.address()+"/team/base:1", "linux/amd64")

	if got.Error != "The base image could not be read: digest mismatch" {
		t.Errorf("base = %+v", got)
	}
}

func TestBaseLayers_InvalidReferencesMakeNoNetworkCall(t *testing.T) {
	reg := newFakeRegistry(t, "team/base", "1")
	r := newBaseRunner(reg.srv.Client().Transport)
	for _, ref := range []string{
		"", "UPPER/case:1", "node:", "node:bad tag", "a//b", "node@sha256:short", "-bad/name:1",
		reg.address() + "/team/../base:1", "http://" + reg.address() + "/team/base:1",
		reg.address() + "/" + strings.Repeat("a", 256),
	} {
		got := r.baseLayers(context.Background(), nil, ref, "linux/amd64")
		if got.Error != "The base image reference is not valid." {
			t.Errorf("ref %q: base = %+v", ref, got)
		}
	}
	if reg.requestCount() != 0 {
		t.Errorf("invalid references reached the network: %v", reg.requests)
	}
}

func TestParseBaseRef_Normalisation(t *testing.T) {
	cases := []struct{ ref, host, name, reference string }{
		{"node", "registry-1.docker.io", "library/node", "latest"},
		{"node:20-bookworm-slim", "registry-1.docker.io", "library/node", "20-bookworm-slim"},
		{"grafana/grafana:11", "registry-1.docker.io", "grafana/grafana", "11"},
		{"docker.io/library/python:3.12", "registry-1.docker.io", "library/python", "3.12"},
		{"docker.io/nginx", "registry-1.docker.io", "library/nginx", "latest"},
		{"public.ecr.aws/docker/library/python:3.12", "public.ecr.aws", "docker/library/python", "3.12"},
		{"localhost/app", "localhost", "app", "latest"},
		{"localhost:5000/app:2", "localhost:5000", "app", "2"},
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com/platform-base:3.2", "123456789012.dkr.ecr.eu-west-1.amazonaws.com", "platform-base", "3.2"},
		{"node@" + diffID("a"), "registry-1.docker.io", "library/node", diffID("a")},
		{"node:20@" + diffID("a"), "registry-1.docker.io", "library/node", diffID("a")},
	}
	for _, tc := range cases {
		got, err := parseBaseRef(tc.ref)
		if err != nil || got.host != tc.host || got.name != tc.name || got.reference != tc.reference {
			t.Errorf("parseBaseRef(%q) = %+v, %v; want %s %s %s", tc.ref, got, err, tc.host, tc.name, tc.reference)
		}
	}
}

func TestBaseLayers_BearerRealmMustBeHTTPS(t *testing.T) {
	var tokenHits int
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		_, _ = io.WriteString(w, `{"token":"x"}`)
	}))
	defer insecure.Close()
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+insecure.URL+`/token",service="s"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registry.Close()
	r := newBaseRunner(registry.Client().Transport)

	got := r.baseLayers(context.Background(), nil, registry.Listener.Addr().String()+"/team/base:1", "linux/amd64")

	if got.Error != "The registry refused access to the base image." || tokenHits != 0 {
		t.Errorf("base = %+v, token hits = %d", got, tokenHits)
	}
}

func TestBaseLayers_CrossHostRedirectDoesNotCarryAuthorization(t *testing.T) {
	host := testAccount + ".dkr.ecr.us-east-1.amazonaws.com"
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:"+testPassword))
	reg := newFakeRegistry(t, "platform-base", "3.2")
	reg.wantBasic = basic
	config := configJSON(reg.amd64Layers)

	var mu sync.Mutex
	var cdnAuth []string
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cdnAuth = append(cdnAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write(config)
	}))
	defer cdn.Close()
	reg.blobRedirect = cdn.URL + "/blob"

	h := newHarness(t)
	r := h.runner
	r.BaseTransport = transportFor(reg.srv, host)

	got := r.baseLayers(context.Background(), h.job.Registries, host+"/platform-base:3.2", "linux/amd64")

	if got.Error != "" || !slices.Equal(got.Layers, reg.amd64Layers) {
		t.Fatalf("base = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cdnAuth) != 1 || cdnAuth[0] != "" {
		t.Errorf("Authorization values seen by the other host = %q, want one empty", cdnAuth)
	}
	if seen := reg.authSeen["/v2/platform-base/blobs/"+sha(config)]; seen != basic {
		t.Errorf("the registry's own blob request carried %q, want the Basic credentials", seen)
	}
}

func TestBaseLayers_ECRHostUsesBasicAuthFromTheAccountsRole(t *testing.T) {
	host := testAccount + ".dkr.ecr.us-east-1.amazonaws.com"
	reg := newFakeRegistry(t, "platform-base", "3.2")
	reg.wantBasic = "Basic " + base64.StdEncoding.EncodeToString([]byte("AWS:"+testPassword))
	h := newHarness(t)
	r := h.runner
	r.BaseTransport = transportFor(reg.srv, host)

	got := r.baseLayers(context.Background(), h.job.Registries, host+"/platform-base:3.2", "linux/amd64")

	if got.Error != "" || got.Digest != reg.amd64Digest || !slices.Equal(got.Layers, reg.amd64Layers) {
		t.Errorf("base = %+v", got)
	}
	if want := []string{"arn:aws:iam::" + testAccount + ":role/harbormaster-scan|ext-1|us-east-1"}; !slices.Equal(h.assumed, want) {
		t.Errorf("assumed roles = %v, want %v", h.assumed, want)
	}
	if h.ecr.tokenCalls != 1 {
		t.Errorf("GetAuthorizationToken calls = %d, want 1", h.ecr.tokenCalls)
	}
}

func TestBaseLayers_ECRAccountNotConnectedMakesNoNetworkCall(t *testing.T) {
	reg := newFakeRegistry(t, "platform-base", "3.2")
	h := newHarness(t)
	r := h.runner
	r.BaseTransport = transportFor(reg.srv, "999999999999.dkr.ecr.eu-west-1.amazonaws.com")

	got := r.baseLayers(context.Background(), h.job.Registries, "999999999999.dkr.ecr.eu-west-1.amazonaws.com/platform-base:3.2", "linux/amd64")

	if got.Error != "Connect AWS account 999999999999 to read this base image." {
		t.Errorf("error = %q", got.Error)
	}
	if reg.requestCount() != 0 || h.ecr.tokenCalls != 0 {
		t.Errorf("network was used: %d registry requests, %d token calls", reg.requestCount(), h.ecr.tokenCalls)
	}
}

func TestBaseLayers_SlowRegistryEndsAtTheTimeout(t *testing.T) {
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	old := baseTimeout
	baseTimeout = 100 * time.Millisecond
	defer func() { baseTimeout = old }()
	r := newBaseRunner(slow.Client().Transport)

	started := time.Now()
	got := r.baseLayers(context.Background(), nil, slow.Listener.Addr().String()+"/team/base:1", "linux/amd64")

	if got.Error != "The base image could not be read: timeout" || time.Since(started) > 2*time.Second {
		t.Errorf("base = %+v after %v", got, time.Since(started))
	}
}

func TestRun_ImageWithBaseRefGetsBaseLayersAndLayerIDs(t *testing.T) {
	host := testAccount + ".dkr.ecr.us-east-1.amazonaws.com"
	reg := newFakeRegistry(t, "platform-base", "3.2")
	img := image(digestSingle, "stable")
	img.BaseRef = host + "/platform-base:3.2"
	h := newHarness(t, img)
	h.runner.BaseTransport = transportFor(reg.srv, host)

	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	res := storedResult(t, h, digestSingle)
	if res.Base == nil || res.Base.Error != "" || res.Base.Ref != img.BaseRef || !slices.Equal(res.Base.Layers, reg.amd64Layers) || res.Base.Digest != reg.amd64Digest {
		t.Errorf("base = %+v", res.Base)
	}
	if want := []string{diffID("1"), diffID("2"), diffID("3"), diffID("4")}; !slices.Equal(res.Layers, want) {
		t.Errorf("layers = %v, want %v", res.Layers, want)
	}
	if res.Findings[0].LayerID != diffID("1") {
		t.Errorf("first finding layer id = %q", res.Findings[0].LayerID)
	}
}

func TestRun_BaseFailureStillProducesAScannedResult(t *testing.T) {
	missing := image(digestIndex, "edge")
	missing.BaseRef = "999999999999.dkr.ecr.eu-west-1.amazonaws.com/platform-base:3.2"
	malformed := image(digestSingle, "stable")
	malformed.BaseRef = "Not A Ref"
	plain := image(digestAMD64, "plain")
	h := newHarness(t, missing, malformed, plain)
	h.ecr.manifests[digestAMD64] = h.ecr.manifests[digestSingle]
	h.cmd.grypeFiles = append(h.cmd.grypeFiles, "grype_empty.json")

	if code := h.run(); code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	for _, d := range []string{digestIndex, digestSingle, digestAMD64} {
		if outcome := h.outcome(d); outcome.Status != scan.OutcomeScanned {
			t.Errorf("%s outcome = %+v, want scanned", d, outcome)
		}
	}
	if base := storedResult(t, h, digestIndex).Base; base == nil || base.Error != "Connect AWS account 999999999999 to read this base image." || len(base.Layers) != 0 {
		t.Errorf("missing-account base = %+v", base)
	}
	if base := storedResult(t, h, digestSingle).Base; base == nil || base.Error != "The base image reference is not valid." {
		t.Errorf("malformed base = %+v", base)
	}
	if base := storedResult(t, h, digestAMD64).Base; base != nil {
		t.Errorf("an image without a base ref got base %+v", base)
	}
}

// storedResult decodes the result object as written, since scan.ParseResult does not yet keep layer fields.
func storedResult(t *testing.T, h *harness, digest string) scan.Result {
	t.Helper()
	var res scan.Result
	if err := json.Unmarshal(h.s3.objects[scan.ResultKey(testRunID, digest)], &res); err != nil {
		t.Fatal(err)
	}
	return res
}
