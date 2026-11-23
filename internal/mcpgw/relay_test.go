package mcpgw

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const relayMarker = "SECRET-MARKER-9f3a"

func tokenForm() string {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {relayMarker + "-code"},
		"code_verifier": {relayMarker + "-verifier"}, "client_id": {"https://client.example/meta.json"},
	}.Encode()
}

func relayHarness(t *testing.T, upstream http.HandlerFunc) *gatewayHarness {
	t.Helper()
	return newHarness(t, harnessOptions{mutateConfig: withOAuth, upstream: upstream})
}

func (h *gatewayHarness) relay(t *testing.T, contentType, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/oauth/token", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const formType = "application/x-www-form-urlencoded"

func TestRelay_PassesFormAndAnswerThrough(t *testing.T) {
	const answer = `{"access_token":"hmo_x","token_type":"Bearer","expires_in":3600}`
	h := relayHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Secret", "leak")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, answer)
	})
	form := tokenForm()
	resp := h.relay(t, formType+"; charset=UTF-8", form, map[string]string{
		"Authorization": "Bearer hmp_should_not_travel", "Cookie": "session=abc",
	})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || body != answer {
		t.Errorf("status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Upstream-Secret") != "" {
		t.Error("upstream headers must not be copied through")
	}
	seen := h.upstream.seen()
	if len(seen) != 1 {
		t.Fatalf("%d upstream requests, want 1", len(seen))
	}
	got := seen[0]
	if got.Method != http.MethodPost || got.EscapedPath != "/api/oauth/token" || got.Body != form {
		t.Errorf("upstream saw %s %s body %q", got.Method, got.EscapedPath, got.Body)
	}
	if got.Header.Get("Content-Type") != formType {
		t.Errorf("upstream Content-Type = %q", got.Header.Get("Content-Type"))
	}
	for _, header := range []string{"Authorization", "Cookie"} {
		if v := got.Header.Get(header); v != "" {
			t.Errorf("upstream received %s: %q", header, v)
		}
	}
}

func TestRelay_UpstreamStatusIsReturnedUnchanged(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		h := relayHarness(t, respondJSON(status, `{"error":"invalid_grant"}`))
		resp := h.relay(t, formType, tokenForm(), nil)
		body := readBody(t, resp)
		if resp.StatusCode != status || body != `{"error":"invalid_grant"}` || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("status %d: got %d %q cache %q", status, resp.StatusCode, body, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestRelay_RefusesJSONAndOversizedBodies(t *testing.T) {
	h := relayHarness(t, nil)
	check := func(name string, resp *http.Response, want int) {
		t.Helper()
		readBody(t, resp)
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, want)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q", name, resp.Header.Get("Cache-Control"))
		}
	}
	check("json", h.relay(t, "application/json", `{"grant_type":"x"}`, nil), http.StatusUnsupportedMediaType)
	check("no content type", h.relay(t, "", "a=b", nil), http.StatusUnsupportedMediaType)
	check("multipart", h.relay(t, "multipart/form-data; boundary=x", "a=b", nil), http.StatusUnsupportedMediaType)
	check("over the cap", h.relay(t, formType, "a="+strings.Repeat("x", 8<<10), nil), http.StatusRequestEntityTooLarge)
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls for refused requests", n)
	}

	// Unknown length (chunked) is capped by reading, not only by Content-Length.
	req, _ := http.NewRequest(http.MethodPost, h.gateway.URL+"/oauth/token", io.NopCloser(strings.NewReader("a="+strings.Repeat("x", 20<<10))))
	req.ContentLength = -1
	req.Header.Set("Content-Type", formType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	check("chunked over the cap", resp, http.StatusRequestEntityTooLarge)
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls for a chunked oversized body", n)
	}

	// Exactly the cap goes through.
	resp = h.relay(t, formType, "a="+strings.Repeat("x", (8<<10)-2), nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("body of exactly 8 KiB: status %d, want 200", resp.StatusCode)
	}
}

