// Package secretshape detects names and values that look like secrets. It is
// used to redact environment values and to scrub log text before it leaves
// for a model provider. All matching uses Go's linear-time regexp engine.
package secretshape

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Redacted replaces every secret-shaped span.
const Redacted = "[redacted]"

var secretSegments = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true,
	"password": true, "passwd": true, "pwd": true,
	"credential": true, "credentials": true,
	"auth": true, "authorization": true, "private": true, "cert": true, "dsn": true,
	"keys": true, "apikeys": true, "tokens": true, "secrets": true, "passwords": true, "passwds": true,
	"pass": true, "passphrase": true, "secretkey": true, "accesskey": true, "privatekey": true,
	"clientsecret": true, "authtoken": true, "accesstoken": true, "secretaccesskey": true, "awssecretkey": true,
	"connectionstring": true,
}

// boundary is a non-alphanumeric character or the start of the text. It is
// used instead of \b because underscore is a word character, which would
// hide env_AKIA... from \b.
const boundary = `(^|[^A-Za-z0-9])`

// valuePatterns match a secret on its own; group 2 is the secret.
var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(boundary + `((?:AKIA|ASIA)[A-Z0-9]{16})`),
	regexp.MustCompile(`()(-----BEGIN [A-Z0-9 ]+-----)`),
	regexp.MustCompile(boundary + `(eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)`),
	regexp.MustCompile(boundary + `(sk-[A-Za-z0-9_-]{16,})`),
	regexp.MustCompile(boundary + `(gh[po]_[A-Za-z0-9]{20,})`),
	regexp.MustCompile(boundary + `(xox[bp]-[A-Za-z0-9-]{10,})`),
	regexp.MustCompile(boundary + `(AIza[A-Za-z0-9_-]{30,})`),
	// Harbormaster tokens: prefix, 12 base32 characters, "_", 43 base64url characters. The second branch keeps
	// shorter or malformed tokens caught.
	regexp.MustCompile(boundary + `(hm[poar]_(?:[a-z2-7]{12}_[A-Za-z0-9_-]{43}|[A-Za-z0-9]{16,}))`),
	// Standalone "Bearer <token>" as sent in an Authorization header.
	regexp.MustCompile(boundary + `Bearer[ \t]+([A-Za-z0-9._~+/=-]+)`),
}

// urlUserinfo matches scheme://user:password@; group 2 is the password. The
// password may contain '/', so it runs to the last '@' in the token.
var urlUserinfo = regexp.MustCompile(`(\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]*:)([^\s"'<>]+)@`)

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// longRun finds candidate random tokens: base64, base64url and hex.
var longRun = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)

var (
	hexOnly = regexp.MustCompile(`^[0-9a-fA-F]{40,}$`)
	uuid    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// wordySegment is a run of plain words, camelCase words, acronyms or
	// numbers: an identifier piece, not random data. Lowercase hex pieces
	// such as pod hashes and short ids are treated the same way.
	wordySegment = regexp.MustCompile(`^(?:[A-Z]?[a-z]{3,}|[A-Z]{2,}|[0-9]+)+$`)
	hexSegment   = regexp.MustCompile(`^[0-9a-f]{6,}$`)

	digestPrefix = regexp.MustCompile(`sha[0-9]+:$`)
	arnSpan      = regexp.MustCompile(`\barn:aws[a-z-]*:[^\s"'<>,;]+`)
)

// pairName matches the name and operator of name=value, name: value and
// "name": "value". The value is parsed separately so that a secret nested in
// another pair's value is still found.
var pairName = regexp.MustCompile(`((?:\\*["'])?)([A-Za-z0-9_.-]+)((?:\\*["'])?)[ \t]*[=:][ \t]*`)

// unquotedValue is a run of characters up to whitespace, a separator or a quote. Brackets inside it belong to the
// value; only a trailing run of unbalanced closing brackets is JSON structure (see trimClosers).
var unquotedValue = regexp.MustCompile(`^[^\s,;&"']+`)

