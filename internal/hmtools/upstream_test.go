package hmtools

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/hmtools/hmtoolstest"
)

func newTestUpstream(t *testing.T, base string) *Upstream {
	t.Helper()
	up, err := NewUpstream(base, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return up
}

func TestUpstream_BuildsThePathAndQuery(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	resp, err := up.Do(context.Background(), "tok", "1.2.3.4", Call{
		Route: Route{Method: "GET", Pattern: "/api/services/{serviceId}/revisions/{revision}"},
		Path:  map[string]string{"serviceId": "svc-1", "revision": "12"},
		Query: url.Values{"tag": {"1", "2"}, "limit": {"50"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	got := rs.Seen()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	if got[0].Method != "GET" || got[0].EscapedPath != "/api/services/svc-1/revisions/12" {
		t.Errorf("request = %s %s", got[0].Method, got[0].EscapedPath)
	}
	query, _ := url.ParseQuery(got[0].Query)
	if strings.Join(query["tag"], ",") != "1,2" || query.Get("limit") != "50" {
		t.Errorf("query = %q", got[0].Query)
	}
}

func TestUpstream_PostSendsTheJSONBody(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	_, err := up.Do(context.Background(), "tok", "", Call{
		Route: Route{Method: "POST", Pattern: "/api/log-search"}, Body: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rs.Seen()[0]
	if got.Method != "POST" || got.Body != `{"query":"x"}` || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("post = %s %q content-type %q", got.Method, got.Body, got.Header.Get("Content-Type"))
	}
}

func TestUpstream_MissingPathValueIsAnErrorBeforeAnyRequest(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	for _, values := range []map[string]string{nil, {"serviceId": ""}, {"other": "x"}, {"serviceId": ".."}, {"serviceId": "."}} {
		_, err := up.Do(context.Background(), "tok", "", Call{
			Route: Route{Method: "GET", Pattern: "/api/services/{serviceId}"}, Path: values,
		})
		if err == nil {
			t.Errorf("values %v: want an error", values)
		}
	}
	if n := len(rs.Seen()); n != 0 {
		t.Errorf("%d requests were sent, want 0", n)
	}
}

func TestUpstream_PathArgumentsCannotChangeTheRoute(t *testing.T) {
	// The stub's mux has the declared route and a decoy for what an escape would reach.
	var mu sync.Mutex
	hits := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/services/{serviceId}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["declared"]++
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["other:"+r.URL.Path]++
		mu.Unlock()
		http.NotFound(w, r)
	})
	var seenMu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		seenMu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	up := newTestUpstream(t, server.URL)

	for _, value := range []string{"../users", "a/b", "x?y=1", "%2e%2e", "..%2fusers", "a#b", `a\b`} {
		seenMu.Lock()
		paths = nil
		seenMu.Unlock()
		resp, err := up.Do(context.Background(), "tok", "", Call{
			Route: Route{Method: "GET", Pattern: "/api/services/{serviceId}"}, Path: map[string]string{"serviceId": value},
		})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if resp.Status != 200 {
			t.Errorf("%q: status %d, want the declared route to answer", value, resp.Status)
		}
		seenMu.Lock()
		got := append([]string(nil), paths...)
		seenMu.Unlock()
		if len(got) != 1 {
			t.Fatalf("%q: %d requests", value, len(got))
		}
		segments := strings.Split(strings.TrimPrefix(got[0], "/"), "/")
		if len(segments) != 3 || segments[0] != "api" || segments[1] != "services" {
			t.Errorf("%q: path %q is not /api/services/<one segment>", value, got[0])
		}
		if strings.Contains(segments[len(segments)-1], "/") || strings.Contains(got[0], "?") {
			t.Errorf("%q: path %q leaked a separator", value, got[0])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for name, n := range hits {
		if name != "declared" {
			t.Errorf("an unexpected route %s was hit %d times", name, n)
		}
	}
}

func TestUpstream_SendsOnlyTheExpectedHeaders(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	if _, err := up.Do(context.Background(), "secret-token", "9.9.9.9", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}}); err != nil {
		t.Fatal(err)
	}
	header := rs.Seen()[0].Header
	want := map[string]string{
		"Authorization":   "Bearer secret-token",
		"Accept":          "application/json",
		"X-Forwarded-For": "9.9.9.9",
		"User-Agent":      "harbormaster-mcp/" + Version,
	}
	for name, value := range want {
		if got := header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	allowedExtra := map[string]bool{"Accept-Encoding": true, "Content-Length": true}
	for name := range header {
		if _, ok := want[name]; !ok && !allowedExtra[name] {
			t.Errorf("unexpected header %s", name)
		}
	}
	if header.Get("Cookie") != "" || header.Get("Content-Type") != "" {
		t.Errorf("cookie or content type sent on a body-less GET: %v", header)
	}
}

func TestUpstream_NeverSendsACookie(t *testing.T) {
	// The server sets a cookie on the first answer; the second request must not carry it back.
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "hm_session", Value: "abc"})
		_, _ = io.WriteString(w, `{}`)
	})
	up := newTestUpstream(t, rs.URL)
	for range 2 {
		if _, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, req := range rs.Seen() {
		if req.Header.Get("Cookie") != "" {
			t.Errorf("a cookie was sent: %q", req.Header.Get("Cookie"))
		}
	}
}

func TestUpstream_ParsesTheErrorEnvelope(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"No access to that tag."}}`)
	})
	up := newTestUpstream(t, rs.URL)
	resp, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 403 || resp.ErrorCode != "forbidden" || resp.ErrorMessage != "No access to that tag." || resp.RetryAfter != "17" {
		t.Errorf("response = %+v", resp)
	}
}

func TestUpstream_NonJSONErrorBodyStillAnswers(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<html>bad gateway</html>`)
	})
	up := newTestUpstream(t, rs.URL)
	resp, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}})
	if err != nil || resp.Status != 502 || resp.ErrorCode != "" {
		t.Errorf("resp = %+v err = %v", resp, err)
	}
}

