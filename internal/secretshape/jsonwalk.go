package secretshape

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// hiding is how the values of a JSON member are treated because of its name.
type hiding int

const (
	// hideNothing judges strings by their own content.
	hideNothing hiding = iota
	// hideLists hides list items and scalars that are not counters.
	hideLists
	// hideScalars hides scalars, and scalars in lists.
	hideScalars
	// hideAll hides every scalar, including those of objects inside.
	hideAll
)

// jsonWalker finds the edits for one valid JSON value. It never re-encodes: strings are decoded with an offset map,
// and only the secret part of a token is replaced, so everything else keeps its bytes.
type jsonWalker struct {
	text  string
	pos   int
	depth int
	edits []edit
	// secretValues are the ranges of "value" members whose object names a secret environment variable.
	secretValues [][2]int
}

// jsonEdits returns the edits for text, which must be valid JSON.
func jsonEdits(text string, depth int) []edit {
	w := &jsonWalker{text: text, depth: depth}
	w.value(hideNothing)
	if len(w.secretValues) == 0 {
		return w.edits
	}
	// The name may follow the value, so these are hidden afterwards, the outermost first: what it covers is done.
	ranges := w.secretValues
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	first := w.edits
	w.edits = nil
	var edits []edit
	covered, next := 0, 0
	for _, r := range ranges {
		if r[0] < covered {
			continue
		}
		covered = r[1]
		for ; next < len(first) && first[next].start < r[0]; next++ {
			edits = append(edits, first[next])
		}
		for next < len(first) && first[next].start < r[1] {
			next++
		}
		w.pos, w.edits = r[0], nil
		w.value(hideAll)
		edits = append(edits, w.edits...)
	}
	return append(edits, first[next:]...)
}

func (w *jsonWalker) space() {
	for w.pos < len(w.text) && strings.IndexByte(" \t\r\n", w.text[w.pos]) >= 0 {
		w.pos++
	}
}

func (w *jsonWalker) value(hide hiding) {
	w.space()
	switch c := w.text[w.pos]; c {
	case '{':
		w.object(hide)
	case '[':
		w.pos++
		for w.space(); w.text[w.pos] != ']'; w.space() {
			if w.text[w.pos] == ',' {
				w.pos++
			}
			w.value(hide)
		}
		w.pos++
	case '"':
		w.str(hide)
	default:
		w.literal(hide)
	}
}

func (w *jsonWalker) object(hide hiding) {
	var values [][2]int
	secretName := false
	w.pos++
	for w.space(); w.text[w.pos] != '}'; w.space() {
		if w.text[w.pos] == ',' {
			w.pos++
			w.space()
		}
		key := w.str(hideNothing)
		w.space()
		w.pos++ // the colon
		w.space()
		start := w.pos
		if w.text[start] == '"' && (strings.EqualFold(key, "name") || strings.EqualFold(key, "key")) {
			label, _ := unescape(w.text[start+1 : stringEnd(w.text, start)])
			secretName = secretName || NameLooksSecret(label)
		}
		w.value(memberHiding(key, hide, w.text[start]))
		if strings.EqualFold(key, "value") {
			values = append(values, [2]int{start, w.pos})
		}
	}
	w.pos++
	if secretName && hide != hideAll {
		w.secretValues = append(w.secretValues, values...)
	}
}

// memberHiding is how the value of a member is treated; first is the value's first character.
func memberHiding(key string, outer hiding, first byte) hiding {
	if outer == hideAll {
		return hideAll
	}
	switch pairNameKind(key) {
	case nameSecret:
		return hideScalars
	case nameCounter:
		if first != '[' || !listsNames(key) {
			return hideLists
		}
	}
	return hideNothing
}

// str walks the string at the current position and returns its decoded content.
func (w *jsonWalker) str(hide hiding) string {
	start, end := w.pos+1, stringEnd(w.text, w.pos)
	w.pos = end + 1
	content, offsets := unescape(w.text[start:end])
	if hide == hideLists && harmlessCounterValue(firstItem(content)) {
		hide = hideNothing
	}
	if hide != hideNothing {
		if content != "" {
			w.edits = append(w.edits, edit{start, end, Redacted})
		}
		return content
	}
	for _, e := range scrub(content, w.depth+1) {
		// A replacement may itself hold quotes when a number deeper inside became a string.
		quoted := strconv.Quote(e.replacement)
		w.edits = append(w.edits, edit{start + offsets[e.start], start + offsets[e.end], quoted[1 : len(quoted)-1]})
	}
	return content
}

// literal walks a number, true, false or null. The last three are no secret and stay readable.
func (w *jsonWalker) literal(hide hiding) {
	start := w.pos
	for w.pos < len(w.text) && strings.IndexByte(" \t\r\n,]}", w.text[w.pos]) < 0 {
		w.pos++
	}
	token := w.text[start:w.pos]
	if strings.IndexByte("tfn", token[0]) >= 0 {
		return
	}
	if hide == hideScalars || hide == hideAll || ValueLooksSecret(token) {
		w.edits = append(w.edits, edit{start, w.pos, `"` + Redacted + `"`})
	}
}

// stringEnd returns the position of the quote that closes the JSON string opened at start.
func stringEnd(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(text)
}

var shortEscapes = map[byte]byte{
	'n': '\n', 't': '\t', 'r': '\r', 'b': '\b', 'f': '\f', '"': '"', '\'': '\'', '\\': '\\', '/': '/',
}

// unescape removes one level of JSON escapes. offsets holds, for each byte of the result and for its end, where in text
// the character it came from begins. What is not an escape is kept.
func unescape(text string) (string, []int) {
	out := make([]byte, 0, len(text))
	offsets := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		start := i
		size := len(out)
		if short, ok := byte(0), false; text[i] == '\\' && i+1 < len(text) {
			if short, ok = shortEscapes[text[i+1]]; ok {
				out = append(out, short)
				i += 2
			} else if r, ok := hexEscape(text[i:]); ok {
				i += 6
				if utf16.IsSurrogate(r) {
					low, _ := hexEscape(text[i:])
					if r = utf16.DecodeRune(r, low); r != utf8.RuneError {
						i += 6
					}
				}
				out = utf8.AppendRune(out, r)
			}
		}
		if i == start {
			out = append(out, text[i])
			i++
		}
		for ; size < len(out); size++ {
			offsets = append(offsets, start)
		}
	}
	return string(out), append(offsets, len(text))
}

// hexEscape reads a leading \uXXXX.
func hexEscape(text string) (rune, bool) {
	if len(text) < 6 || text[0] != '\\' || text[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(text[2:6], 16, 32)
	return rune(n), err == nil
}