var authScheme = regexp.MustCompile(`^(?i:bearer|basic)$`)

var authToken = regexp.MustCompile(`^[ \t]+[A-Za-z0-9._~+/=-]+`)

// notPassWords end in "pass" without being a password name.
var notPassWords = map[string]bool{
	"bypass": true, "compass": true, "trespass": true, "surpass": true, "overpass": true, "underpass": true,
	"encompass": true, "impass": true,
}

// NameLooksSecret reports whether an environment variable or field name suggests a secret. It is deliberately
// wide because over-redacting a name is safe there; scrubbing free text uses the narrower pairNameKind.
// Matching is by whole segment, split on _, - and . (and camelCase), so KEYBOARD_LAYOUT and AUTHOR are not secret.
func NameLooksSecret(name string) bool {
	segments := nameSegments(name)
	for i, segment := range segments {
		if secretSegments[segment] || endsInPasswordWord(segment) {
			return true
		}
		if strings.HasSuffix(segment, "pass") && !notPassWords[segment] {
			return true
		}
		if segment == "connection" && i+1 < len(segments) && segments[i+1] == "string" {
			return true
		}
	}
	return false
}

// endsInPasswordWord matches glued names such as pgpassword, dbpassword and rootpasswd.
func endsInPasswordWord(segment string) bool {
	return strings.HasSuffix(segment, "password") || strings.HasSuffix(segment, "passwd")
}

// nameKind is how a name found in free text is judged.
type nameKind int

const (
	nameClean nameKind = iota
	nameSecret
	// nameCounter is a plural such as max_tokens or API_KEYS: secret unless the value is a number or a boolean.
	nameCounter
)

// freeTextSecrets are the segments that make a name in log text secret. Plurals and "pass" are handled apart.
var freeTextSecrets = map[string]bool{
	"key": true, "apikey": true, "apikeys": true, "token": true, "secret": true,
	"password": true, "passwords": true, "passwd": true, "passwds": true, "passphrase": true, "pwd": true,
	"credential": true, "credentials": true, "auth": true, "authorization": true, "private": true,
	"cert": true, "dsn": true,
}

// pairNameKind judges a name in free text. It is narrower than NameLooksSecret: "tokens: 1200", "keys: 12" and
// "--- PASS: TestX" are counters and test output, not secrets.
func pairNameKind(name string) nameKind {
	segments := nameSegments(name)
	for _, segment := range segments {
		if freeTextSecrets[segment] || endsInPasswordWord(segment) ||
			strings.HasSuffix(segment, "secretkey") || strings.HasSuffix(segment, "accesskey") ||
			strings.HasSuffix(segment, "privatekey") || strings.HasSuffix(segment, "apikey") {
			return nameSecret
		}
	}
	if len(segments) < 2 {
		return nameClean
	}
	switch last := segments[len(segments)-1]; last {
	case "pass":
		if segments[len(segments)-2] == "proxy" {
			return nameClean
		}
		return nameSecret
	case "keys", "tokens", "secrets":
		return nameCounter
	}
	return nameClean
}

// harmlessCounterValue reports whether the first item of a plural-named value is a number (signed, decimal, with a
// unit suffix), n/m, a boolean, null, or the start of a JSON array or object: none of those is a secret.
var harmlessCounter = regexp.MustCompile(`^(?i:[+-]?[0-9]+(?:\.[0-9]+)?(?:k|m|s|ms|%)?|n/m|true|false|yes|no|on|off|null)$`)

func harmlessCounterValue(first string) bool {
	return harmlessCounter.MatchString(first) || strings.HasPrefix(first, "[") || strings.HasPrefix(first, "{")
}

// listEnd extends a span over the comma separated items that follow it.
func listEnd(text string, span [2]int) int {
	end := span[1]
	for end < len(text) && text[end] == ',' {
		item := unquotedValue.FindString(text[end+1:])
		if item == "" {
			break
		}
		end += 1 + len(trimClosers(item))
	}
	return end
}