func TestRelay_OnlyPOSTAndOriginChecked(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: func(c *Config) {
		withOAuth(c)
		c.AllowedOrigins = []string{"https://claude.ai"}
	}})
	resp, _ := getPath(t, h, http.MethodGet, "/oauth/token")
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Errorf("GET: status %d allow %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	resp = h.relay(t, formType, tokenForm(), map[string]string{"Origin": "https://evil.example"})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "origin_not_allowed") {
		t.Errorf("bad origin: status %d body %q", resp.StatusCode, body)
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls with a bad Origin", n)
	}
	resp = h.relay(t, formType, tokenForm(), map[string]string{"Origin": "https://claude.ai"})
	readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allowed origin: status %d", resp.StatusCode)
	}
	resp = h.relay(t, formType, tokenForm(), nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no origin: status %d", resp.StatusCode)
	}
}

func TestRelay_UpstreamDownIs502(t *testing.T) {
	h := relayHarness(t, nil)
	h.upstream.Close()
	resp := h.relay(t, formType, tokenForm(), nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "temporarily_unavailable") {
		t.Errorf("status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	if strings.Contains(body, relayMarker) || strings.Contains(h.log.String(), relayMarker) {
		t.Error("the form leaked into the 502 answer or the log")
	}
}

func TestRelay_OffWithoutAuthorizeURL(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	resp := h.relay(t, formType, tokenForm(), nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /oauth/token status %d, want 404", resp.StatusCode)
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls with OAuth off", n)
	}
}

// M1: a shared egress IP (claude.ai) must not be lockable out of /mcp or the
// token endpoint by junk codes; the WAF and Harbormaster limit /oauth/token.
func TestRelay_TokenFailuresNeverBlockTheCaller(t *testing.T) {
	for _, failing := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		h := relayHarness(t, respondJSON(failing, `{"error":"invalid_grant"}`))
		for i := 0; i < failureLimit*3; i++ {
			resp := h.relay(t, formType, tokenForm(), nil)
			readBody(t, resp)
			if resp.StatusCode != failing {
				t.Fatalf("upstream %d: attempt %d got %d, want it relayed unchanged", failing, i, resp.StatusCode)
			}
		}
	}
}

func TestRelay_SuccessAndServerErrorsDoNotCountAsFailures(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		h := relayHarness(t, respondJSON(status, `{}`))
		for i := 0; i < failureLimit+5; i++ {
			resp := h.relay(t, formType, tokenForm(), nil)
			readBody(t, resp)
			if resp.StatusCode != status {
				t.Fatalf("upstream %d: attempt %d got %d", status, i, resp.StatusCode)
			}
		}
	}
}

func TestRelay_TokenFailuresDoNotBlockMCP(t *testing.T) {
	h := relayHarness(t, respondJSON(http.StatusBadRequest, `{}`))
	for i := 0; i < failureLimit*3; i++ {
		readBody(t, h.relay(t, formType, tokenForm(), nil))
	}
	resp := h.rpc(t, h.authed(), "tools/list", nil)
	readBody(t, resp)
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Error("/mcp blocked by token-endpoint failures")
	}
}

// L6: only Retry-After is copied from the upstream answer.
func TestRelay_PassesRetryAfterThrough(t *testing.T) {
	h := relayHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "17")
		w.Header().Set("Set-Cookie", "a=b")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow_down"}`)
	})
	resp := h.relay(t, formType, tokenForm(), nil)
	readBody(t, resp)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "17" {
		t.Errorf("status %d Retry-After %q, want 429 and 17", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("Set-Cookie must not be copied")
	}
}

func TestRelay_NeverLogsTheBody(t *testing.T) {
	h := relayHarness(t, respondJSON(http.StatusOK, `{"access_token":"`+relayMarker+`-access","refresh_token":"`+relayMarker+`-refresh"}`))
	readBody(t, h.relay(t, formType, tokenForm(), nil))
	h.upstream.Close()
	readBody(t, h.relay(t, formType, tokenForm(), nil)) // the transport-failure path logs too
	readBody(t, h.relay(t, formType, "a="+strings.Repeat("x", 9<<10)+relayMarker, nil))
	logs := h.log.String()
	if strings.Contains(logs, relayMarker) {
		t.Errorf("the log contains the form or the answer:\n%s", logs)
	}
	if !strings.Contains(logs, "oauth/token") {
		t.Errorf("expected a one-line record of the relay:\n%s", logs)
	}
}

