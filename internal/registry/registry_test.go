package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/safedial"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ociIdx  = "application/vnd.oci.image.index.v1+json"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		in   string
		want Ref
	}{
		{"nginx", Ref{"registry-1.docker.io", "library/nginx", "latest"}},
		{"nginx:1.27", Ref{"registry-1.docker.io", "library/nginx", "1.27"}},
		{"docker.io/grafana/grafana:11.0.0", Ref{"registry-1.docker.io", "grafana/grafana", "11.0.0"}},
		{"index.docker.io/library/redis", Ref{"registry-1.docker.io", "library/redis", "latest"}},
		{"public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0", Ref{"public.ecr.aws", "aws-observability/aws-otel-collector", "v0.40.0"}},
		{"GHCR.io/org/app@" + digestA, Ref{"ghcr.io", "org/app", digestA}},
		{"10.0.0.5:5000/x:1", Ref{"10.0.0.5:5000", "x", "1"}},
	}
	for _, tc := range cases {
		got, err := ParseRef(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "UPPER/case", "a/b:", "a/b@sha256:xyz", "ghcr.io/../x", "a b", strings.Repeat("a", 300)} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted, want an error", bad)
		}
	}
}

// registryServer is a TLS registry that asks for a Bearer token on first contact. Its address is on loopback, so the
// client under test is allowed loopback and trusts the server's certificate.
type registryServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // "METHOD path"
	manifest func(w http.ResponseWriter, r *http.Request)
}

func newRegistryServer(t *testing.T) *registryServer {
	t.Helper()
	rs := &registryServer{}
	rs.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests = append(rs.requests, r.Method+" "+r.URL.Path)
		rs.mu.Unlock()
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"tok"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="registry",scope="repository:library/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rs.manifest(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *registryServer) client() *Client {
	pool := x509.NewCertPool()
	pool.AddCert(rs.Certificate())
	return &Client{HTTP: NewHTTPClient(safedial.Policy{AllowLoopback: true}, &tls.Config{RootCAs: pool})}
}

func (rs *registryServer) ref() Ref {
	return Ref{Host: strings.TrimPrefix(rs.URL, "https://"), Name: "library/app", Reference: "latest"}
}

func TestClient_DigestUsesHeadAndToken(t *testing.T) {
	rs := newRegistryServer(t)
	rs.manifest = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", digestA)
		w.WriteHeader(http.StatusOK)
	}
	got, err := rs.client().Digest(t.Context(), rs.ref())
	if err != nil || got != digestA {
		t.Fatalf("Digest = %q, %v; want %s", got, err, digestA)
	}
	want := []string{"HEAD /v2/library/app/manifests/latest", "GET /token", "HEAD /v2/library/app/manifests/latest"}
	if strings.Join(rs.requests, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v, want %v (a HEAD, one token fetch, a HEAD with the token)", rs.requests, want)
	}
}

func TestClient_DigestEmptyWhenHeaderMissing(t *testing.T) {
	rs := newRegistryServer(t)
	rs.manifest = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	got, err := rs.client().Digest(t.Context(), rs.ref())
	if err != nil || got != "" {
		t.Fatalf("Digest = %q, %v; want empty and no error", got, err)
	}
	rs.manifest = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "not-a-digest")
		w.WriteHeader(http.StatusOK)
	}
	if got, _ := rs.client().Digest(t.Context(), rs.ref()); got != "" {
		t.Fatalf("Digest = %q, want a malformed header ignored", got)
	}
}

func TestClient_ManifestReturnsBodyAndMediaType(t *testing.T) {
	rs := newRegistryServer(t)
	rs.manifest = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), ociIdx) {
			t.Errorf("manifest request = %s accepting %q", r.Method, r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", ociIdx+"; charset=utf-8")
		_, _ = w.Write([]byte(`{"manifests":[]}`))
	}
	body, mediaType, err := rs.client().Manifest(t.Context(), rs.ref())
	if err != nil || string(body) != `{"manifests":[]}` || mediaType != ociIdx {
		t.Fatalf("Manifest = %q, %q, %v", body, mediaType, err)
	}
}

func TestClient_ManifestIsBounded(t *testing.T) {
	rs := newRegistryServer(t)
	rs.manifest = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ociIdx)
		_, _ = w.Write([]byte(strings.Repeat("x", MaxDocumentBytes+1)))
	}
	if _, _, err := rs.client().Manifest(t.Context(), rs.ref()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestClient_StatusesMapToErrors(t *testing.T) {
	rs := newRegistryServer(t)
	for status, want := range map[int]error{http.StatusNotFound: ErrNotFound, http.StatusForbidden: ErrDenied, http.StatusTooManyRequests: ErrRateLimited} {
		rs.manifest = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }
		if _, err := rs.client().Digest(t.Context(), rs.ref()); !errors.Is(err, want) {
			t.Errorf("HTTP %d: err = %v, want %v", status, err, want)
		}
	}
	rs.manifest = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	if _, err := rs.client().Digest(t.Context(), rs.ref()); err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrDenied) {
		t.Errorf("HTTP 500: err = %v, want a plain failure", err)
	}
}

