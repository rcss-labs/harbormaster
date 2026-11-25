package agentresult

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A link is judged by one blunt rule over whitespace-separated tokens: it is kept only when the whole token is Harbormaster's
// own base, or the base and a plain path. Any other token that looks like a link, in any spelling a reader's client may
// turn into one, is replaced whole. Bare hosts without a path or port are the documented residual.

var baseShape = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)

// linkMarkers make a token a link wherever they appear, after folding.
var linkMarkers = []string{"://", ":/", "//", "www.", "mailto:", "tel:", "javascript:", "data:", "]("}

// StripForeignURLs replaces every link that is not baseURL's own with URLPlaceholder. An empty or unusable baseURL
// removes all of them.
func StripForeignURLs(text, baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if !baseShape.MatchString(base) {
		base = ""
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

func stripToken(token, base string) string {
	if isPlainOwnLink(strings.TrimRight(token, ".,;:!?)"), base) || !looksLikeLink(token) {
		return token
	}
	return URLPlaceholder
}

// isPlainOwnLink reports the base alone, or the base and a path of plain characters: no query, hash, escape or user info.
func isPlainOwnLink(token, base string) bool {
	if base == "" || !strings.HasPrefix(token, base) {
		return false
	}
	path := token[len(base):]
	if path == "" {
		return true
	}
	if path[0] != '/' {
		return false
	}
	for i := 1; i < len(path); i++ {
		c := path[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._~/-", c) >= 0) {
			return false
		}
	}
	return true
}

// fold lowercases and maps the look-alikes of "." and the fullwidth ASCII forms to their plain characters.
func fold(token string) string {
	var out strings.Builder
	out.Grow(len(token))
	for _, r := range token {
		switch {
		case r == 0x3002 || r == 0xFF0E || r == 0xFF61 || r == 0x2024:
			r = '.'
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

func looksLikeLink(token string) bool {
	folded := fold(token)
	for _, marker := range linkMarkers {
		if strings.Contains(folded, marker) {
			return true
		}
	}
	return hasHostRun(folded)
}

func isLabelRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '-' || r == '_'
}

// hasHostRun reports a run of two or more dot-joined labels (optionally ending in a dot) followed by "/", "?", "#" or
// ":" and a digit. The run is found in one pass. Plain fractions and short versions such as "1.5/2" are not links.
func hasHostRun(folded string) bool {
	labels, dots := 0, 0
	inLabel, digitsOnly := false, true
	reset := func() { labels, dots, inLabel, digitsOnly = 0, 0, false, true }
	for i := 0; i < len(folded); {
		r, size := utf8.DecodeRuneInString(folded[i:])
		switch {
		case isLabelRune(r):
			if !inLabel {
				inLabel = true
				labels++
			}
			digitsOnly = digitsOnly && r >= '0' && r <= '9'
		case r == '.' && inLabel:
			inLabel = false
			dots++
		default:
			next := i + size
			ends := r == '/' || r == '?' || r == '#' || r == ':' && next < len(folded) && folded[next] >= '0' && folded[next] <= '9'
			if ends && labels >= 2 && !(digitsOnly && dots <= 2 && r == '/' && allDigits(folded[next:])) {
				return true
			}
			reset()
		}
		i += size
	}
	return false
}

// allDigits reports ASCII digits, at least one, then at most closing punctuation, up to the end of s. It stops at the
// first byte that is neither.
func allDigits(s string) bool {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	return digits > 0 && strings.Trim(s[digits:], ".,;:!?)") == ""
}
