package hmtools

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientAddr is the peer address, unless trustedProxy says exactly one trusted
// proxy (Harbormaster's load balancer) is in front: then it is the rightmost
// non-empty entry across every X-Forwarded-For line, the address that proxy
// appended. Entries to its left are the caller's claims and never count. The
// result is a bare address (port and IPv6 zone dropped); an entry that is not
// an address falls back to the peer. Same rule as internal/api/scope.go.
func ClientAddr(r *http.Request, trustedProxy bool) string {
	peer := peerAddr(r)
	if !trustedProxy {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" {
			continue
		}
		if addr, ok := parseAddr(hop); ok {
			return addr
		}
		return peer
	}
	return peer
}

func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, ok := parseAddr(host); ok {
		return addr
	}
	return host
}

// parseAddr reads an address with or without a port and returns it without
// the port and zone.
func parseAddr(s string) (string, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().WithZone("").String(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.WithZone("").String(), true
	}
	return "", false
}
