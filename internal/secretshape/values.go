package secretshape

import (
	"regexp"
	"strings"
)

func isQuote(c byte) bool { return c == '"' || c == '\'' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// endsValue reports whether c ends an unquoted value. Brackets and backslashes do not: they may be part of a secret.
func endsValue(c byte) bool { return isSpace(c) || isQuote(c) || c == ',' || c == ';' || c == '&' }

// tally counts one kind of bracket: opened[i] and closed[i] are how many come before i, at[k] is where closer k is.
type tally struct{ opened, closed, at []int32 }

// matches holds, for each position, the closer of the bracket that is open there, or where reading stopped without one,
// written as ^position.
type matches []int32

// valueTables answers where the value at any position of a text ends. It is built in one pass, and each answer takes
// constant time, so every pair can be answered for, however pairs are nested.
type valueTables struct {
	text     string
	run      []int32    // next character that ends an unquoted value
	line     []int32    // end of the line
	quote    [2][]int32 // next " and next ' that no backslash escapes
	slashes  []int32    // how many backslashes come directly before
	closers  []int32    // how many closing brackets come directly before
	brackets []int32    // how many brackets come directly before
	list     tally
	braces   tally
	// commas memoises listEnd; matched holds the closers, built when first asked for.
	commas  []int32
	matched map[[2]bool]matches
}

func newValueTables(text string) *valueTables {
	n := len(text)
	// All but the closer positions have one entry per position and one for the end.
	backing := make([]int32, 12*(n+1))
	table := func() []int32 {
		t := backing[: n+1 : n+1]
		backing = backing[n+1:]
		return t
	}
	v := &valueTables{
		text: text, run: table(), line: table(), quote: [2][]int32{table(), table()},
		slashes: table(), closers: table(), brackets: table(),
		list: tally{opened: table(), closed: table()}, braces: tally{opened: table(), closed: table()},
		commas: table(), matched: map[[2]bool]matches{},
	}
	for i := 0; i < n; i++ {
		c := text[i]
		if c == '\\' {
			v.slashes[i+1] = v.slashes[i] + 1
		}
		if c == ']' || c == '}' {
			v.closers[i+1] = v.closers[i] + 1
		}
		if c == '[' || c == '{' || c == ']' || c == '}' {
			v.brackets[i+1] = v.brackets[i] + 1
		}
		v.list.count(i, c, '[', ']')
		v.braces.count(i, c, '{', '}')
	}
	v.run[n], v.line[n], v.quote[0][n], v.quote[1][n] = int32(n), int32(n), int32(n), int32(n)
	for i := n - 1; i >= 0; i-- {
		c := text[i]
		v.run[i], v.line[i] = v.run[i+1], v.line[i+1]
		v.quote[0][i], v.quote[1][i] = v.quote[0][i+1], v.quote[1][i+1]
		if endsValue(c) {
			v.run[i] = int32(i)
		}
		if c == '\n' {
			v.line[i] = int32(i)
		}
		if isQuote(c) && v.slashes[i]%2 == 0 {
			v.quote[quoteIndex(c)][i] = int32(i)
		}
	}
	return v
}

func quoteIndex(c byte) int {
	if c == '"' {
		return 0
	}
	return 1
}

func (t *tally) count(i int, c, open, closer byte) {
	t.opened[i+1], t.closed[i+1] = t.opened[i], t.closed[i]
	switch c {
	case open:
		t.opened[i+1]++
	case closer:
		t.closed[i+1]++
		t.at = append(t.at, int32(i))
	}
}

// unbalanced is how many more closers than openers text[start:end] holds.
func (t *tally) unbalanced(start, end int) int {
	return int(t.closed[end]-t.closed[start]) - int(t.opened[end]-t.opened[start])
}

// keptCloser is the position behind the last closer before end that stays when the unbalanced ones are trimmed, or 0.
func (t *tally) keptCloser(start, end int) int {
	last := int(t.closed[end]) - 1 - max(0, t.unbalanced(start, end))
	if last < 0 {
		return 0
	}
	return int(t.at[last]) + 1
}

// trim returns end without the trailing closing brackets that no opening bracket in text[start:end] balances: in
// {"password": hunter2} the value is hunter2} and the brace is the object's, in {user}:{pw} both are the value's.
func (v *valueTables) trim(start, end int) int {
	zone := max(start, end-int(v.closers[end]))
	return max(zone, v.list.keptCloser(start, end), v.braces.keptCloser(start, end))
}

// beforeQuote returns end without the backslashes that escape a quote at end.
func (v *valueTables) beforeQuote(start, end int) int {
	if end < len(v.text) && isQuote(v.text[end]) {
		return max(start, end-int(v.slashes[end]))
	}
	return end
}

// closingQuote is the position of the quote that closes the one at start, or the end of the line.
func (v *valueTables) closingQuote(start int) int {
	return int(min(v.quote[quoteIndex(v.text[start])][start+1], v.line[start]))
}

// closer is the position of the bracket that closes the one at start, or false and the end of the line or, when the
// brackets hold no quotes of their own, of the quote around them. A ] directly followed by text is part of an item.
func (v *valueTables) closer(start int, quotes bool) (int, bool) {
	open := v.text[start]
	key := [2]bool{open == '[', quotes}
	if v.matched[key] == nil {
		v.matched[key] = v.match(open, open+2, quotes) // ] and } are two behind [ and {
	}
	if end := v.matched[key][start+1]; end < 0 {
		return int(^end), false
	} else {
		return int(end), true
	}
}

func (v *valueTables) match(open, closer byte, quotes bool) matches {
	n := len(v.text)
	m := make(matches, n+1)
	m[n] = ^int32(n)
	for i := n - 1; i >= 0; i-- {
		m[i] = m[i+1]
		switch c := v.text[i]; {
		case c == '\n':
			m[i] = ^int32(i)
		case isQuote(c) && !quotes:
			m[i] = ^int32(i)
		case isQuote(c):
			if end := v.closingQuote(i); end < n && v.text[end] == c {
				m[i] = m[end+1]
			} else {
				m[i] = ^int32(end)
			}
		case strings.HasPrefix(v.text[i:], Redacted):
			m[i] = m[i+len(Redacted)]
		case c == open && m[i+1] >= 0:
			m[i] = m[m[i+1]+1]
		case c == closer && (closer == '}' || closesList(v.text, i)):
			m[i] = int32(i)
		}
	}
	return m
}

// closesList reports whether the ] at i closes a list; one that text follows directly is part of an item.
func closesList(text string, i int) bool {
	return i+1 >= len(text) || endsValue(text[i+1]) || strings.IndexByte(`]}).:\`, text[i+1]) >= 0
}

var authScheme = regexp.MustCompile(`^(?i:bearer|basic)[ \t]+`)

// schemeLength is the length of "Bearer " or "Basic " at start, before the credential.
func schemeLength(text string, start int) int {
	// The window is for speed only: no scheme is longer, and spaces past it are not looked for.
	return len(authScheme.FindString(text[start:min(len(text), start+32)]))
}

// span is the value at start of a pair: the one place that decides where a value ends, whatever the name. plural
// values go on over commas. A value that is a JSON object, or a list of them, is one only when objects is set.
func (v *valueTables) span(start int, plural, objects bool) ([2]int, bool) {
	if start >= len(v.text) {
		return [2]int{}, false
	}
	if n := schemeLength(v.text, start); n > 0 && start+n < len(v.text) {
		// "Authorization: Bearer <token>": the credential follows the scheme.
		if span, ok := v.bareSpan(start+n, plural, objects); ok {
			return span, true
		}
	}
	return v.bareSpan(start, plural, objects)
}

func (v *valueTables) bareSpan(start int, plural, objects bool) ([2]int, bool) {
	c := v.text[start]
	if isQuote(c) {
		end := v.closingQuote(start)
		return [2]int{start + 1, end}, end > start+1
	}
	if c == '[' || c == '{' {
		if span, isGroup := v.groupSpan(start, objects); isGroup {
			return span, span[1] > span[0]
		}
	}
	end := v.wordEnd(start)
	if end <= start {
		return [2]int{}, false
	}
	if plural {
		end = v.listEnd(end)
	}
	return [2]int{start, end}, true
}

// wordEnd is the end of the unquoted value at start, without the structure of the JSON around it. It is start when
// there is none.
func (v *valueTables) wordEnd(start int) int {
	run := int(v.run[start])
	end := v.beforeQuote(start, run)
	if int(v.brackets[end]) < end-start {
		return v.trim(start, end)
	}
	// Only brackets: structure when more structure or nothing follows, otherwise a secret.
	if end == start || strings.IndexByte("{[", v.text[end-1]) >= 0 || run >= len(v.text) || v.text[run] == ',' || isSpace(v.text[run]) {
		return start
	}
	return end
}

// listEnd extends a value over the comma separated items that follow it.
func (v *valueTables) listEnd(end int) int {
	first := end
	for end < len(v.text) && v.text[end] == ',' && v.commas[end] == 0 {
		run := int(v.run[end+1])
		next := v.trim(end+1, v.beforeQuote(end+1, run))
		if next == end+1 {
			break
		}
		end = next
	}
	if end < len(v.text) && v.text[end] == ',' && v.commas[end] != 0 {
		end = int(v.commas[end])
	}
	// Remember it for the commas passed, so that no item is passed twice.
	for at := first; at < end && v.text[at] == ',' && v.commas[at] == 0; at = int(v.run[at+1]) {
		v.commas[at] = int32(end)
	}
	return end
}

// groupSpan is the content of the list, or the braces, at start. isGroup is false when there is nothing in them, or
// when the brackets are part of a word, as in [abc]def.
func (v *valueTables) groupSpan(start int, objects bool) (span [2]int, isGroup bool) {
	text := v.text
	if strings.HasPrefix(text[start:], Redacted) {
		return span, false
	}
	if word := v.trim(start, int(v.run[start])); text[start] == '[' && text[word-1] != ']' && v.list.unbalanced(start, word) == 0 {
		return span, false
	}
	first, holdsObject := v.firstContent(start)
	if first >= len(text) || strings.IndexByte("\n\r\\]}", text[first]) >= 0 {
		return span, false
	}
	quotes := isQuote(text[first])
	if quotes && holdsObject && !objects {
		return span, true
	}
	end, closed := v.closer(start, quotes)
	inside := 0
	if text[start] == '[' {
		inside = 1
	}
	if !closed {
		return [2]int{start + inside, v.beforeQuote(start+1, end)}, true
	}
	// What follows the bracket directly belongs to the value, as in {user}:{pw}.
	if after := end + 1; after < len(text) && !endsValue(text[after]) {
		if tail := v.trim(after, v.beforeQuote(after, int(v.run[after]))); tail > after {
			return [2]int{start, tail}, true
		}
	}
	if onlyRedactions(text[start+1 : end]) {
		return span, true
	}
	return [2]int{start + inside, end + 1 - inside}, true
}

// firstContent is the first position in the brackets at start that is neither blank nor another opening bracket, and
// whether one of the brackets is a brace.
func (v *valueTables) firstContent(start int) (first int, holdsObject bool) {
	for i := start; i < len(v.text); i++ {
		switch c := v.text[i]; {
		case c == '{':
			holdsObject = true
		case c == '[' && !strings.HasPrefix(v.text[i:], Redacted):
		case c != ' ' && c != '\t':
			return i, holdsObject
		}
	}
	return len(v.text), holdsObject
}

// onlyRedactions reports whether content is nothing but redacted items.
func onlyRedactions(content string) bool {
	for len(content) > 0 {
		switch {
		case strings.HasPrefix(content, Redacted):
			content = content[len(Redacted):]
		case strings.IndexByte(" \t,[]\"'", content[0]) >= 0:
			content = content[1:]
		default:
			return false
		}
	}
	return true
}
