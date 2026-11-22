package oauthsrv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// docServer serves one client metadata document over https and counts how often it was fetched.
type docServer struct {
	*httptest.Server
	hits atomic.Int32
}

// newDocServer starts a TLS server whose /client.json answers handler. The resolver under test allowlists its host
// (127.0.0.1) and trusts its certificate through the injected client.
func newDocServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, clientID string)) *docServer {
	t.Helper()
	d := &docServer{}
	d.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		handler(w, r, "https://"+r.Host+r.URL.Path)
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *docServer) clientID() string { return d.URL + "/client.json" }

func (d *docServer) resolver() *ClientResolver {
	hc := d.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := NewClientResolver([]string{"127.0.0.1"}, []string{"https://claude.ai/api/mcp/auth_callback"})
	r.HTTP = hc
	return r
}

func serveDocument(doc map[string]any) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, clientID string) {
		body := map[string]any{"client_id": clientID}
		for k, v := range doc {
			body[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

func goodDoc() map[string]any {
	return map[string]any{"client_name": "Claude", "redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback"}}
}

func TestClientResolver_AcceptsAValidDocument(t *testing.T) {
	d := newDocServer(t, serveDocument(goodDoc()))
	got, err := d.resolver().Resolve(t.Context(), d.clientID())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != d.clientID() || got.Name != "Claude" || len(got.RedirectURIs) != 1 ||
		got.RedirectURIs[0] != "https://claude.ai/api/mcp/auth_callback" {
		t.Fatalf("client = %+v", got)
	}
}

func TestClientResolver_CleansTheNameAndFallsBackToTheHost(t *testing.T) {
	doc := goodDoc()
	doc["client_name"] = "  Cla\u0000ude‮ " + strings.Repeat("x", 200)
	d := newDocServer(t, serveDocument(doc))
	got, err := d.resolver().Resolve(t.Context(), d.clientID())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got.Name, "\u0000‮") || len([]rune(got.Name)) > 100 || !strings.HasPrefix(got.Name, "Claude") {
		t.Fatalf("name = %q, want printable, at most 100 characters", got.Name)
	}
	doc2 := goodDoc()
	delete(doc2, "client_name")
	d2 := newDocServer(t, serveDocument(doc2))
	got, err = d2.resolver().Resolve(t.Context(), d2.clientID())
	if err != nil || got.Name != "127.0.0.1" {
		t.Fatalf("nameless client = %+v, %v; want the host as its name", got, err)
	}
}

func TestClientResolver_RejectsHostsOffTheList(t *testing.T) {
	d := newDocServer(t, serveDocument(goodDoc()))
	r := d.resolver()
	r.Hosts = []string{"claude.ai"}
	if _, err := r.Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientNotAllowed) {
		t.Fatalf("err = %v, want ErrClientNotAllowed", err)
	}
	if d.hits.Load() != 0 {
		t.Fatal("an off-list host was fetched")
	}
	// Look-alike hosts of an allowlisted one never match.
	r = NewClientResolver([]string{"claude.ai"}, nil)
	for _, id := range []string{
		"https://claude.ai.evil.example/c.json", "https://evilclaude.ai/c.json", "https://sub.claude.ai/c.json",
		"https://claude.ai@evil.example/c.json", "https://evil.example/c.json",
	} {
		if _, err := r.Resolve(t.Context(), id); !errors.Is(err, ErrClientNotAllowed) {
			t.Errorf("%s: err = %v, want ErrClientNotAllowed", id, err)
		}
	}
}

