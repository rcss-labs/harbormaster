package hmtools

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientAddr(t *testing.T) {
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = remote
		for _, line := range xff {
			r.Header.Add("X-Forwarded-For", line)
		}
		return r
	}
	rows := []struct {
		name    string
		r       *http.Request
		trusted bool
		want    string
	}{
		{"peer when not trusted, header ignored", req("10.0.0.5:1234", "6.6.6.6"), false, "10.0.0.5"},
		{"rightmost entry when trusted", req("10.0.0.5:1234", "6.6.6.6, 1.2.3.4"), true, "1.2.3.4"},
		{"trailing empty entries skipped", req("10.0.0.5:1234", "1.2.3.4, ,"), true, "1.2.3.4"},
		{"no header falls back to the peer", req("10.0.0.5:1234"), true, "10.0.0.5"},
		{"peer without a port", req("10.0.0.5"), false, "10.0.0.5"},
		{"two lines: the last line's last entry counts", req("10.0.0.5:1234", "6.6.6.6, 1.1.1.1", "2.2.2.2, 203.0.113.7"), true, "203.0.113.7"},
		{"two lines: a spoofed first line never counts", req("10.0.0.5:1234", "9.9.9.9", "203.0.113.7"), true, "203.0.113.7"},
		{"port dropped", req("10.0.0.5:1234", "203.0.113.7:4444"), true, "203.0.113.7"},
		{"bracketed IPv6 with port", req("10.0.0.5:1234", "[2001:db8::1]:443"), true, "2001:db8::1"},
		{"IPv6 zone dropped", req("10.0.0.5:1234", "fe80::1%eth0"), true, "fe80::1"},
		{"unparsable entry falls back to the peer", req("10.0.0.5:1234", "1.2.3.4, not-an-address"), true, "10.0.0.5"},
	}
	for _, row := range rows {
		if got := ClientAddr(row.r, row.trusted); got != row.want {
			t.Errorf("%s: %q, want %q", row.name, got, row.want)
		}
	}
}
