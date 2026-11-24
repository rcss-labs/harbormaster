package hmtools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseBaseURL(t *testing.T) {
	good := map[string]string{
		"https://hm.example.com":       "https://hm.example.com",
		"https://hm.example.com/":      "https://hm.example.com",
		" https://hm.example.com:8443": "https://hm.example.com:8443",
		"http://127.0.0.1:8080":        "http://127.0.0.1:8080",
		"http://localhost":             "http://localhost",
		"http://[::1]:9":               "http://[::1]:9",
	}
	for raw, want := range good {
		if got, err := ParseBaseURL("X_URL", raw); err != nil || got != want {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"", "hm.example.com", "ftp://hm.example.com", "http://hm.example.com", "http://127.0.0.1.evil.example",
		"http://localhost.evil.example", "https://u:secretpw@hm.example.com", "https://hm.example.com/api", "https://hm.example.com?q=1",
		"https://hm.example.com#f", "https://", "https://hm.example.com?", "http://[::1",
	} {
		_, err := ParseBaseURL("X_URL", raw)
		if err == nil {
			t.Errorf("%q: no error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "X_URL") {
			t.Errorf("%q: the error does not name the setting: %v", raw, err)
		}
		if strings.Contains(err.Error(), "secretpw") || (raw != "" && strings.Contains(err.Error(), raw)) {
			t.Errorf("%q: the error repeats the value: %v", raw, err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LocalHost": true, "127.0.0.1": true, "127.5.5.5": true, "::1": true,
		"localhost.evil.example": false, "xlocalhost": false, "127.0.0.1.evil.example": false, "10.0.0.1": false, "": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v", host, got)
		}
	}
}

func TestUpstreamForProductSendsItsOwnUserAgent(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("User-Agent") }))
	defer server.Close()
	up, err := NewUpstreamForProduct(server.URL, time.Second, "", "harbormaster-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := up.Do(t.Context(), "tok", "", Call{Route: Route{Method: http.MethodGet, Pattern: "/api/overview"}}); err != nil {
		t.Fatal(err)
	}
	if got != "harbormaster-agent/"+Version {
		t.Errorf("User-Agent = %q", got)
	}
}