func TestClientResolver_RejectsHTTPAndFragmentsAndCredentials(t *testing.T) {
	r := NewClientResolver([]string{"claude.ai"}, nil)
	for _, id := range []string{
		"http://claude.ai/c.json",
		"https://claude.ai/c.json#frag",
		"https://user:pw@claude.ai/c.json",
		"https://user@claude.ai/c.json",
		"https://claude.ai/c.json?x=1",
		"https://claude.ai/",
		"https://claude.ai",
		"https://claude.ai/a/../c.json",
		"https://claude.ai/./c.json",
		"ftp://claude.ai/c.json",
		"",
		"claude.ai/c.json",
		"https://claude.ai/c json",
		"https://claude.ai\\@evil.example/c.json",
	} {
		if _, err := r.Resolve(t.Context(), id); !errors.Is(err, ErrClientNotAllowed) {
			t.Errorf("%q: err = %v, want ErrClientNotAllowed", id, err)
		}
	}
	// An allowlisted host with different letter case still matches.
	r.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	if _, err := r.Resolve(t.Context(), "https://Claude.AI/c.json"); !errors.Is(err, ErrClientUnavailable) {
		t.Fatalf("case-insensitive host: err = %v, want it allowed (and then unavailable)", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientResolver_RejectsAMismatchedClientID(t *testing.T) {
	doc := goodDoc()
	doc["client_id"] = "https://claude.ai/some-other-client.json"
	d := newDocServer(t, serveDocument(doc))
	if _, err := d.resolver().Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientInvalid) {
		t.Fatalf("err = %v, want ErrClientInvalid", err)
	}
	// No redirect URIs at all is invalid too.
	noURIs := map[string]any{"client_name": "x"}
	d2 := newDocServer(t, serveDocument(noURIs))
	if _, err := d2.resolver().Resolve(t.Context(), d2.clientID()); !errors.Is(err, ErrClientInvalid) {
		t.Fatalf("no redirect_uris: err = %v, want ErrClientInvalid", err)
	}
	empty := map[string]any{"client_name": "x", "redirect_uris": []string{}}
	d3 := newDocServer(t, serveDocument(empty))
	if _, err := d3.resolver().Resolve(t.Context(), d3.clientID()); !errors.Is(err, ErrClientInvalid) {
		t.Fatalf("empty redirect_uris: err = %v, want ErrClientInvalid", err)
	}
}

func TestClientResolver_RejectsOversizedAndNonJSON(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request, string){
		"not json": func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte("<html>hi</html>")) },
		"oversized": func(w http.ResponseWriter, _ *http.Request, id string) {
			pad := strings.Repeat("a", 65<<10)
			_, _ = fmt.Fprintf(w, `{"client_id":%q,"client_name":%q,"redirect_uris":["https://claude.ai/api/mcp/auth_callback"]}`, id, pad)
		},
		"not found": func(w http.ResponseWriter, _ *http.Request, _ string) { http.NotFound(w, nil) },
		"server error": func(w http.ResponseWriter, _ *http.Request, _ string) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDocServer(t, handler)
			if _, err := d.resolver().Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientUnavailable) {
				t.Fatalf("err = %v, want ErrClientUnavailable", err)
			}
		})
	}
}

func TestClientResolver_TimesOutSlowDocuments(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 second fetch timeout")
	}
	release := make(chan struct{})
	d := newDocServer(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	start := time.Now()
	if _, err := d.resolver().Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientUnavailable) {
		t.Fatalf("err = %v, want ErrClientUnavailable", err)
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("took %v, want about 5s", took)
	}
}

// countingListener counts the connections a server accepts, so a test can prove nothing connected at all.
type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

func TestClientResolver_RefusesPrivateAndRedirectedTargets(t *testing.T) {
	t.Run("a loopback target is never connected to", func(t *testing.T) {
		listener := &countingListener{}
		var err error
		if listener.Listener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveDocument(goodDoc())(w, r, "https://"+r.Host+r.URL.Path)
		}))
		srv.Listener = listener
		srv.StartTLS()
		defer srv.Close()
		// The real guarded client, with the real policy: it must refuse before dialling.
		r := NewClientResolver([]string{"127.0.0.1"}, nil)
		if _, err := r.Resolve(t.Context(), srv.URL+"/client.json"); !errors.Is(err, ErrClientUnavailable) {
			t.Fatalf("err = %v, want ErrClientUnavailable", err)
		}
		if n := listener.accepted.Load(); n != 0 {
			t.Fatalf("the guarded client connected %d times to a loopback address", n)
		}
	})

	t.Run("other private and special addresses are refused", func(t *testing.T) {
		for _, host := range []string{"10.0.0.5", "192.168.1.1", "172.16.0.1", "169.254.169.254", "[::1]", "[fd00:ec2::254]", "100.64.0.1"} {
			r := NewClientResolver([]string{strings.Trim(host, "[]")}, nil)
			_, err := r.Resolve(t.Context(), "https://"+host+"/client.json")
			if !errors.Is(err, ErrClientUnavailable) {
				t.Errorf("%s: err = %v, want ErrClientUnavailable", host, err)
			}
		}
	})

	t.Run("a redirect is not followed", func(t *testing.T) {
		var target atomic.Int32
		targetSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target.Add(1)
			serveDocument(goodDoc())(w, r, "https://"+r.Host+r.URL.Path)
		}))
		defer targetSrv.Close()
		d := newDocServer(t, func(w http.ResponseWriter, r *http.Request, _ string) {
			http.Redirect(w, r, targetSrv.URL+"/client.json", http.StatusFound)
		})
		// Keep NewClientResolver's own client (its redirect policy), but let it reach the test server.
		r := NewClientResolver([]string{"127.0.0.1"}, nil)
		transport := r.HTTP.Transport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{}).DialContext
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server certificate
		r.HTTP.Transport = transport
		if _, err := r.Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientUnavailable) {
			t.Fatalf("err = %v, want ErrClientUnavailable", err)
		}
		if target.Load() != 0 {
			t.Fatal("the redirect was followed")
		}
	})
}

func TestClientResolver_CachesSuccessForAnHour(t *testing.T) {
	d := newDocServer(t, serveDocument(goodDoc()))
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	r := d.resolver()
	r.Now = func() time.Time { return now }
	for range 2 {
		if _, err := r.Resolve(t.Context(), d.clientID()); err != nil {
			t.Fatal(err)
		}
	}
	if n := d.hits.Load(); n != 1 {
		t.Fatalf("%d upstream hits for two resolves, want 1", n)
	}
	now = now.Add(59 * time.Minute)
	if _, err := r.Resolve(t.Context(), d.clientID()); err != nil || d.hits.Load() != 1 {
		t.Fatalf("at 59 minutes: err %v, hits %d, want still cached", err, d.hits.Load())
	}
	now = now.Add(2 * time.Minute) // 61 minutes after the first fetch
	if _, err := r.Resolve(t.Context(), d.clientID()); err != nil {
		t.Fatal(err)
	}
	if n := d.hits.Load(); n != 2 {
		t.Fatalf("%d upstream hits after 61 minutes, want 2", n)
	}
}

