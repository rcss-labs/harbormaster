package agentresult

import (
	"strings"
	"testing"
	"time"
)

func TestStripForeignURLs_Table(t *testing.T) {
	const base = "https://harbormaster.example"
	const gone = URLPlaceholder
	cases := []struct{ name, in, base, want string }{
		{"plain text", "nothing to see here", base, "nothing to see here"},
		{"http foreign", "see http://evil.example/x now", base, "see " + gone + " now"},
		{"https foreign", "see https://evil.example/x now", base, "see " + gone + " now"},
		{"other scheme", "ftp://evil.example/x and javascript://x and file:///etc/passwd", base, gone + " and " + gone + " and " + gone},
		{"www", "go to www.evil.example now", base, "go to " + gone + " now"},
		{"mailto", "write mailto:a@evil.example", base, "write " + gone},
		{"bare host with path", "open evil.example/path?q=1 please", base, "open " + gone + " please"},
		{"markdown link", "[click here](https://evil.example/x) ok", base, "[click " + gone + ") ok"},
		{"markdown single token", "[x](https://evil.example/x)", base, "[" + gone + ")"},
		{"slack form", "<https://evil.example/x|text> ok", base, "<" + gone + "> ok"},
		{"in brackets", "(see https://evil.example/x) and [www.evil.example]", base, "(see " + gone + ") and [" + gone + "]"},
		{"quoted", `"https://evil.example/x" 'http://evil.example'`, base, `"` + gone + `" '` + gone + `'`},
		{"own url kept", "open https://harbormaster.example/services/1 now", base, "open https://harbormaster.example/services/1 now"},
		{"own url in markdown", "(https://harbormaster.example/a)", base, "(https://harbormaster.example/a)"},
		{"own host upper case", "HTTPS://Harbormaster.Example/a", base, "HTTPS://Harbormaster.Example/a"},
		{"other scheme same host", "http://harbormaster.example/a", base, gone},
		{"other port", "https://harbormaster.example:8443/a", base, gone},
		{"prefix host", "https://harbormaster.example.evil.example/a", base, gone},
		{"suffix host", "https://evilharbormaster.example/a", base, gone},
		{"userinfo trick", "https://harbormaster.example@evil.example/", base, gone},
		{"userinfo on own host", "https://user:pw@harbormaster.example/", base, gone},
		{"junk after scheme", "https://", base, gone},
		{"empty base removes all", "https://harbormaster.example/a and evil.example/x", "", gone + " and " + gone},
		{"unparsable base removes all", "https://harbormaster.example/a", "://nope", gone},
		{"version number kept", "version 1.5/2 and and/or", base, "version 1.5/2 and and/or"},
		{"whitespace kept", "a\n\nhttps://evil.example/x\tb  c", base, "a\n\n" + gone + "\tb  c"},
		{"with port base", "https://hm.example:8443/a https://hm.example/b", "https://hm.example:8443", "https://hm.example:8443/a " + gone},
	}
	for _, c := range cases {
		if got := StripForeignURLs(c.in, c.base); got != c.want {
			t.Errorf("%s: StripForeignURLs(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestStripForeignURLs_LinearOnLongInput(t *testing.T) {
	const base = "https://harbormaster.example"
	inputs := map[string]func(n int) string{
		"no spaces":        func(n int) string { return strings.Repeat("a", n) },
		"scheme markers":   func(n int) string { return strings.Repeat("://", n/3) },
		"brackets":         func(n int) string { return strings.Repeat("([<\"'", n/5) },
		"dots and slashes": func(n int) string { return strings.Repeat("a./", n/3) },
		"many tokens":      func(n int) string { return strings.Repeat("http://e.example ", n/17) },
	}
	for name, make := range inputs {
		small, big := make(25_000), make(200_000)
		ts := timeOf(func() { StripForeignURLs(small, base) })
		tb := timeOf(func() { StripForeignURLs(big, base) })
		// Linear grows 8x; quadratic 64x.
		if tb > 30*ts+5*time.Millisecond {
			t.Errorf("%s: 8x the input took %v against %v", name, tb, ts)
		}
	}
}
