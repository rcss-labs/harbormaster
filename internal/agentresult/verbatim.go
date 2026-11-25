package agentresult

import (
	"encoding/json"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Normalize lowercases text and folds everything that is not a letter or digit into single spaces, with no space at
// either end, so two renderings of one log line compare equal.
func Normalize(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	pendingSpace := false
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSpace && out.Len() > 0 {
				out.WriteByte(' ')
			}
			pendingSpace = false
			out.WriteRune(unicode.ToLower(r))
		} else {
			pendingSpace = true
		}
	}
	return out.String()
}

// valueSeparator joins stored values. Normalised text never holds it, so no run spans two values.
const valueSeparator = '\n'

// Corpus indexes the text a run read, for the copy check.
type Corpus struct {
	text []byte // normalised values, each followed by valueSeparator
	raw  int    // raw bytes offered so far
	full bool
}

// Add keeps the string values of a run's read text: every string value of a JSON body (keys are not values), the
// whole text otherwise. Past CorpusCap raw bytes nothing more is stored and the corpus is Full.
func (c *Corpus) Add(text string) {
	if c.full {
		return
	}
	c.raw += len(text)
	if c.raw > CorpusCap {
		c.full = true
		c.text = nil
		return
	}
	trimmed := strings.TrimSpace(text)
	if trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[') {
		if values, ok := jsonStringValues(trimmed); ok {
			for _, v := range values {
				c.store(v)
			}
			return
		}
	}
	c.store(text)
}

// jsonStringValues returns every string value of the JSON in text, in order, with duplicate keys' values all kept
// and keys left out. ok is false when text is not JSON.
func jsonStringValues(text string) (values []string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	var inObject, expectKey []bool // one entry per open container
	valueDone := func() {
		if n := len(inObject); n > 0 && inObject[n-1] {
			expectKey[n-1] = true
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return values, len(inObject) == 0
		}
		if err != nil {
			return nil, false
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				inObject = append(inObject, v == '{')
				expectKey = append(expectKey, v == '{')
				continue
			}
			inObject, expectKey = inObject[:len(inObject)-1], expectKey[:len(expectKey)-1]
			valueDone()
		case string:
			if n := len(inObject); n > 0 && inObject[n-1] && expectKey[n-1] {
				expectKey[n-1] = false // a key
				continue
			}
			values = append(values, v)
			valueDone()
		default:
			valueDone()
		}
	}
}

// store keeps the normalised text, and the normalised text with one level of JSON escapes decoded when that differs,
// so a log line that carries \u0020 or \n is compared as it reads.
func (c *Corpus) store(text string) {
	if n := Normalize(text); n != "" {
		c.text = append(append(c.text, n...), valueSeparator)
	}
	if strings.IndexByte(text, '\\') >= 0 {
		if n := Normalize(unescapeJSON(text)); n != "" {
			c.text = append(append(c.text, n...), valueSeparator)
		}
	}
}

var jsonEscapes = map[byte]string{'n': "\n", 't': "\t", 'r': "\r", 'b': "\b", 'f': "\f", '"': "\"", '\\': "\\", '/': "/"}

// unescapeJSON decodes the escapes of a JSON string without its quotes; anything unrecognised stays as it is.
func unescapeJSON(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 == len(text) {
			out.WriteByte(text[i])
			continue
		}
		if rep, ok := jsonEscapes[text[i+1]]; ok {
			out.WriteString(rep)
			i++
		} else if text[i+1] == 'u' && i+6 <= len(text) {
			if n, err := strconv.ParseUint(text[i+2:i+6], 16, 32); err == nil {
				out.WriteRune(rune(n))
				i += 5
				continue
			}
			out.WriteByte(text[i])
		} else {
			out.WriteByte(text[i])
		}
	}
	return out.String()
}

// Full reports that the corpus passed its cap, so the copy check cannot vouch for anything.
func (c *Corpus) Full() bool { return c.full }

// hashBase is random per process so the rolling hash cannot be aimed at.
var hashBase = rand.Uint64() | 1

// window is a rolling hash over the last VerbatimRun runes.
type window struct {
	hash, top uint64 // top is hashBase^(VerbatimRun-1)
}

func newWindow() window {
	top := uint64(1)
	for i := 1; i < VerbatimRun; i++ {
		top *= hashBase
	}
	return window{top: top}
}

func (w *window) push(r rune) { w.hash = w.hash*hashBase + uint64(r) + 1 }

// slide drops the oldest rune out and pushes r.
func (w *window) slide(out, r rune) {
	w.hash = (w.hash-(uint64(out)+1)*w.top)*hashBase + uint64(r) + 1
}

// HasRunIn reports whether the normalised candidate and the corpus share VerbatimRun consecutive characters. The
// candidate's windows are indexed once; the corpus is rolled over once. Time is linear in the corpus, memory in the
// candidate.
func (c *Corpus) HasRunIn(candidate string) bool {
	cand := Normalize(candidate)
	candRunes := []rune(cand)
	if len(candRunes) < VerbatimRun || len(c.text) < VerbatimRun {
		return false
	}
	// byteAt[i] is the byte offset of rune i, so a window's text is cand[byteAt[i]:byteAt[i+VerbatimRun]].
	byteAt := make([]int, len(candRunes)+1)
	for i, r := range candRunes {
		byteAt[i+1] = byteAt[i] + utf8.RuneLen(r)
	}
	windowText := func(i int) string { return cand[byteAt[i]:byteAt[i+VerbatimRun]] }

	w := newWindow()
	index := make(map[uint64][]int, len(candRunes))
	for i, r := range candRunes {
		if i < VerbatimRun {
			w.push(r)
		} else {
			w.slide(candRunes[i-VerbatimRun], r)
		}
		if i < VerbatimRun-1 {
			continue
		}
		start := i - VerbatimRun + 1
		known := false
		for _, other := range index[w.hash] {
			if windowText(other) == windowText(start) {
				known = true
				break
			}
		}
		if !known {
			index[w.hash] = append(index[w.hash], start)
		}
	}

	// Roll over the corpus; ring holds the last VerbatimRun runes and their byte offsets.
	var ring [VerbatimRun]rune
	var offsets [VerbatimRun]int
	filled := 0 // runes in the ring since the last separator
	w = newWindow()
	for pos := 0; pos < len(c.text); {
		r, size := utf8.DecodeRune(c.text[pos:])
		if r == valueSeparator {
			filled, w.hash = 0, 0
			pos += size
			continue
		}
		slot := filled % VerbatimRun
		if filled < VerbatimRun {
			w.push(r)
		} else {
			w.slide(ring[slot], r)
		}
		ring[slot], offsets[slot] = r, pos
		filled++
		pos += size
		if filled >= VerbatimRun {
			if starts, ok := index[w.hash]; ok {
				first := offsets[filled%VerbatimRun]
				chunk := string(c.text[first:pos])
				for _, start := range starts {
					if windowText(start) == chunk {
						return true
					}
				}
			}
		}
	}
	return false
}
