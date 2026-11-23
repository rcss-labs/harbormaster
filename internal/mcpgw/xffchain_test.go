package mcpgw

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/auth"
)

// Deployed chain: internet client 203.0.113.7 -> public ALB (node 10.0.100.5, appends the client) -> gateway task
// 10.0.9.20 (forwardedFor) -> internal ALB (appends its peer, the gateway task) -> Harbormaster (auth.ClientIP).
func TestClientIPAcrossBothLoadBalancers(t *testing.T) {
	gatewayCIDRs := []netip.Prefix{netip.MustParsePrefix("10.0.8.0/22")}
	rows := []struct {
		name     string
		incoming string
		want     string
	}{
		{"spoofed entry in front of the client", "6.6.6.6, 203.0.113.7", "203.0.113.7"},
		{"several spoofed entries", "6.6.6.6, 7.7.7.7, 8.8.8.8, 203.0.113.7", "203.0.113.7"},
		{"no spoofing", "203.0.113.7", "203.0.113.7"},
	}
	for _, row := range rows {
		atGateway := httptest.NewRequest("POST", "/mcp", nil)
		atGateway.RemoteAddr = "10.0.100.5:44321" // public ALB node
		atGateway.Header.Set("X-Forwarded-For", row.incoming)
		sentUpstream := forwardedFor(atGateway, true)

		atHarbormaster := httptest.NewRequest("GET", "/api/overview", nil)
		atHarbormaster.RemoteAddr = "10.0.50.9:5555"                             // internal ALB node
		atHarbormaster.Header.Set("X-Forwarded-For", sentUpstream+", 10.0.9.20") // internal ALB appends the gateway task
		got := auth.ClientIP(atHarbormaster, true, gatewayCIDRs)
		if got != row.want {
			t.Errorf("%s: chain at Harbormaster %q gives client ip %q, want %q",
				row.name, atHarbormaster.Header.Get("X-Forwarded-For"), got, row.want)
		}
	}
}

func TestForwardedFor_SendsExactlyOneEntry(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		xff     string
		trusted bool
		want    string
	}{
		{"trusted: the entry the load balancer appended", "10.0.100.5:1", "6.6.6.6, 203.0.113.7", true, "203.0.113.7"},
		{"trusted: trailing empty entries skipped", "10.0.100.5:1", "203.0.113.7, ,", true, "203.0.113.7"},
		{"trusted without a header: the peer", "10.0.100.5:1", "", true, "10.0.100.5"},
		{"untrusted: the peer, header dropped", "10.0.100.5:1", "6.6.6.6", false, "10.0.100.5"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := forwardedFor(r, c.trusted); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
