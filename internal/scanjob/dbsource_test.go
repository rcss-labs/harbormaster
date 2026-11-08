package scanjob

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPDBSource_OpenArchiveKeepsTheConfiguredHost(t *testing.T) {
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Host+r.URL.RequestURI())
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	source := newHTTPDBSource(srv.Client(), srv.URL+"/databases/v6/latest.json", true)
	for _, name := range []string{"https://evil/x", "http://evil/x", "//evil/x", "a/b", "x?y", "x#y", "../x", "..", ".", "", `a\b`} {
		if body, err := source.OpenArchive(context.Background(), name); err == nil {
			_ = body.Close()
			t.Errorf("OpenArchive(%q) was accepted", name)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("requests made for rejected names: %v", seen)
	}
	body, err := source.OpenArchive(context.Background(), "db_v6.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if len(seen) != 1 || seen[0] != srv.Listener.Addr().String()+"/databases/v6/db_v6.tar.zst" {
		t.Fatalf("requests = %v, want one for the configured host and directory", seen)
	}
}

func TestSafeDial_RefusesInternalAddresses(t *testing.T) {
	dialed := ""
	dial := func(_ context.Context, _, addr string) (net.Conn, error) { dialed = addr; return nil, nil }
	lookup := func(ips ...string) func(context.Context, string) ([]net.IP, error) {
		return func(context.Context, string) ([]net.IP, error) {
			var out []net.IP
			for _, ip := range ips {
				out = append(out, net.ParseIP(ip))
			}
			return out, nil
		}
	}
	refused := []string{"169.254.169.254", "169.254.170.2", "10.1.2.3", "172.16.0.9", "192.168.0.1", "0.0.0.0", "224.0.0.1",
		"127.0.0.1", "::1", "fe80::1", "::", "fd00::1", "ff02::1", "::ffff:169.254.169.254"}
	for _, ip := range refused {
		dialed = ""
		if _, err := safeDialContext(dial, lookup(ip), false)(context.Background(), "tcp", "example.test:443"); err == nil || dialed != "" {
			t.Errorf("%s: err = %v, dialed %q, want refused before dialing", ip, err, dialed)
		}
	}
	// One bad answer among good ones is enough to refuse (DNS rebinding tricks).
	if _, err := safeDialContext(dial, lookup("93.184.216.34", "10.0.0.1"), false)(context.Background(), "tcp", "example.test:443"); err == nil {
		t.Error("a host resolving partly to a private address was accepted")
	}
	if _, err := safeDialContext(dial, lookup("93.184.216.34"), false)(context.Background(), "tcp", "example.test:443"); err != nil || dialed != "93.184.216.34:443" {
		t.Errorf("public address: err = %v, dialed %q, want it dialed by IP", err, dialed)
	}
	// The test-only allowance covers loopback and nothing else.
	if _, err := safeDialContext(dial, lookup("127.0.0.1"), true)(context.Background(), "tcp", "example.test:443"); err != nil {
		t.Errorf("loopback with the test allowance: %v", err)
	}
	if _, err := safeDialContext(dial, lookup("169.254.169.254"), true)(context.Background(), "tcp", "example.test:443"); err == nil {
		t.Error("link-local accepted under the loopback allowance")
	}
}
