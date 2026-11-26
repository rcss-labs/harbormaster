package secretshape

import (
	"regexp"
	"strings"
)

// pairName matches the name and operator of name=value, name: value, "name": "value" and user[name]=value. The value is
// parsed separately so that a secret nested in another pair's value is still found.
var pairName = regexp.MustCompile(`(?:["']?([A-Za-z0-9_.-]+)["']?[ \t]*[=:]|[A-Za-z0-9_.\]-]\[["']?([A-Za-z0-9_.-]+)["']?\](?:\[\])?[ \t]*[=:])[ \t]*`)

// namedValue matches {"name": "DB_PASSWORD", "value": up to the value.
var namedValue = regexp.MustCompile(`(?i)["'](?:name|key)["']\s*:\s*["']([^"'\s]*)["']\s*,\s*["']value["']\s*:\s*`)

// secretReference matches the ARN of a secret, with the JSON key and version ECS may add. The "secret:" and "password:"
// in one are no pairs.
var secretReference = regexp.MustCompile(`arn:aws[a-z-]*:secretsmanager:[a-z0-9-]*:[0-9]*:secret:[A-Za-z0-9/_+=.@!-]+(?::[A-Za-z0-9_.-]*){0,3}`)

// harmlessCounter matches a number (signed, decimal, with a unit suffix), a ratio, a boolean or null.
var harmlessCounter = regexp.MustCompile(`^(?i:[+-]?[0-9]+(?:\.[0-9]+)?(?:k|m|s|ms|%)?(?:/[0-9]+)?|n/m|true|false|yes|no|on|off|null)$`)

var jsonEscape = regexp.MustCompile(`\\+(?:[nrtbf"'/u]|$)`)

// nameStates say what became of the names a list of keys holds, as in missing_keys: such a list holds no secrets.
var nameStates = map[string]bool{
	"missing": true, "unknown": true, "unexpected": true, "extra": true, "duplicate": true, "required": true,
	"unused": true, "evicted": true, "expired": true, "changed": true, "added": true, "removed": true, "deleted": true,
}

// listsNames reports whether a plural name announces key names rather than keys: missing_keys, not api_keys.
func listsNames(name string) bool {
	segments := nameSegments(name)
	last := len(segments) - 1
	if last < 1 || segments[last] != "keys" {
		return false
	}
	for _, segment := range segments[:last] {
		if !nameStates[segment] {
			return false
		}
	}
	return true
}

// maxCounter is longer than any counter, its sentence punctuation included.
const maxCounter = 64

// harmlessCounterValue reports whether the first comma separated item of a plural-named value is a counter. Sentence
// punctuation and closing brackets after it do not matter, and a JSON escape ends it in text that could not be parsed.
func harmlessCounterValue(value string) bool {
	value = value[:min(len(value), maxCounter)]
	if i := strings.IndexByte(value, ','); i >= 0 {
		value = value[:i]
	}
	if escape := jsonEscape.FindStringIndex(value); escape != nil {
		value = value[:escape[0]]
	}
	return harmlessCounter.MatchString(strings.TrimRight(value, `.:!?)]}\`))
}

// harmlessItems reports whether every item of a list is a counter or already redacted.
func harmlessItems(list string) bool {
	for len(list) > 0 {
		if strings.HasPrefix(list, Redacted) {
			list = list[len(Redacted):]
			continue
		}
		if endsValue(list[0]) || list[0] == '[' || list[0] == ']' {
			list = list[1:]
			continue
		}
		end := 1
		for end < len(list) && end < maxCounter && !endsValue(list[end]) && list[end] != ']' {
			end++
		}
		if !harmlessCounterValue(list[:end]) {
			return false
		}
		list = list[end:]
	}
	return true
}

// pairSpans returns the value of every secret-named pair, wherever the pair appears, including inside another pair's
// value or a query string. Spans may overlap: what any pair hides is hidden.
func pairSpans(text string) [][2]int {
	var spans [][2]int
	var values *valueTables
	add := func(start int, kind nameKind, name string) {
		objects := kind != nameClean && hidesObject(name)
		if text[start] == '{' && holdsSecretMap(name) && opensObject(text, start) {
			kind, objects = nameSecret, true
		}
		if kind == nameClean {
			return
		}
		if values == nil {
			values = newValueTables(text)
		}
		span, ok := values.span(start, kind == nameCounter, objects)
		if ok && (kind == nameSecret || !harmlessPlural(text, start, span, name)) {
			spans = append(spans, span)
		}
	}
	for _, m := range namedValue.FindAllStringSubmatchIndex(text, -1) {
		if m[1] < len(text) {
			label := text[m[2]:m[3]]
			add(m[1], labelKind(label), label)
		}
	}
	var references [][]int
	if strings.Contains(text, ":secretsmanager:") {
		references = secretReference.FindAllStringIndex(text, -1)
	}
	for _, m := range pairName.FindAllStringSubmatchIndex(text, -1) {
		start, nameStart, nameEnd := m[1], m[2], m[3]
		bracketed := nameStart < 0
		if bracketed {
			nameStart, nameEnd = m[4], m[5]
		}
		if start >= len(text) {
			continue
		}
		name := text[nameStart:nameEnd]
		colon := strings.HasSuffix(strings.TrimRight(text[nameEnd:start], " \t"), ":")
		if colon && insideAny(references, []int{nameStart, start}) {
			continue
		}
		if colon && bracketed && !namesOneSecret(name) {
			// "app[auth]: starting" is a log line of a process.
			continue
		}
		add(start, pairNameKind(name), name)
	}
	return spans
}

// harmlessPlural reports whether the value at start of a plural-named pair is a counter, or a list of counters or key
// names. It does not decide where the value ends.
func harmlessPlural(text string, start int, span [2]int, name string) bool {
	if text[start] == '[' && !strings.HasPrefix(text[start:], Redacted) {
		return listsNames(name) || harmlessItems(text[span[0]:span[1]])
	}
	if schemeLength(text, start) > 0 {
		return false
	}
	value := text[span[0]:span[1]]
	return harmlessCounterValue(value)
}

// opensObject reports whether the brace at start opens a JSON object with members.
func opensObject(text string, start int) bool {
	i := start + 1
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i < len(text) && isQuote(text[i])
}
