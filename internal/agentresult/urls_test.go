package agentresult

import (
	"strings"
	"testing"
	"time"
)

const ownBase = "https://harbormaster.example"

// A link is kept only when the whole token is the base, or the base and a plain path.
func TestStripForeignURLs_KeepsOnlyPlainOwnLinksAndOrdinaryText(t *testing.T) {
	kept := []string{
		"nothing", "checkout-api.", "e.g.", "metadata:", "hotel:", "metadata:x", "stel:", "i.e.,", "v1.2.3", "and/or", "a/b", "1.5/2", "10.0.1/3", "(1.5/2)", "12/34",
		ownBase,
		ownBase + "/",
		ownBase + "/services/1",
		ownBase + "/services/svc-1/timeline",
		ownBase + "/a.b_c~d",
		ownBase + "/a.",
		ownBase + "/a),",
		ownBase + "/a;",
		ownBase + "/a!?",
	}
	for _, token := range kept {
		in := "see " + token + " now"
		if got := StripForeignURLs(in, ownBase); got != in {
			t.Errorf("StripForeignURLs(%q) = %q, want it unchanged", in, got)
		}
	}
	if got := StripForeignURLs("a https://hm.example:8443/a b", "https://hm.example:8443"); got != "a https://hm.example:8443/a b" {
		t.Errorf("base with port: %q", got)
	}
	if got := StripForeignURLs("a https://hm.example/b", "https://hm.example/"); got != "a https://hm.example/b" {
		t.Errorf("base with trailing slash: %q", got)
	}
}

func TestStripForeignURLs_RemovesEverythingElse(t *testing.T) {
	removed := []string{
		// plain foreign links
		"http://evil.example/x", "https://evil.example/x", "ftp://evil.example/x", "file:///etc/passwd", "javascript://x",
		"www.evil.example", "WWW.EVIL.EXAMPLE", "mailto:a@evil.example", "tel:+123456", "javascript:alert(1)",
		"data:text/html,x", "//evil.example/p", "https:/x", "https://", "evil.example/path?q=1",
		// own link with something extra
		ownBase + "/a?x=1", ownBase + "/a#f", ownBase + "/a%2e", ownBase + "/a%2Fb", ownBase + `/a"`, ownBase + "/a>",
		ownBase + "/a,https://evil.example/x", ownBase + "/a;www.evil.example", ownBase + "/a)[x](https://evil.example/x",
		ownBase + `"evil.example/x`, ownBase + ">evil.example/x", ownBase + ",evil.example/x", ownBase + ";www.evil.example",
		ownBase + "#evil.example/x", ownBase + "?evil.example/x", ownBase + ":8443/a", ownBase + "@evil.example/x",
		"http://harbormaster.example/a", "HTTPS://harbormaster.example/a", "https://Harbormaster.example/a",
		"https://harbormaster.example:8443/a", "https://harbormaster.example.evil.example/a",
		"https://evilharbormaster.example/a", "https://harbormaster.example@evil.example/",
		"https://user@harbormaster.example/", "https://user:pw@harbormaster.example/a",
		"https://harbormaster.example\\@evil.example/", "https://harbormaster.example/a\\b",
		// link shapes the review found
		"[https://harbormaster.example/](https://evil.example/x)", "<https://harbormaster.example/a|https://evil.example/x>",
		"<https://evil.example/x|text>", "[x](https://evil.example/x)", "[x](evil.example/p)", "[x](//evil.example/p)",
		"**evil.example/p**", "`evil.example/p`", "link:evil.example/p", "a,www.evil.example", "{www.evil.example",
		"1.2.3.4/leak", "1.2.3.4/5", "10.0.0.1:8080", "user@evil.example/x", "user:pw@evil.example/x",
		"evil.xn--p1ai/x", "пример.рф/x", "evil。com/x", "evil．com/x", "evil｡com/x", "evil․com/x", "evil.example./x",
		"my_evil.example.com/x", "EVIL.EXAMPLE/x", "evil.example?x=1", "evil.example#a", "evil.example:8080",
		"ｅｖｉｌ．ｅｘａｍｐｌｅ／x", "ｈｔｔｐｓ：／／ｅｖｉｌ．ｅｘａｍｐｌｅ", "ｗｗｗ．ｅｖｉｌ．ｅｘａｍｐｌｅ", "बुरा.भारत/x",
		"x(tel:1", "x(javascript:alert(1)", "(data:text/html,x", ";mailto:a@b", "\U0001F4A9.la/x", "\u24d4\u24e5\u24d8\u24db.com/x", "evil..example/x", "evil...example/x", "(see:https://evil.example/x)", "a.b/c", "x.y.z/", "1.5/2/evil.example/x",
	}
	for _, token := range removed {
		for _, base := range []string{ownBase, ""} {
			in := "see " + token + " now"
			if got := StripForeignURLs(in, base); got != "see "+URLPlaceholder+" now" {
				t.Errorf("base %q: StripForeignURLs(%q) = %q, want the token replaced whole", base, in, got)
			}
		}
	}
	// Whitespace between tokens is kept; every kind of space separates.
	in := "a\n\nhttps://evil.example/x\tb  c\u00a0evil.example/x\u2028ok\u0085www.evil.example"
	want := "a\n\n" + URLPlaceholder + "\tb  c\u00a0" + URLPlaceholder + "\u2028ok\u0085" + URLPlaceholder
	if got := StripForeignURLs(in, ownBase); got != want {
		t.Errorf("spacing: %q, want %q", got, want)
	}
	// An unusable base removes every link, its own included.
	for _, base := range []string{"", "://nope", "harbormaster.example", "https://", "https://harbormaster.example/a b", "javascript:x"} {
		if got := StripForeignURLs(ownBase+"/a", base); got != URLPlaceholder {
			t.Errorf("base %q kept the link: %q", base, got)
		}
	}
}