func TestUpstream_OnlyEverCallsTheConfiguredHost(t *testing.T) {
	configured := hmtoolstest.NewRecordingServer(t, nil)
	other := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, configured.URL)

	hostile := []string{
		other.URL, other.URL + "/x", "//" + strings.TrimPrefix(other.URL, "http://") + "/x",
		"//other.example/x", "@" + strings.TrimPrefix(other.URL, "http://"), "http://other.example/../..", "\\\\other.example\\x",
	}
	for _, value := range hostile {
		_, _ = up.Do(context.Background(), "tok", "", Call{
			Route: Route{Method: "GET", Pattern: "/api/services/{serviceId}"},
			Path:  map[string]string{"serviceId": value},
			Query: url.Values{"account": {value}, "region": {"//other.example/x"}},
		})
		_, _ = up.Do(context.Background(), "tok", "", Call{
			Route: Route{Method: "POST", Pattern: "/api/log-search"},
			Body:  map[string]any{"query": value, "service_ids": []string{value}},
		})
	}
	if n := len(other.Seen()); n != 0 {
		t.Errorf("the other server received %d requests, want 0", n)
	}
	if n := len(configured.Seen()); n != len(hostile)*2 {
		t.Errorf("the configured server received %d requests, want %d", n, len(hostile)*2)
	}
}

