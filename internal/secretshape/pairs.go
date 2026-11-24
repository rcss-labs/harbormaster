package secretshape

import (
	"regexp"
	"strings"
)

// pairName matches the name and operator of name=value, name: value and "name": "value". The value is parsed
// separately so that a secret nested in another pair's value is still found.
var pairName = regexp.MustCompile(`["']?([A-Za-z0-9_.-]+)["']?[ \t]*[=:][ \t]*`)

// namedValue matches {"name": "DB_PASSWORD", "value": up to the value.
var namedValue = regexp.MustCompile(`(?i)["'](?:name|key)["']\s*:\s*["']([^"'\s]*)["']\s*,\s*["']value["']\s*:\s*`)

// harmlessCounter matches a number (signed, decimal, with a unit suffix), a ratio, a boolean or null.
var harmlessCounter = regexp.MustCompile(`^(?i:[+-]?[0-9]+(?:\.[0-9]+)?(?:k|m|s|ms|%)?(?:/[0-9]+)?|n/m|true|false|yes|no|on|off|null)$`)

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

// harmlessCounterValue reports whether one item of a plural-named value is a counter. Sentence punctuation and closing
// brackets after it do not matter.
func harmlessCounterValue(item string) bool {
	return harmlessCounter.MatchString(strings.TrimRight(item, ".:!?)]}"))
}

// firstItem is the first item of a comma separated value.
func firstItem(value string) string {
	return value[:itemEnd(value, 0, func(c byte) bool { return c == ',' })]
}

// itemEnd is where the item at start ends: where ends says, or at a JSON escape of text that could not be parsed.
// Reading on to the end of the value would be quadratic in a run of counters.
func itemEnd(text string, start int, ends func(byte) bool) int {
	end := start
	for end < len(text) && !ends(text[end]) {
		if text[end] != '\\' {
			end++
			continue
		}
		run := end
		for run < len(text) && text[run] == '\\' {
			run++
		}
		if run == len(text) || strings.IndexByte(`nrtbf"'/u`, text[run]) >= 0 {
			break
		}
		end = run
	}
	return end
}

// pairSpans returns the value of every secret-named pair, wherever the pair appears, including inside another pair's
// value or a query string.
func pairSpans(text string) [][2]int {
	var spans [][2]int
	for _, m := range namedValue.FindAllStringSubmatchIndex(text, -1) {
		if NameLooksSecret(text[m[2]:m[3]]) {
			if span, ok := secretSpan(text, m[1]); ok {
				spans = append(spans, span)
			}
		}
	}
	// A pair inside a value that is already hidden ends no later than that value does. Skipping it keeps the work
	// proportional to the text when pairs are nested in pairs.
	covered := 0
	for _, m := range pairName.FindAllStringSubmatchIndex(text, -1) {
		start := m[1]
		name := text[m[2]:m[3]]
		kind := pairNameKind(name)
		if kind == nameClean || start >= len(text) {
			continue
		}
		var span [2]int
		var ok bool
		switch {
		case start < covered:
			span, ok = schemeSpan(text, start)
		case kind == nameCounter:
			span, ok = counterSpan(text, start, listsNames(name))
		default:
			span, ok = secretSpan(text, start)
		}
		if ok {
			spans = append(spans, span)
			covered = max(covered, span[1])
		}
	}
	return spans
}

// secretSpan is the span to hide of the value at start of a pair with a secret name.
func secretSpan(text string, start int) ([2]int, bool) {
	if start >= len(text) {
		return [2]int{}, false
	}
	if isQuote(text[start]) {
		return quotedSpan(text, start)
	}
	if span, ok := schemeSpan(text, start); ok {
		return span, true
	}
	span, found := unquotedSpan(text, start)
	if opensList(text, start) && !(found && endsAfterList(text[span[0]:span[1]])) {
		return withList(span, found, readList(text, start))
	}
	return span, found
}

// counterSpan is the span to hide of the value at start of a plural-named pair: none for a counter, and otherwise the
// whole list.
func counterSpan(text string, start int, namesOnly bool) ([2]int, bool) {
	switch {
	case isQuote(text[start]):
		span, ok := quotedSpan(text, start)
		return span, ok && !harmlessCounterValue(firstItem(text[span[0]:span[1]]))
	case opensList(text, start):
		if namesOnly {
			return [2]int{}, false
		}
		list := readList(text, start)
		if !list.hasSecret {
			return [2]int{}, false
		}
		span, found := unquotedSpan(text, start)
		return withList(span, found, list)
	}
	if harmlessCounterValue(text[start:itemEnd(text, start, endsValue)]) {
		return [2]int{}, false
	}
	span, ok := unquotedSpan(text, start)
	if ok {
		span[1] = listEnd(text, span[1])
	}
	return span, ok
}

// listEnd extends a span over the comma separated items that follow it.
func listEnd(text string, end int) int {
	for end < len(text) && text[end] == ',' {
		item := text[end+1 : valueEnd(text, end+1)]
		if item == "" {
			break
		}
		end += 1 + len(trimClosers(item))
	}
	return end
}

func isQuote(c byte) bool { return c == '"' || c == '\'' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// endsValue reports whether c ends an unquoted value. Brackets and backslashes do not: they may be part of a secret.
func endsValue(c byte) bool { return isSpace(c) || isQuote(c) || c == ',' || c == ';' || c == '&' }

func valueEnd(text string, start int) int {
	end := start
	for end < len(text) && !endsValue(text[end]) {
		end++
	}
	return end
}

// quotedEnd is the position of the quote that closes the one at start, or the end of the line.
func quotedEnd(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		switch {
		case text[i] == '\n' || text[i] == text[start]:
			return i
		case text[i] == '\\' && i+1 < len(text) && text[i+1] != '\n':
			i++
		}
	}
	return len(text)
}