func TestStripForeignURLs_LinearOnLongInput(t *testing.T) {
	cases := []struct {
		name string
		make func(n int) string
		want func(in string) string
	}{
		{"no spaces", func(n int) string { return strings.Repeat("a", n) }, func(in string) string { return in }},
		{"scheme markers", func(n int) string { return strings.Repeat("://", n/3) }, func(string) string { return URLPlaceholder }},
		{"brackets", func(n int) string { return strings.Repeat("([<\"'", n/5) }, func(in string) string { return in }},
		{"dots and slashes", func(n int) string { return strings.Repeat("a./", n/3) }, func(in string) string { return in }},
		{"dots only", func(n int) string { return strings.Repeat("a.", n/2) }, func(in string) string { return in }},
		{"digits and dots", func(n int) string { return strings.Repeat("1.", n/2) + "/" + strings.Repeat("9", 10) }, func(string) string { return URLPlaceholder }},
		{"many fractions", func(n int) string { return strings.Repeat("1.5/1.5/", n/8) }, func(string) string { return URLPlaceholder }},
		{"many tokens", func(n int) string { return strings.Repeat("http://e.example ", n/17) }, func(in string) string {
			return strings.Repeat(URLPlaceholder+" ", len(in)/17)[:len(in)/17*(len(URLPlaceholder)+1)-1] + in[len(in)/17*17-1:]
		}},
		{"own links", func(n int) string { return strings.Repeat(ownBase+"/a ", n/len(ownBase+"/a ")) }, func(in string) string { return in }},
	}
	for _, c := range cases {
		small, big := c.make(25_000), c.make(200_000)
		if got := StripForeignURLs(big, ownBase); got != c.want(big) {
			t.Errorf("%s: wrong result for 200 KB (%d bytes out)", c.name, len(got))
		}
		ts := timeOf(func() { StripForeignURLs(small, ownBase) })
		tb := timeOf(func() { StripForeignURLs(big, ownBase) })
		// 8x the input: linear grows 8x, quadratic 64x.
		if tb > 30*ts+20*time.Millisecond {
			t.Errorf("%s: 8x the input took %v against %v", c.name, tb, ts)
		}
		if tb > 2*time.Second {
			t.Errorf("%s: 200 KB took %v", c.name, tb)
		}
	}
}