func TestUpstream_RefusesAPatternOutsideAPI(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	for _, pattern := range []string{"/healthz", "//other.example/api/x", "api/x", "http://other.example/api/x"} {
		if _, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: pattern}}); err == nil {
			t.Errorf("pattern %q: want an error", pattern)
		}
	}
	if n := len(rs.Seen()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestUpstream_TimeoutIsAnError(t *testing.T) {
	release := make(chan struct{})
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	up := newTestUpstream(t, rs.URL)
	started := time.Now()
	_, err := up.Do(context.Background(), "tok", "", Call{
		Route: Route{Method: "GET", Pattern: "/api/overview"}, Timeout: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %s, the call's own timeout was not applied", elapsed)
	}
}

func TestUpstream_DoesNotFollowRedirects(t *testing.T) {
	target := hmtoolstest.NewRecordingServer(t, nil)
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/overview", http.StatusFound)
	})
	up := newTestUpstream(t, rs.URL)
	resp, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusFound {
		t.Errorf("status = %d, want the 302 itself", resp.Status)
	}
	if n := len(target.Seen()); n != 0 {
		t.Errorf("the redirect target received %d requests (the bearer token would have leaked)", n)
	}
}

func TestUpstream_RedirectPolicyIsKeptWhenAClientIsGiven(t *testing.T) {
	target := hmtoolstest.NewRecordingServer(t, nil)
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/overview", http.StatusFound)
	})
	own := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	up, err := NewUpstream(rs.URL, time.Second, own)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}})
	if err != nil || resp.Status != http.StatusFound || len(target.Seen()) != 0 {
		t.Errorf("resp = %+v err = %v target hits = %d", resp, err, len(target.Seen()))
	}
}

func TestUpstream_RefusesOtherMethods(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, nil)
	up := newTestUpstream(t, rs.URL)
	for _, method := range []string{"DELETE", "PUT", "PATCH", ""} {
		if _, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: method, Pattern: "/api/overview"}}); err == nil {
			t.Errorf("method %q: want an error", method)
		}
	}
	if n := len(rs.Seen()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestUpstream_BodyIsCapped(t *testing.T) {
	rs := hmtoolstest.NewRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("a", 1<<20)
		for range 10 {
			_, _ = io.WriteString(w, chunk)
		}
	})
	up := newTestUpstream(t, rs.URL)
	resp, err := up.Do(context.Background(), "tok", "", Call{Route: Route{Method: "GET", Pattern: "/api/overview"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Body) != maxUpstreamBodyBytes {
		t.Errorf("body = %d bytes, want it capped at %d", len(resp.Body), maxUpstreamBodyBytes)
	}
}

func TestNewUpstream_RejectsBadBaseURLs(t *testing.T) {
	for _, base := range []string{"", "ftp://x", "https://", "https://x/api", "https://x?a=b", "https://u@x", "not a url"} {
		if _, err := NewUpstream(base, time.Second, nil); err == nil {
			t.Errorf("base %q: want an error", base)
		}
	}
	if _, err := NewUpstream("https://x", 0, nil); err == nil {
		t.Error("zero timeout: want an error")
	}
}

func TestRoute_String(t *testing.T) {
	if got := (Route{Method: "GET", Pattern: "/api/services/{serviceId}"}).String(); got != "GET /api/services/{serviceId}" {
		t.Errorf("String = %q", got)
	}
}

// Harbormaster's internal ALB may carry a certificate from a private CA the gateway image does not know.
func TestUpstream_TrustsTheConfiguredExtraCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	call := Call{Route: Route{Method: "GET", Pattern: "/api/overview"}}

	without, err := NewUpstreamWithCA(srv.URL, 5*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := without.Do(context.Background(), "tok", "", call); err == nil {
		t.Error("an unknown CA was trusted without HM_MCP_UPSTREAM_CA_PEM")
	}

	with, err := NewUpstreamWithCA(srv.URL, 5*time.Second, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := with.Do(context.Background(), "tok", "", call)
	if err != nil || resp.Status != http.StatusOK {
		t.Errorf("with the CA configured: resp %+v, err %v; want 200", resp, err)
	}
}

func TestNewUpstreamWithCA_RejectsPEMWithoutCertificates(t *testing.T) {
	if _, err := NewUpstreamWithCA("https://x.internal", time.Second, "garbage"); err == nil {
		t.Error("garbage PEM accepted")
	}
}
