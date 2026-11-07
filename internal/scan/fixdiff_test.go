package scan

import "testing"

func TestFixDiff(t *testing.T) {
	a := FindingKey{"CVE-0000-0001", "openssl"}
	b := FindingKey{"CVE-0000-0002", "nginx"}
	c := FindingKey{"CVE-0000-0003", "zlib"}
	d := FindingKey{"CVE-0000-0004", "curl"}
	fixes, introduces := FixDiff([]FindingKey{a, b, c}, []FindingKey{c, d})
	if fixes != 2 || introduces != 1 {
		t.Errorf("fixes %d introduces %d, want 2 and 1", fixes, introduces)
	}
	fixes, introduces = FixDiff([]FindingKey{a, a}, []FindingKey{a, d, d})
	if fixes != 0 || introduces != 1 {
		t.Errorf("duplicates should count once: fixes %d introduces %d", fixes, introduces)
	}
}