func TestClientResolver_CachesFailureForAMinute(t *testing.T) {
	d := newDocServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) { http.Error(w, "no", 500) })
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	r := d.resolver()
	r.Now = func() time.Time { return now }
	for range 3 {
		if _, err := r.Resolve(t.Context(), d.clientID()); !errors.Is(err, ErrClientUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if d.hits.Load() != 1 {
		t.Fatalf("%d hits, want the failure cached", d.hits.Load())
	}
	now = now.Add(61 * time.Second)
	_, _ = r.Resolve(t.Context(), d.clientID())
	if d.hits.Load() != 2 {
		t.Fatalf("%d hits after a minute, want a retry", d.hits.Load())
	}
}

func TestClientResolver_CacheIsBounded(t *testing.T) {
	d := newDocServer(t, serveDocument(goodDoc()))
	r := d.resolver()
	for i := range 130 {
		// A query is not allowed in a client_id, so vary the path.
		id := fmt.Sprintf("%s/client-%d.json", d.URL, i)
		if _, err := r.Resolve(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	size := len(r.cache)
	r.mu.Unlock()
	if size > 100 {
		t.Fatalf("cache holds %d entries, want at most 100", size)
	}
}

func TestClientResolver_ACancelledCallerIsNotRememberedAsTheClientsFailure(t *testing.T) {
	d := newDocServer(t, serveDocument(goodDoc()))
	r := d.resolver()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Resolve(ctx, d.clientID()); !errors.Is(err, ErrClientUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Resolve(t.Context(), d.clientID()); err != nil {
		t.Fatalf("a later resolve failed: %v", err)
	}
}

func TestRedirectAllowed_Table(t *testing.T) {
	r := NewClientResolver(nil, []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost/callback",
		"http://127.0.0.1/callback",
	})
	listedAll := Client{RedirectURIs: []string{
		"https://claude.ai/api/mcp/auth_callback", "http://localhost/callback", "http://127.0.0.1/callback",
		"https://listed-only.example/cb",
	}}
	cases := []struct {
		name   string
		client Client
		uri    string
		want   bool
	}{
		{"listed and patterned", listedAll, "https://claude.ai/api/mcp/auth_callback", true},
		{"listed but not patterned", listedAll, "https://listed-only.example/cb", false},
		{"patterned but not listed", Client{RedirectURIs: []string{"https://other.example/cb"}}, "https://claude.ai/api/mcp/auth_callback", false},
		{"loopback with another port", Client{RedirectURIs: []string{"http://localhost/callback"}}, "http://localhost:54321/callback", true},
		{"127.0.0.1 with another port", Client{RedirectURIs: []string{"http://127.0.0.1:1234/callback"}}, "http://127.0.0.1:9999/callback", true},
		{"loopback with another path", listedAll, "http://localhost:54321/evil", false},
		{"loopback with a nested path", listedAll, "http://localhost:54321/callback/x", false},
		{"loopback with a query", listedAll, "http://localhost:54321/callback?x=1", false},
		{"loopback over https", listedAll, "https://localhost:54321/callback", false},
		{"other loopback host", listedAll, "http://[::1]:54321/callback", false},
		{"look-alike host", listedAll, "https://claude.ai.evil.example/api/mcp/auth_callback", false},
		{"host with the real one as userinfo", listedAll, "https://claude.ai@evil.example/api/mcp/auth_callback", false},
		{"credentials", listedAll, "https://user:pw@claude.ai/api/mcp/auth_callback", false},
		{"trailing slash", listedAll, "https://claude.ai/api/mcp/auth_callback/", false},
		{"added query", listedAll, "https://claude.ai/api/mcp/auth_callback?x=1", false},
		{"fragment", listedAll, "https://claude.ai/api/mcp/auth_callback#x", false},
		{"http for an https entry", listedAll, "http://claude.ai/api/mcp/auth_callback", false},
		{"upper-case path", listedAll, "https://claude.ai/API/mcp/auth_callback", false},
		{"empty", listedAll, "", false},
		{"not a url", listedAll, "::::", false},
		{"javascript scheme", listedAll, "javascript:alert(1)", false},
		{"whitespace", listedAll, "https://claude.ai/api/mcp/auth_callback ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.RedirectAllowed(tc.client, tc.uri); got != tc.want {
				t.Fatalf("RedirectAllowed(%q) = %v, want %v", tc.uri, got, tc.want)
			}
		})
	}
	// Sanity: the cases above are real URLs where they should parse.
	if _, err := url.Parse("https://claude.ai/api/mcp/auth_callback"); err != nil {
		t.Fatal(err)
	}
}
