// Package secretshape detects names and values that look like secrets. It is
// used to redact environment values and to scrub log text before it leaves
// for a model provider. All matching uses Go's linear-time regexp engine.
//
// Known limit: in text that is not valid JSON, such as an answer cut at the
// read cap, redaction never goes past the end of the line, so the later lines
// of a list or object spread over several lines under a secret name stay. A
// value that comes before its name, as in "value":"x","name":"DB_PASSWORD", is
// found only in valid JSON. Text glued to a redaction without a separator may
// be hidden by a second pass only.
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

// valuePatterns match a secret on its own; group 2 is the secret. A pattern is tried only on text that contains its hint,
// which is far cheaper to look for.
var valuePatterns = []struct {
	hint    string
	pattern *regexp.Regexp
}{
	{"AKIA", regexp.MustCompile(boundary + `(AKIA[A-Z0-9]{16})`)},
	{"ASIA", regexp.MustCompile(boundary + `(ASIA[A-Z0-9]{16})`)},
	{"-----BEGIN", regexp.MustCompile(`()(-----BEGIN [A-Z0-9 ]+-----)`)},
	{"eyJ", regexp.MustCompile(boundary + `(eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)`)},
	{"sk-", regexp.MustCompile(boundary + `(sk-[A-Za-z0-9_-]{16,})`)},
	{"gh", regexp.MustCompile(boundary + `(gh[po]_[A-Za-z0-9]{20,})`)},
	{"xox", regexp.MustCompile(boundary + `(xox[bp]-[A-Za-z0-9-]{10,})`)},
	{"AIza", regexp.MustCompile(boundary + `(AIza[A-Za-z0-9_-]{30,})`)},
	// Harbormaster tokens: prefix, 12 base32 characters, "_", 43 base64url characters. The second branch keeps
	// shorter or malformed tokens caught.
	{"hm", regexp.MustCompile(boundary + `(hm[poar]_(?:[a-z2-7]{12}_[A-Za-z0-9_-]{43}|[A-Za-z0-9]{16,}))`)},
	// Standalone "Bearer <token>" as sent in an Authorization header.
	{"Bearer", regexp.MustCompile(boundary + `Bearer[ \t]+([A-Za-z0-9._~+/=-]+)`)},
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

// Bounds on how far before a hex run the digest and image-tag checks look.
const (
	digestWindow = 16  // "sha512:" and then some
	prefixWindow = 512 // an image reference such as registry/team/app:
)

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
		if secretSegments[segment] || strings.HasSuffix(segment, "password") || strings.HasSuffix(segment, "passwd") {
			// The suffixes match glued names such as pgpassword, dbpassword and rootpasswd.
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

// nameKind is how a name found in free text is judged.
type nameKind int

const (
	nameClean nameKind = iota
	nameSecret
	// nameCounter is a plural such as max_tokens or API_KEYS: secret unless the value is a number or a boolean.
	nameCounter
)

// strongWords make a name in log text secret as one of its segments or as the glued ending of one (pgpassword).
// Plurals such as keys and tokens, and "pass", are handled apart.
var strongWords = []string{
	"password", "passwd", "pwd", "passphrase", "secret", "authorization", "credential", "apikey", "dsn",
	"secretkey", "accesskey", "privatekey", "authtoken", "accesstoken", "connectionstring",
}

var strongPlurals = []string{"passwords", "passwds", "credentials", "apikeys"}

// weakWords make a name secret as one of its segments, unless the name ends in a benign descriptor: key_id, token_ttl.
var weakWords = map[string]bool{"token": true, "key": true, "auth": true, "cert": true, "private": true}

var benignDescriptors = map[string]bool{
	"id": true, "ids": true, "name": true, "names": true, "ttl": true, "count": true, "total": true, "type": true,
	"size": true, "path": true, "file": true, "url": true, "uri": true, "arn": true, "version": true, "prefix": true,
	"index": true, "expiry": true, "expires": true, "service": true, "subnets": true, "enabled": true, "status": true,
	"limit": true, "per": true, "rate": true, "length": true, "hash": true, "fingerprint": true, "thumbprint": true,
	"arns": true, "policy": true, "ip": true, "ips": true,
}

// strongBenign are the descriptors that clear strong words too: secret_name, password_file.
var strongBenign = map[string]bool{
	"name": true, "names": true, "arn": true, "arns": true, "count": true, "file": true, "path": true, "version": true,
	"ttl": true, "length": true, "policy": true, "type": true,
}

// cursorWords before "token" make a pagination cursor: nextToken, next_page_token.
var cursorWords = map[string]bool{"next": true, "page": true, "continuation": true, "pagination": true, "cursor": true}

func endsInAny(segment string, words []string) bool {
	for _, word := range words {
		if strings.HasSuffix(segment, word) {
			return true
		}
	}
	return false
}

// pairNameKind judges a name in free text. It is narrower than NameLooksSecret: "tokens: 1200", "keys: 12" and
// "--- PASS: TestX" are counters and test output, not secrets, and {"key":"Environment"} is a tag.
func pairNameKind(name string) nameKind {
	segments := nameSegments(name)
	if len(segments) == 0 {
		return nameClean
	}
	before, last := segments[:len(segments)-1], segments[len(segments)-1]
	weak := false
	for _, segment := range segments {
		if !strongBenign[last] && (endsInAny(segment, strongWords) || endsInAny(segment, strongPlurals)) {
			return nameSecret
		}
		weak = weak || weakWords[segment]
	}
	if weak && !benignDescriptors[last] && !(last == "key" && len(before) == 0) && !isCursor(before, last) {
		return nameSecret
	}
	if len(before) == 0 {
		return nameClean
	}
	switch last {
	case "pass":
		if before[len(before)-1] == "proxy" {
			return nameClean
		}
		return nameSecret
	case "keys", "tokens", "secrets":
		return nameCounter
	}
	return nameClean
}

func isCursor(before []string, last string) bool {
	if len(before) == 2 && before[0] == "next" && (before[1] == "forward" || before[1] == "backward") {
		return last == "token"
	}
	for _, segment := range before {
		if !cursorWords[segment] {
			return false
		}
	}
	return last == "token" && len(before) > 0
}

// namesOneSecret reports whether a free-text name holds a strong word in the singular: what is under it, however
// nested, is that secret.
func namesOneSecret(name string) bool {
	for _, segment := range nameSegments(name) {
		if endsInAny(segment, strongWords) {
			return true
		}
	}
	return false
}

// hidesObject reports whether an object under a free-text name holds nothing but that secret, or those secrets.
func hidesObject(name string) bool {
	for _, segment := range nameSegments(name) {
		if endsInAny(segment, strongWords) || endsInAny(segment, strongPlurals) {
			return !strings.EqualFold(name, "authorizationConfig")
		}
	}
	return false
}

// holdsSecretMap reports whether an object under name maps names to secrets. A list under it, as in an ECS container,
// holds references.
func holdsSecretMap(name string) bool {
	segments := nameSegments(name)
	return len(segments) > 0 && segments[len(segments)-1] == "secrets"
}

var environmentName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// labelKind judges the name in {"name": ..., "value": ...}. A name written like an environment variable is judged as
// one; any other, which may be a service or a metric, must end in the secret word: db.password, not auth-service.
func labelKind(label string) nameKind {
	if environmentName.MatchString(label) {
		if NameLooksSecret(label) {
			return nameSecret
		}
		return nameClean
	}
	kind := pairNameKind(label)
	if kind != nameSecret {
		return kind
	}
	segments := nameSegments(label)
	last := segments[len(segments)-1]
	if endsInAny(last, strongWords) || endsInAny(last, strongPlurals) || weakWords[last] || last == "pass" {
		return nameSecret
	}
	return nameClean
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

// valueSpans returns the spans that are secret regardless of any name.
func valueSpans(text string) [][2]int {
	var spans [][2]int
	for _, p := range valuePatterns {
		if !strings.Contains(text, p.hint) {
			continue
		}
		for _, m := range p.pattern.FindAllStringSubmatchIndex(text, -1) {
			spans = append(spans, [2]int{m[4], m[5]})
		}
	}
	if strings.Contains(text, "://") {
		for _, m := range urlUserinfo.FindAllStringSubmatchIndex(text, -1) {
			if !looksLikePort(text[m[4]:m[5]]) {
				spans = append(spans, [2]int{m[4], m[5]})
			}
		}
	}
	return append(spans, longRunSpans(text)...)
}

// looksLikePort reports whether the text after "host:" is a port followed by
// a path, as in http://host:8080/path?email=a@b.example, not a password.
func looksLikePort(afterColon string) bool {
	slash := strings.IndexByte(afterColon, '/')
	return slash > 0 && digitsOnly.MatchString(afterColon[:slash])
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
		if hexOnly.MatchString(run) && hexRunIsDiagnostic(text, m[0]) {
			continue
		}
		spans = append(spans, [2]int{m[0], m[1]})
	}
	return spans
}

// hexRunIsDiagnostic reports whether the hex run starting at start is a
// digest or an image tag. It looks only at a short stretch before the run:
// matching the whole text before every run would be quadratic on a big log.
func hexRunIsDiagnostic(text string, start int) bool {
	before := text[max(0, start-prefixWindow):start]
	return digestPrefix.MatchString(before[max(0, len(before)-digestWindow):]) || isImageTag(before)
}

// insideAny reports whether inner lies within one of outer, which holds
// sorted, non-overlapping spans.
func insideAny(outer [][]int, inner []int) bool {
	i := sort.Search(len(outer), func(i int) bool { return outer[i][0] > inner[0] })
	return i > 0 && inner[1] <= outer[i-1][1]
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