func quotedSpan(text string, start int) ([2]int, bool) {
	end := quotedEnd(text, start)
	return [2]int{start + 1, end}, end > start+1
}

// unquotedSpan is the span of an unquoted value, without the structure of the JSON around it.
func unquotedSpan(text string, start int) ([2]int, bool) {
	end := valueEnd(text, start)
	value := text[start:end]
	if end < len(text) && isQuote(text[end]) {
		// The backslashes escape the quote.
		value = strings.TrimRight(value, `\`)
	}
	if value == "" {
		return [2]int{}, false
	}
	if strings.Trim(value, "{}[]") == "" {
		// Only brackets: structure when more structure or nothing follows, otherwise a secret.
		last := value[len(value)-1]
		if last == '{' || last == '[' || end >= len(text) || text[end] == ',' || isSpace(text[end]) {
			return [2]int{}, false
		}
		return [2]int{start, end}, true
	}
	return [2]int{start, start + len(trimClosers(value))}, true
}

// trimClosers removes the trailing closing brackets of value that no opening bracket inside it balances: in
// {"password": hunter2} the value is hunter2} and the brace is the object's, in {user}:{pw} both are the value's.
func trimClosers(value string) string {
	braces := strings.Count(value, "}") - strings.Count(value, "{")
	brackets := strings.Count(value, "]") - strings.Count(value, "[")
	for len(value) > 0 {
		switch last := value[len(value)-1]; {
		case last == '}' && braces > 0:
			braces--
		case last == ']' && brackets > 0:
			brackets--
		default:
			return value
		}
		value = value[:len(value)-1]
	}
	return value
}

var authScheme = regexp.MustCompile(`^(?i:bearer|basic)[ \t]+`)

// schemeSpan is the credential of "Authorization: Bearer <token>", which follows the scheme.
func schemeSpan(text string, start int) ([2]int, bool) {
	// The window is for speed only: no scheme is longer, and spaces past it are not looked for.
	m := authScheme.FindString(text[start:min(len(text), start+32)])
	if m == "" {
		return [2]int{}, false
	}
	return unquotedSpan(text, start+len(m))
}

// opensList reports whether a bracketed list starts at start. A value that is already redacted is not a list.
func opensList(text string, start int) bool {
	return text[start] == '[' && !strings.HasPrefix(text[start:], Redacted)
}

// list is a bracketed list in plain text.
type list struct {
	start, end int  // from its first item to the end of its last
	after      int  // behind the closing bracket, or where reading stopped
	hasContent bool // an item is not already redacted
	hasSecret  bool // an item is neither a counter nor already redacted
}

// readList reads the list at start. It stops at the end of the line, at the quote around the text it is in, or at an
// object.
func readList(text string, start int) list {
	l := list{start: start + 1, end: start + 1}
	quoted, nesting := false, 0
	i := start
	add := func(item string, from, to int) {
		if item == "" {
			return
		}
		if l.end == start+1 {
			l.start = from
		}
		l.end = to
		l.hasContent = l.hasContent || item != Redacted
		l.hasSecret = l.hasSecret || item != Redacted && !harmlessCounterValue(firstItem(item))
	}
	for i < len(text) {
		switch c := text[i]; {
		case c == ' ' || c == '\t' || c == ',':
			i++
		case c == '[' && !strings.HasPrefix(text[i:], Redacted):
			nesting++
			i++
		case c == ']' && nesting == 1:
			l.after = i + 1
			return l
		case c == ']':
			nesting--
			i++
		case isQuote(c) && (quoted || l.end == start+1):
			quoted = true
			end := quotedEnd(text, i)
			if end == len(text) || text[end] != c {
				add(text[i+1:end], i, end)
				l.after = end
				return l
			}
			add(text[i+1:end], i, end+1)
			i = end + 1
		case isSpace(c) || isQuote(c) || c == '{' && startsObject(text, i):
			l.after = i
			return l
		default:
			end := i + 1
			if strings.HasPrefix(text[i:], Redacted) {
				end = i + len(Redacted)
			}
			for end < len(text) && text[end] != ',' && !isSpace(text[end]) && !isQuote(text[end]) && !closesList(text, end) {
				end++
			}
			item := text[i:end]
			if end < len(text) && isQuote(text[end]) {
				// The quote around the list ends it.
				item = strings.TrimRight(item, `\`)
				add(item, i, i+len(item))
				l.after = l.end
				return l
			}
			add(item, i, end)
			i = end
		}
	}
	l.after = len(text)
	return l
}

// closesList reports whether the bracket at i closes a list; one that text follows directly is part of an item.
func closesList(text string, i int) bool {
	if text[i] != ']' {
		return false
	}
	return i+1 >= len(text) || endsValue(text[i+1]) || strings.IndexByte(`]}).:\`, text[i+1]) >= 0
}

func startsObject(text string, i int) bool {
	return i+1 < len(text) && (isQuote(text[i+1]) || isSpace(text[i+1]) || text[i+1] == '}' || text[i+1] == '\\')
}

// endsAfterList reports whether value has text behind its balanced brackets, as in [abc]def: it is no list.
func endsAfterList(value string) bool {
	opened := strings.Count(value, "[")
	return opened > 0 && opened == strings.Count(value, "]") && !strings.HasSuffix(value, "]")
}

// withList is the span that hides a list and the unquoted value that starts with it, whichever reaches further.
func withList(span [2]int, found bool, l list) ([2]int, bool) {
	if !found || span[1] <= l.after {
		return [2]int{l.start, l.end}, l.hasContent
	}
	return [2]int{span[0], max(span[1], l.end)}, true
}
