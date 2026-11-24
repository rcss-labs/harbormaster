// Package secretshape detects names and values that look like secrets. It is
// used to redact environment values and to scrub log text before it leaves
// for a model provider. All matching uses Go's linear-time regexp engine.
package secretshape

import (
	"regexp"
	"strings"
	"unicode"
)

// Redacted replaces every secret-shaped span.
const Redacted = "[redacted]"

var secretSegments = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true,
	"password": true, "passwd": true, "pwd": true,
	"credential": true, "credentials": true,
	"auth": true, "private": true, "cert": true, "dsn": true,
}

// valuePatterns match a span that is secret on its own, whole span replaced.
var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bgh[po]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[bp]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`),
	regexp.MustCompile(`\bhm[poar]_[A-Za-z0-9]{16,}`),
}

// urlPassword captures the part before and after the password in a URL with
// userinfo, so only the password is replaced and the host stays readable.
var urlPassword = regexp.MustCompile(`(\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]*:)[^\s/@]+(@)`)

// longRun finds candidate high-entropy tokens; looksLikeRandomRun decides.
var longRun = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var hexOnly = regexp.MustCompile(`^[0-9a-fA-F]{40,}$`)

// namedPair matches name=value, name: value and "name": "value".
var namedPair = regexp.MustCompile(`(["']?)([A-Za-z0-9_.-]+)(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`)

// NameLooksSecret reports whether a variable or field name suggests a secret.
// Matching is by whole segment, split on _, - and . (and camelCase), so
// KEYBOARD_LAYOUT and AUTHOR are not secret.
func NameLooksSecret(name string) bool {
	for _, segment := range nameSegments(name) {
		if secretSegments[segment] {
			return true
		}
	}
	return false
}

func nameSegments(name string) []string {
	var segments []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			segments = append(segments, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]):
			// camelCase boundary: apiKey -> api, key
			flush()
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	return segments
}

// ValueLooksSecret reports whether value contains a secret-shaped span.
func ValueLooksSecret(value string) bool {
	for _, p := range valuePatterns {
		if p.MatchString(value) {
			return true
		}
	}
	if urlPassword.MatchString(value) {
		return true
	}
	for _, run := range longRun.FindAllString(value, -1) {
		if looksLikeRandomRun(run) {
			return true
		}
	}
	return false
}

// looksLikeRandomRun accepts runs of 32+ characters with at least three
// character classes, or pure hex of 40+ (SHA-1 and longer digests).
func looksLikeRandomRun(run string) bool {
	if uuid.MatchString(run) {
		return false
	}
	if hexOnly.MatchString(run) {
		return true
	}
	var lower, upper, digit, other bool
	for _, r := range run {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit, other} {
		if present {
			classes++
		}
	}
	return classes >= 3
}

// ScrubText replaces each secret-shaped span in text with Redacted and leaves
// the rest byte-identical.
func ScrubText(text string) string {
	text = namedPair.ReplaceAllStringFunc(text, func(match string) string {
		parts := namedPair.FindStringSubmatch(match)
		if !NameLooksSecret(parts[2]) {
			return match
		}
		value := parts[4]
		replacement := Redacted
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
			replacement = value[:1] + Redacted + value[len(value)-1:]
		}
		return parts[1] + parts[2] + parts[3] + replacement
	})
	text = urlPassword.ReplaceAllString(text, "${1}"+Redacted+"${2}")
	for _, p := range valuePatterns {
		text = p.ReplaceAllString(text, Redacted)
	}
	return longRun.ReplaceAllStringFunc(text, func(run string) string {
		if looksLikeRandomRun(run) {
			return Redacted
		}
		return run
	})
}
