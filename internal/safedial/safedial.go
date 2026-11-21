// Package safedial is the shared outbound guard for requests whose destination an operator or a remote
// registry chooses: it checks the address actually dialled (so DNS rebinding, redirects and IP literals
// cannot reach an internal address) and offers URL and redirect checks for the same policy.
package safedial

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// ErrBlocked is returned when no address of the destination may be connected to.
var ErrBlocked = errors.New("destination is not allowed")

// Policy says where a client may connect. The zero value allows public addresses only, over https.
type Policy struct {
	AllowPrivate   bool           // every RFC 1918 / fc00::/7 address (the scanner, for ECR only)
	AllowedPrivate []netip.Prefix // private addresses allowed when AllowPrivate is false
	AllowLoopback  bool           // fake mode and tests only
	Schemes        []string       // URL schemes CheckURL accepts; empty means https only
	Hosts          []string       // when set, the only host names CheckURL accepts (exact, case-insensitive)
}

// Prefixes that stay blocked for every caller, including ECR.
var (
	carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")
	// Special-use IPv4 ranges (RFC 6890) that are not routable public destinations. 240.0.0.0/4 includes
	// the 255.255.255.255 broadcast address.
	specialIPv4 = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
		netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
		netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
		netip.MustParsePrefix("240.0.0.0/4"),   // reserved
	}
	ec2IPv6Metadata = netip.MustParsePrefix("fd00:ec2::/32") // inside fc00::/7, so it needs its own entry
	// Translation prefixes embed an IPv4 address the filter would otherwise not see.
	translatedIPv6 = []netip.Prefix{
		netip.MustParsePrefix("64:ff9b::/96"),   // NAT64
		netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
		netip.MustParsePrefix("2002::/16"),      // 6to4
	}
)

// Allowed reports whether a client may connect to addr. Unspecified, link-local (which holds the
// 169.254.169.254 metadata endpoint), multicast, carrier-grade NAT, the EC2 IPv6 metadata range and the
// IPv6 translation prefixes are never allowed. Loopback needs AllowLoopback. Private ranges (RFC 1918,
// fc00::/7) need AllowPrivate or a prefix in AllowedPrivate.
func Allowed(addr netip.Addr, p Policy) bool {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid(), addr.IsUnspecified(), addr.IsLinkLocalUnicast(), addr.IsMulticast(),
		carrierGradeNAT.Contains(addr), ec2IPv6Metadata.Contains(addr), inAny(translatedIPv6, addr),
		inAny(specialIPv4, addr):
		return false
	case addr.IsLoopback():
		return p.AllowLoopback
	case addr.IsPrivate():
		return p.AllowPrivate || inAny(p.AllowedPrivate, addr)
	}
	return true
}

func inAny(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Dialer resolves the host itself and dials only an address that passed Allowed, so the address checked is
// the address connected to.
type Dialer struct {
	Policy Policy
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)         // nil = net.DefaultResolver
	Dial   func(ctx context.Context, network, address string) (net.Conn, error) // nil = net.Dialer
}

// DialContext is an http.Transport DialContext.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addrs = []netip.Addr{literal}
	} else if d.Lookup != nil {
		addrs, err = d.Lookup(ctx, host)
	} else {
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	if err != nil {
		return nil, err
	}
	dial := d.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	var lastErr error = ErrBlocked
	for _, addr := range addrs {
		if !Allowed(addr, d.Policy) {
			continue
		}
		conn, dialErr := dial(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

// CheckURL reports whether u may be requested: an accepted scheme, a host (on the host list when there is
// one) and no credentials in the URL.
func (p Policy) CheckURL(u *url.URL) error {
	schemes := p.Schemes
	if len(schemes) == 0 {
		schemes = []string{"https"}
	}
	okScheme := false
	for _, s := range schemes {
		if strings.EqualFold(u.Scheme, s) {
			okScheme = true
		}
	}
	if !okScheme {
		return errors.New("scheme is not allowed")
	}
	if u.Hostname() == "" {
		return errors.New("URL has no host")
	}
	if u.User != nil {
		return errors.New("URL must not contain credentials")
	}
	if len(p.Hosts) > 0 {
		for _, h := range p.Hosts {
			if strings.EqualFold(u.Hostname(), h) {
				return nil
			}
		}
		return errors.New("host is not allowed")
	}
	return nil
}

// CheckRedirect returns an http.Client CheckRedirect that follows at most maxRedirects redirects, each of
// which must pass CheckURL, and drops the Authorization header when the host changes.
func (p Policy) CheckRedirect(maxRedirects int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		if err := p.CheckURL(req.URL); err != nil {
			return err
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}
}
