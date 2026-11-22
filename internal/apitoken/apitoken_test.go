package apitoken

import (
	"regexp"
	"strings"
	"testing"
)

var rawShape = regexp.MustCompile(`^hm[por]_[a-z2-7]{12}_[A-Za-z0-9_-]{43}$`)

var allKinds = []Kind{KindPAT, KindOAuthAccess, KindOAuthRefresh}

func TestGenerate_RoundTripsThroughParse(t *testing.T) {
	for _, kind := range allKinds {
		raw, generated, err := Generate(kind)
		if err != nil {
			t.Fatalf("Generate(%q) error: %v", kind, err)
		}
		if !rawShape.MatchString(raw) {
			t.Errorf("Generate(%q) raw = %q, want shape %s", kind, raw, rawShape)
		}
		parsed, ok := Parse(raw)
		if !ok {
			t.Fatalf("Parse(%q) ok = false, want true", raw)
		}
		if parsed != generated {
			t.Errorf("Parse(%q) = %+v, want %+v", raw, parsed, generated)
		}
		if parsed.Kind != kind {
			t.Errorf("Parse(%q).Kind = %q, want %q", raw, parsed.Kind, kind)
		}
	}
}

func TestParse_RejectsMalformed(t *testing.T) {
	validSecret := strings.Repeat("A", 43)
	validID := "abcdefghijk2"
	cases := map[string]string{
		"empty":          "",
		"bearer prefix":  "Bearer hmp_" + validID + "_" + validSecret,
		"wrong prefix":   "hmx_" + validID + "_" + validSecret,
		"short id":       "hmp_abcdefghij_" + validSecret,
		"uppercase id":   "hmp_ABCDEFGHIJK2_" + validSecret,
		"short secret":   "hmp_" + validID + "_" + strings.Repeat("A", 42),
		"plus in secret": "hmp_" + validID + "_" + strings.Repeat("A", 42) + "+",
		"trailing space": "hmp_" + validID + "_" + validSecret + " ",
		"ten kilobytes":  strings.Repeat("x", 10*1024),
	}
	for name, raw := range cases {
		if _, ok := Parse(raw); ok {
			t.Errorf("%s: Parse(%q) ok = true, want false", name, raw)
		}
	}
}

func TestHash_IsStableHexSHA256(t *testing.T) {
	const wantSHA256OfAbc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Hash("abc"); got != wantSHA256OfAbc {
		t.Errorf("Hash(\"abc\") = %q, want %q", got, wantSHA256OfAbc)
	}
}

func TestDisplayID_NeverContainsTheSecret(t *testing.T) {
	for _, kind := range allKinds {
		raw, parsed, err := Generate(kind)
		if err != nil {
			t.Fatalf("Generate(%q) error: %v", kind, err)
		}
		display := DisplayID(parsed)
		if strings.Contains(display, parsed.Secret) {
			t.Errorf("DisplayID(%+v) = %q contains the secret", parsed, display)
		}
		if strings.Contains(display, raw) {
			t.Errorf("DisplayID returned the whole raw token %q", display)
		}
		wantPrefix := map[Kind]string{KindPAT: "hmp_", KindOAuthAccess: "hmo_", KindOAuthRefresh: "hmr_"}[kind]
		if want := wantPrefix + parsed.ID; display != want {
			t.Errorf("DisplayID = %q, want %q", display, want)
		}
	}
}