// trimClosers removes the trailing closing brackets of value that no opening bracket inside it balances: in
// {"password": hunter2} the value is hunter2} and the brace is the object's, in {user}:{pw} both are the value's.
func trimClosers(value string) string {
	for len(value) > 0 {
		last := value[len(value)-1]
		var open byte
		switch last {
		case '}':
			open = '{'
		case ']':
			open = '['
		default:
			return value
		}
		if strings.Count(value, string(open)) >= strings.Count(value, string(last)) {
			return value
		}
		value = value[:len(value)-1]
	}
	return value
}

// escapedValueSpan finds a value opened by backslashes and a quote, as in a JSON string inside a JSON string. It
// closes at the first quote preceded by exactly as many backslashes as the opener had, so a deeper escaped quote
// inside the value does not end it.
func escapedValueSpan(text string, start int) ([2]int, bool) {
	n := 0
	for start+n < len(text) && text[start+n] == '\\' {
		n++
	}
	if start+n >= len(text) || (text[start+n] != '"' && text[start+n] != '\'') {
		return [2]int{}, false
	}
	quote := text[start+n]
	begin := start + n + 1
	for i := begin; i < len(text); i++ {
		if text[i] == '\n' {
			return [2]int{begin, i}, i > begin
		}
		if text[i] != quote {
			continue
		}
		run := 0
		for j := i - 1; j >= begin && text[j] == '\\'; j-- {
			run++
		}
		if run == n {
			return [2]int{begin, i - n}, i-n > begin
		}
	}
	return [2]int{begin, len(text)}, len(text) > begin
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
	return len(valueSpans(value)) > 0
}