func TestClient_RefusesRedirectToPlainHTTP(t *testing.T) {
	rs := newRegistryServer(t)
	rs.manifest = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
	}
	if _, err := rs.client().Digest(t.Context(), rs.ref()); err == nil {
		t.Fatal("a redirect to http was followed")
	}
}

func TestClient_RefusesTokenRealmWithCredentialsOrPlainHTTP(t *testing.T) {
	for _, realm := range []string{"http://127.0.0.1:1/token", "https://user:pw@127.0.0.1:1/token"} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		client := &Client{HTTP: NewHTTPClient(safedial.Policy{AllowLoopback: true}, &tls.Config{RootCAs: pool})}
		_, err := client.Digest(t.Context(), Ref{Host: strings.TrimPrefix(srv.URL, "https://"), Name: "a/b", Reference: "1"})
		srv.Close()
		if !errors.Is(err, ErrDenied) {
			t.Errorf("realm %q: err = %v, want ErrDenied", realm, err)
		}
	}
}

func TestClient_BlocksInternalAddresses(t *testing.T) {
	client := &Client{HTTP: NewHTTPClient(safedial.Policy{}, nil)}
	for _, host := range []string{"127.0.0.1", "[::1]", "169.254.169.254", "100.64.0.1", "[fd00:ec2::254]", "[64:ff9b::a00:1]", "10.0.0.5", "192.168.1.1", "0.0.0.0"} {
		_, err := client.Digest(context.Background(), Ref{Host: host + ":9", Name: "a/b", Reference: "1"})
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want ErrBlocked before any connection", host, err)
		}
	}
}

func TestNewHTTPClient_NeverUsesAProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	transport := NewHTTPClient(safedial.Policy{}, nil).Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "https://registry-1.docker.io/v2/", nil)
	if transport.Proxy != nil {
		if proxy, _ := transport.Proxy(req); proxy != (*url.URL)(nil) {
			t.Fatalf("proxy = %v, want none", proxy)
		}
	}
}

func TestPlatforms(t *testing.T) {
	index := `{"mediaType":"` + ociIdx + `","manifests":[
	  {"digest":"` + digestA + `","platform":{"os":"linux","architecture":"amd64"}},
	  {"digest":"` + digestA + `","platform":{"os":"linux","architecture":"arm64","variant":"v8"}},
	  {"digest":"` + digestA + `","platform":{"os":"linux","architecture":"arm","variant":"v7"}},
	  {"digest":"` + digestA + `","platform":{"os":"unknown","architecture":"unknown"}}]}`
	got, isIndex, err := Platforms([]byte(index), ociIdx)
	want := []string{"linux/amd64", "linux/arm64/v8", "linux/arm/v7"}
	if err != nil || !isIndex || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Platforms = %v, %v, %v; want %v (attestations skipped)", got, isIndex, err, want)
	}
}

func TestPlatforms_SingleManifestIsNotIndex(t *testing.T) {
	got, isIndex, err := Platforms([]byte(`{"schemaVersion":2,"config":{}}`), "application/vnd.docker.distribution.manifest.v2+json")
	if err != nil || isIndex || len(got) != 0 {
		t.Fatalf("Platforms = %v, %v, %v; want none, not an index", got, isIndex, err)
	}
}

func TestPlatforms_RejectsOversizedOrMalformed(t *testing.T) {
	if _, _, err := Platforms([]byte(`{not json`), ociIdx); err == nil {
		t.Error("malformed index accepted")
	}
	if _, _, err := Platforms([]byte(strings.Repeat(" ", MaxDocumentBytes+1)), ociIdx); err == nil {
		t.Error("oversized index accepted")
	}
	// A value that is not os/architecture is dropped, never stored or shown.
	index := `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"li nux<script>","architecture":"x"}}]}`
	got, _, err := Platforms([]byte(index), ociIdx)
	if err != nil || len(got) != 1 || got[0] != "linux/amd64" {
		t.Fatalf("Platforms = %v, %v; want only the valid entry", got, err)
	}
	var many strings.Builder
	many.WriteString(`{"manifests":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"platform":{"os":"linux","architecture":"a` + strings.Repeat("b", i%5) + `","variant":"v` + string(rune('a'+i%20)) + string(rune('a'+i/20)) + `"}}`)
	}
	many.WriteString(`]}`)
	if got, _, _ := Platforms([]byte(many.String()), ociIdx); len(got) > MaxPlatforms {
		t.Fatalf("%d platforms kept, want at most %d", len(got), MaxPlatforms)
	}
}