func TestAuthorize_OffIs404(t *testing.T) {
	for name, mutate := range map[string]func(*Config){"oauth off": nil, "oauth on, gateway switch off": withOAuth} {
		h := newHarness(t, harnessOptions{mutateConfig: mutate})
		resp, _ := getPath(t, h, http.MethodGet, "/oauth/authorize?client_id=x")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, resp.StatusCode)
		}
	}
}

// noFollow returns the first response without following a redirect.
func noFollowDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	return resp
}

func TestAuthorize_RedirectsToTheConfiguredHostOnly(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: withOAuthViaGateway})
	rawQuery := "response_type=code&client_id=https%3A%2F%2Fclient.example%2Fmeta.json&redirect_uri=https%3A%2F%2Fevil.example%2Fcb&state=a%20b&code_challenge=abc-_&scope=harbormaster%3Aread"
	req, _ := http.NewRequest(http.MethodGet, h.gateway.URL+"/oauth/authorize?"+rawQuery, nil)
	resp := noFollowDo(t, req)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want 302", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Location"), testAuthorizeURL+"/oauth/authorize?"+rawQuery; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if n := len(h.upstream.seen()); n != 0 {
		t.Errorf("%d upstream calls from a bare redirect", n)
	}

	// No query at all.
	req, _ = http.NewRequest(http.MethodGet, h.gateway.URL+"/oauth/authorize", nil)
	if got := noFollowDo(t, req).Header.Get("Location"); got != testAuthorizeURL+"/oauth/authorize" {
		t.Errorf("Location without a query = %q", got)
	}
}

func TestAuthorize_IsNotAnOpenRedirect(t *testing.T) {
	h := newHarness(t, harnessOptions{mutateConfig: withOAuthViaGateway})
	const want = testAuthorizeURL
	hostile := []struct {
		name, path, host string
		headers          map[string]string
	}{
		{name: "host header", path: "/oauth/authorize?x=1", host: "evil.example"},
		{name: "forwarded host", path: "/oauth/authorize?x=1", headers: map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http", "Forwarded": "host=evil.example"}},
		{name: "absolute url in a value", path: "/oauth/authorize?next=https://evil.example/&redirect_uri=//evil.example"},
		{name: "at-sign and backslash", path: "/oauth/authorize?@evil.example&x=\\\\evil.example"},
		{name: "scheme-relative query", path: "/oauth/authorize?//evil.example"},
		{name: "empty value", path: "/oauth/authorize?"},
	}
	for _, row := range hostile {
		t.Run(row.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, h.gateway.URL+row.path, nil)
			if row.host != "" {
				req.Host = row.host
			}
			for k, v := range row.headers {
				req.Header.Set(k, v)
			}
			resp := noFollowDo(t, req)
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status %d", resp.StatusCode)
			}
			location, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if got := location.Scheme + "://" + location.Host; got != want || location.Path != "/oauth/authorize" {
				t.Errorf("Location %q leaves %s/oauth/authorize", resp.Header.Get("Location"), want)
			}
		})
	}

	// Paths that are not exactly /oauth/authorize are not redirects at all.
	for _, path := range []string{"//evil.example/oauth/authorize", "/oauth/authorize/", "/oauth/authorize/../x", "/OAUTH/authorize", "/oauth/authorizeX", "///evil.example"} {
		req, _ := http.NewRequest(http.MethodGet, h.gateway.URL+path, nil)
		resp := noFollowDo(t, req)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && !strings.HasPrefix(resp.Header.Get("Location"), testAuthorizeURL+"/oauth/authorize") {
			t.Errorf("%s redirects to %q", path, resp.Header.Get("Location"))
		}
		if resp.StatusCode == http.StatusFound {
			t.Errorf("%s: status 302; only the exact path may redirect", path)
		}
	}

	// Other methods are refused, not redirected.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, h.gateway.URL+"/oauth/authorize?x=1", strings.NewReader("a=b"))
		if resp := noFollowDo(t, req); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, resp.StatusCode)
		}
	}
}