// ScrubText replaces each secret-shaped span in text with Redacted and leaves
// the rest byte-identical.
func ScrubText(text string) string {
	spans := append(pairSpans(text), valueSpans(text)...)
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var merged [][2]int
	for _, span := range spans {
		if n := len(merged); n > 0 && span[0] <= merged[n-1][1] {
			if span[1] > merged[n-1][1] {
				merged[n-1][1] = span[1]
			}
			continue
		}
		merged = append(merged, span)
	}
	var out strings.Builder
	last := 0
	for _, span := range merged {
		out.WriteString(text[last:span[0]])
		out.WriteString(Redacted)
		last = span[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// valueSpans returns the spans that are secret regardless of any name.
func valueSpans(text string) [][2]int {
	var spans [][2]int
	for _, p := range valuePatterns {
		for _, m := range p.FindAllStringSubmatchIndex(text, -1) {
			spans = append(spans, [2]int{m[4], m[5]})
		}
	}
	for _, m := range urlUserinfo.FindAllStringSubmatchIndex(text, -1) {
		if looksLikePort(text[m[4]:m[5]]) {
			continue
		}
		spans = append(spans, [2]int{m[4], m[5]})
	}
	return append(spans, longRunSpans(text)...)
}

// looksLikePort reports whether the text after "host:" is a port followed by
// a path, as in http://host:8080/path?email=a@b.example, not a password.
func looksLikePort(afterColon string) bool {
	slash := strings.IndexByte(afterColon, '/')
	return slash > 0 && digitsOnly.MatchString(afterColon[:slash])
}

// pairSpans returns the value of every secret-named pair, wherever the pair
// appears, including inside another pair's value or a query string.
func pairSpans(text string) [][2]int {
	var spans [][2]int
	for _, m := range pairName.FindAllStringSubmatchIndex(text, -1) {
		kind := pairNameKind(text[m[4]:m[5]])
		if kind == nameClean {
			continue
		}
		span, ok := pairValueSpan(text, m[1])
		if !ok {
			continue
		}
		if kind == nameCounter {
			// A counter or list: only the first item is judged, and a secret list is hidden whole.
			first := text[span[0]:span[1]]
			if comma := strings.IndexByte(first, ','); comma >= 0 {
				first = first[:comma]
			}
			if harmlessCounterValue(first) {
				continue
			}
			span[1] = listEnd(text, span)
		}
		spans = append(spans, span)
	}
	return spans
}

func pairValueSpan(text string, start int) ([2]int, bool) {
	if start >= len(text) {
		return [2]int{}, false
	}
	if quote := text[start]; quote == '"' || quote == '\'' {
		for i := start + 1; i < len(text); i++ {
			switch text[i] {
			case '\\':
				i++
			case quote:
				return [2]int{start + 1, i}, i > start+1
			case '\n':
				return [2]int{start + 1, i}, i > start+1
			}
		}
		return [2]int{start + 1, len(text)}, len(text) > start+1
	}
	if text[start] == '\\' {
		if span, ok := escapedValueSpan(text, start); ok {
			return span, true
		}
	}
	value := unquotedValue.FindString(text[start:])
	if value == "" {
		return [2]int{}, false
	}
	end := start + len(value)
	if trimmed := trimClosers(value); trimmed == "" {
		// Only brackets: JSON structure when the text ends or a separator follows, otherwise a secret.
		if end >= len(text) || strings.IndexByte(", \t\r\n", text[end]) >= 0 {
			return [2]int{}, false
		}
	} else {
		end = start + len(trimmed)
		value = trimmed
	}
	if authScheme.MatchString(value) {
		// "Authorization: Bearer <token>": the credential follows the scheme.
		if token := authToken.FindString(text[end:]); token != "" {
			return [2]int{end + len(token) - len(strings.TrimLeft(token, " \t")), end + len(token)}, true
		}
	}
	return [2]int{start, end}, true
}

// longRunSpans finds random-looking tokens, skipping identifiers that are
// common in diagnostics: digests, ARNs and image tags.
func longRunSpans(text string) [][2]int {
	arns := arnSpan.FindAllStringIndex(text, -1)
	var spans [][2]int
	for _, m := range longRun.FindAllStringIndex(text, -1) {
		run := text[m[0]:m[1]]
		if !looksLikeRandomRun(run) {
			continue
		}
		if insideAny(arns, m) {
			continue
		}
		if hexOnly.MatchString(run) && (digestPrefix.MatchString(text[:m[0]]) || isImageTag(text[:m[0]])) {
			continue
		}
		spans = append(spans, [2]int{m[0], m[1]})
	}
	return spans
}

func insideAny(outer [][]int, inner []int) bool {
	for _, o := range outer {
		if o[0] <= inner[0] && inner[1] <= o[1] {
			return true
		}
	}
	return false
}

// isImageTag reports whether the text before a run ends in "<repo path>:",
// that is a colon directly after a token containing a slash.
func isImageTag(before string) bool {
	if !strings.HasSuffix(before, ":") {
		return false
	}
	tokenStart := strings.LastIndexAny(before, " \t\r\n\"'=") + 1
	return strings.Contains(before[tokenStart:], "/")
}

// looksLikeRandomRun separates random secrets from long identifiers. Pure hex
// of 40+ is random. Otherwise the run is split on - _ and /, pieces that are
// plain words, numbers or hex ids are set aside, and the rest must be at
// least 24 characters with three character classes (separators are not a
// class). A pod name or test name is mostly words; a random base64url token
// is not.
func looksLikeRandomRun(run string) bool {
	if uuid.MatchString(run) {
		return false
	}
	if hexOnly.MatchString(run) {
		return true
	}
	var random strings.Builder
	for _, piece := range strings.FieldsFunc(run, func(r rune) bool { return r == '-' || r == '_' || r == '/' }) {
		if wordySegment.MatchString(piece) || hexSegment.MatchString(piece) {
			continue
		}
		random.WriteString(piece)
	}
	if random.Len() < 24 {
		return false
	}
	var lower, upper, digit bool
	for _, r := range random.String() {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit} {
		if present {
			classes++
		}
	}
	return classes >= 3
}
