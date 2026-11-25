package agentresult

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const peeledChars = "()<>[]\"'"

// StripForeignURLs replaces every link that is not baseURL's own with URLPlaceholder, in one pass over the
// whitespace-separated tokens. A token is a link when it holds "://", starts with "www." or "mailto:", or begins with a
// dotted host followed by "/". Brackets and quotes around it are peeled first, so Markdown and Slack forms are caught.
// Only a token that parses and has baseURL's scheme and host, with no user info, is kept. An empty or unparsable
// baseURL removes all. Best effort: it cannot see a link a reader's client builds from pieces.
func StripForeignURLs(text, baseURL string) string {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		base = nil
	}
	var out strings.Builder
	out.Grow(len(text))
	tokenStart := 0
	flush := func(end int) {
		if end > tokenStart {
			out.WriteString(stripToken(text[tokenStart:end], base))
		}
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			flush(i)
			out.WriteString(text[i : i+size])
			tokenStart = i + size
		}
		i += size
	}
	flush(len(text))
	return out.String()
}

// stripToken peels brackets and quotes, and replaces what is left when it is a link that is not base's own.
func stripToken(token string, base *url.URL) string {
	core := strings.TrimLeft(token, peeledChars)
	lead := token[:len(token)-len(core)]
	trimmed := strings.TrimRight(core, peeledChars)
	trail := core[len(trimmed):]
	if !looksLikeLink(trimmed) || isOwnLink(trimmed, base) {
		return token
	}
	return lead + URLPlaceholder + trail
}

func looksLikeLink(token string) bool {
	if strings.Contains(token, "://") {
		return true
	}
	lower := strings.ToLower(token[:min(len(token), 7)])
	if strings.HasPrefix(lower, "www.") || strings.HasPrefix(lower, "mailto:") {
		return true
	}
	slash := strings.IndexByte(token, '/')
	return slash > 0 && isDottedHost(token[:slash])
}

// isDottedHost reports a name like "evil.example" or "a.b:8080": letters, digits, hyphens and dots, ending in a
// letters-only label of two or more characters, so "1.5" is not a host.
func isDottedHost(s string) bool {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	dot := strings.LastIndexByte(s, '.')
	if dot <= 0 || len(s)-dot-1 < 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if i > dot && !letter || !(letter || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// isOwnLink reports a parsed link with base's scheme and host (port included) and no user info.
func isOwnLink(token string, base *url.URL) bool {
	if base == nil {
		return false
	}
	u, err := url.Parse(token)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}
