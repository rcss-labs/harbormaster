package agentresult

import (
	"encoding/json"
	"math/rand/v2"
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
		var body any
		if json.Unmarshal([]byte(trimmed), &body) == nil {
			c.addValues(body)
			return
		}
	}
	c.store(text)
}

func (c *Corpus) addValues(node any) {
	switch v := node.(type) {
	case string:
		c.store(v)
	case []any:
		for _, item := range v {
			c.addValues(item)
		}
	case map[string]any:
		for _, item := range v {
			c.addValues(item)
		}
	}
}

func (c *Corpus) store(text string) {
	if n := Normalize(text); n != "" {
		c.text = append(append(c.text, n...), valueSeparator)
	}
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
