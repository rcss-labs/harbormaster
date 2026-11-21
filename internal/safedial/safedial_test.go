package safedial

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
)

func TestAllowed(t *testing.T) {
	ten := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	linkLocal := []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}
	tests := []struct {
		addr   string
		policy Policy
		want   bool
	}{
		{"169.254.169.254", Policy{}, false},
		{"169.254.169.254", Policy{AllowPrivate: true}, false},
		{"169.254.169.254", Policy{AllowedPrivate: linkLocal}, false},
		{"169.254.170.2", Policy{AllowPrivate: true}, false},
		{"127.0.0.1", Policy{}, false},
		{"127.0.0.1", Policy{AllowPrivate: true}, false},
		{"127.0.0.1", Policy{AllowLoopback: true}, true},
		{"::1", Policy{}, false},
		{"::1", Policy{AllowLoopback: true}, true},
		{"0.0.0.0", Policy{AllowLoopback: true, AllowPrivate: true}, false},
		{"100.64.0.1", Policy{AllowPrivate: true}, false},
		{"fd00:ec2::254", Policy{AllowPrivate: true}, false},
		{"64:ff9b::a9fe:a9fe", Policy{AllowPrivate: true}, false},
		{"2002::1", Policy{AllowPrivate: true}, false},
		{"::ffff:10.0.0.1", Policy{}, false},
		{"::ffff:10.0.0.1", Policy{AllowedPrivate: ten}, true},
		{"10.0.0.5", Policy{}, false},
		{"10.0.0.5", Policy{AllowPrivate: true}, true},
		{"10.0.0.5", Policy{AllowedPrivate: ten}, true},
		{"11.0.0.5", Policy{AllowedPrivate: ten}, true}, // public
		{"172.16.0.1", Policy{AllowedPrivate: ten}, false},
		{"fd12::1", Policy{}, false},
		{"fd12::1", Policy{AllowPrivate: true}, true},
		// Special-use IPv4 ranges (RFC 6890) that are neither public nor covered by the checks above.
		{"0.1.2.3", Policy{AllowPrivate: true}, false},         // "this network"
		{"192.0.0.8", Policy{AllowPrivate: true}, false},       // IETF protocol assignments
		{"198.18.0.1", Policy{AllowPrivate: true}, false},      // benchmarking
		{"240.0.0.1", Policy{AllowPrivate: true}, false},       // reserved
		{"255.255.255.255", Policy{AllowPrivate: true}, false}, // broadcast
		{"8.8.8.8", Policy{}, true},
		{"2606:4700::1", Policy{}, true},
	}
	for _, tc := range tests {
		if got := Allowed(netip.MustParseAddr(tc.addr), tc.policy); got != tc.want {
			t.Errorf("Allowed(%s, %+v) = %v, want %v", tc.addr, tc.policy, got, tc.want)
		}
	}
	if Allowed(netip.Addr{}, Policy{AllowLoopback: true, AllowPrivate: true}) {
		t.Error("the zero address must not be allowed")
	}
}

func TestDialer_ChecksTheAddressItDials(t *testing.T) {
	var dialled []string
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("8.8.8.8")}, nil
		},
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialled = append(dialled, address)
			c, _ := net.Pipe()
			return c, nil
		},
	}
	conn, err := d.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(dialled) != 1 || dialled[0] != "8.8.8.8:443" {
		t.Fatalf("dialled %v, want only 8.8.8.8:443", dialled)
	}
}

func TestDialer_AllBlockedReturnsErrBlocked(t *testing.T) {
	called := false
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("169.254.169.254")}, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil },
	}
	_, err := d.DialContext(context.Background(), "tcp", "internal.example:443")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if called {
		t.Fatal("Dial must not be called")
	}
}

func TestDialer_IPLiteralIsChecked(t *testing.T) {
	called := false
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			t.Fatal("a literal must not be resolved")
			return nil, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil },
	}
	if _, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:80"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "[::1]:80"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if called {
		t.Fatal("Dial must not be called")
	}
}

func TestPolicy_CheckURL(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		url    string
		ok     bool
	}{
		{"https by default", Policy{}, "https://example.com/x", true},
		{"http refused by default", Policy{}, "http://example.com/x", false},
		{"http when listed", Policy{Schemes: []string{"http", "https"}}, "http://example.com", true},
		{"host list allows member", Policy{Hosts: []string{"hooks.slack.com"}}, "https://hooks.slack.com/a", true},
		{"host list is case-insensitive", Policy{Hosts: []string{"hooks.slack.com"}}, "https://HOOKS.slack.com/a", true},
		{"host list refuses others", Policy{Hosts: []string{"hooks.slack.com"}}, "https://evil.example/a", false},
		{"host list refuses a suffix trick", Policy{Hosts: []string{"slack.com"}}, "https://slack.com.evil.example/", false},
		{"userinfo refused", Policy{}, "https://user:pw@example.com/", false},
		{"no host refused", Policy{}, "https:///x", false},
	}
	for _, tc := range tests {
		u, err := url.Parse(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		if got := tc.policy.CheckURL(u) == nil; got != tc.ok {
			t.Errorf("%s: CheckURL(%s) ok = %v, want %v", tc.name, tc.url, got, tc.ok)
		}
	}
}

func TestPolicy_CheckRedirect(t *testing.T) {
	policy := Policy{Hosts: []string{"a.example", "b.example"}}
	check := policy.CheckRedirect(2)
	first, _ := http.NewRequest(http.MethodGet, "https://a.example/start", nil)
	next, _ := http.NewRequest(http.MethodGet, "https://b.example/next", nil)
	next.Header.Set("Authorization", "Basic secret")
	if err := check(next, []*http.Request{first}); err != nil {
		t.Fatalf("allowed redirect refused: %v", err)
	}
	if next.Header.Get("Authorization") != "" {
		t.Error("credentials must not follow a redirect to another host")
	}
	off, _ := http.NewRequest(http.MethodGet, "https://evil.example/", nil)
	if check(off, []*http.Request{first}) == nil {
		t.Error("redirect to a host outside the list must be refused")
	}
	plain, _ := http.NewRequest(http.MethodGet, "http://a.example/", nil)
	if check(plain, []*http.Request{first}) == nil {
		t.Error("redirect to http must be refused")
	}
	if check(next, []*http.Request{first, first, first}) == nil {
		t.Error("too many redirects must be refused")
	}
}
